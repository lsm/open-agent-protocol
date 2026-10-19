use oap_validation::{codes, Validator};
use serde_json::{json, Value};

fn envelope(mut value: Value) -> Value {
    value["protocol"] = json!("open-agent-protocol");
    value["version"] = json!("0.1");
    value["profile"] = json!("open-agent-protocol.agent-control-core");
    value
}

fn scope_mismatches(
    asked: Option<&str>,
    payload_session: Option<&str>,
    envelope_session: Option<&str>,
) -> usize {
    let mut request = json!({"type": "action.tools.list.request", "id": "l1", "capability_revision": "v1", "payload": {}});
    if let Some(session) = asked {
        request["payload"]["session_id"] = json!(session);
    }
    let mut response = json!({"type": "action.tools.list.response", "id": "l2", "in_reply_to": "l1",
        "capability_revision": "v1", "payload": {"tools": [], "sources": []}});
    if let Some(session) = payload_session {
        response["payload"]["session_id"] = json!(session);
    }
    if let Some(session) = envelope_session {
        response["session_id"] = json!(session);
    }
    let events = [
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"}, "features": {"action.tools.list": {"level": "native"}}}}),
        ),
        envelope(request),
        envelope(response),
    ];
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "list-scope");
    result
        .diagnostics
        .iter()
        .filter(|d| d.code == codes::SCOPE_MISMATCH)
        .count()
}

#[test]
fn a_session_scoped_catalog_names_its_session_on_the_envelope() {
    assert_eq!(scope_mismatches(Some("s"), Some("s"), Some("s")), 0);
    assert_eq!(scope_mismatches(Some("s"), Some("s"), None), 1);
}

#[test]
fn an_unscoped_list_request_is_answered_with_no_session_in_the_payload_or_on_the_envelope() {
    assert_eq!(scope_mismatches(None, None, None), 0);
    assert_eq!(scope_mismatches(None, None, Some("s")), 1);
    assert_eq!(scope_mismatches(None, Some("s"), Some("s")), 1);
}

#[test]
fn a_catalog_answering_another_kind_of_request_is_not_judged_as_a_list_response() {
    let events = [
        envelope(
            json!({"type": "session.message.submit.request", "id": "m1", "session_id": "s",
            "payload": {"session_id": "s", "messages": [{"role": "user", "content": "go"}], "delivery": "auto"}}),
        ),
        envelope(
            json!({"type": "action.tools.list.response", "id": "l2", "in_reply_to": "m1",
            "capability_revision": "v1", "payload": {"session_id": "s", "tools": [], "sources": []}}),
        ),
    ];
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "list-scope");
    assert!(result
        .diagnostics
        .iter()
        .all(|d| !d.message.contains("catalog must name its session")));
}
