use std::sync::{Arc, Mutex};

use futures::{executor::block_on, StreamExt};
use oap_adapter::jsonrpc::NotificationMessage;
use oap_adapter::Error;
use oap_adapter_acp::{native::PromptResult, TextRun, TextRunContext, UsageScope};
use oap_protocol::{EnvelopeType, RunStatus};
use serde_json::{json, Value};

fn engine() -> TextRun {
    scoped_engine(Arc::default(), "run")
}

fn scoped_engine(scope: Arc<Mutex<UsageScope>>, id: &str) -> TextRun {
    let mut next = 0;
    TextRun::new_in_scope(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native".into(),
            run_id: id.into(),
            message_id: "session/message".into(),
            capability_revision: Some("text-engine".into()),
        },
        move |kind| {
            next += 1;
            format!("{kind}-{next}")
        },
        scope,
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
fn malformed_tool_identity_is_refused() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    run.observe(chunk("one", ""), 20).unwrap();
    run.observe(
        update(json!({"sessionUpdate":"tool_call", "toolCallId":"tool"})),
        30,
    )
    .unwrap();
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
        "acp_invalid_tool_call"
    );
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

#[test]
fn prompt_usage_preserves_reported_counts_and_optional_fields() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    run.complete(PromptResult {
        stop_reason: "end_turn".into(),
        usage: Some(serde_json::value::RawValue::from_string(r#"{"totalTokens":10,"inputTokens":8,"outputTokens":2,"thoughtTokens":1,"cachedReadTokens":4,"cachedWriteTokens":0}"#.into()).unwrap()),
    }, 20).unwrap();
    let events = block_on(stream.collect::<Vec<_>>());
    let terminal = serde_json::to_value(events.last().unwrap().as_ref().unwrap()).unwrap();
    assert_eq!(
        terminal["payload"]["usage"],
        json!({"input_tokens":8,"output_tokens":2,"total_tokens":10})
    );
    assert_eq!(
        terminal["extensions"]["com.agentclientprotocol.turn_usage"],
        json!({"total_tokens":10,"input_tokens":8,"output_tokens":2,"thought_tokens":1,"cached_read_tokens":4,"cached_write_tokens":0})
    );
}

#[test]
fn native_message_identities_keep_separate_text_and_reuse_their_oap_ids() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    for (text, id) in [
        ("prefix", ""),
        ("one", "native-one"),
        ("two", "native-two"),
        ("again", "native-one"),
        ("tail", ""),
    ] {
        run.observe(chunk(text, id), 20).unwrap();
    }
    run.complete(completed(), 30).unwrap();
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    assert_eq!(events.len(), 7);
    let values = events
        .iter()
        .map(|event| serde_json::to_value(event).unwrap())
        .collect::<Vec<_>>();
    let first = &values[1]["payload"]["message_id"];
    assert_eq!(first, "session/message");
    assert_eq!(values[2]["payload"]["message_id"], *first);
    assert_ne!(values[3]["payload"]["message_id"], *first);
    assert_eq!(values[4]["payload"]["message_id"], *first);
    assert_eq!(values[5]["payload"]["message_id"], *first);
    assert_eq!(values[6]["payload"]["final_response"]["id"], *first);
    assert_eq!(
        values[6]["payload"]["final_response"]["content"],
        "prefixoneagaintail"
    );
}

fn raw_update(raw: &str) -> NotificationMessage {
    NotificationMessage {
        method: "session/update".into(),
        params: Some(
            serde_json::value::RawValue::from_string(format!(
                r#"{{"sessionId":"native","update":{raw}}}"#
            ))
            .unwrap(),
        ),
    }
}

fn reported(raw: &str) -> PromptResult {
    PromptResult {
        usage: Some(serde_json::value::RawValue::from_string(raw.into()).unwrap()),
        ..completed()
    }
}

#[test]
fn raw_usage_counts_keep_zero_and_uint64_precision_and_drop_only_bad_fields() {
    for (raw, expected) in [
        (r#"null"#, None),
        (r#"[]"#, None),
        (r#"{"totalTokens":10,"inputTokens":8}"#, None),
        (
            r#"{"totalTokens":10,"inputTokens":-0,"outputTokens":10}"#,
            None,
        ),
        (
            r#"{"totalTokens":10,"inputTokens":1.0,"outputTokens":9}"#,
            None,
        ),
        (
            r#"{"totalTokens":10,"inputTokens":1e0,"outputTokens":9}"#,
            None,
        ),
        (
            r#"{"totalTokens":0,"inputTokens":0,"outputTokens":0}"#,
            Some(json!({"total_tokens":0,"input_tokens":0,"output_tokens":0})),
        ),
        (
            r#"{"totalTokens":18446744073709551615,"inputTokens":18446744073709551615,"outputTokens":0,"cachedReadTokens":0}"#,
            Some(
                json!({"total_tokens":u64::MAX,"input_tokens":u64::MAX,"output_tokens":0,"cached_read_tokens":0}),
            ),
        ),
        (
            r#"{"totalTokens":12,"inputTokens":10,"outputTokens":2,"thoughtTokens":-4,"cachedWriteTokens":3}"#,
            Some(
                json!({"total_tokens":12,"input_tokens":10,"output_tokens":2,"cached_write_tokens":3}),
            ),
        ),
        (
            r#"{"totalTokens":10,"TOTALTOKENS":12,"inputTokens":10,"outputTokens":2,"thoughtTo\u212aens":1}"#,
            Some(json!({"total_tokens":12,"input_tokens":10,"output_tokens":2,"thought_tokens":1})),
        ),
        (
            r#"{"totalTokens":10,"TOTALTOKENS":null,"inputTokens":8,"outputTokens":2}"#,
            None,
        ),
    ] {
        let mut run = engine();
        let stream = run.start(10).unwrap();
        run.complete(reported(raw), 20).unwrap();
        let events = block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        let terminal = events.last().unwrap();
        let value = serde_json::to_value(terminal).unwrap();
        assert_eq!(value["type"], "run.completed", "{raw}");
        let observed = terminal
            .extensions
            .get("com.agentclientprotocol.turn_usage")
            .map(|value| serde_json::from_str::<Value>(value.get()).unwrap());
        assert_eq!(observed, expected, "{raw}");
        assert_eq!(
            value["payload"].get("usage").is_some(),
            expected.is_some(),
            "{raw}"
        );
    }
}

#[test]
fn usage_context_replaces_valid_snapshots_preserves_bad_counts_and_drops_bad_cost() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    run.observe(raw_update(r#"{"sessionUpdate":"usage_update","used":100,"size":1000,"cost":{"amount":0.25,"currency":"USD"}}"#), 20).unwrap();
    run.observe(
        raw_update(r#"{"sessionUpdate":"usage_update","used":-1,"size":1000}"#),
        20,
    )
    .unwrap();
    run.observe(raw_update(r#"{"sessionUpdate":"usage_update","used":200,"size":1000,"cost":{"amount":"lots","currency":"USD"}}"#), 20).unwrap();
    run.observe(
        raw_update(r#"{"sessionUpdate":"usage_update","used":null,"size":1000}"#),
        20,
    )
    .unwrap();
    run.complete(completed(), 30).unwrap();
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let value = serde_json::to_value(events.last().unwrap()).unwrap();
    assert_eq!(events.len(), 2);
    assert_eq!(
        value["extensions"]["com.agentclientprotocol.context"],
        json!({"used_tokens":200,"size_tokens":1000})
    );
    assert!(value["payload"].get("usage").is_none());
}

#[test]
fn usage_cost_keeps_go_float_spelling_and_case_folded_members() {
    let mut run = engine();
    let stream = run.start(10).unwrap();
    run.observe(raw_update(r#"{"sessionUpdate":"usage_update","used":3,"USED":0,"\u017fize":1000,"COST":{"Amount":-0.0,"CURRENCY":"<\u0026\u2028"}}"#), 20).unwrap();
    run.complete(completed(), 30).unwrap();
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    assert_eq!(
        events.last().unwrap().extensions["com.agentclientprotocol.context"].get(),
        r#"{"cost":{"amount":-0,"currency":"\u003c\u0026\u2028"},"size_tokens":1000,"used_tokens":0}"#
    );
}

#[test]
fn late_answers_and_prompt_errors_consume_the_first_turn_scope() {
    for early in [true, false] {
        let scope = Arc::default();
        let mut first = scoped_engine(Arc::clone(&scope), "first");
        let stream = first.start(10).unwrap();
        if early {
            first
                .observe(
                    update(
                        json!({"sessionUpdate":"agent_message_chunk","content":{"type":"image"}}),
                    ),
                    20,
                )
                .unwrap();
            first
                .complete(
                    reported(r#"{"totalTokens":10,"inputTokens":8,"outputTokens":2}"#),
                    30,
                )
                .unwrap();
        } else {
            first.prompt_error("agent failed", false, 20).unwrap();
        }
        let events = block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        assert_eq!(
            events.last().unwrap().envelope_type,
            EnvelopeType::RUN_FAILED
        );
        let mut next = scoped_engine(scope, "next");
        let stream = next.start(40).unwrap();
        next.complete(
            reported(r#"{"totalTokens":10,"inputTokens":8,"outputTokens":2}"#),
            50,
        )
        .unwrap();
        let events = block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap();
        let value = serde_json::to_value(events.last().unwrap()).unwrap();
        assert!(value["payload"].get("usage").is_none());
        assert_eq!(
            value["extensions"]["com.agentclientprotocol.turn_usage"]["total_tokens"],
            10
        );
    }
}

#[test]
fn shared_usage_scope_accepts_idle_observations_without_a_run() {
    let scope = Arc::default();
    let mut run = scoped_engine(Arc::clone(&scope), "run");
    let notification = raw_update(r#"{"sessionUpdate":"usage_update","used":0,"size":1000}"#);
    assert!(!scope.lock().unwrap().observe(&notification, "foreign"));
    assert!(scope.lock().unwrap().observe(&notification, "native"));
    let stream = run.start(10).unwrap();
    run.complete(completed(), 20).unwrap();
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let value = serde_json::to_value(events.last().unwrap()).unwrap();
    assert_eq!(
        value["extensions"]["com.agentclientprotocol.context"],
        json!({"used_tokens":0,"size_tokens":1000})
    );
}

#[test]
fn failed_admission_consumes_scope_before_any_run_started() {
    let scope = Arc::default();
    let mut first = scoped_engine(Arc::clone(&scope), "unadmitted");
    let _ = first.prompt_error("native write failed", true, 10);
    assert_eq!(first.state().sequence, 0);
    let mut next = scoped_engine(scope, "next");
    let stream = next.start(20).unwrap();
    next.complete(
        reported(r#"{"totalTokens":10,"inputTokens":8,"outputTokens":2}"#),
        30,
    )
    .unwrap();
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let terminal = serde_json::to_value(events.last().unwrap()).unwrap();
    assert!(terminal["payload"].get("usage").is_none(), "{terminal}");
    assert_eq!(
        terminal["extensions"]["com.agentclientprotocol.turn_usage"]["total_tokens"],
        10
    );
    assert!(first.state().closed);
    assert!(matches!(first.start(40), Err(Error::SessionClosed)));
}
