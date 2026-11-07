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
