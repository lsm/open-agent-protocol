mod support;

use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use oxap_adapter::runtime::Runtime;
use oxap_adapter::testkit::{drain, RunTrace};
use oxap_adapter_codex_appserver::adapter::Adapter;
use oxap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    MessageSubmitResponse, RequestedDeliveryMode, RunCompletedPayload,
};
use serde_json::value::RawValue;
use serde_json::{json, Value};
use support::{adapter, request, Fake};

const SCHEMA: &str =
    r#"{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}"#;

fn submission(schema: Option<&str>) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "s".into(),
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text("hi"),
            ..Message::default()
        }],
        delivery: RequestedDeliveryMode::AUTO,
        output_schema: schema.map(|schema| RawValue::from_string(schema.into()).unwrap()),
        ..MessageSubmitRequest::default()
    }
}

fn run(schema: Option<&str>, notify: impl FnOnce(&Fake)) -> Vec<Envelope> {
    let fake = Fake::new([
        Ok(r#"{"thread":{"id":"t-1"}}"#),
        Ok(r#"{"turn":{"id":"turn-1","status":"inProgress"}}"#),
    ]);
    let clock = Arc::new(AtomicI64::new(0));
    let next = Arc::new(AtomicU64::new(0));
    let adapter = adapter(&fake, |config| {
        config.runtime = Runtime::new(
            move || clock.fetch_add(1, Ordering::SeqCst) + 1,
            move |kind| format!("{kind}-{}", next.fetch_add(1, Ordering::SeqCst) + 1),
        );
    });
    let session = adapter.open(request()).unwrap();
    let (admission, mut stream) = session.submit(submission(schema)).unwrap();
    notify(&fake);
    let events = drain(&mut stream, Duration::from_secs(5));
    validate(&adapter, schema, admission, &events);
    events
}

fn validate(
    adapter: &Adapter,
    schema: Option<&str>,
    admission: MessageSubmitResponse,
    events: &[Envelope],
) {
    RunTrace {
        submitted: Some(submission(schema)),
        ..RunTrace::new(admission, adapter.describe(), events.to_vec())
    }
    .assert_valid();
}

fn start(fake: &Fake) {
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-1","turn":{"id":"turn-1"}}"#,
    );
}

fn message(fake: &Fake, thread: &str, text: &str) {
    fake.notify(
        "item/completed",
        &json!({"threadId": thread, "turnId": "turn-1", "item": {"type": "agentMessage", "id": "m", "text": text}})
            .to_string(),
    );
}

fn complete(fake: &Fake) {
    fake.notify(
        "turn/completed",
        r#"{"threadId":"t-1","turn":{"id":"turn-1","status":"completed"}}"#,
    );
}

fn payload(envelope: &Envelope) -> Value {
    serde_json::from_str(envelope.payload.as_ref().unwrap().get()).unwrap()
}

fn result(envelope: &Envelope) -> Option<String> {
    let completed: RunCompletedPayload =
        serde_json::from_str(envelope.payload.as_ref().unwrap().get()).unwrap();
    completed.result.map(|raw| raw.get().to_owned())
}

#[test]
fn the_last_final_message_is_the_compacted_result_as_go_compacts_it() {
    let events = run(Some(SCHEMA), |fake| {
        start(fake);
        fake.notify(
            "item/agentMessage/delta",
            r#"{"threadId":"t-1","turnId":"turn-1","delta":"typed"}"#,
        );
        message(fake, "t-1", r#"{"answer":"draft"}"#);
        message(
            fake,
            "t-1",
            "\u{a0}\n  {\"answer\" : \"y e\\u00e9\\\"s\",\t\"n\": [1.50, 2E3] }\r\n \u{c}",
        );
        complete(fake);
    });
    let last = events.last().unwrap();
    assert_eq!(last.envelope_type, EnvelopeType::RUN_COMPLETED);
    assert_eq!(
        result(last).as_deref(),
        Some(r#"{"answer":"y e\u00e9\"s","n":[1.50,2E3]}"#)
    );
    assert_eq!(payload(last)["final_response"]["content"], "typed");
}

#[test]
fn a_final_message_the_schema_cannot_read_fails_the_run_as_go_does() {
    for (text, message_text) in [
        (
            None,
            "the turn completed without a final assistant message to read the structured result from",
        ),
        (Some("yes"), "the final assistant message is not JSON"),
        (Some(r#"{"answer":"a"} {}"#), "the final assistant message is not JSON"),
        (
            Some(r#"{"answer":7}"#),
            "the final assistant message does not conform to the output_schema",
        ),
    ] {
        let events = run(Some(SCHEMA), |fake| {
            start(fake);
            if let Some(text) = text {
                message(fake, "t-1", text);
            }
            complete(fake);
        });
        let last = events.last().unwrap();
        assert_eq!(last.envelope_type, EnvelopeType::RUN_FAILED, "{text:?}");
        assert_eq!(
            payload(last),
            json!({"session_id": "s", "run_id": "run-2", "error": {"code": "structured_output_failed", "message": message_text}}),
            "{text:?}"
        );
    }
}

#[test]
fn only_a_completed_agent_message_of_the_started_turn_is_read() {
    let events = run(Some(SCHEMA), |fake| {
        message(fake, "t-1", r#"{"answer":"early"}"#);
        start(fake);
        message(fake, "t-2", r#"{"answer":"other"}"#);
        for (method, kind) in [
            ("item/started", "agentMessage"),
            ("item/completed", "reasoning"),
        ] {
            fake.notify(
                method,
                &json!({"threadId": "t-1", "turnId": "turn-1", "item": {"type": kind, "id": "m", "text": r#"{"answer":"no"}"#}})
                    .to_string(),
            );
        }
        complete(fake);
    });
    assert_eq!(
        payload(events.last().unwrap())["error"]["message"],
        "the turn completed without a final assistant message to read the structured result from"
    );
}

#[test]
fn a_run_without_a_schema_carries_no_result() {
    let events = run(None, |fake| {
        start(fake);
        message(fake, "t-1", r#"{"answer":"yes"}"#);
        complete(fake);
    });
    let last = events.last().unwrap();
    assert_eq!(last.envelope_type, EnvelopeType::RUN_COMPLETED);
    assert_eq!(result(last), None);
}

#[test]
fn an_undecodable_item_fails_the_active_run() {
    for method in ["item/started", "item/completed"] {
        let events = run(None, |fake| {
            start(fake);
            fake.notify(method, "[]");
        });
        assert_eq!(
            payload(events.last().unwrap())["error"],
            json!({"code": "invalid_native_event", "message": "invalid item lifecycle payload"}),
            "{method}"
        );
    }
}
