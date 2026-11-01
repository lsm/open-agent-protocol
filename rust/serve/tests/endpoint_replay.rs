mod support;

use std::time::Duration;

use oap_serve::endpoint::{Endpoint, EndpointError, EndpointOptions};
use oap_serve::{Hub, Registry};
use serde_json::{json, Value};

fn run(hub: &Hub, lines: &[Value]) -> (Vec<Value>, Result<(), EndpointError>) {
    let endpoint = Endpoint::new(
        hub,
        EndpointOptions {
            adapter: "memory".into(),
            shutdown: Some(Duration::from_millis(200)),
            ..EndpointOptions::default()
        },
    )
    .unwrap();
    let input: Vec<String> = lines.iter().map(Value::to_string).collect();
    let out = support::Captured::default();
    let result = endpoint.run(std::io::Cursor::new(input.join("\n")), out.clone());
    (out.values(), result)
}

fn request(kind: &str, id: &str, payload: Value) -> Value {
    json!({
        "protocol": "oap",
        "version": "0.1",
        "profile": "open-agent-protocol.agent-control-core",
        "type": kind,
        "id": id,
        "session_id": "s",
        "payload": payload,
    })
}

fn started_run(hub: &Hub) -> String {
    let (answered, _) = run(
        hub,
        &[
            request("session.open.request", "o", json!({"session_id": "s"})),
            request(
                "session.message.submit.request",
                "m",
                json!({"session_id": "s", "delivery": "auto", "messages": [{"role": "user", "content": "run"}]}),
            ),
        ],
    );
    answered
        .iter()
        .find(|line| line["in_reply_to"] == "m")
        .unwrap()["run_id"]
        .as_str()
        .unwrap()
        .to_owned()
}

#[test]
fn a_replay_is_accepted_and_then_streams_the_run_from_its_cursor() {
    let hub = Hub::new(Registry::default_registry());
    let run_id = started_run(&hub);
    let (answered, result) = run(
        &hub,
        &[
            json!({"control": "replay", "id": "r1", "session_id": "s", "run_id": run_id, "after": 0}),
        ],
    );
    result.unwrap();
    assert_eq!(
        answered[0],
        json!({"control": "replay.accepted", "id": "r1", "session_id": "s", "run_id": run_id, "after": 0})
    );
    assert_eq!(answered[1]["type"], "run.started", "{answered:?}");
    assert_eq!(answered[1]["sequence"], 1);
    assert!(answered[1..]
        .iter()
        .all(|event| event["run_id"] == run_id.as_str()));
    let (resumed, _) = run(
        &hub,
        &[json!({"control": "replay", "session_id": "s", "run_id": run_id, "after": 1})],
    );
    assert_eq!(resumed[0]["after"], 1);
    assert_eq!(resumed[1]["sequence"], 2, "{resumed:?}");
}

#[test]
fn a_replay_that_cannot_be_served_is_answered_with_its_reason() {
    let hub = Hub::new(Registry::default_registry());
    let run_id = started_run(&hub);
    let (answered, result) = run(
        &hub,
        &[
            json!({"control": "replay", "id": "a"}),
            json!({"control": "replay", "id": "b", "session_id": "ghost"}),
            json!({"control": "replay", "id": "c", "session_id": "s", "run_id": run_id, "after": 1000}),
        ],
    );
    result.unwrap();
    let codes: Vec<(&str, &str)> = answered
        .iter()
        .map(|line| {
            (
                line["control"].as_str().unwrap(),
                line["code"].as_str().unwrap(),
            )
        })
        .collect();
    assert_eq!(
        codes,
        [
            ("replay.error", "invalid_request"),
            ("replay.error", "unknown_session"),
            ("replay.error", "replay_cursor_future"),
        ],
        "{answered:?}"
    );
    assert_eq!(answered[1]["session_id"], "ghost");
}

#[test]
fn a_replay_whose_cursor_is_not_a_sequence_ends_serving() {
    let hub = Hub::new(Registry::default_registry());
    let (answered, result) = run(
        &hub,
        &[json!({"control": "replay", "session_id": "s", "after": "x"})],
    );
    assert!(answered.is_empty());
    assert!(
        matches!(result, Err(EndpointError::MalformedLine(_))),
        "{result:?}"
    );
}
