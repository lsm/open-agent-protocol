mod support;

use support::*;

const COMPACT: [EnvelopeType; 2] = [
    EnvelopeType::SESSION_COMPACT_REQUEST,
    EnvelopeType::SESSION_COMPACT_RESPONSE,
];

fn try_compact(
    session: &MemorySession,
    request: SessionCompactRequest,
) -> Result<(SessionCompactResponse, EventStream), Error> {
    let compactor = Session::compactor(session).expect("the memory session compacts");
    block_on(compactor.compact(CompactRequest {
        request,
        envelope_id: "compact-request".into(),
    }))
}

fn compact(
    session: &MemorySession,
    request: SessionCompactRequest,
) -> (SessionCompactResponse, EventStream, [Envelope; 2]) {
    let (response, stream) = try_compact(session, request.clone()).unwrap();
    let asked = exchange(COMPACT, "compact-request", None, &request, &response);
    (response, stream, asked)
}

fn idle_compaction() -> SessionCompactRequest {
    SessionCompactRequest {
        session_id: "session-1".into(),
        ..SessionCompactRequest::default()
    }
}

fn kinds(events: &[Envelope]) -> Vec<&str> {
    events
        .iter()
        .map(|event| event.envelope_type.as_str())
        .collect()
}

fn compactions(events: &[Envelope]) -> Vec<RunCompactionStartedPayload> {
    events
        .iter()
        .filter(|event| event.envelope_type == EnvelopeType::RUN_COMPACTION_STARTED)
        .map(|event| event.decode_payload().unwrap())
        .collect()
}

fn settles(events: &[Envelope]) -> bool {
    events.iter().any(|event| is_terminal(&event.envelope_type))
}

fn policy_open(policy: CompactionPolicy) -> OpenRequest {
    OpenRequest {
        compaction_policy: Some(policy),
        ..session_open()
    }
}

fn opened(open: &OpenRequest) -> MemorySession {
    memory(64).open_session(open).unwrap()
}

fn tokens(count: i64) -> CompactionPolicy {
    CompactionPolicy {
        kind: CompactionKind::TOKENS,
        tokens: count,
        ..CompactionPolicy::default()
    }
}

fn share(percent: i64) -> CompactionPolicy {
    CompactionPolicy {
        kind: CompactionKind::SHARE,
        share_percent: percent,
        ..CompactionPolicy::default()
    }
}

fn off() -> CompactionPolicy {
    CompactionPolicy {
        kind: CompactionKind::OFF,
        ..CompactionPolicy::default()
    }
}

fn saying(text: &str) -> MessageSubmitRequest {
    MessageSubmitRequest {
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text(text),
            ..Message::default()
        }],
        ..plain()
    }
}

fn first_turn(session: &MemorySession, text: &str) -> Vec<Envelope> {
    let (_, mut stream) = submit(session, saying(text));
    drain(&mut stream)
}

fn completed(session: &MemorySession, id: &str) -> Vec<Envelope> {
    let (admission, mut stream, asked) = submit_as(session, id, plain());
    let run = admission.run_id.unwrap();
    let initial = drain(&mut stream);
    let rest = resolve_gates(session, &run, &mut stream, &initial);
    [asked.to_vec(), initial, rest].concat()
}

#[test]
fn an_idle_compaction_runs_alone_and_settles_with_a_summary_naming_its_focus() {
    let session = session(64);
    let request = SessionCompactRequest {
        focus: Some("the parser".into()),
        ..idle_compaction()
    };
    let (response, mut stream, asked) = compact(&session, request);
    assert!(response.accepted);
    assert_eq!(response.admission, Admission::STARTED);
    assert_eq!(response.requested_delivery, RequestedDeliveryMode::AUTO);
    assert_eq!(response.effective_delivery, EffectiveDeliveryMode::START);
    assert_eq!(response.status, RunStatus::RUNNING);
    let events = drain(&mut stream);
    assert_eq!(
        kinds(&events),
        [
            "run.started",
            "run.compaction.started",
            "run.compaction.ended",
            "run.completed"
        ]
    );
    let started: RunCompactionStartedPayload = events[1].decode_payload().unwrap();
    let ended: RunCompactionEndedPayload = events[2].decode_payload().unwrap();
    assert_eq!(started.reason, CompactionReason::REQUESTED);
    assert_eq!(ended.compaction_id, started.compaction_id);
    assert_eq!(ended.outcome, CompactionOutcome::COMPLETED);
    let summary = ended.summary.unwrap().content.text().unwrap_or_default();
    assert!(summary.contains("the parser"), "{summary}");
    let settled: RunCompletedPayload = events[3].decode_payload().unwrap();
    assert_eq!(settled.stop_reason, "compacted");
    assert!(idle(&session));
    assert_flow(&session_open(), &[&asked, &events]);
}

#[test]
fn a_compaction_that_continues_takes_the_scripted_turn_after_it() {
    let session = session(64);
    let request = SessionCompactRequest {
        continue_after: true,
        ..idle_compaction()
    };
    let (response, mut stream, asked) = compact(&session, request);
    let run = response.run_id.clone().unwrap();
    let events = drain(&mut stream);
    assert!(!settles(&events), "{:?}", kinds(&events));
    let ended = events
        .iter()
        .position(|event| event.envelope_type == EnvelopeType::RUN_COMPACTION_ENDED)
        .unwrap();
    assert_eq!(events[ended + 1].envelope_type, EnvelopeType::CONTENT_DELTA);
    answer(&session, &run, &events);
    let rest = drain(&mut stream);
    let settled: RunCompletedPayload = of_type(&rest, EnvelopeType::RUN_COMPLETED)
        .decode_payload()
        .unwrap();
    assert_ne!(settled.stop_reason, "compacted");
    assert_flow(&session_open(), &[&asked, &events, &rest]);
}

#[test]
fn a_compaction_queues_behind_a_busy_run_and_starts_when_it_settles() {
    let session = session(64);
    let (admission, mut first, submitted) = submit_as(&session, "submit-request", plain());
    let run = admission.run_id.unwrap();
    let initial = drain(&mut first);
    let (response, mut second, asked) = compact(&session, idle_compaction());
    assert_eq!(response.admission, Admission::QUEUED);
    assert_eq!(response.effective_delivery, EffectiveDeliveryMode::QUEUE);
    assert_eq!(response.status, RunStatus::QUEUED);
    assert_eq!(response.delivery_resolution, "session_busy");
    assert!(drain(&mut second).is_empty());
    let rest = resolve_gates(&session, &run, &mut first, &initial);
    let promoted = drain(&mut second);
    assert_eq!(
        kinds(&promoted),
        [
            "run.started",
            "run.compaction.started",
            "run.compaction.ended",
            "run.completed"
        ]
    );
    assert_flow(
        &session_open(),
        &[&submitted, &initial, &asked, &rest, &promoted],
    );
}

#[test]
fn a_compaction_is_refused_when_the_queue_is_full_or_its_delivery_is_not_auto_or_queue() {
    let session = session(64);
    let _running = started(&session);
    let _queued = submit(&session, plain());
    assert_eq!(
        try_compact(&session, idle_compaction()).err(),
        Some(Error::RunActive)
    );
    for (delivery, key) in [
        (RequestedDeliveryMode::STEER, FEATURE_DELIVERY_STEER),
        (RequestedDeliveryMode::BTW, FEATURE_DELIVERY_BTW),
    ] {
        let request = SessionCompactRequest {
            delivery,
            ..idle_compaction()
        };
        let control = refusal(try_compact(&session, request).err().unwrap());
        assert_eq!(
            (control.feature.as_str(), control.reason),
            (key, CONTROL_UNADVERTISED)
        );
    }
    let foreign = SessionCompactRequest {
        session_id: "another-session".into(),
        ..idle_compaction()
    };
    assert_eq!(
        try_compact(&session, foreign).err(),
        Some(Error::RunNotFound)
    );
}

#[test]
fn a_run_whose_history_reaches_the_threshold_compacts_and_carries_on_with_its_turn() {
    let events = first_turn(&opened(&policy_open(tokens(1))), "go");
    assert_eq!(
        kinds(&events)[..4],
        [
            "run.started",
            "run.compaction.started",
            "run.compaction.ended",
            "content.delta"
        ]
    );
    assert!(!settles(&events));
    let started = &compactions(&events)[0];
    assert_eq!(started.reason, CompactionReason::THRESHOLD);
    assert_eq!(started.history_tokens, Some(1));
    let ended: RunCompactionEndedPayload = events[2].decode_payload().unwrap();
    assert_eq!(ended.outcome, CompactionOutcome::COMPLETED);
    assert!(ended.summary.is_some());
    assert_eq!(ended.history_tokens, Some(8));

    assert!(compactions(&first_turn(&opened(&policy_open(tokens(2))), "go")).is_empty());
}

#[test]
fn a_share_threshold_is_taken_of_the_reference_window_and_off_never_compacts_on_its_own() {
    let long = "abcd".repeat(100);
    let narrow = compactions(&first_turn(&opened(&policy_open(share(1))), &long));
    assert_eq!(narrow.len(), 1);
    assert_eq!(narrow[0].reason, CompactionReason::THRESHOLD);
    for open in [policy_open(share(50)), session_open(), policy_open(off())] {
        let started = compactions(&first_turn(&opened(&open), &long));
        assert!(started.is_empty(), "{:?}", open.compaction_policy);
    }
}

#[test]
fn a_threshold_crossed_at_settlement_compacts_the_next_run_and_both_runs_validate() {
    let open = policy_open(tokens(9));
    let session = opened(&open);
    let first = completed(&session, "submit-first");
    assert!(compactions(&first).is_empty());
    assert_eq!(
        first.last().unwrap().envelope_type,
        EnvelopeType::RUN_COMPLETED
    );
    let second = completed(&session, "submit-second");
    let started = compactions(&second);
    assert_eq!(started.len(), 1);
    assert_eq!(started[0].reason, CompactionReason::THRESHOLD);
    assert_eq!(started[0].history_tokens, Some(9));
    assert_eq!(
        second[3].envelope_type,
        EnvelopeType::RUN_COMPACTION_STARTED
    );
    assert_eq!(
        second.last().unwrap().envelope_type,
        EnvelopeType::RUN_COMPLETED
    );
    let third = completed(&session, "submit-third");
    assert_eq!(compactions(&third)[0].history_tokens, Some(16));
    assert_flow(&open, &[&first, &second, &third]);
}

#[test]
fn a_queued_run_promoted_by_the_settlement_that_crossed_the_threshold_compacts() {
    let session = opened(&policy_open(tokens(9)));
    let (running, mut stream) = submit(&session, plain());
    let run = running.run_id.unwrap();
    let queued = MessageSubmitRequest {
        delivery: RequestedDeliveryMode::QUEUE,
        ..plain()
    };
    let (waiting, mut queued) = submit(&session, queued);
    let initial = drain(&mut stream);
    resolve_gates(&session, &run, &mut stream, &initial);
    let started = compactions(&drain(&mut queued));
    assert_eq!(started.len(), 1);
    assert_eq!(Some(&started[0].run_id), waiting.run_id.as_ref());
    assert_eq!(started[0].history_tokens, Some(9));
}

#[test]
fn a_settings_update_moves_the_threshold_the_next_run_compacts_against() {
    let session = opened(&policy_open(off()));
    let update = SessionSettingsUpdateRequest {
        session_id: "session-1".into(),
        compaction_policy: Some(tokens(1)),
        ..SessionSettingsUpdateRequest::default()
    };
    session.update_settings(&update).unwrap();
    let started = compactions(&first_turn(&session, "go"));
    assert_eq!(started.len(), 1);
    assert_eq!(started[0].reason, CompactionReason::THRESHOLD);
}

#[test]
fn a_history_outgrowing_the_window_compacts_for_overflow_whatever_the_policy() {
    let overflowing = "abcd".repeat(8200);
    for policy in [off(), tokens(100_000), share(1)] {
        let started = compactions(&first_turn(&opened(&policy_open(policy)), &overflowing));
        assert_eq!(started.len(), 1);
        assert_eq!(started[0].reason, CompactionReason::OVERFLOW);
    }
    let inside = compactions(&first_turn(
        &opened(&policy_open(off())),
        &"abcd".repeat(8000),
    ));
    assert!(inside.is_empty());
}
