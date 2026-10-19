use oap_validation::{codes, Validator};
use serde_json::{json, Value};

const HEAD: [(&str, &str); 3] = [
    ("protocol", "open-agent-protocol"),
    ("version", "0.1"),
    ("profile", "open-agent-protocol.agent-control-core"),
];

fn envelope(mut value: Value) -> Value {
    for (key, field) in HEAD {
        value[key] = json!(field);
    }
    value
}

fn call(id: &str, kind: &str, sequence: u64) -> Value {
    let mut payload = json!({"session_id": "s", "run_id": "r", "tool_call_id": "t", "execution_owner": "agent", "name": "x"});
    match kind {
        "requested" => {
            payload["arguments_json"] = json!({});
            payload["requested_by"] = json!("agent");
        }
        "progress" => payload["progress"] = json!({}),
        "completed" => payload["result"] = json!({}),
        _ => {}
    }
    envelope(json!({
        "type": format!("action.call.{kind}"), "id": id, "session_id": "s", "run_id": "r", "sequence": sequence,
        "tool_call_id": "t", "capability_revision": "v1", "payload": payload
    }))
}

fn trace(steps: &[&str]) -> Vec<String> {
    let mut events = vec![
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"}, "features": {"tools": {"level": "native"}}}}),
        ),
        envelope(
            json!({"type": "session.message.submit.request", "id": "m1", "session_id": "s",
            "payload": {"session_id": "s", "messages": [{"role": "user", "content": "go"}], "delivery": "auto"}}),
        ),
        envelope(
            json!({"type": "session.message.submit.response", "id": "m2", "in_reply_to": "m1", "session_id": "s",
            "payload": {"session_id": "s", "accepted": true, "submission_id": "sub", "requested_delivery": "auto",
                "effective_delivery": "start", "admission": "started", "run_id": "r", "status": "running"}}),
        ),
        envelope(
            json!({"type": "run.started", "id": "e1", "session_id": "s", "run_id": "r", "sequence": 1,
            "payload": {"session_id": "s", "run_id": "r", "status": "running"}}),
        ),
    ];
    let mut sequence = 2;
    for (index, kind) in steps.iter().enumerate() {
        events.push(call(&format!("t{index}"), kind, sequence));
        sequence += 1;
    }
    events.push(envelope(json!({"type": "run.completed", "id": "done", "session_id": "s", "run_id": "r", "sequence": sequence,
        "payload": {"session_id": "s", "run_id": "r", "final_response": {"role": "assistant", "content": "ok"}, "stop_reason": "end_turn"}})));
    let result = Validator::new()
        .unwrap()
        .validate(Value::Array(events).to_string().as_bytes(), "tools");
    let mut found: Vec<String> = result
        .diagnostics
        .iter()
        .map(|d| {
            format!(
                "{}{}",
                d.code,
                if d.code == "schema_invalid" {
                    format!(" {}", d.message)
                } else {
                    String::new()
                }
            )
        })
        .collect();
    found.sort();
    found
}

#[test]
fn a_call_moves_requested_started_completed_and_any_step_out_of_order_is_illegal() {
    assert_eq!(
        trace(&["requested", "started", "progress", "completed"]),
        Vec::<String>::new()
    );
    assert_eq!(trace(&["requested", "cancelled"]), Vec::<String>::new());
    assert_eq!(
        trace(&["requested", "started", "started", "completed"]),
        [codes::ILLEGAL_TOOL_TRANSITION]
    );
    assert_eq!(
        trace(&["requested", "completed"]),
        [codes::ILLEGAL_TOOL_TRANSITION]
    );
    assert_eq!(
        trace(&["requested", "started", "completed", "cancelled"]),
        [codes::ILLEGAL_TOOL_TRANSITION]
    );
    assert_eq!(
        trace(&["started"]),
        [codes::PENDING_TOOL_AT_TERMINAL, codes::UNMATCHED_TOOL]
    );
    assert_eq!(
        trace(&["requested", "started"]),
        [codes::PENDING_TOOL_AT_TERMINAL]
    );
}
