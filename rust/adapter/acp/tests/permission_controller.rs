use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::{executor::block_on, StreamExt};
use oap_adapter::jsonrpc::{Inbound, IncomingRequest, Message, Responder};
use oap_adapter::InteractionResolution;
use oap_adapter_acp::{
    native::PromptParams,
    process::{Process, ProcessConfig},
    Permissions, PromptBridge, TextRun, TextRunContext,
};
use oap_protocol::{PermissionRequestedPayload, PermissionResolveRequest};
use serde_json::json;

static NEXT: AtomicU64 = AtomicU64::new(0);
struct Fixture(PathBuf);
impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn permission_case(mode: &str) {
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-permission-engine-{}-{stamp}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    let script = r#"read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"native"}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"native","toolCall":{"toolCallId":"native-tool","title":"Read"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}'; if [ "$2" = parent ]; then printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'; fi; read -r reply; printf '%s\n' "$reply" > "$1"; if [ "$2" = grant ]; then printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'; fi; read -r eof; exit 0"#;
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: "/bin/sh".into(),
            args: vec![
                "-c".into(),
                script.into(),
                "fixture".into(),
                fixture.0.join("reply").to_string_lossy().into(),
                if mode == "parent" { "parent" } else { "grant" }.into(),
            ],
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(200)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let (send, receive) = mpsc::channel();
    let bridge = Arc::new(PromptBridge::new(process, move |message| {
        if let Inbound::Request(request) = message {
            send.send(request).unwrap();
        }
    }));
    block_on(bridge.call(
        "session/new".into(),
        Some(serde_json::value::to_raw_value(&json!({"cwd":"/","mcpServers":[]})).unwrap()),
        None,
    ))
    .unwrap();
    let scope = Permissions::new(bridge.clone(), "user".into(), || 50).unwrap();
    let mut next = 0;
    let run = Arc::new(Mutex::new(TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native".into(),
            run_id: "run".into(),
            message_id: "session/message".into(),
            capability_revision: None,
        },
        move |kind| {
            next += 1;
            format!("{kind}-{next}")
        },
    )));
    let mut call = bridge.prompt(
        PromptParams {
            session_id: "native".into(),
            ..PromptParams::default()
        },
        None,
    );
    block_on(call.admitted()).unwrap();
    let mut stream = run.lock().unwrap().start(10).unwrap();
    let request = receive.recv_timeout(Duration::from_secs(3)).unwrap();
    if mode == "foreign" {
        struct RefusedSource;
        impl Responder for RefusedSource {
            fn send(&self, _: Message) -> Result<(), oap_adapter::jsonrpc::Error> {
                panic!("foreign source received a reply")
            }
        }
        let foreign = Arc::new(IncomingRequest::new(
            oap_adapter_acp::process::DIALECT,
            request.id.clone(),
            request.method.clone(),
            request.params.clone(),
            Arc::new(RefusedSource),
        ));
        assert_eq!(
            block_on(scope.clone().request(run.clone(), foreign)).unwrap_err(),
            oap_adapter::Error::InvalidResolution
        );
    }
    block_on(scope.clone().request(run.clone(), request.clone())).unwrap();
    assert_eq!(
        block_on(scope.clone().request(run.clone(), request)).unwrap_err(),
        oap_adapter::Error::InvalidResolution
    );
    let mut events = Vec::new();
    for _ in 0..3 {
        events.push(block_on(stream.next()).unwrap().unwrap());
    }
    let permission = events
        .last()
        .unwrap()
        .decode_payload::<PermissionRequestedPayload>()
        .unwrap();
    let resolution = InteractionResolution {
        run_id: permission.run_id.clone(),
        responded_by: permission.responded_by.clone(),
        permission: Some(PermissionResolveRequest {
            interaction_id: permission.interaction_id,
            requested_by: permission.requested_by,
            responded_by: permission.responded_by,
            session_id: permission.session_id,
            run_id: permission.run_id,
            choice_id: "allow".into(),
            granted: true,
            ..PermissionResolveRequest::default()
        }),
        ..InteractionResolution::default()
    };
    if mode != "parent" {
        if mode == "concurrent" {
            let first = scope.clone().resolve(run.clone(), resolution.clone());
            let second = scope.clone().resolve(run.clone(), resolution.clone());
            let (first, second) = block_on(futures::future::join(first, second));
            assert_eq!(usize::from(first.is_ok()) + usize::from(second.is_ok()), 1);
            assert_eq!(
                first.err().or(second.err()),
                Some(oap_adapter::Error::InteractionResolved)
            );
        } else {
            block_on(scope.clone().resolve(run.clone(), resolution.clone())).unwrap();
        }
    }
    block_on(scope.clone().complete(run.clone(), block_on(call).unwrap())).unwrap();
    assert_eq!(
        block_on(scope.clone().resolve(run.clone(), resolution)).unwrap_err(),
        oap_adapter::Error::InteractionResolved
    );
    events.extend(
        block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap(),
    );
    block_on(bridge.clone().close()).unwrap();
    let values = events
        .iter()
        .map(|event| serde_json::to_value(event).unwrap())
        .collect::<Vec<_>>();
    let resolved = values
        .iter()
        .find(|event| event["type"] == "action.permission.resolved")
        .unwrap();
    assert_eq!(resolved["in_reply_to"], values[2]["id"]);
    assert_eq!(
        resolved["payload"]["outcome"],
        if mode != "parent" {
            "resolved"
        } else {
            "cancelled"
        }
    );
    assert_eq!(values.last().unwrap()["type"], "run.completed");
    assert!(
        values
            .iter()
            .position(|event| event["type"] == "action.permission.resolved")
            .unwrap()
            < values
                .iter()
                .position(|event| event["type"] == "action.call.failed")
                .unwrap()
    );
    let reply: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(fixture.0.join("reply")).unwrap()).unwrap();
    assert_eq!(
        reply["result"]["outcome"]["outcome"],
        if mode != "parent" {
            "selected"
        } else {
            "cancelled"
        }
    );
}

#[test]
fn native_permission_resolves_through_the_public_engine() {
    permission_case("grant");
}
#[test]
fn parent_completion_settles_permission_before_tool_and_run() {
    permission_case("parent");
}

#[test]
fn foreign_source_with_the_same_wire_id_is_refused_before_publication() {
    permission_case("foreign");
}

#[test]
fn concurrent_answers_publish_and_write_exactly_once() {
    permission_case("concurrent");
}

fn controller_peer(
    script: &str,
) -> (
    Fixture,
    Arc<PromptBridge>,
    mpsc::Receiver<Arc<IncomingRequest>>,
    Arc<Permissions>,
) {
    let path = std::env::temp_dir().join(format!(
        "oap-permission-review-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    std::fs::create_dir(&path).unwrap();
    let fixture = Fixture(path);
    let source = r#"import json,sys,os
from pathlib import Path
out=Path(sys.argv[1])
(out/'pid').write_text(str(os.getpid()))
def read():
 line=sys.stdin.readline()
 with (out/'wire').open('a') as wire: wire.write(line)
 return json.loads(line) if line else None
def send(value): print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
"#;
    std::fs::write(fixture.0.join("agent.py"), format!("{source}\n{script}")).unwrap();
    let selected = std::process::Command::new("python3")
        .args(["-c", "import sys; print(sys.executable)"])
        .output()
        .unwrap();
    assert!(selected.status.success());
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: String::from_utf8(selected.stdout).unwrap().trim().into(),
            args: vec![
                "-u".into(),
                fixture.0.join("agent.py").to_string_lossy().into(),
                fixture.0.to_string_lossy().into(),
            ],
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let (send, receive) = mpsc::channel();
    let bridge = Arc::new(PromptBridge::new(process, move |message| {
        if let Inbound::Request(request) = message {
            send.send(request).unwrap();
        }
    }));
    let permissions = Permissions::new(bridge.clone(), "user".into(), || 1000).unwrap();
    (fixture, bridge, receive, permissions)
}

fn controller_run(id: &str) -> Arc<Mutex<TextRun>> {
    let mut next = 0;
    Arc::new(Mutex::new(TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native".into(),
            run_id: id.into(),
            message_id: format!("session/{id}-message").into(),
            capability_revision: None,
        },
        move |kind| {
            next += 1;
            format!("{kind}-{next}")
        },
    )))
}

fn controller_proof(fixture: &Fixture, case: &str, events: serde_json::Value) {
    let pid = std::fs::read_to_string(fixture.0.join("pid")).unwrap();
    assert!(!std::process::Command::new("/bin/kill")
        .args(["-0", pid.trim()])
        .output()
        .unwrap()
        .status
        .success());
    let wire = std::fs::read_to_string(fixture.0.join("wire")).unwrap();
    let frames = wire
        .lines()
        .map(|line| serde_json::from_str::<serde_json::Value>(line).unwrap())
        .collect::<Vec<_>>();
    println!(
        "controller-review-proof {}",
        json!({"case":case,"native_pid":pid.trim().parse::<u32>().unwrap(),"wire":frames,"runs":events})
    );
}

fn malformed_request_is_answered(closed: bool) {
    let (fixture, bridge, received, permissions) = controller_peer(
        r#"
send(dict(id='ask',method='session/request_permission',params=dict(toolCall=7)))
reply=read()
(out/'reply.json').write_text(json.dumps(reply))
sys.stdin.read()
"#,
    );
    let request = received.recv_timeout(Duration::from_secs(3)).unwrap();
    let run = controller_run("unstarted");
    if closed {
        run.lock().unwrap().close();
    }
    let outcome = block_on(permissions.request(run, request));
    block_on(bridge.close()).unwrap();
    assert!(outcome.is_err());
    let reply: serde_json::Value =
        serde_json::from_slice(&std::fs::read(fixture.0.join("reply.json")).unwrap()).unwrap();
    controller_proof(
        &fixture,
        if closed {
            "malformed-closed"
        } else {
            "malformed-unstarted"
        },
        json!([]),
    );
    assert_eq!(reply["id"], "ask");
    assert_eq!(reply["error"]["code"], -32602);
}

#[test]
fn malformed_request_is_answered_before_a_run_starts() {
    malformed_request_is_answered(false);
}

#[test]
fn malformed_request_is_answered_after_its_projection_closes() {
    malformed_request_is_answered(true);
}

#[test]
fn a_second_run_with_restarted_ids_settles_its_own_native_request() {
    let (fixture, bridge, received, permissions) = controller_peer(
        r#"
for index in range(2):
 prompt=read()
 send(dict(id='ask-'+str(index),method='session/request_permission',params=dict(sessionId='native',toolCall=dict(toolCallId='tool-'+str(index),title='Act'),options=[dict(optionId='allow',name='Allow',kind='allow_once')])))
 send(dict(id=prompt['id'],result=dict(stopReason='end_turn')))
 reply=read()
 (out/('reply-'+str(index)+'.json')).write_text(json.dumps(reply))
sys.stdin.read()
"#,
    );
    let mut retained = Vec::new();
    let mut outcomes = Vec::new();
    let mut all_events = Vec::new();
    for index in 0..2 {
        let run = controller_run(&format!("run-{index}"));
        let mut call = bridge.prompt(
            PromptParams {
                session_id: "native".into(),
                ..PromptParams::default()
            },
            None,
        );
        block_on(call.admitted()).unwrap();
        let stream = run.lock().unwrap().start(1000).unwrap();
        let request = received.recv_timeout(Duration::from_secs(3)).unwrap();
        retained.push(request.clone());
        block_on(permissions.clone().request(run.clone(), request)).unwrap();
        let result = block_on(call).unwrap();
        let outcome = block_on(permissions.clone().complete(run.clone(), result));
        if outcome.is_err() {
            run.lock().unwrap().close();
        }
        outcomes.push(outcome);
        all_events.push(block_on(stream.collect::<Vec<_>>()));
    }
    block_on(bridge.close()).unwrap();
    let observed = all_events
        .iter()
        .map(|events| {
            events
                .iter()
                .map(|event| serde_json::to_value(event.as_ref().unwrap()).unwrap())
                .collect::<Vec<_>>()
        })
        .collect::<Vec<_>>();
    controller_proof(&fixture, "restarted-ids", json!(observed));
    for (index, outcome) in outcomes.into_iter().enumerate() {
        outcome.unwrap();
        let reply: serde_json::Value = serde_json::from_slice(
            &std::fs::read(fixture.0.join(format!("reply-{index}.json"))).unwrap(),
        )
        .unwrap();
        assert_eq!(reply["id"], format!("ask-{index}"));
        assert_eq!(reply["result"]["outcome"]["outcome"], "cancelled");
        let events = all_events[index]
            .iter()
            .map(|event| event.as_ref().unwrap())
            .collect::<Vec<_>>();
        assert_eq!(
            events
                .iter()
                .filter(|event| event.envelope_type.as_str() == "action.permission.resolved")
                .count(),
            1
        );
        assert_eq!(
            events.last().unwrap().envelope_type.as_str(),
            "run.completed"
        );
    }
    drop(retained);
}
