mod support;

use support::*;

#[test]
fn a_resume_replays_the_retained_suffix_and_a_tampered_copy_never_reaches_the_journal() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let mut published = drain(&mut stream);
    let original = published[3].payload.as_ref().unwrap().get().to_owned();
    published[3].payload = raw(r#"{"tampered":true}"#);
    let (recovery, mut replay) = resume(&session, &run, 2).unwrap();
    assert_eq!(
        (
            recovery.requested_after,
            recovery.replayed_from,
            recovery.replayed_through
        ),
        (2, 3, 4)
    );
    assert!(recovery.replay_gap.is_none());
    let mut replayed = drain(&mut replay);
    types_and_sequence(
        &replayed,
        &[
            EnvelopeType::ACTION_CALL_REQUESTED,
            EnvelopeType::ACTION_PERMISSION_REQUESTED,
        ],
        3,
    );
    assert_eq!(replayed[1].payload.as_ref().unwrap().get(), original);
    replayed[0].sequence = Some(103);
    let (_, mut again) = resume(&session, &run, 2).unwrap();
    assert_eq!(drain(&mut again)[0].sequence, Some(3));
}

#[test]
fn a_resume_past_the_retained_window_is_a_gap_and_the_state_stays_authoritative() {
    let session = session(2);
    let (admission, mut stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    drain(&mut stream);
    match resume(&session, &run, 0) {
        Err(Error::ReplayGap(gap)) => assert_eq!(
            (
                gap.requested_after,
                gap.oldest_available,
                gap.latest_available
            ),
            (0, 3, 4)
        ),
        Err(other) => panic!("{other:?}"),
        Ok(_) => panic!("a gap replayed"),
    }
    let state = state(&session);
    assert_eq!(state.active_run_id, Some(run));
    assert_eq!(state.status, SessionStatus::WAITING_FOR_INPUT);
}

#[test]
fn a_resume_never_waits_on_a_subscriber_that_does_not_read() {
    let session = session(64);
    let (admission, _stream) = started(&session);
    let run = admission.run_id.clone().unwrap();
    let (_, _replay) = resume(&session, &run, 4).unwrap();
    assert!(cancel(&session, &run).unwrap().accepted);
}

#[test]
fn a_resume_from_the_future_is_refused_and_a_wholly_evicted_run_reports_nothing_retained() {
    let session = session(4);
    let (admission, mut stream) = started(&session);
    let first = admission.run_id.clone().unwrap();
    drain(&mut stream);
    assert!(matches!(
        resume(&session, &first, 99),
        Err(Error::ReplayCursorFuture)
    ));
    cancel(&session, &first).unwrap();
    drain(&mut stream);
    let (_, mut second) = started(&session);
    drain(&mut second);
    match resume(&session, &first, 0) {
        Err(Error::ReplayGap(gap)) => {
            assert_eq!(gap.oldest_available, 0);
            assert_ne!(gap.latest_available, 0);
        }
        Err(other) => panic!("{other:?}"),
        Ok(_) => panic!("an evicted run replayed"),
    }
}

#[test]
fn the_adapter_and_session_traits_serve_the_same_script() {
    let memory = memory(64);
    let adapter: &dyn Adapter = &memory;
    assert!(adapter.admitter().is_some());
    let described = block_on(adapter.probe()).unwrap();
    assert_eq!(described.capability_revision, CAPABILITY_REVISION);
    let open = OpenRequest {
        session_id: "session-1".into(),
        ..user_open()
    };
    let session = block_on(adapter.open(open)).unwrap();
    let submitted = SubmitRequest {
        request: plain(),
        ..SubmitRequest::default()
    };
    let (admission, mut stream) = block_on(session.submit(submitted)).unwrap();
    let run = admission.run_id.clone().unwrap();
    let mut events = drain(&mut stream);
    let permission: PermissionRequestedPayload =
        of_type(&events, EnvelopeType::ACTION_PERMISSION_REQUESTED)
            .decode_payload()
            .unwrap();
    block_on(session.resolve(approval(&run, &permission))).unwrap();
    let middle = drain(&mut stream);
    let input: UserInputRequestedPayload = of_type(&middle, EnvelopeType::USER_INPUT_REQUESTED)
        .decode_payload()
        .unwrap();
    events.extend(middle);
    block_on(session.resolve(answering(&run, &input, yes()))).unwrap();
    events.extend(drain(&mut stream));
    assert_eq!(
        events.last().unwrap().envelope_type,
        EnvelopeType::RUN_COMPLETED
    );
    assert_valid(&[submission(plain(), &admission)], &events);
    assert_eq!(
        block_on(session.state()).unwrap().status,
        SessionStatus::IDLE
    );
    let resumed = ResumeRequest {
        run_id: run.clone(),
        after_sequence: 0,
    };
    let (_, mut replay) = block_on(session.resume(resumed)).unwrap();
    assert_eq!(drain(&mut replay).len(), events.len());
    assert!(matches!(
        block_on(session.cancel(run)),
        Err(Error::RunAlreadyTerminal { .. })
    ));
    block_on(session.close()).unwrap();
}
