use oap_validation::{codes, Validator};
use serde_json::{json, Value};

fn envelope(mut value: Value) -> Value {
    value["protocol"] = json!("open-agent-protocol");
    value["version"] = json!("0.1");
    value["profile"] = json!("open-agent-protocol.agent-control-core");
    value
}

fn compound_open_refused_for(feature: &str) -> Vec<String> {
    let events = [
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"}, "features": {}}}),
        ),
        envelope(
            json!({"type": "session.open.request", "id": "o1", "capability_revision": "v1",
            "payload": {"session_id": "s", "subscribe": true,
                "message": {"messages": [{"role": "user", "content": "go"}], "delivery": "auto", "model_id": "m"}}}),
        ),
        envelope(
            json!({"type": "error.response", "id": "o2", "in_reply_to": "o1",
            "payload": {"error": {"code": "unsupported_feature", "message": "no",
                "details": {"feature": feature, "reason": "unadvertised"}}}}),
        ),
    ];
    events.iter().map(Value::to_string).collect()
}

fn codes_for(feature: &str) -> Vec<String> {
    let trace = compound_open_refused_for(feature).join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "refusals");
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

#[test]
fn a_refusal_answering_any_of_an_open_s_unadvertised_elections_conforms() {
    assert_eq!(codes_for("session.open.subscribe"), Vec::<String>::new());
    assert_eq!(codes_for("run.model_selection"), Vec::<String>::new());
    assert_eq!(
        codes_for("run.instructions"),
        [codes::UNAVAILABLE_CAPABILITY]
    );
}
