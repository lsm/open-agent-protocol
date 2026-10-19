mod support;

use support::*;

#[test]
fn a_submission_starts_the_script_and_waits_at_its_permission_gate() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    assert_eq!(admission.delivery_resolution, "session_idle");
    assert_eq!(admission.effective_delivery, EffectiveDeliveryMode::START);
    let events = drain(&mut stream);
    types_and_sequence(&events, &INITIAL, 1);
    assert!(events
        .iter()
        .all(|event| event.capability_revision.as_deref() == Some(CAPABILITY_REVISION)));
    let permission: PermissionRequestedPayload = events[3].decode_payload().unwrap();
    assert_eq!(
        (
            permission.requested_by.as_str(),
            permission.responded_by.as_str()
        ),
        ("reference.memory", "user")
    );
    let state = state(&session);
    assert_eq!(state.status, SessionStatus::WAITING_FOR_INPUT);
    assert_eq!(state.active_run_id, admission.run_id);
    assert_eq!(
        state.active_runs[0].pending_interactions,
        [permission.interaction_id]
    );
    assert_eq!(state.transcript_cursor, "4");
}

#[test]
fn a_tool_choice_decides_whether_the_script_calls_its_tool() {
    for (policy, calls) in [
        (r#"{"disallowed":["scripted_tool"]}"#, 0),
        (r#"{"allowed":[]}"#, 0),
        (r#"{"allowed":["scripted_tool"]}"#, 1),
    ] {
        let session = session(64);
        let request = MessageSubmitRequest {
            tool_choice: raw(policy),
            ..plain()
        };
        let (_, mut stream) = submit(&session, request);
        let events = drain(&mut stream);
        let made = events
            .iter()
            .filter(|event| event.envelope_type == EnvelopeType::ACTION_CALL_REQUESTED)
            .count();
        assert_eq!(made, calls, "{policy}");
    }
    let session = session(64);
    let request = MessageSubmitRequest {
        tool_choice: raw(r#"{"allowed":[]}"#),
        instructions: Some("Be terse.".into()),
        ..plain()
    };
    let (_, mut stream) = submit(&session, request);
    let events = drain(&mut stream);
    types_and_sequence(
        &events,
        &[
            EnvelopeType::RUN_STARTED,
            EnvelopeType::CONTENT_DELTA,
            EnvelopeType::USER_INPUT_REQUESTED,
            EnvelopeType::RUN_STATUS_UPDATED,
        ],
        1,
    );
    let delta: ContentDeltaPayload = events[1].decode_payload().unwrap();
    assert!(
        delta.part.text.starts_with("Be terse."),
        "{}",
        delta.part.text
    );
    assert!(events[2].tool_call_id.is_none());
}

#[test]
fn a_busy_session_reserves_one_run_behind_it_and_refuses_another() {
    let session = session(64);
    let (first, _stream) = started(&session);
    let queued = MessageSubmitRequest {
        delivery: RequestedDeliveryMode::QUEUE,
        ..plain()
    };
    let (reservation, mut reserved) = submit(&session, queued);
    assert_eq!(reservation.admission, Admission::QUEUED);
    assert_eq!(reservation.status, RunStatus::QUEUED);
    assert_eq!(reservation.delivery_resolution, "session_busy");
    assert!(drain(&mut reserved).is_empty());
    let state = state(&session);
    assert_eq!(state.active_run_id, first.run_id);
    let positions: Vec<_> = state
        .active_runs
        .iter()
        .map(|entry| (entry.run_id.clone(), entry.queue_position))
        .collect();
    assert_eq!(
        positions,
        [
            (first.run_id.unwrap(), None),
            (reservation.run_id.unwrap(), Some(1))
        ]
    );
    assert!(matches!(
        try_submit(&session, plain()),
        Err(Error::RunActive)
    ));
}

#[test]
fn a_submission_missing_its_session_or_messages_is_refused_before_admission() {
    let session = session(64);
    let no_messages = MessageSubmitRequest {
        messages: Vec::new(),
        ..plain()
    };
    let no_session = MessageSubmitRequest {
        session_id: SessionId::default(),
        ..plain()
    };
    for request in [no_messages, no_session] {
        assert!(matches!(
            try_submit(&session, request),
            Err(Error::InvalidSubmission)
        ));
    }
    assert!(idle(&session));
}

#[test]
fn a_model_applies_to_its_own_run_and_an_unknown_one_is_refused_by_name() {
    let session = session(64);
    for model in ["another-model", ""] {
        let request = MessageSubmitRequest {
            model_id: Some(model.into()),
            ..plain()
        };
        match try_submit(&session, request) {
            Err(Error::ModelNotFound(named)) => assert_eq!(named, model),
            Err(other) => panic!("{other:?}"),
            Ok(_) => panic!("{model:?} was admitted"),
        }
    }
    assert!(idle(&session));
    assert_eq!(state(&session).current_model_id, "");
    let request = MessageSubmitRequest {
        model_id: Some(MODEL_SECONDARY.into()),
        ..plain()
    };
    let (admission, mut stream) = submit(&session, request);
    assert_eq!(admission.model_id, MODEL_SECONDARY);
    let events = drain(&mut stream);
    let started: RunStartedPayload = events[0].decode_payload().unwrap();
    assert_eq!(started.model_id, MODEL_SECONDARY);
    assert_eq!(state(&session).current_model_id, "");
}

#[test]
fn every_run_control_is_judged_and_a_refusal_allocates_nothing() {
    let cases = [
        (
            "untyped tool choice",
            r#""none""#,
            "",
            FEATURE_TOOL_SELECTION,
            "",
        ),
        (
            "unknown member",
            r#"{"allowed":["scripted_tool"],"limit":2}"#,
            "",
            FEATURE_TOOL_SELECTION,
            "",
        ),
        (
            "allowed and disallowed",
            r#"{"allowed":["scripted_tool"],"disallowed":["scripted_tool"]}"#,
            "",
            FEATURE_TOOL_SELECTION,
            "",
        ),
        (
            "outside the catalog",
            r#"{"allowed":["absent_tool"]}"#,
            "",
            FEATURE_TOOL_SELECTION,
            "absent_tool",
        ),
        (
            "non-object schema",
            "",
            r#"{"type":"array"}"#,
            FEATURE_STRUCTURED_OUTPUT,
            "",
        ),
        (
            "external reference",
            "",
            r#"{"type":"object","properties":{"a":{"$ref":"https://example.test/s.json"}}}"#,
            FEATURE_STRUCTURED_OUTPUT,
            "",
        ),
        (
            "uncompilable schema",
            "",
            r#"{"type":"object","required":"x"}"#,
            FEATURE_STRUCTURED_OUTPUT,
            "",
        ),
        (
            "unsatisfied schema",
            "",
            r#"{"type":"object","required":["answer"]}"#,
            FEATURE_STRUCTURED_OUTPUT,
            "",
        ),
    ];
    for (name, choice, schema, feature, tool) in cases {
        let session = session(64);
        let request = MessageSubmitRequest {
            tool_choice: (!choice.is_empty()).then(|| raw(choice)).flatten(),
            output_schema: (!schema.is_empty()).then(|| raw(schema)).flatten(),
            ..plain()
        };
        let control = refusal(
            try_submit(&session, request)
                .err()
                .unwrap_or_else(|| panic!("{name}: admitted")),
        );
        assert_eq!(
            (control.feature.as_str(), control.reason),
            (feature, CONTROL_UNSATISFIABLE),
            "{name}"
        );
        assert_eq!(control.tool, tool, "{name}");
        assert!(idle(&session), "{name}");
    }
}

#[test]
fn the_lowest_capability_key_names_the_refusal_and_outranks_ordinary_validation() {
    let unsatisfiable = r#"{"allowed":["absent_tool"]}"#;
    let uncompilable = r#"{"type":"object","required":"x"}"#;
    let session = session(64);
    let both = MessageSubmitRequest {
        output_schema: raw(uncompilable),
        tool_choice: raw(unsatisfiable),
        ..plain()
    };
    assert_eq!(
        refusal(try_submit(&session, both.clone()).err().unwrap()).feature,
        FEATURE_STRUCTURED_OUTPUT
    );
    for request in [
        MessageSubmitRequest {
            model_id: Some("no-such-model".into()),
            ..both.clone()
        },
        MessageSubmitRequest {
            model_id: Some("no-such-model".into()),
            tool_choice: raw(unsatisfiable),
            ..plain()
        },
    ] {
        assert!(matches!(
            try_submit(&session, request),
            Err(Error::ModelNotFound(_))
        ));
    }
    for request in [
        MessageSubmitRequest {
            messages: Vec::new(),
            tool_choice: raw(unsatisfiable),
            ..plain()
        },
        MessageSubmitRequest {
            session_id: SessionId::default(),
            tool_choice: raw(unsatisfiable),
            ..plain()
        },
        MessageSubmitRequest {
            delivery: RequestedDeliveryMode::from("sideways"),
            tool_choice: raw(unsatisfiable),
            ..plain()
        },
    ] {
        let control = refusal(try_submit(&session, request).err().unwrap());
        assert_eq!(
            (control.feature.as_str(), control.reason),
            (FEATURE_TOOL_SELECTION, CONTROL_UNSATISFIABLE)
        );
    }
    assert!(idle(&session));
    let bare = MessageSubmitRequest {
        messages: Vec::new(),
        model_id: Some(MODEL_SECONDARY.into()),
        ..plain()
    };
    assert!(matches!(
        try_submit(&session, bare),
        Err(Error::InvalidSubmission)
    ));
}

#[test]
fn a_session_with_a_live_run_refuses_to_close_and_keeps_the_stream() {
    let session = session(64);
    let (_, mut stream) = started(&session);
    assert_eq!(session.close(), Err(Error::RunActive));
    assert!(!drain(&mut stream).is_empty());
}

#[test]
fn a_delivery_the_adapter_does_not_serve_is_refused_under_its_own_key() {
    let session = session(64);
    for (delivery, key) in [
        (RequestedDeliveryMode::BTW, FEATURE_DELIVERY_BTW),
        (RequestedDeliveryMode::STEER, FEATURE_DELIVERY_STEER),
    ] {
        let request = MessageSubmitRequest {
            delivery,
            ..plain()
        };
        let control = refusal(try_submit(&session, request).err().unwrap());
        assert_eq!(
            (control.feature.as_str(), control.reason),
            (key, CONTROL_UNADVERTISED)
        );
    }
}

#[test]
fn a_closed_session_reopens_once_and_declares_itself_recovered() {
    let memory = memory(64);
    let session = open_with(&memory, "user");
    session.close().unwrap();
    assert_eq!(state(&session).status, SessionStatus::CLOSED);
    assert!(matches!(
        try_submit(&session, plain()),
        Err(Error::SessionClosed)
    ));
    let reopen = OpenRequest {
        session_id: "session-1".into(),
        reopen: true,
        ..user_open()
    };
    let reopened = memory.open_session(&reopen).unwrap();
    assert!(state(&reopened)
        .recovery
        .is_some_and(|recovery| recovery.recovered));
    assert!(matches!(
        memory.open_session(&reopen),
        Err(Error::UnknownSession(_))
    ));
}
