mod support;

use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use oap_adapter::runtime::Runtime;
use oap_adapter::testkit::{drain, RunTrace};
use oap_adapter::EventStream;
use oap_adapter_codex_appserver::adapter::Adapter;
use oap_adapter_codex_appserver::session::Session;
use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    MessageSubmitResponse, RequestedDeliveryMode, SessionStatus,
};
use serde_json::{json, Value};
use support::{adapter, request, Fake};

const TURN: &str = r#"{"turn":{"id":"turn-1","status":"inProgress"}}"#;

fn open() -> (Arc<Fake>, Adapter, Session) {
    let fake = Fake::new([Ok(r#"{"thread":{"id":"t-1"}}"#), Ok(TURN), Ok(TURN)]);
    let clock = Arc::new(AtomicI64::new(0));
    let next = Arc::new(AtomicU64::new(0));
    let adapter = adapter(&fake, |config| {
        config.runtime = Runtime::new(
            move || clock.fetch_add(1, Ordering::SeqCst) + 1,
            move |kind| format!("{kind}-{}", next.fetch_add(1, Ordering::SeqCst) + 1),
        );
    });
    let session = adapter.open(request()).unwrap();
    (fake, adapter, session)
}

fn submission() -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "s".into(),
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text("hi"),
            ..Message::default()
        }],
        delivery: RequestedDeliveryMode::AUTO,
        ..MessageSubmitRequest::default()
    }
}

fn submit(session: &Session) -> (MessageSubmitResponse, EventStream) {
    session.submit(submission()).unwrap()
}

fn events(stream: &mut EventStream) -> Vec<Envelope> {
    drain(stream, Duration::from_secs(5))
}

fn payload(envelope: &Envelope) -> Value {
    serde_json::from_str(envelope.payload.as_ref().unwrap().get()).unwrap()
}

fn completed(status: &str, error: Value) -> String {
    json!({"threadId": "t-1", "turn": {"id": "turn-1", "status": status, "error": error}})
        .to_string()
}

fn validate(adapter: &Adapter, admission: MessageSubmitResponse, events: &[Envelope]) {
    RunTrace {
        submitted: Some(submission()),
        ..RunTrace::new(admission, adapter.describe(), events.to_vec())
    }
    .assert_valid();
}

#[test]
fn a_turn_streams_its_start_and_completion_as_go_emits_them() {
    let (fake, adapter, session) = open();
    let (admission, mut stream) = submit(&session);
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-1","turn":{"id":"turn-1","status":"inProgress"}}"#,
    );
    fake.notify("turn/completed", &completed("completed", Value::Null));
    let events = events(&mut stream);
    validate(&adapter, admission, &events);
    let shape: Vec<_> = events
        .iter()
        .map(|envelope| {
            (
                envelope.envelope_type.clone(),
                envelope.id.as_str().to_owned(),
                envelope.sequence,
                envelope.timestamp_ms,
            )
        })
        .collect();
    assert_eq!(
        shape,
        [
            (
                EnvelopeType::RUN_STARTED,
                "event-5".into(),
                Some(1),
                Some(5)
            ),
            (
                EnvelopeType::RUN_COMPLETED,
                "event-6".into(),
                Some(2),
                Some(6)
            ),
        ]
    );
    assert_eq!(
        payload(&events[0]),
        json!({"session_id": "s", "run_id": "run-2", "status": "running", "model_id": "gpt-test", "started_at_ms": 4})
    );
    assert_eq!(
        payload(&events[1]),
        json!({"session_id": "s", "run_id": "run-2", "final_response": {"id": "message-3", "role": "assistant", "content": ""}, "stop_reason": "end_turn"})
    );
    let state = session.state().unwrap();
    assert_eq!(state.status, SessionStatus::IDLE);
    assert_eq!(state.active_run_id, None);
    assert_eq!(state.transcript_cursor, "2");
    assert_eq!(state.updated_at_ms, 6);
    let (_again, _stream) = submit(&session);
}

#[test]
fn a_repeated_start_and_notifications_for_another_thread_or_turn_are_ignored() {
    let (fake, adapter, session) = open();
    let (admission, mut stream) = submit(&session);
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-2","turn":{"id":"turn-1"}}"#,
    );
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-1","turn":{"id":"turn-9"}}"#,
    );
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-1","turn":{"id":"turn-1"}}"#,
    );
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-1","turn":{"id":"turn-1"}}"#,
    );
    fake.notify("thread/unknown", "{}");
    fake.notify(
        "turn/completed",
        &json!({"threadId": "t-2", "turn": {"id": "turn-1", "status": "failed"}}).to_string(),
    );
    fake.notify("turn/completed", &completed("completed", Value::Null));
    let events = events(&mut stream);
    validate(&adapter, admission, &events);
    let kinds: Vec<_> = events.iter().map(|e| e.envelope_type.clone()).collect();
    assert_eq!(
        kinds,
        [EnvelopeType::RUN_STARTED, EnvelopeType::RUN_COMPLETED]
    );
    assert_eq!(payload(&events[1])["final_response"]["content"], "");
}

#[test]
fn an_event_reads_its_payload_clock_then_mints_its_id_then_stamps_it() {
    let fake = Fake::new([Ok(r#"{"thread":{"id":"t-1"}}"#), Ok(TURN)]);
    let shared = Arc::new(AtomicU64::new(0));
    let ticks = shared.clone();
    let adapter = adapter(&fake, |config| {
        config.runtime = Runtime::new(
            move || ticks.fetch_add(1, Ordering::SeqCst) as i64 + 1,
            move |kind| format!("{kind}-{}", shared.fetch_add(1, Ordering::SeqCst) + 1),
        );
    });
    let session = adapter.open(request()).unwrap();
    let (_admission, mut stream) = submit(&session);
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-1","turn":{"id":"turn-1"}}"#,
    );
    fake.notify("turn/completed", &completed("completed", Value::Null));
    let started = &events(&mut stream)[0];
    let minted: i64 = started.id.as_str()["event-".len()..].parse().unwrap();
    assert_eq!(payload(started)["started_at_ms"], minted - 1);
    assert_eq!(started.timestamp_ms, Some(minted + 1));
}

#[test]
fn each_native_terminal_status_settles_the_run_as_go_does() {
    for (status, error, kind, detail) in [
        (
            "interrupted",
            Value::Null,
            EnvelopeType::RUN_CANCELLED,
            json!({"reason": "native turn interrupted"}),
        ),
        (
            "failed",
            json!({"message": "quota"}),
            EnvelopeType::RUN_FAILED,
            json!({"error": {"code": "native_turn_failed", "message": "quota"}}),
        ),
        (
            "failed",
            json!({"message": ""}),
            EnvelopeType::RUN_FAILED,
            json!({"error": {"code": "native_turn_failed", "message": "native turn failed"}}),
        ),
        (
            "failed",
            Value::Null,
            EnvelopeType::RUN_FAILED,
            json!({"error": {"code": "native_turn_failed", "message": "native turn failed"}}),
        ),
        (
            "inProgress",
            Value::Null,
            EnvelopeType::RUN_FAILED,
            json!({"error": {"code": "invalid_native_terminal", "message": "turn/completed did not contain a terminal status"}}),
        ),
    ] {
        let (fake, adapter, session) = open();
        let (admission, mut stream) = submit(&session);
        fake.notify(
            "turn/started",
            r#"{"threadId":"t-1","turn":{"id":"turn-1"}}"#,
        );
        fake.notify("turn/completed", &completed(status, error));
        let events = events(&mut stream);
        if kind != EnvelopeType::RUN_CANCELLED {
            validate(&adapter, admission, &events);
        }
        let last = events.last().unwrap();
        assert_eq!(last.envelope_type, kind, "{status}");
        let mut wanted = json!({"session_id": "s", "run_id": "run-2"});
        wanted
            .as_object_mut()
            .unwrap()
            .extend(detail.as_object().unwrap().clone());
        assert_eq!(payload(last), wanted, "{status}");
        assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
    }
}

#[test]
fn an_undecodable_notification_fails_the_active_run() {
    for (method, message) in [
        ("turn/started", "invalid turn/started payload"),
        ("turn/completed", "invalid turn/completed payload"),
    ] {
        let (fake, adapter, session) = open();
        let (admission, mut stream) = submit(&session);
        fake.notify(method, "[]");
        let events = events(&mut stream);
        validate(&adapter, admission, &events);
        assert_eq!(events.len(), 1, "{method}");
        assert_eq!(
            payload(&events[0]),
            json!({"session_id": "s", "run_id": "run-2", "error": {"code": "invalid_native_event", "message": message}})
        );
    }
}

#[test]
fn a_settled_run_lets_the_session_close() {
    let (fake, _adapter, session) = open();
    let (_admission, mut stream) = submit(&session);
    fake.notify(
        "turn/started",
        r#"{"threadId":"t-1","turn":{"id":"turn-1"}}"#,
    );
    fake.notify("turn/completed", &completed("completed", Value::Null));
    let _ = events(&mut stream);
    session.close().unwrap();
    assert_eq!(fake.closes(), 1);
}
