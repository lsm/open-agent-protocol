mod support;

use oap_serve::endpoint::{Endpoint, EndpointOptions};
use oap_serve::{Hub, Registry};
use serde_json::{json, Value};

fn run(hub: &Hub, lines: &[Value]) -> Vec<Value> {
    let endpoint = Endpoint::new(
        hub,
        EndpointOptions {
            adapter: "memory".into(),
            ..EndpointOptions::default()
        },
    )
    .unwrap();
    let input: Vec<String> = lines.iter().map(Value::to_string).collect();
    let out = support::Captured::default();
    endpoint
        .run(std::io::Cursor::new(input.join("\n")), out.clone())
        .unwrap();
    out.values()
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

fn switch(id: &str, model: &str) -> Value {
    request(
        "session.model.switch.request",
        id,
        json!({"session_id": "s", "model_id": model}),
    )
}

#[test]
fn a_model_switch_answers_and_publishes_the_state_naming_the_new_model() {
    let hub = Hub::new(Registry::default_registry());
    let listed = run(
        &hub,
        &[
            request("session.open.request", "o", json!({"session_id": "s"})),
            request("models.request", "m", json!({"session_id": "s"})),
        ],
    );
    let current = listed[0]["payload"]["current_model_id"].clone();
    let other = listed[1]["payload"]["models"]
        .as_array()
        .unwrap()
        .iter()
        .map(|model| model["id"].clone())
        .find(|id| *id != current)
        .expect("the memory catalog holds a second model");
    let answered = run(
        &hub,
        &[switch("w", other.as_str().unwrap()), switch("x", "absent")],
    );
    assert_eq!(
        answered[0]["type"], "session.model.switch.response",
        "{answered:?}"
    );
    assert_eq!(answered[0]["payload"]["model_id"], other);
    assert_eq!(answered[1]["type"], "session.state.updated");
    assert_eq!(answered[1]["payload"]["current_model_id"], other);
    assert_eq!(answered[1]["sequence"], 1);
    let refused = &answered[2]["payload"]["error"];
    assert_eq!(refused["code"], "model_not_found", "{refused}");
    assert_eq!(refused["details"]["model_id"], "absent");
}
