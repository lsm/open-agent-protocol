mod support;

use support::*;

fn steer_request(target: Option<&RunId>) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "session-1".into(),
        delivery: RequestedDeliveryMode::STEER,
        target_run_id: target.cloned(),
        messages: vec![Message {
            id: Some("guidance".into()),
            role: MessageRole::USER,
            content: MessageContent::from_text("wait"),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    }
}

fn steer_refusal(err: Error) -> (RunId, &'static str, Option<u64>) {
    match err {
        Error::InvalidSteerTarget {
            run_id,
            reason,
            target_sequence,
        } => (run_id, reason, target_sequence),
        other => panic!("got {other:?}, want a steer target refusal"),
    }
}

fn entry(session: &MemorySession, run: &RunId) -> ActiveRun {
    state(session)
        .active_runs
        .into_iter()
        .find(|entry| &entry.run_id == run)
        .unwrap_or_else(|| panic!("run {} is not active", run.as_str()))
}

#[test]
fn a_steer_waits_on_its_run_and_is_applied_at_the_input_gate() {
    let session = session(64);
    let (admission, mut stream, submitted) = submit_as(&session, "submit-request", plain());
    let run = admission.run_id.clone().unwrap();
    let initial = drain(&mut stream);

    let request = steer_request(Some(&run));
    let (steer, mut steered, guided) = submit_as(&session, "steer-request", request);
    assert_eq!(steer.admission, Admission::STEERED);
    assert_eq!(steer.requested_delivery, RequestedDeliveryMode::STEER);
    assert_eq!(steer.effective_delivery, EffectiveDeliveryMode::STEER);
    assert_eq!(steer.run_id.as_ref(), Some(&run));
    assert_eq!(steer.status, RunStatus::RUNNING);
    assert_eq!(steer.target_sequence, Some(4));
    assert_eq!(steer.message_ids, [MessageId::from("guidance")]);
    assert!(ended(&mut steered), "a steer opens no stream of its own");

    let pending = entry(&session, &run);
    assert_eq!(
        pending.pending_steers,
        [PendingSteer {
            submission_id: steer.submission_id.clone(),
            request_id: "steer-request".into(),
        }]
    );
    assert_eq!(
        pending.admitted_submit_requests,
        [EnvelopeId::from("steer-request")]
    );
    assert_eq!(pending.as_of_sequence, steer.target_sequence);
    assert!(
        drain(&mut stream).is_empty(),
        "applied before the input gate"
    );

    approve(&session, &run, &initial);
    let gated = drain(&mut stream);
    let applied = of_type(&gated, EnvelopeType::RUN_STEER_APPLIED);
    let payload: RunSteerAppliedPayload = applied.decode_payload().unwrap();
    assert_eq!(payload.submission_id, steer.submission_id);
    assert_eq!(payload.request_id.as_str(), "steer-request");
    assert_eq!(payload.message_ids, [MessageId::from("guidance")]);
    assert_eq!(payload.boundary, SteerBoundary::TURN);
    let input = of_type(&gated, EnvelopeType::USER_INPUT_REQUESTED);
    assert!(applied.sequence < input.sequence);

    let settled = entry(&session, &run);
    assert!(settled.pending_steers.is_empty());
    assert_eq!(
        settled.admitted_submit_requests,
        [EnvelopeId::from("steer-request")]
    );
    assert!(settled.as_of_sequence >= applied.sequence);

    answer(&session, &run, &gated);
    let rest = drain(&mut stream);
    assert!(state(&session).active_runs.is_empty());
    assert_flow(
        &session_open(),
        &[&submitted, &initial, &guided, &gated, &rest],
    );
}

#[test]
fn a_steer_still_pending_at_the_terminal_is_dropped_before_it() {
    let session = session(64);
    let (admission, mut stream, submitted) = submit_as(&session, "submit-request", plain());
    let run = admission.run_id.clone().unwrap();
    let initial = drain(&mut stream);
    let (steer, _, guided) = submit_as(&session, "steer-request", steer_request(None));
    assert_eq!(steer.run_id.as_ref(), Some(&run));

    let cancelled = cancel(&session, &run).unwrap();
    let rest = drain(&mut stream);
    let dropped = of_type(&rest, EnvelopeType::RUN_STEER_DROPPED);
    let payload: RunSteerDroppedPayload = dropped.decode_payload().unwrap();
    assert_eq!(payload.submission_id, steer.submission_id);
    assert_eq!(payload.request_id.as_str(), "steer-request");
    assert_eq!(payload.reason.code, "run_terminated");
    let terminal = of_type(&rest, EnvelopeType::RUN_CANCELLED);
    assert!(dropped.sequence < terminal.sequence);
    assert!(state(&session).active_runs.is_empty());

    let request = RunCancelRequest {
        session_id: "session-1".into(),
        run_id: run.clone(),
        ..RunCancelRequest::default()
    };
    let asked = exchange(
        [
            EnvelopeType::RUN_CANCEL_REQUEST,
            EnvelopeType::RUN_CANCEL_RESPONSE,
        ],
        "cancel-request",
        Some(&run),
        &request,
        &cancelled,
    );
    assert_flow(
        &session_open(),
        &[&submitted, &initial, &guided, &asked, &rest],
    );
}

#[test]
fn a_steer_names_why_its_target_cannot_take_guidance() {
    let (_, reason, at) =
        steer_refusal(try_submit(&session(64), steer_request(None)).err().unwrap());
    assert_eq!((reason, at), (STEER_NO_ACTIVE_RUN, None));

    let session = session(64);
    let (first, mut stream) = started(&session);
    let first = first.run_id.unwrap();
    drain(&mut stream);
    let unknown = RunId::from("run-404");
    let refused = steer_refusal(
        try_submit(&session, steer_request(Some(&unknown)))
            .err()
            .unwrap(),
    );
    assert_eq!(refused, (unknown, STEER_UNKNOWN_TARGET, None));

    let queued = MessageSubmitRequest {
        delivery: RequestedDeliveryMode::QUEUE,
        ..plain()
    };
    let (reservation, _) = submit(&session, queued);
    let reserved = reservation.run_id.unwrap();
    let refused = steer_refusal(
        try_submit(&session, steer_request(Some(&reserved)))
            .err()
            .unwrap(),
    );
    assert_eq!(refused, (reserved.clone(), STEER_QUEUED, Some(0)));

    cancel(&session, &reserved).unwrap();
    let refused = steer_refusal(
        try_submit(&session, steer_request(Some(&reserved)))
            .err()
            .unwrap(),
    );
    assert_eq!(refused, (reserved, STEER_TERMINAL, Some(1)));

    cancel(&session, &first).unwrap();
    let refused = steer_refusal(
        try_submit(&session, steer_request(Some(&first)))
            .err()
            .unwrap(),
    );
    assert_eq!(refused, (first, STEER_TERMINAL, Some(8)));

    let foreign = MessageSubmitRequest {
        session_id: "another-session".into(),
        ..steer_request(None)
    };
    assert_eq!(
        try_submit(&session, foreign).err(),
        Some(Error::RunNotFound)
    );
    let empty = MessageSubmitRequest {
        messages: Vec::new(),
        ..steer_request(None)
    };
    assert_eq!(
        try_submit(&session, empty).err(),
        Some(Error::InvalidSubmission)
    );
}

#[test]
fn a_steer_carrying_a_run_control_is_refused_under_the_lowest_key() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.unwrap();
    drain(&mut stream);
    let base = || steer_request(Some(&run));
    let cases = [
        (
            MessageSubmitRequest {
                model_id: Some(MODEL_SECONDARY.into()),
                ..base()
            },
            FEATURE_MODEL_SELECTION,
        ),
        (
            MessageSubmitRequest {
                instructions: Some("be brief".into()),
                ..base()
            },
            FEATURE_INSTRUCTIONS,
        ),
        (
            MessageSubmitRequest {
                tool_choice: raw(r#"{"allowed":["scripted_tool"]}"#),
                ..base()
            },
            FEATURE_TOOL_SELECTION,
        ),
        (
            MessageSubmitRequest {
                output_schema: raw(r#"{"type":"object"}"#),
                ..base()
            },
            FEATURE_STRUCTURED_OUTPUT,
        ),
        (
            MessageSubmitRequest {
                model_id: Some(MODEL_SECONDARY.into()),
                instructions: Some("be brief".into()),
                ..base()
            },
            FEATURE_INSTRUCTIONS,
        ),
    ];
    for (request, key) in cases {
        let control = refusal(try_submit(&session, request).err().unwrap());
        assert_eq!(
            (control.feature.as_str(), control.reason),
            (key, CONTROL_UNSATISFIABLE)
        );
    }
    assert!(entry(&session, &run).pending_steers.is_empty());
}
