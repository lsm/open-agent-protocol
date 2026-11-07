use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::{executor::block_on, StreamExt};
use oap_adapter::jsonrpc::Inbound;
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
                mode.into(),
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
    block_on(scope.clone().request(run.clone(), request)).unwrap();
    let mut events = Vec::new();
    for _ in 0..3 {
        events.push(block_on(stream.next()).unwrap().unwrap());
    }
    let permission = events
        .last()
        .unwrap()
        .decode_payload::<PermissionRequestedPayload>()
        .unwrap();
    if mode == "grant" {
        block_on(scope.clone().resolve(
            run.clone(),
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
            },
        ))
        .unwrap();
    }
    block_on(scope.clone().complete(run.clone(), block_on(call).unwrap())).unwrap();
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
        if mode == "grant" {
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
        if mode == "grant" {
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
