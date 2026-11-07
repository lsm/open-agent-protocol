use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::{executor::block_on, StreamExt};
use oap_adapter::{Adapter as _, InteractionResolution, OpenRequest, SubmitRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{
    Message, MessageContent, MessageSubmitRequest, Participant, PermissionRequestedPayload,
    PermissionResolveRequest,
};

static NEXT: AtomicU64 = AtomicU64::new(0);
struct Fixture(PathBuf);

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

#[test]
fn public_permission_can_be_resolved_immediately_after_publication() {
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let serial = NEXT.fetch_add(1, Ordering::Relaxed);
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-acp-permission-{}-{stamp}-{serial}",
        std::process::id()
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    let script = r#"read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"native"}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"native","toolCall":{"toolCallId":"native-tool","title":"Read"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}'; read -r reply; printf '%s\n' "$reply" > "$1"; printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'; read -r eof; exit 0"#;
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: "/bin/sh".into(),
            args: vec![
                "-c".into(),
                script.into(),
                "fixture".into(),
                fixture.0.join("reply").to_string_lossy().into(),
            ],
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(200)),
            ..ProcessConfig::default()
        },
        ..Config::default()
    })
    .unwrap();
    let session = block_on(adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))
    .unwrap();
    let (_, mut stream) = block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                role: "user".into(),
                content: MessageContent::from_text("synthetic"),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap();
    let mut events = Vec::new();
    while let Some(event) = block_on(stream.next()) {
        let event = event.unwrap();
        if event.envelope_type.as_str() == "action.permission.requested" {
            let permission = event
                .decode_payload::<PermissionRequestedPayload>()
                .unwrap();
            block_on(session.resolve(InteractionResolution {
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
            }))
            .unwrap();
        }
        events.push(serde_json::to_value(event).unwrap());
    }
    block_on(session.close()).unwrap();
    assert_eq!(
        events
            .iter()
            .filter(|e| e["type"] == "action.permission.requested")
            .count(),
        1,
        "{events:?}"
    );
    assert_eq!(
        events
            .iter()
            .filter(|e| e["type"] == "action.permission.resolved")
            .count(),
        1,
        "{events:?}"
    );
    let reply: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(fixture.0.join("reply")).unwrap()).unwrap();
    assert_eq!(reply["result"]["outcome"]["outcome"], "selected");
    assert_eq!(reply["result"]["outcome"]["optionId"], "allow");
    assert_eq!(events.last().unwrap()["type"], "run.completed");
}

fn open_permission_peer(mode: &str) -> (Fixture, Box<dyn oap_adapter::Session>) {
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-session-permission-peer-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    std::fs::write(fixture.0.join("agent.py"),r#"import json,sys,os
from pathlib import Path
out=Path(sys.argv[1])
mode=sys.argv[2]
(out/'pid').write_text(str(os.getpid()))
def read():
 line=sys.stdin.readline()
 with (out/'wire.jsonl').open('a') as wire: wire.write(line)
 return json.loads(line) if line else None
def send(value): print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
request=read()
model=dict(id='model',category='model',type='select',currentValue='sol',options=[dict(value='sol',name='Sol'),dict(value='luna',name='Luna')])
send(dict(id=request['id'],result=dict(sessionId='native',configOptions=[model])))
def ask(index):
 params=dict(sessionId='native',toolCall=dict(toolCallId='tool-'+str(index),title='Act'),options=[dict(optionId='allow',name='Allow',kind='allow_once')])
 if mode=='idle-malformed': params=dict(toolCall=7)
 send(dict(id='ask-'+str(index),method='session/request_permission',params=params))
if mode.startswith('idle'):
 ask('idle')
 (out/'idle.json').write_text(json.dumps(read()))
 sys.stdin.read()
else:
 for index in range(2):
  prompt=read()
  ask(index)
  reply=read()
  (out/('reply-'+str(index)+'.json')).write_text(json.dumps(reply))
  send(dict(id=prompt['id'],result=dict(stopReason='end_turn')))
 request=read()
 if request is not None:
  assert request['method']=='session/set_config_option'
  model['currentValue']=request['params']['value']
  send(dict(id=request['id'],result=dict(configOptions=[model])))
 sys.stdin.read()
"#).unwrap();
    let python = std::process::Command::new("python3")
        .args(["-c", "import sys; print(sys.executable)"])
        .output()
        .unwrap();
    assert!(python.status.success());
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: String::from_utf8(python.stdout).unwrap().trim().into(),
            args: vec![
                "-u".into(),
                fixture.0.join("agent.py").to_string_lossy().into(),
                fixture.0.to_string_lossy().into(),
                mode.into(),
            ],
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        ..Config::default()
    })
    .unwrap();
    let session = block_on(adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))
    .unwrap();
    (fixture, session)
}

fn permission_answer(permission: PermissionRequestedPayload) -> InteractionResolution {
    InteractionResolution {
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
    }
}

#[test]
fn settled_permission_stays_known_after_the_next_run_is_admitted() {
    let (fixture, session) = open_permission_peer("turns");
    let mut previous: Option<InteractionResolution> = None;
    for index in 0..2 {
        let (_, mut stream) = block_on(session.submit(SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "session".into(),
                messages: vec![Message {
                    id: Some(format!("input-{index}").into()),
                    role: "user".into(),
                    content: MessageContent::from_text("synthetic"),
                    ..Message::default()
                }],
                allow_degraded_features: vec![
                    "session.message.submit".into(),
                    "run.streaming".into(),
                ],
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        }))
        .unwrap();
        let mut events = Vec::new();
        while let Some(event) = block_on(stream.next()) {
            let event = event.unwrap();
            if event.envelope_type.as_str() == "action.permission.requested" {
                let state = block_on(session.state()).unwrap();
                assert_eq!(state.status.as_str(), "running");
                assert!(state.transcript_cursor.parse::<u64>().unwrap() >= event.sequence.unwrap());
                assert_eq!(
                    block_on(session.close()),
                    Err(oap_adapter::Error::RunActive)
                );
                if let Some(old) = &previous {
                    assert_eq!(
                        block_on(session.resolve(old.clone())),
                        Err(oap_adapter::Error::InteractionResolved)
                    );
                }
                let answer = permission_answer(
                    event
                        .decode_payload::<PermissionRequestedPayload>()
                        .unwrap(),
                );
                let mut wrong = answer.clone();
                wrong.responded_by = "intruder".into();
                assert_eq!(
                    block_on(session.resolve(wrong)),
                    Err(oap_adapter::Error::WrongResponder)
                );
                block_on(session.resolve(answer.clone())).unwrap();
                assert_eq!(
                    block_on(session.resolve(answer.clone())),
                    Err(oap_adapter::Error::InteractionResolved)
                );
                previous = Some(answer);
            }
            events.push(event);
        }
        assert_eq!(
            events.last().unwrap().envelope_type.as_str(),
            "run.completed"
        );
        let state = block_on(session.state()).unwrap();
        assert_eq!(state.status.as_str(), "idle");
        assert_eq!(
            state.transcript_cursor,
            events.last().unwrap().sequence.unwrap().to_string()
        );
    }
    let before = block_on(session.state()).unwrap();
    std::thread::sleep(Duration::from_millis(20));
    let (_, switched) = block_on(session.model_switcher().unwrap().switch_model(
        oap_protocol::SessionModelSwitchRequest {
            session_id: "session".into(),
            model_id: "luna".into(),
            allow_degraded_features: vec!["session.model.switch".into()],
            ..oap_protocol::SessionModelSwitchRequest::default()
        },
    ))
    .unwrap();
    let sampled = block_on(session.state()).unwrap();
    block_on(session.close()).unwrap();
    assert_eq!(sampled.current_model_id, "luna");
    assert!(switched.updated_at_ms > before.updated_at_ms);
    println!(
        "model-metadata-proof {}",
        serde_json::json!({"run_timestamp":before.updated_at_ms,"switched_timestamp":switched.updated_at_ms,"sampled_timestamp":sampled.updated_at_ms})
    );
    assert_eq!(
        sampled.updated_at_ms, switched.updated_at_ms,
        "state refresh replaced a newer model-switch timestamp"
    );
    for index in 0..2 {
        let reply: serde_json::Value = serde_json::from_slice(
            &std::fs::read(fixture.0.join(format!("reply-{index}.json"))).unwrap(),
        )
        .unwrap();
        assert_eq!(reply["id"], format!("ask-{index}"));
        assert_eq!(reply["result"]["outcome"]["outcome"], "selected");
    }
}

#[test]
fn idle_native_asks_receive_cancelled_or_malformed_replies() {
    for mode in ["idle", "idle-malformed"] {
        let (fixture, session) = open_permission_peer(mode);
        let deadline = std::time::Instant::now() + Duration::from_secs(3);
        while !fixture.0.join("idle.json").exists() && std::time::Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(5));
        }
        let state = block_on(session.state()).unwrap();
        block_on(session.close()).unwrap();
        assert_eq!(state.status.as_str(), "idle");
        assert!(state.active_run_id.is_none());
        assert!(state.transcript_cursor.is_empty());
        let reply: serde_json::Value =
            serde_json::from_slice(&std::fs::read(fixture.0.join("idle.json")).unwrap()).unwrap();
        assert_eq!(reply["id"], "ask-idle");
        if mode == "idle" {
            assert_eq!(reply["result"]["outcome"]["outcome"], "cancelled");
        } else {
            assert_eq!(reply["error"]["code"], -32602);
        }
    }
}
