mod prompt_support;

use std::sync::atomic::Ordering;

use futures::{executor::block_on, FutureExt};
use oap_adapter::{message_text, run_to_terminal, Error, RunErrorKind};
use oap_protocol::*;
use prompt_support::ScriptSession;
use serde_json::json;

fn event(kind: EnvelopeType, sequence: u64, payload: serde_json::Value) -> Envelope {
    let mut event = Envelope::new(kind, format!("event-{sequence}"), &payload).unwrap();
    event.run_id = Some("run-1".into());
    event.session_id = Some("session-1".into());
    event.tool_call_id = payload
        .get("tool_call_id")
        .and_then(|id| id.as_str())
        .map(Into::into);
    event.sequence = Some(sequence);
    event
}

fn completed(usage: serde_json::Value) -> Envelope {
    event(
        EnvelopeType::RUN_COMPLETED,
        5,
        json!({
            "session_id":"session-1", "run_id":"run-1", "stop_reason":"end_turn",
            "final_response":{"role":"assistant","content":[
                {"type":"text","text":"é漢"},
                {"type":"reasoning","reasoning":"hidden"},
                {"type":"text","text":"😀"}
            ]}, "usage":usage
        }),
    )
}

#[test]
fn completed_returns_authoritative_parts_usage_and_drops_open_stream() {
    let call = event(
        EnvelopeType::ACTION_CALL_STARTED,
        2,
        json!({"session_id":"session-1","run_id":"run-1","tool_call_id":"call-1","execution_owner":"agent","name":"lookup","arguments_json":{}}),
    );
    let session = ScriptSession::new(
        vec![
            Ok(call.clone()),
            Ok(call),
            Ok(completed(
                json!({"input_tokens":0,"total_tokens":18446744073709551615u64}),
            )),
        ],
        true,
    );
    let outcome = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap();
    assert_eq!(outcome.text, "é漢😀");
    assert_eq!(outcome.run_id.as_ref().unwrap().as_str(), "run-1");
    assert_eq!(outcome.tool_calls, 1);
    let usage = outcome.usage.unwrap();
    assert_eq!(usage.input_tokens, Some(0));
    assert_eq!(usage.output_tokens, None);
    assert_eq!(usage.total_tokens, Some(u64::MAX));
    assert!(outcome.response.is_some());
    assert!(session.dropped.load(Ordering::SeqCst));
}

#[test]
fn usage_absence_and_empty_measurement_remain_distinct() {
    for (wire, expected) in [
        (serde_json::Value::Null, None),
        (json!({}), Some(Usage::default())),
    ] {
        let session = ScriptSession::new(vec![Ok(completed(wire))], false);
        let outcome = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap();
        assert_eq!(outcome.usage, expected);
    }
}

#[test]
fn failed_run_preserves_failure_and_measured_zero() {
    let failed = event(
        EnvelopeType::RUN_FAILED,
        1,
        json!({
            "session_id":"session-1", "run_id":"run-1",
            "error":{"code":"provider_unavailable","message":"offline","retriable":true},
            "usage":{"output_tokens":0}
        }),
    );
    let session = ScriptSession::new(vec![Ok(failed)], true);
    let error = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap_err();
    let RunErrorKind::Failed(failure) = error.kind else {
        panic!("{error:?}")
    };
    assert_eq!(failure.code, "provider_unavailable");
    assert_eq!(failure.retriable, Some(true));
    assert_eq!(error.outcome.usage.as_ref().unwrap().output_tokens, Some(0));
    assert!(error.outcome.response.is_none());
    assert!(session.dropped.load(Ordering::SeqCst));
}

#[test]
fn cancelled_and_exhausted_stream_are_distinct_and_keep_admission_id() {
    for (events, cancelled) in [
        (
            vec![Ok(event(
                EnvelopeType::RUN_CANCELLED,
                1,
                json!({"session_id":"session-1","run_id":"run-1"}),
            ))],
            true,
        ),
        (vec![], false),
    ] {
        let session = ScriptSession::new(events, false);
        let error =
            block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap_err();
        assert_eq!(error.outcome.run_id.as_ref().unwrap().as_str(), "run-1");
        assert_eq!(matches!(error.kind, RunErrorKind::Cancelled), cancelled);
        assert_eq!(matches!(error.kind, RunErrorKind::NoTerminal), !cancelled);
        assert!(session.dropped.load(Ordering::SeqCst));
    }
}

#[test]
fn stream_and_submission_errors_keep_their_source() {
    let session = ScriptSession::new(vec![Err(Error::EventStreamOverflow)], false);
    let error = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap_err();
    assert!(matches!(
        error.kind,
        RunErrorKind::Adapter(Error::EventStreamOverflow)
    ));
    assert!(session.dropped.load(Ordering::SeqCst));
    let mut session = ScriptSession::new(vec![], false);
    session.submit_error = Some(Error::SessionClosed);
    let error = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap_err();
    assert!(matches!(
        error.kind,
        RunErrorKind::Adapter(Error::SessionClosed)
    ));
    assert!(error.outcome.run_id.is_none());
}

#[test]
fn dropping_pending_consumer_releases_its_stream() {
    let session = ScriptSession::new(vec![], true);
    assert!(run_to_terminal(&session, MessageSubmitRequest::default())
        .now_or_never()
        .is_none());
    assert!(session.dropped.load(Ordering::SeqCst));
}

#[test]
fn message_text_accepts_text_parts_and_absence_but_rejects_other_shapes() {
    for (content, expected) in [
        (json!("é漢😀"), "é漢😀"),
        (json!([]), ""),
        (json!(null), ""),
        (
            json!([{"type":"text","text":"one"},{"type":"image","image":{"url":"synthetic:image"}},{"type":"text","text":"two"}]),
            "onetwo",
        ),
    ] {
        let message: Message =
            serde_json::from_value(json!({"role":"assistant","content":content})).unwrap();
        assert_eq!(message_text(&message).unwrap(), expected);
    }
    let message: Message = serde_json::from_value(json!({"content":42})).unwrap();
    assert!(message_text(&message).is_err());
}

#[test]
fn invalid_terminal_payload_and_content_are_typed_errors() {
    let malformed = event(EnvelopeType::RUN_COMPLETED, 1, json!({"run_id":42}));
    let session = ScriptSession::new(vec![Ok(malformed)], true);
    let error = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap_err();
    assert!(matches!(error.kind, RunErrorKind::Decode(_)));
    assert!(session.dropped.load(Ordering::SeqCst));
    let mut malformed = completed(json!({}));
    let mut payload: serde_json::Value = malformed.decode_payload().unwrap();
    payload["final_response"]["content"] = json!(42);
    malformed.payload = Some(serde_json::value::to_raw_value(&payload).unwrap());
    let session = ScriptSession::new(vec![Ok(malformed)], true);
    let error = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap_err();
    assert!(matches!(error.kind, RunErrorKind::Content(_)));
    assert!(error.outcome.response.is_none());
    assert!(session.dropped.load(Ordering::SeqCst));
}

#[test]
fn cancellation_keeps_optional_usage_instead_of_inventing_zeroes() {
    let cancelled = event(
        EnvelopeType::RUN_CANCELLED,
        1,
        json!({
            "session_id":"session-1", "run_id":"run-1", "usage":{"output_tokens":0}
        }),
    );
    let session = ScriptSession::new(vec![Ok(cancelled)], true);
    let error = block_on(run_to_terminal(&session, MessageSubmitRequest::default())).unwrap_err();
    assert!(matches!(error.kind, RunErrorKind::Cancelled));
    let usage = error.outcome.usage.as_ref().unwrap();
    assert_eq!(usage.output_tokens, Some(0));
    assert_eq!(usage.total_tokens, None);
    assert!(session.dropped.load(Ordering::SeqCst));
}
