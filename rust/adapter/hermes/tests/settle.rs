mod support;

use std::time::Duration;

use oap_adapter::gojson::go_marshal;
use oap_adapter::testkit::drain;
use oap_adapter::EventStream;
use support::{opened, submission, Fake};

const STREAMING: &str = r#"{"status":"streaming"}"#;
const SETTLE: &str = r#"{"text":"done","usage":{"model":"m","input":3,"output":5,"reasoning":1,"prompt":3,"completion":5,"total":8,"calls":2},"status":"complete"}"#;

fn events(stream: &mut EventStream) -> Vec<String> {
    drain(stream, Duration::from_secs(5))
        .iter()
        .map(|envelope| go_marshal(&serde_json::to_string(envelope).unwrap()))
        .collect()
}

fn traced(steps: &[(&str, &str)], child: bool) -> Vec<String> {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let (_, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    for (index, (kind, payload)) in steps.iter().enumerate() {
        fake.event(kind, index as i64 + 2, payload);
    }
    if child {
        fake.event("message.complete", steps.len() as i64 + 2, SETTLE);
    }
    events(&mut stream)
}

#[test]
fn each_settlement_maps_to_the_terminal_go_emits() {
    for (name, steps, want) in [
        (
            "complete",
            vec![
                (r#"message.delta"#, r#"{"text":"He"}"#),
                (r#"message.delta"#, r#"{"text":""}"#),
                (r#"reasoning.delta"#, r#"{"text":"think"}"#),
                (r#"thinking.delta"#, r#"{"text":"t"}"#),
                (r#"message.delta"#, r#"{"text":"llo <&>"}"#),
                (
                    r#"message.complete"#,
                    r#"{"text":"Hello","usage":{"model":"m","input":3,"output":5,"reasoning":1,"prompt":3,"completion":5,"total":8,"calls":2},"status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"event-d","payload":{"session_id":"session","run_id":"run-b","message_id":"message-a","part":{"type":"text","text":"He"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"event-e","payload":{"session_id":"session","run_id":"run-b","message_id":"message-a","part":{"type":"reasoning","reasoning":"think"}},"sequence":3,"timestamp_ms":5,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"event-f","payload":{"session_id":"session","run_id":"run-b","message_id":"message-a","part":{"type":"text","text":"llo \u003c\u0026\u003e"}},"sequence":4,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"Hello"},"stop_reason":"completed","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "partialusage",
            vec![(
                r#"message.complete"#,
                r#"{"text":"x","usage":{"input":3},"status":"complete"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"x"},"stop_reason":"completed","usage":{"input_tokens":3}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "nullusage",
            vec![(
                r#"message.complete"#,
                r#"{"text":"","usage":null,"status":"complete"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":""},"stop_reason":"completed"},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "zerousage",
            vec![(
                r#"message.complete"#,
                r#"{"text":"x","usage":{"input":0,"output":0,"total":0},"status":"complete"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"x"},"stop_reason":"completed","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "negative",
            vec![(
                r#"message.complete"#,
                r#"{"text":"x","usage":{"input":3,"output":-1},"status":"complete"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_event","message":"invalid settlement"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "error",
            vec![(
                r#"message.complete"#,
                r#"{"text":"partial","status":"error","error":"provider down"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_error","message":"provider down"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "interrupted",
            vec![(
                r#"message.complete"#,
                r#"{"text":"half","status":"interrupted"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_interrupted","message":"half"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "bare",
            vec![(
                r#"message.complete"#,
                r#"{"text":"","status":"interrupted"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_interrupted","message":"turn interrupted"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "surface",
            vec![(
                r#"message.complete"#,
                r#"{"text":"","status":"error","error":"limit","error_surface":{"layer":"provider","code":"rate_limit","retryable":true}}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_rate_limit","message":"limit"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "emptysurface",
            vec![(
                r#"message.complete"#,
                r#"{"text":"t","status":"error","error":"e","error_surface":{"layer":"provider","code":""}}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_error","message":"e"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "child",
            vec![(r#"message.complete"#, r#"{"text":"sub"}"#)],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_settlement","message":"child-mirror settlement on the parent stream"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
    ] {
        assert_eq!(traced(&steps, name == "child"), want, "{name}");
    }
}

#[test]
fn an_event_seen_before_the_turn_opened_is_replayed_after_the_run_starts() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let (_, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.event("message.delta", 1, r#"{"text":"early"}"#);
        fake.event("message.start", 2, "");
        submitted.join().unwrap().unwrap()
    });
    fake.event("message.complete", 3, SETTLE);
    assert_eq!(
        events(&mut stream),
        [
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"event-d","payload":{"session_id":"session","run_id":"run-b","message_id":"message-a","part":{"type":"text","text":"early"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-e","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"done"},"stop_reason":"completed","usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}},"sequence":3,"timestamp_ms":5,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#
        ]
    );
    assert_eq!(
        serde_json::to_string(&session.state().unwrap().status).unwrap(),
        r#""idle""#
    );
}
