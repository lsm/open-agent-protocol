use std::io::Read;
use std::path::Path;
use std::time::{Duration, Instant};

use futures::{executor::block_on, StreamExt};
use oxap_adapter::{Adapter as _, InteractionResolution, OpenRequest, SubmitRequest};
use oxap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oxap_protocol::{
    Message, MessageContent, MessageSubmitRequest, Participant, PermissionRequestedPayload,
    PermissionResolveRequest,
};
use serde_json::{json, Value};

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    let input: Value = serde_json::from_str(&input).unwrap();
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: input["path"].as_str().unwrap().into(),
            args: serde_json::from_value(input["args"].clone()).unwrap(),
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        abort_active_on_close: true,
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
    let (admission, mut stream) = block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                id: Some("input-message".into()),
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
        if event.envelope_type.as_str() == "action.permission.requested"
            && input["resolve"].as_bool().unwrap_or(false)
        {
            let permission = event
                .decode_payload::<PermissionRequestedPayload>()
                .unwrap();
            let state = block_on(session.state()).unwrap();
            assert!(state.transcript_cursor.parse::<u64>().unwrap() >= event.sequence.unwrap());
            block_on(session.resolve(InteractionResolution {
                run_id: permission.run_id.clone(),
                responded_by: permission.responded_by.clone(),
                permission: Some(PermissionResolveRequest {
                    interaction_id: permission.interaction_id,
                    requested_by: permission.requested_by,
                    responded_by: permission.responded_by,
                    session_id: permission.session_id,
                    run_id: permission.run_id,
                    choice_id: "pick".into(),
                    granted: input["granted"].as_bool().unwrap_or(true),
                    ..PermissionResolveRequest::default()
                }),
                ..InteractionResolution::default()
            }))
            .unwrap();
        }
        events.push(event);
    }
    let deadline = Instant::now() + Duration::from_secs(10);
    let marker = Path::new(input["case"].as_str().unwrap()).join("finished");
    while !marker.exists() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(1));
    }
    assert!(marker.exists());
    let state = block_on(session.state()).unwrap();
    assert_eq!(state.status.as_str(), "idle");
    assert_eq!(
        state.transcript_cursor,
        events.last().unwrap().sequence.unwrap().to_string()
    );
    let closed = block_on(session.close());
    let descriptor = adapter.describe();
    println!(
        "{}",
        json!({"events":events,"admission":admission,"state":state,"descriptor":descriptor.capabilities,"closeError":closed.err().map(|error|error.to_string())})
    );
}
