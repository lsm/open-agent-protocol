use oap_validation::{codes, Validator};
use serde_json::{json, Value};

fn envelope(mut value: Value) -> Value {
    value["protocol"] = json!("open-agent-protocol");
    value["version"] = json!("0.1");
    value["profile"] = json!("open-agent-protocol.agent-control-core");
    value
}

fn codes_when_started_with(model: Option<&str>) -> Vec<String> {
    let mut started = json!({"session_id": "s", "run_id": "r", "status": "running"});
    if let Some(model) = model {
        started["model_id"] = json!(model);
    }
    let events = [
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"},
                "features": {"run.model_selection": {"level": "native", "scope": "run"}}}}),
        ),
        envelope(
            json!({"type": "session.message.submit.request", "id": "m1", "session_id": "s", "capability_revision": "v1",
            "payload": {"session_id": "s", "messages": [{"role": "user", "content": "go"}], "delivery": "auto", "model_id": "a"}}),
        ),
        envelope(
            json!({"type": "session.message.submit.response", "id": "m2", "in_reply_to": "m1", "session_id": "s",
            "capability_revision": "v1",
            "payload": {"session_id": "s", "accepted": true, "submission_id": "sub", "requested_delivery": "auto",
                "effective_delivery": "start", "admission": "started", "run_id": "r", "status": "running", "model_id": "a"}}),
        ),
        envelope(
            json!({"type": "run.started", "id": "e1", "session_id": "s", "run_id": "r", "sequence": 1,
            "capability_revision": "v1", "payload": started}),
        ),
        envelope(
            json!({"type": "run.failed", "id": "e2", "session_id": "s", "run_id": "r", "sequence": 2,
            "capability_revision": "v1",
            "payload": {"session_id": "s", "run_id": "r", "error": {"code": "x", "message": "x"}}}),
        ),
    ];
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "started-model");
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

#[test]
fn run_started_must_name_the_model_its_run_was_admitted_under() {
    assert_eq!(codes_when_started_with(Some("a")), Vec::<String>::new());
    assert_eq!(
        codes_when_started_with(Some("b")),
        [codes::UNAPPLIED_CONTROL]
    );
    assert_eq!(codes_when_started_with(None), [codes::UNAPPLIED_CONTROL]);
}
