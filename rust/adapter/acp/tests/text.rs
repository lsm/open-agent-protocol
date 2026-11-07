use futures::{executor::block_on, StreamExt};
use oap_adapter::jsonrpc::NotificationMessage;
use oap_adapter::Error;
use oap_adapter_acp::{native::PromptResult, TextRun, TextRunContext};
use oap_protocol::{EnvelopeType, RunStatus};
use serde_json::{json, Value};

fn engine() -> TextRun {
    let mut next = 0;
    TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native".into(),
            run_id: "run".into(),
            message_id: "session/message".into(),
            capability_revision: Some("text-engine".into()),
        },
        move |kind| {
            next += 1;
            format!("{kind}-{next}")
        },
    )
}

fn update(value: Value) -> NotificationMessage {
    NotificationMessage {
        method: "session/update".into(),
        params: Some(
            serde_json::value::to_raw_value(&json!({"sessionId":"native", "update":value}))
                .unwrap(),
        ),
    }
}

fn chunk(text: &str, message: &str) -> NotificationMessage {
    update(
        json!({"sessionUpdate":"agent_message_chunk", "messageId":message,
                  "content":{"type":"text", "text":text}}),
    )
}

fn completed() -> PromptResult {
    PromptResult {
        stop_reason: "end_turn".into(),
        ..PromptResult::default()
    }
}

#[test]
fn text_runs_emit_scoped_ordered_events_and_a_single_completed_message() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    assert!(matches!(run.start(11), Err(Error::RunActive)));
    run.observe(chunk("Hello", "native-message"), 20).unwrap();
    run.observe(chunk(" 世界", "native-message"), 30).unwrap();
    run.complete(completed(), 40).unwrap();
    run.complete(completed(), 50).unwrap();
    run.observe(chunk("late", "native-message"), 60).unwrap();
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    assert_eq!(events.len(), 4);
    for (index, event) in events.iter().enumerate() {
        assert_eq!(event.sequence, Some(index as u64 + 1));
        assert_eq!(event.session_id.as_ref().unwrap().as_str(), "session");
        assert_eq!(event.run_id.as_ref().unwrap().as_str(), "run");
        assert_eq!(event.capability_revision.as_deref(), Some("text-engine"));
    }
    let final_message = events[3]
        .decode_payload::<oap_protocol::RunCompletedPayload>()
        .unwrap()
        .final_response;
    assert_eq!(final_message.id.unwrap().as_str(), "session/message");
    assert_eq!(final_message.content.text().as_deref(), Some("Hello 世界"));
    assert_eq!(run.state().status, RunStatus::COMPLETED);
    assert_eq!(run.state().sequence, 4);
    assert_eq!(run.state().updated_at_ms, 40);
    assert!(run.state().closed);
}

#[test]
fn malformed_or_foreign_updates_select_one_failure_before_late_completion() {
    let cases = [
        (
            json!({"sessionId":"native", "update":{"sessionUpdate":"config_option_update", "configOptions":"malformed"}}),
            "acp_invalid_update",
        ),
        (
            json!({"sessionId":"foreign", "update":{}}),
            "acp_invalid_update",
        ),
        (
            json!({"sessionId":"native", "update":[]}),
            "acp_invalid_update",
        ),
        (
            json!({"sessionId":"native", "update":{"sessionUpdate":"new_stable"}}),
            "acp_unknown_update",
        ),
        (
            json!({"sessionId":"native", "update":{"sessionUpdate":"agent_message_chunk", "content":{"type":"image"}}}),
            "acp_invalid_message_chunk",
        ),
    ];
    for (params, code) in cases {
        let mut run = engine();
        let stream = run.start(10).unwrap();
        run.observe(
            NotificationMessage {
                method: "session/update".into(),
                params: Some(serde_json::value::to_raw_value(&params).unwrap()),
            },
            20,
        )
        .unwrap();
        run.complete(completed(), 30).unwrap();
        let events = block_on(stream.collect::<Vec<_>>());
        assert_eq!(events.len(), 2);
        let event = events[1].as_ref().unwrap();
        assert_eq!(event.envelope_type, EnvelopeType::RUN_FAILED);
        assert_eq!(
            event
                .decode_payload::<oap_protocol::RunFailedPayload>()
                .unwrap()
                .error
                .code,
            code
        );
    }
}

#[test]
fn slow_consumers_receive_the_bounded_prefix_and_an_explicit_overflow() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    for _ in 0..100 {
        run.observe(chunk("x", ""), 20).unwrap();
    }
    run.complete(completed(), 30).unwrap();
    let events = block_on(stream.collect::<Vec<_>>());
    assert_eq!(events.len(), 65);
    assert!(events[..64].iter().all(Result::is_ok));
    assert!(matches!(&events[64], Err(Error::EventStreamOverflow)));
    assert_eq!(run.state().sequence, 102);
    assert_eq!(run.state().status, RunStatus::COMPLETED);
}

#[test]
fn closing_a_text_run_finishes_its_observed_prefix() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    run.observe(chunk("prefix", ""), 20).unwrap();
    run.close();
    run.complete(completed(), 30).unwrap();
    let events = block_on(stream.collect::<Vec<_>>());
    assert_eq!(events.len(), 2);
    assert_eq!(
        events[1].as_ref().unwrap().envelope_type,
        EnvelopeType::CONTENT_DELTA
    );
    assert!(run.state().closed);
    assert_eq!(run.state().status, RunStatus::RUNNING);
}

#[test]
fn dropping_the_text_run_owner_finishes_its_subscriber() {
    use std::future::Future;
    use std::task::{Context, Poll};
    let mut run = engine();
    let mut stream = run.start(10).unwrap();
    run.observe(chunk("prefix", ""), 20).unwrap();
    drop(run);
    assert_eq!(
        block_on(stream.next()).unwrap().unwrap().envelope_type,
        EnvelopeType::RUN_STARTED
    );
    assert_eq!(
        block_on(stream.next()).unwrap().unwrap().envelope_type,
        EnvelopeType::CONTENT_DELTA
    );
    let mut next = Box::pin(stream.next());
    let waker = futures::task::noop_waker();
    assert!(matches!(
        next.as_mut().poll(&mut Context::from_waker(&waker)),
        Poll::Ready(None)
    ));
}

#[test]
fn known_stop_reasons_and_failures_keep_their_observed_payloads() {
    for (stop, status, kind) in [
        (
            "end_turn",
            RunStatus::COMPLETED,
            EnvelopeType::RUN_COMPLETED,
        ),
        (
            "max_tokens",
            RunStatus::COMPLETED,
            EnvelopeType::RUN_COMPLETED,
        ),
        (
            "max_turn_requests",
            RunStatus::COMPLETED,
            EnvelopeType::RUN_COMPLETED,
        ),
        ("cancelled", RunStatus::FAILED, EnvelopeType::RUN_FAILED),
        ("refusal", RunStatus::FAILED, EnvelopeType::RUN_FAILED),
        ("unknown", RunStatus::FAILED, EnvelopeType::RUN_FAILED),
    ] {
        let mut run = engine();
        let stream = run.start(10).unwrap();
        run.complete(
            PromptResult {
                stop_reason: stop.into(),
                ..PromptResult::default()
            },
            20,
        )
        .unwrap();
        let events = block_on(stream.collect::<Vec<_>>());
        assert_eq!(events[1].as_ref().unwrap().envelope_type, kind);
        assert_eq!(run.state().status, status);
    }
    let mut run = engine();
    let stream = run.start(10).unwrap();
    run.fail("acp_transport_failure", "EOF", true, 20).unwrap();
    let events = block_on(stream.collect::<Vec<_>>());
    assert_eq!(
        events[1]
            .as_ref()
            .unwrap()
            .decode_payload::<oap_protocol::RunFailedPayload>()
            .unwrap()
            .settled_by,
        "inferred"
    );
}

#[test]
fn future_tool_and_multi_message_mapping_is_explicitly_unavailable() {
    for (first, second, code) in [
        (
            chunk("one", "first"),
            chunk("two", "second"),
            "acp_unavailable_message_identity",
        ),
        (
            chunk("one", ""),
            update(json!({"sessionUpdate":"tool_call", "toolCallId":"tool"})),
            "acp_unavailable_update",
        ),
    ] {
        let mut run = engine();
        let stream = run.start(10).unwrap();
        run.observe(first, 20).unwrap();
        run.observe(second, 30).unwrap();
        run.complete(completed(), 40).unwrap();
        let events = block_on(stream.collect::<Vec<_>>());
        assert_eq!(events.len(), 3);
        assert_eq!(
            events[2]
                .as_ref()
                .unwrap()
                .decode_payload::<oap_protocol::RunFailedPayload>()
                .unwrap()
                .error
                .code,
            code
        );
    }
}

#[test]
fn run_start_and_ignored_native_updates_preserve_the_admission_boundary() {
    let mut run = engine();
    assert!(matches!(
        run.complete(completed(), 10),
        Err(Error::InvalidSubmission)
    ));
    assert!(matches!(
        run.observe(chunk("early", ""), 10),
        Err(Error::InvalidSubmission)
    ));
    let stream = run.start(20).unwrap();
    for kind in [
        "agent_thought_chunk",
        "plan",
        "usage_update",
        "config_option_update",
        "_extension",
    ] {
        run.observe(update(json!({"sessionUpdate":kind})), 30)
            .unwrap();
    }
    run.complete(completed(), 40).unwrap();
    assert_eq!(block_on(stream.collect::<Vec<_>>()).len(), 2);
}
