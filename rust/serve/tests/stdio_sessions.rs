use futures::executor::block_on;
use oap_adapter::OpenRequest;
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn hub_with(ids: &[&str]) -> Hub {
    let hub = Hub::new(Registry::default_registry());
    for id in ids {
        block_on(hub.open(
            "memory",
            OpenRequest {
                session_id: (*id).into(),
                ..OpenRequest::default()
            },
        ))
        .unwrap();
    }
    hub
}

fn answers(hub: &Hub, lines: &[&str]) -> Vec<Value> {
    let input: String = lines.iter().map(|line| format!("{line}\n")).collect();
    let mut output = Vec::new();
    serve(hub, input.as_bytes(), &mut output, &StdioOptions::default()).unwrap();
    String::from_utf8(output)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

#[test]
fn state_answers_a_session_state_response_envelope() {
    let hub = hub_with(&["s"]);
    let lines = answers(&hub, &[r#"{"id":1,"op":"state","session_id":"s"}"#]);
    let envelope = &lines[0]["result"];
    assert_eq!(lines[0]["ok"], true);
    assert_eq!(envelope["type"], "session.state.response");
    assert_eq!(envelope["id"], "oap-response-1");
    assert_eq!(envelope["in_reply_to"], "oap-request-2");
    assert_eq!(envelope["session_id"], "s");
    assert_eq!(envelope["payload"]["session_id"], "s");
    assert_eq!(envelope["payload"]["status"], "idle");
}

#[test]
fn an_op_on_a_session_the_hub_does_not_hold_is_unknown_session() {
    let hub = hub_with(&[]);
    for line in [
        r#"{"id":1,"op":"state","session_id":"ghost"}"#,
        r#"{"id":1,"op":"close","session_id":"ghost"}"#,
    ] {
        let lines = answers(&hub, &[line]);
        assert_eq!(lines[0]["error"]["code"], "unknown_session", "{line}");
        assert_eq!(lines[0]["error"]["message"], "no session \"ghost\"");
    }
    let lines = answers(&hub, &[r#"{"id":1,"op":"state","adapter":"memory"}"#]);
    assert_eq!(lines[0]["error"]["code"], "invalid_request");
}

#[test]
fn sessions_lists_each_live_session_with_its_adapter_status_and_creation_time() {
    let hub = hub_with(&["b", "a"]);
    let lines = answers(&hub, &[r#"{"id":1,"op":"sessions"}"#]);
    let sessions = lines[0]["result"]["sessions"].as_array().unwrap();
    let ids: Vec<&str> = sessions
        .iter()
        .map(|session| session["session_id"].as_str().unwrap())
        .collect();
    assert_eq!(ids, ["a", "b"]);
    assert_eq!(sessions[0]["adapter"], "memory");
    assert_eq!(sessions[0]["status"], "idle");
    let created = sessions[0]["created_at"].as_str().unwrap();
    assert_eq!(created.len(), 20, "{created}");
    assert!(
        created.ends_with('Z') && created.as_bytes()[10] == b'T',
        "{created}"
    );
    assert!(created.starts_with("20"), "{created}");
}

#[test]
fn close_answers_null_and_the_session_is_gone_after_it() {
    let hub = hub_with(&["s"]);
    let lines = answers(
        &hub,
        &[
            r#"{"id":1,"op":"close","session_id":"s"}"#,
            r#"{"id":2,"op":"state","session_id":"s"}"#,
            r#"{"id":3,"op":"sessions"}"#,
        ],
    );
    assert_eq!(lines[0]["ok"], true);
    assert!(lines[0]["result"].is_null());
    assert_eq!(lines[1]["error"]["code"], "unknown_session");
    assert_eq!(lines[2]["result"]["sessions"], serde_json::json!([]));
}
