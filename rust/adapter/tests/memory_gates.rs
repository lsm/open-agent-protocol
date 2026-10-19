mod support;

use support::*;

#[test]
fn the_golden_script_runs_permission_then_input_to_one_valid_completion() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let mut trace = drain(&mut stream);
    types_and_sequence(&trace, &INITIAL, 1);
    let requested: PermissionRequestedPayload = trace[3].decode_payload().unwrap();
    assert_eq!(
        (
            requested.requested_by.as_str(),
            requested.responded_by.as_str()
        ),
        ("reference.memory", "user")
    );
    resolve(&session, approval(&run, &requested)).unwrap();
    let events = drain(&mut stream);
    types_and_sequence(
        &events,
        &[
            EnvelopeType::ACTION_PERMISSION_RESOLVED,
            EnvelopeType::ACTION_CALL_STARTED,
            EnvelopeType::ACTION_CALL_COMPLETED,
            EnvelopeType::USER_INPUT_REQUESTED,
            EnvelopeType::RUN_STATUS_UPDATED,
        ],
        5,
    );
    let status: RunStatusUpdatedPayload = events[4].decode_payload().unwrap();
    assert_eq!(status.status, RunStatus::WAITING_FOR_INPUT);
    assert!(status.pending_user_input_id.is_some());
    let input: UserInputRequestedPayload = events[3].decode_payload().unwrap();
    assert_eq!(
        (input.requested_by.as_str(), input.responded_by.as_str()),
        ("reference.memory", "user")
    );
    trace.extend(events);
    resolve(&session, answering(&run, &input, yes())).unwrap();
    let events = drain(&mut stream);
    types_and_sequence(
        &events,
        &[
            EnvelopeType::USER_INPUT_RESOLVED,
            EnvelopeType::CONTENT_DELTA,
            EnvelopeType::RUN_COMPLETED,
        ],
        10,
    );
    trace.extend(events);
    assert!(
        matches!(stream.next().now_or_never(), Some(None)),
        "the stream outlived its terminal"
    );
    assert_valid(&[submission(plain(), &admission)], &trace);
    assert!(idle(&session));
}

#[test]
fn every_emitted_envelope_carries_the_advertised_revision() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let mut events = drain(&mut stream);
    assert!(
        cancel(&session, admission.run_id.as_ref().unwrap())
            .unwrap()
            .accepted
    );
    events.extend(drain(&mut stream));
    assert!(events
        .iter()
        .all(|event| event.capability_revision.as_deref() == Some(CAPABILITY_REVISION)));
}

#[test]
fn a_cancel_closes_the_permission_gate_and_a_repeat_is_answered_cancelled() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let mut events = drain(&mut stream);
    let ack = cancel(&session, &run).unwrap();
    assert!(
        ack.accepted && ack.status == RunStatus::CANCELLING,
        "{ack:?}"
    );
    let rest = drain(&mut stream);
    types_and_sequence(
        &rest,
        &[
            EnvelopeType::RUN_STATUS_UPDATED,
            EnvelopeType::ACTION_PERMISSION_RESOLVED,
            EnvelopeType::ACTION_CALL_CANCELLED,
            EnvelopeType::RUN_CANCELLED,
        ],
        5,
    );
    events.extend(rest);
    let mut cancelled = submission(plain(), &admission);
    cancelled.cancelled = true;
    assert_valid(&[cancelled], &events);
    let again = cancel(&session, &run).unwrap();
    assert!(
        again.accepted && again.status == RunStatus::CANCELLED,
        "{again:?}"
    );
}

#[test]
fn a_cancel_after_completion_is_refused_with_the_terminal_status() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let initial = drain(&mut stream);
    resolve_gates(&session, &run, &mut stream, &initial);
    match cancel(&session, &run) {
        Err(Error::RunAlreadyTerminal { status, .. }) => {
            assert_eq!(status, Some(RunStatus::COMPLETED))
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_racing_answer_and_cancel_settle_the_run_exactly_once() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let mut events = drain(&mut stream);
    approve(&session, &run, &events);
    let middle = drain(&mut stream);
    let input: UserInputRequestedPayload = of_type(&middle, EnvelopeType::USER_INPUT_REQUESTED)
        .decode_payload()
        .unwrap();
    events.extend(middle);
    let shared = &session;
    let ack = std::thread::scope(|scope| {
        let answered = scope.spawn(|| resolve(shared, answering(&run, &input, yes())));
        let cancelled = scope.spawn(|| cancel(shared, &run));
        let _ = answered.join().unwrap();
        cancelled.join().unwrap()
    });
    let rest = drain(&mut stream);
    assert_eq!(
        rest.iter()
            .filter(|event| is_terminal(&event.envelope_type))
            .count(),
        1,
        "{rest:?}"
    );
    events.extend(rest);
    let mut submitted = submission(plain(), &admission);
    submitted.cancelled = ack.is_ok_and(|ack| ack.accepted);
    assert_valid(&[submitted], &events);
}

#[test]
fn the_tool_completion_omits_the_request_only_arguments_and_the_input_gate_carries_the_tool_binding(
) {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let initial = drain(&mut stream);
    approve(&session, admission.run_id.as_ref().unwrap(), &initial);
    let events = drain(&mut stream);
    let completed: ActionCallPayload = of_type(&events, EnvelopeType::ACTION_CALL_COMPLETED)
        .decode_payload()
        .unwrap();
    assert!(completed.arguments_json.is_none());
    let gate = of_type(&events, EnvelopeType::USER_INPUT_REQUESTED);
    let input: UserInputRequestedPayload = gate.decode_payload().unwrap();
    assert!(gate.tool_call_id.is_some());
    assert_eq!(gate.tool_call_id, input.tool_call_id);
}

#[test]
fn a_resolution_that_disagrees_with_its_gate_is_refused() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let initial = drain(&mut stream);
    let permission: PermissionRequestedPayload = initial[3].decode_payload().unwrap();
    let mutations: [Mutation; 4] = [
        ("foreign requester", |p| p.requested_by = "intruder".into()),
        ("foreign responder", |p| p.responded_by = "intruder".into()),
        ("unoffered choice", |p| p.choice_id = "bogus".into()),
        ("grant disagrees", |p| p.granted = false),
    ];
    for (name, mutate) in mutations {
        let mut resolution = approval(&run, &permission);
        mutate(resolution.permission.as_mut().unwrap());
        assert_eq!(
            resolve(&session, resolution),
            Err(Error::InvalidResolution),
            "{name}"
        );
    }
    resolve(&session, approval(&run, &permission)).unwrap();
    let middle = drain(&mut stream);
    let input: UserInputRequestedPayload = middle[3].decode_payload().unwrap();
    let mut foreign = answering(&run, &input, yes());
    foreign.input.as_mut().unwrap().requested_by = "intruder".into();
    assert_eq!(resolve(&session, foreign), Err(Error::InvalidResolution));
    let answer = |question: &str, option: &str, text: &str| InputAnswer {
        question_id: question.into(),
        selected_option_ids: vec![option.into()],
        text: text.into(),
    };
    for (name, answers) in [
        ("empty answers", Vec::new()),
        ("unoffered option", vec![answer("choice", "no", "")]),
        ("mixed form", vec![answer("choice", "yes", "yes")]),
        ("foreign question", vec![answer("other", "yes", "")]),
    ] {
        assert_eq!(
            resolve(&session, answering(&run, &input, answers)),
            Err(Error::InvalidResolution),
            "{name}"
        );
    }
}

#[test]
fn the_published_catalog_governs_which_tool_a_policy_may_name() {
    for policy in [
        r#"{"allowed":["scripted_tool"]}"#,
        r#"{"disallowed":[]}"#,
        r#"{"disallowed":["absent_tool"]}"#,
    ] {
        let session = session(64);
        let request = MessageSubmitRequest {
            tool_choice: raw(policy),
            ..plain()
        };
        let (admission, mut stream) = submit(&session, request.clone());
        let run = admission.run_id.clone().unwrap();
        let mut events = drain(&mut stream);
        let rest = resolve_gates(&session, &run, &mut stream, &events);
        events.extend(rest);
        let calls = events
            .iter()
            .filter(|event| event.envelope_type == EnvelopeType::ACTION_CALL_REQUESTED)
            .count();
        assert_eq!(calls, 1, "{policy}");
        assert_valid(&[submission(request, &admission)], &events);
    }
    let tools = descriptor().capabilities.tools;
    assert_eq!(
        tools
            .iter()
            .map(|tool| tool.name.as_str())
            .collect::<Vec<_>>(),
        ["scripted_tool"]
    );
}

#[test]
fn the_active_run_entry_follows_the_run_through_its_gates() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let mut events = drain(&mut stream);
    let gate = of_type(&events, EnvelopeType::ACTION_PERMISSION_REQUESTED)
        .id
        .clone();
    let snapshot = state(&session);
    assert_eq!(snapshot.status, SessionStatus::WAITING_FOR_INPUT);
    assert_eq!(snapshot.active_runs.len(), 1);
    assert_eq!(snapshot.active_runs[0].pending_interactions.len(), 1);
    approve(&session, &run, &events);
    let middle = drain(&mut stream);
    let input: UserInputRequestedPayload = of_type(&middle, EnvelopeType::USER_INPUT_REQUESTED)
        .decode_payload()
        .unwrap();
    let waiting = state(&session);
    let entry = &waiting.active_runs[0];
    assert_eq!(
        (&entry.run_id, &entry.status),
        (&run, &RunStatus::WAITING_FOR_INPUT)
    );
    assert_eq!(
        entry.pending_interactions,
        std::slice::from_ref(&input.interaction_id)
    );
    events.extend(middle);
    resolve(&session, answering(&run, &input, yes())).unwrap();
    events.extend(drain(&mut stream));
    let traced = splice_after(&events, &gate, state_exchange(&snapshot));
    assert_valid(&[submission(plain(), &admission)], &traced);
    let stranger = OpenRequest {
        participant: Participant {
            id: "stranger".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    };
    let result = validate(&stranger, &[submission(plain(), &admission)], &traced);
    assert!(
        result.has_code("unknown_participant"),
        "{:#?}",
        result.diagnostics
    );
}

#[test]
fn a_settled_reservation_and_a_settled_run_are_anchored_in_the_state() {
    let session = session(64);
    let (first, mut first_stream) = started(&session);
    let first_run = first.run_id.clone().unwrap();
    let events = drain(&mut first_stream);
    let gate = of_type(&events, EnvelopeType::ACTION_PERMISSION_REQUESTED)
        .id
        .clone();
    let queued = MessageSubmitRequest {
        delivery: RequestedDeliveryMode::QUEUE,
        ..plain()
    };
    let (reservation, mut reserved) = submit(&session, queued.clone());
    assert_eq!(
        (&reservation.admission, &reservation.status),
        (&Admission::QUEUED, &RunStatus::QUEUED)
    );
    let reserved_run = reservation.run_id.clone().unwrap();
    let unseen = InteractionResolution {
        run_id: reserved_run.clone(),
        responded_by: "user".into(),
        ..InteractionResolution::default()
    };
    assert_eq!(resolve(&session, unseen), Err(Error::InteractionNotFound));
    cancel(&session, &reserved_run).unwrap();
    let snapshot = state(&session);
    assert!(snapshot
        .active_runs
        .iter()
        .all(|entry| entry.run_id != reserved_run));
    let settled = &snapshot.as_of.as_ref().unwrap().settled;
    assert_eq!(settled.len(), 1);
    assert_eq!(
        (&settled[0].run_id, settled[0].sequence),
        (&reserved_run, 1)
    );
    let cancelled = drain(&mut reserved);
    types_and_sequence(&cancelled, &[EnvelopeType::RUN_CANCELLED], 1);
    let rest = resolve_gates(&session, &first_run, &mut first_stream, &events);
    let mut trace = splice_after(&events, &gate, state_exchange(&snapshot));
    trace.extend(cancelled);
    trace.extend(rest.iter().cloned());
    let mut cancelled_submission = submission(queued, &reservation);
    cancelled_submission.cancelled = true;
    assert_valid(&[submission(plain(), &first), cancelled_submission], &trace);
    let terminal = of_type(&rest, EnvelopeType::RUN_COMPLETED);
    let after = state(&session);
    assert!(after.active_runs.is_empty() && idle(&session));
    let settled = &after.as_of.unwrap().settled;
    assert_eq!(
        (&settled[1].run_id, Some(settled[1].sequence)),
        (&first_run, terminal.sequence)
    );
}

#[test]
fn a_queued_run_starts_when_the_active_one_settles() {
    let session = session(64);
    let (first, mut first_stream) = started(&session);
    let initial = drain(&mut first_stream);
    let queued = MessageSubmitRequest {
        delivery: RequestedDeliveryMode::QUEUE,
        ..plain()
    };
    let (reservation, mut reserved) = submit(&session, queued.clone());
    assert_eq!(reservation.delivery_resolution, "session_busy");
    let busy = MessageSubmitRequest {
        delivery: RequestedDeliveryMode::QUEUE,
        ..plain()
    };
    assert!(matches!(try_submit(&session, busy), Err(Error::RunActive)));
    let rest = resolve_gates(
        &session,
        first.run_id.as_ref().unwrap(),
        &mut first_stream,
        &initial,
    );
    let promoted = drain(&mut reserved);
    types_and_sequence(&promoted, &INITIAL, 1);
    let mut trace = initial;
    trace.extend(rest);
    trace.extend(promoted.iter().cloned());
    let second = reservation.run_id.clone().unwrap();
    trace.extend(resolve_gates(&session, &second, &mut reserved, &promoted));
    assert_valid(
        &[
            submission(plain(), &first),
            submission(queued, &reservation),
        ],
        &trace,
    );
}

#[test]
fn an_output_schema_run_completes_with_the_fixed_result() {
    let session = session(64);
    let request = MessageSubmitRequest {
        tool_choice: raw(r#"{"allowed":[]}"#),
        output_schema: raw(r#"{"type":"object","properties":{"ok":{"type":"boolean"}}}"#),
        instructions: Some("Be terse.".into()),
        ..plain()
    };
    let (admission, mut stream) = submit(&session, request.clone());
    let mut events = drain(&mut stream);
    let delta: ContentDeltaPayload = events[1].decode_payload().unwrap();
    assert!(
        delta.part.text.starts_with("Be terse."),
        "{}",
        delta.part.text
    );
    answer(&session, admission.run_id.as_ref().unwrap(), &events);
    events.extend(drain(&mut stream));
    let completed = events.last().unwrap();
    assert_eq!(completed.envelope_type, EnvelopeType::RUN_COMPLETED);
    let payload: RunCompletedPayload = completed.decode_payload().unwrap();
    assert_eq!(payload.result.unwrap().get(), r#"{"ok":true}"#);
    assert_valid(&[submission(request, &admission)], &events);
}
