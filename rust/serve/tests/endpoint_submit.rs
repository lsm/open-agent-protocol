mod support;

use std::time::{Duration, Instant};

use oap_serve::endpoint::{Endpoint, EndpointOptions};
use oap_serve::{Hub, Registry};
use serde_json::{json, Value};

fn run(hub: &Hub, lines: &[Value]) -> Vec<Value> {
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
    endpoint
        .run(input.join("\n").as_bytes(), out.clone())
        .unwrap();
    out.values()
}

fn request(kind: &str, id: &str, session: Option<&str>, payload: Value) -> Value {
    let mut envelope = json!({
        "protocol": "oap",
        "version": "0.1",
        "profile": "open-agent-protocol.agent-control-core",
        "type": kind,
        "id": id,
        "payload": payload,
    });
    if let Some(session) = session {
        envelope["session_id"] = session.into();
    }
    envelope
}

fn submit(id: &str, session: Option<&str>, payload_session: &str) -> Value {
    request(
        "session.message.submit.request",
        id,
        session,
        json!({
            "session_id": payload_session,
            "delivery": "auto",
            "messages": [{"role": "user", "content": "run"}],
        }),
    )
}

fn open() -> Value {
    request(
        "session.open.request",
        "o",
        None,
        json!({"session_id": "s"}),
    )
}

#[test]
fn a_submit_answers_its_admission_and_then_streams_the_run_it_admitted() {
    let hub = Hub::new(Registry::default_registry());
    let started = Instant::now();
    let answered = run(&hub, &[open(), submit("m", Some("s"), "s")]);
    assert!(started.elapsed() < Duration::from_secs(5));
    let answer = &answered[1];
    assert_eq!(
        answer["type"], "session.message.submit.response",
        "{answer}"
    );
    assert_eq!(answer["in_reply_to"], "m");
    assert_eq!(answer["session_id"], "s");
    let run = answer["run_id"].as_str().unwrap();
    assert_eq!(answer["payload"]["admission"], "started");
    let events: Vec<&Value> = answered[2..].iter().collect();
    assert_eq!(events[0]["type"], "run.started", "{events:?}");
    assert!(events.iter().all(|event| event["run_id"] == run));
    let sequences: Vec<u64> = events
        .iter()
        .map(|event| event["sequence"].as_u64().unwrap())
        .collect();
    assert_eq!(sequences, (1..=sequences.len() as u64).collect::<Vec<_>>());
}

#[test]
fn a_submit_is_refused_without_its_session_or_naming_another_one() {
    let hub = Hub::new(Registry::default_registry());
    let answered = run(
        &hub,
        &[
            open(),
            submit("a", None, "s"),
            submit("b", Some("ghost"), "ghost"),
            submit("c", Some("s"), "t"),
        ],
    );
    let code = |answer: &Value| answer["payload"]["error"]["code"].clone();
    assert_eq!(code(&answered[1]), "invalid_request");
    assert_eq!(code(&answered[2]), "unknown_session");
    assert_eq!(code(&answered[3]), "scope_mismatch", "{}", answered[3]);
    assert_eq!(answered.len(), 4, "{answered:?}");
}
