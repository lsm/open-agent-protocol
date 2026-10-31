mod support;

use futures::executor::block_on;
use oap_protocol::{ActionCallResolveRequest, Envelope, EnvelopeType};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn opened() -> Hub {
    let hub = Hub::new(Registry::default_registry());
    block_on(hub.open(
        "memory",
        oap_adapter::OpenRequest {
            session_id: "s".into(),
            ..oap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    hub
}

fn call(session: &str) -> ActionCallResolveRequest {
    ActionCallResolveRequest {
        interaction_id: "call-1".into(),
        session_id: session.into(),
        run_id: "run-1".into(),
        tool_call_id: "tool-1".into(),
        requested_by: "agent".into(),
        responded_by: "user".into(),
        result: Some(serde_json::value::to_raw_value(&serde_json::json!({"ok": true})).unwrap()),
        ..ActionCallResolveRequest::default()
    }
}

fn resolve_line(id: i64, request: &ActionCallResolveRequest) -> String {
    let mut envelope = Envelope::new(
        EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
        format!("resolve-{id}"),
        request,
    )
    .unwrap();
    envelope.session_id = Some("s".into());
    envelope.run_id = Some("run-1".into());
    format!(
        r#"{{"id":{id},"op":"resolve","session_id":"s","request":{}}}"#,
        serde_json::to_string(&envelope).unwrap()
    )
}

fn lines(hub: &Hub, input: &[String]) -> Vec<Value> {
    let input: String = input.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(
        hub,
        input.as_bytes(),
        output.clone(),
        &StdioOptions::default(),
    )
    .unwrap();
    String::from_utf8(output.bytes())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

#[test]
fn a_call_resolution_answers_the_adapters_verdict_in_reply_to_its_request() {
    let hub = opened();
    let answered = lines(&hub, &[resolve_line(1, &call("s"))]);
    let response = &answered[0]["result"];
    assert_eq!(
        response["type"], "action.call.resolve.response",
        "{}",
        answered[0]
    );
    assert_eq!(response["in_reply_to"], "resolve-1");
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["run_id"], "run-1");
    assert_eq!(response["payload"]["tool_call_id"], "tool-1");
    assert_eq!(response["payload"]["accepted"], false);
    assert!(response["payload"]["reason"].is_string());
}

#[test]
fn a_call_resolution_for_another_session_is_a_scope_mismatch() {
    let hub = opened();
    let answered = lines(&hub, &[resolve_line(1, &call("elsewhere"))]);
    assert_eq!(
        answered[0]["error"]["code"], "scope_mismatch",
        "{}",
        answered[0]
    );
}
