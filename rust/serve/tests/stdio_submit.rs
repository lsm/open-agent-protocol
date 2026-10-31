mod support;

use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    RequestedDeliveryMode, SessionCompactRequest,
};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde::Serialize;
use serde_json::Value;

fn envelope(kind: EnvelopeType, id: &str, payload: &impl Serialize) -> String {
    let mut envelope = Envelope::new(kind, id, payload).unwrap();
    envelope.session_id = Some("s".into());
    serde_json::to_string(&envelope).unwrap()
}

fn submit(session: &str) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: session.into(),
        delivery: RequestedDeliveryMode::AUTO,
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text("run"),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    }
}

fn submit_line(id: i64, request: &str) -> String {
    format!(r#"{{"id":{id},"op":"submit","session_id":"s","request":{request}}}"#)
}

fn opened() -> Hub {
    let hub = Hub::new(Registry::default_registry());
    futures::executor::block_on(hub.open(
        "memory",
        oap_adapter::OpenRequest {
            session_id: "s".into(),
            ..oap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    hub
}

fn lines_within(hub: &Hub, input: &[String], options: &StdioOptions) -> Vec<Value> {
    let input: String = input.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(hub, input.as_bytes(), output.clone(), options).unwrap();
    String::from_utf8(output.bytes())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

fn lines(hub: &Hub, input: &[String]) -> Vec<Value> {
    lines_within(hub, input, &StdioOptions::default())
}

#[test]
fn submit_answers_the_admission_in_reply_to_its_request_on_its_run() {
    let hub = opened();
    let answered = lines(
        &hub,
        &[submit_line(
            1,
            &envelope(
                EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
                "submit-1",
                &submit("s"),
            ),
        )],
    );
    let response = &answered[0]["result"];
    assert_eq!(
        response["type"], "session.message.submit.response",
        "{}",
        answered[0]
    );
    assert_eq!(response["in_reply_to"], "submit-1");
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["payload"]["admission"], "started");
    assert_eq!(response["run_id"], response["payload"]["run_id"]);
}

#[test]
fn submit_refuses_another_sessions_payload_and_an_unknown_session() {
    let hub = opened();
    let answered = lines(
        &hub,
        &[
            submit_line(
                1,
                &envelope(
                    EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
                    "submit-1",
                    &submit("elsewhere"),
                ),
            ),
            r#"{"id":2,"op":"submit","session_id":"ghost","request":REQ}"#.replace(
                "REQ",
                &envelope(
                    EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
                    "submit-2",
                    &submit("ghost"),
                ),
            ),
        ],
    );
    assert_eq!(
        answered[0]["error"]["code"], "scope_mismatch",
        "{}",
        answered[0]
    );
    assert_eq!(
        answered[1]["error"]["code"], "unknown_session",
        "{}",
        answered[1]
    );
}

#[test]
fn a_compaction_is_admitted_on_the_submit_op_and_a_steered_one_is_refused() {
    let hub = opened();
    let compact = |delivery| SessionCompactRequest {
        session_id: "s".into(),
        delivery,
        ..SessionCompactRequest::default()
    };
    let answered = lines(
        &hub,
        &[
            submit_line(
                1,
                &envelope(
                    EnvelopeType::SESSION_COMPACT_REQUEST,
                    "compact-1",
                    &compact(RequestedDeliveryMode::AUTO),
                ),
            ),
            submit_line(
                2,
                &envelope(
                    EnvelopeType::SESSION_COMPACT_REQUEST,
                    "compact-2",
                    &compact(RequestedDeliveryMode::STEER),
                ),
            ),
        ],
    );
    let response = &answered[0]["result"];
    assert_eq!(
        response["type"], "session.compact.response",
        "{}",
        answered[0]
    );
    assert_eq!(response["in_reply_to"], "compact-1");
    assert_eq!(response["run_id"], response["payload"]["run_id"]);
    assert_eq!(
        answered[1]["error"]["code"], "unsupported_feature",
        "{}",
        answered[1]
    );
}

#[test]
fn an_unframable_acknowledgement_is_refused_and_its_run_rolled_back() {
    let hub = opened();
    let line = submit_line(
        1,
        &envelope(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
            &"x".repeat(400),
            &submit("s"),
        ),
    );
    let options = StdioOptions {
        frame_limit: line.len() + 8,
        ..StdioOptions::default()
    };
    let answered = lines_within(&hub, &[line], &options);
    assert_eq!(
        answered[0]["error"]["code"], "response_too_large",
        "{}",
        answered[0]
    );
    assert!(answered[0]["error"]["message"]
        .as_str()
        .unwrap()
        .ends_with("; the run was cancelled"));
    let state = futures::executor::block_on(hub.session(&"s".into()).unwrap().state()).unwrap();
    assert!(state.active_run_id.is_none(), "{:?}", state.active_run_id);
    assert!(state.active_runs.is_empty());
}

#[test]
fn an_unframable_steer_acknowledgement_leaves_the_run_it_steered_running() {
    let hub = opened();
    let first = submit_line(
        1,
        &envelope(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
            "submit-1",
            &submit("s"),
        ),
    );
    let steer = submit_line(
        2,
        &envelope(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
            &"x".repeat(400),
            &MessageSubmitRequest {
                delivery: RequestedDeliveryMode::STEER,
                ..submit("s")
            },
        ),
    );
    let options = StdioOptions {
        frame_limit: steer.len() + 8,
        ..StdioOptions::default()
    };
    let answered = lines_within(&hub, &[first, steer], &options);
    let run = answered[0]["result"]["run_id"].clone();
    assert_eq!(
        answered[1]["error"]["code"], "response_too_large",
        "{}",
        answered[1]
    );
    assert!(answered[1]["error"]["message"]
        .as_str()
        .unwrap()
        .ends_with("; the steered run was left running"));
    let state = futures::executor::block_on(hub.session(&"s".into()).unwrap().state()).unwrap();
    assert_eq!(
        serde_json::json!(state.active_run_id),
        run,
        "{:?}",
        state.active_runs
    );
}
