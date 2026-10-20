use oap_validation::{codes, Validator};
use serde_json::{json, Value};

fn envelope(mut value: Value) -> Value {
    value["protocol"] = json!("open-agent-protocol");
    value["version"] = json!("0.1");
    value["profile"] = json!("open-agent-protocol.agent-control-core");
    value
}

fn stale_in_window(request: Value) -> bool {
    let events = [
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"}, "features": {}}}),
        ),
        envelope(
            json!({"type": "capabilities.updated", "id": "u1", "sequence": 1, "capability_revision": "v2",
            "payload": {"previous_revision": "v1"}}),
        ),
        envelope(request),
    ];
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "refresh-window");
    result
        .diagnostics
        .iter()
        .any(|d| d.code == codes::STALE_CAPABILITY_REVISION && d.envelope_id == "r1")
}

fn request(kind: &str, payload: Value) -> Value {
    let mut request = json!({"type": kind, "id": "r1", "session_id": "s", "payload": payload});
    if let Some(run) = request["payload"].get("run_id").cloned() {
        request["run_id"] = run;
    }
    request
}

#[test]
fn every_request_before_the_refresh_is_stale() {
    assert!(stale_in_window(request(
        "models.request",
        json!({"session_id": "s"})
    )));
    assert!(stale_in_window(request(
        "session.state.request",
        json!({"session_id": "s"})
    )));
}

#[test]
fn discovery_and_the_answers_to_work_in_flight_are_exempt() {
    let run = json!({"session_id": "s", "run_id": "run"});
    let owners = json!({"session_id": "s", "run_id": "run", "interaction_id": "i",
        "requested_by": "agent", "responded_by": "user"});
    let with = |extra: Value| {
        let mut payload = owners.clone();
        payload
            .as_object_mut()
            .unwrap()
            .extend(extra.as_object().unwrap().clone());
        payload
    };
    let exempt = [
        request("capabilities.request", json!({})),
        request("run.cancel.request", run),
        request(
            "action.permission.resolve.request",
            with(json!({"granted": true, "choice_id": "approve"})),
        ),
        request(
            "user.input.resolve.request",
            with(json!({"answers": [{"question_id": "q", "text": "a"}]})),
        ),
        request("user.input.cancel.request", owners.clone()),
        request(
            "action.call.resolve.request",
            with(json!({"tool_call_id": "t", "result": {"ok": true}})),
        ),
    ];
    for request in exempt {
        let kind = request["type"].clone();
        assert!(!stale_in_window(request), "{kind}");
    }
}

#[test]
fn pending_interactions_settle_under_the_previous_descriptor_in_the_refresh_window() {
    let validator = Validator::new().unwrap();
    for (name, trace) in [
        (
            "permission",
            include_str!("../../../fixtures/valid/permission-resolved-in-the-refresh-window.json"),
        ),
        (
            "user-input",
            include_str!("../../../fixtures/valid/user-input-resolved-in-the-refresh-window.json"),
        ),
    ] {
        let result = validator.validate(trace.as_bytes(), name);
        assert!(
            result.valid(),
            "{name}: {}",
            serde_json::to_string_pretty(&result).unwrap()
        );
    }
}
