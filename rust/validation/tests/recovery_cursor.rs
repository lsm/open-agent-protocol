use oap_validation::{codes, Validator};
use serde_json::{json, Value};

fn envelope(mut value: Value) -> Value {
    value["protocol"] = json!("open-agent-protocol");
    value["version"] = json!("0.1");
    value["profile"] = json!("open-agent-protocol.agent-control-core");
    value
}

fn undeclared(cursor: &str) -> bool {
    undeclared_in(json!({"recovered": true, "resume_cursor": cursor}))
}

fn undeclared_in(recovery: Value) -> bool {
    let events = [
        envelope(
            json!({"type": "session.open.request", "id": "o1", "session_id": "s",
            "payload": {"session_id": "s", "recovery": recovery}}),
        ),
        envelope(
            json!({"type": "session.open.response", "id": "o2", "in_reply_to": "o1", "session_id": "s",
            "payload": {"session_id": "s", "status": "idle", "recovery": recovery}}),
        ),
    ];
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "recovery-cursor");
    result
        .diagnostics
        .iter()
        .any(|d| d.code == codes::UNDECLARED_REPLAY_GAP)
}

#[test]
fn a_recovery_cursor_is_an_unsigned_decimal_no_greater_than_two_to_the_fifty_three_minus_one() {
    for valid in ["0", "41", "9007199254740991"] {
        assert!(!undeclared(valid), "{valid}");
    }
    for invalid in [
        "9007199254740992",
        "18446744073709551615",
        "+5",
        "-1",
        "4.0",
        "0x10",
        "abc",
    ] {
        assert!(undeclared(invalid), "{invalid}");
    }
}

#[test]
fn a_cursor_of_another_form_is_judged_whether_or_not_the_open_recovered() {
    assert!(undeclared_in(
        json!({"reason": "replay_gap", "resume_cursor": "abc"})
    ));
    assert!(!undeclared_in(
        json!({"reason": "replay_gap", "resume_cursor": "7"})
    ));
}
