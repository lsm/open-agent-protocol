mod support;

use support::*;

const RESOLVE: [EnvelopeType; 2] = [
    EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
    EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE,
];

fn providing() -> OpenRequest {
    OpenRequest {
        tools: vec![ToolDefinition {
            name: "lookup".into(),
            description: "A tool the control layer executes.".into(),
            input_schema: raw(r#"{"type":"object","properties":{"operation":{"type":"string"}}}"#),
            execution_owner: "user".into(),
            source: "reference-native".into(),
            ..ToolDefinition::default()
        }],
        ..session_open()
    }
}

struct Called {
    session: MemorySession,
    run: RunId,
    stream: EventStream,
    events: Vec<Envelope>,
    call: ActionCallPayload,
}

fn called() -> Called {
    let session = memory(64).open_session(&providing()).unwrap();
    let (admission, mut stream, submitted) = submit_as(&session, "submit-request", plain());
    let initial = drain(&mut stream);
    assert_eq!(
        initial.last().map(|event| &event.envelope_type),
        Some(&EnvelopeType::ACTION_CALL_REQUESTED)
    );
    let call: ActionCallPayload = initial.last().unwrap().decode_payload().unwrap();
    assert!(call.interaction_id.is_some() && call.responded_by.is_some());
    assert_eq!(call.execution_owner.as_str(), "user");
    Called {
        session,
        run: admission.run_id.unwrap(),
        stream,
        events: [submitted.to_vec(), initial].concat(),
        call,
    }
}

fn result_arm(call: &ActionCallPayload) -> ActionCallResolveRequest {
    ActionCallResolveRequest {
        interaction_id: call.interaction_id.clone().unwrap(),
        session_id: call.session_id.clone(),
        run_id: call.run_id.clone(),
        tool_call_id: call.tool_call_id.clone(),
        requested_by: call.requested_by.clone().unwrap(),
        responded_by: call.responded_by.clone().unwrap(),
        result: raw(r#"{"found":true}"#),
        ..ActionCallResolveRequest::default()
    }
}

fn acknowledgement(call: &ActionCallPayload) -> ActionCallResolveRequest {
    ActionCallResolveRequest {
        result: None,
        started: Some(ResolveArmStarted {}),
        ..result_arm(call)
    }
}

fn resolve_call(
    session: &MemorySession,
    id: &str,
    request: ActionCallResolveRequest,
) -> (ActionCallResolveResponse, [Envelope; 2]) {
    let resolver = Session::call_resolver(session).expect("the memory session resolves calls");
    let answer = block_on(resolver.resolve_call(CallResolution {
        request_id: id.into(),
        request: request.clone(),
    }))
    .unwrap();
    let run = request.run_id.clone();
    let asked = exchange(RESOLVE, id, Some(&run), &request, &answer);
    (answer, asked)
}

fn refused(answer: &ActionCallResolveResponse) -> ResolveReason {
    assert!(!answer.accepted, "{answer:?}");
    answer.reason.clone()
}

#[test]
fn an_acknowledged_then_resolved_call_runs_to_a_valid_completion() {
    let Called {
        session,
        run,
        mut stream,
        mut events,
        call,
    } = called();
    let (acked, asked) = resolve_call(&session, "resolve-ack", acknowledgement(&call));
    assert!(acked.accepted, "{acked:?}");
    events.extend(asked);
    let started = drain(&mut stream);
    assert_eq!(
        started
            .iter()
            .map(|e| e.envelope_type.as_str())
            .collect::<Vec<_>>(),
        ["action.call.started"]
    );
    assert_eq!(
        state(&session).active_runs[0].acknowledged_interactions,
        [call.interaction_id.clone().unwrap()]
    );
    events.extend(started);
    let (resolved, asked) = resolve_call(&session, "resolve-result", result_arm(&call));
    assert!(resolved.accepted, "{resolved:?}");
    assert_eq!(
        resolved.interaction_id,
        call.interaction_id.clone().unwrap()
    );
    assert_eq!(resolved.tool_call_id, call.tool_call_id);
    events.extend(asked);
    let gated = drain(&mut stream);
    let finished: ActionCallPayload = of_type(&gated, EnvelopeType::ACTION_CALL_COMPLETED)
        .decode_payload()
        .unwrap();
    assert_eq!(
        finished.result.as_ref().map(|raw| raw.get()),
        Some(r#"{"found":true}"#)
    );
    assert_eq!(
        finished.request_id.as_ref().map(EnvelopeId::as_str),
        Some("resolve-result")
    );
    answer(&session, &run, &gated);
    events.extend(gated);
    events.extend(drain(&mut stream));
    assert_eq!(
        events.last().unwrap().envelope_type,
        EnvelopeType::RUN_COMPLETED
    );
    assert_flow(&providing(), &[&events]);
}

#[test]
fn an_unacknowledged_resolution_still_starts_the_call_before_its_terminal() {
    let Called {
        session,
        mut stream,
        call,
        ..
    } = called();
    let (answer, _) = resolve_call(&session, "resolve-1", result_arm(&call));
    assert!(answer.accepted, "{answer:?}");
    let rest = drain(&mut stream);
    assert_eq!(rest[0].envelope_type, EnvelopeType::ACTION_CALL_STARTED);
    assert_eq!(rest[1].envelope_type, EnvelopeType::ACTION_CALL_COMPLETED);
    let started: ActionCallPayload = rest[0].decode_payload().unwrap();
    assert_eq!(
        started.request_id.as_ref().map(EnvelopeId::as_str),
        Some("resolve-1")
    );
}

#[test]
fn an_error_resolution_fails_the_call_and_the_run_carries_on() {
    let Called {
        session,
        run,
        mut stream,
        mut events,
        call,
    } = called();
    let failing = ActionCallResolveRequest {
        result: None,
        error: Some(ProtocolError {
            code: "lookup_failed".into(),
            message: "nothing found".into(),
            ..ProtocolError::default()
        }),
        ..result_arm(&call)
    };
    let (resolved, asked) = resolve_call(&session, "resolve-error", failing);
    assert!(resolved.accepted, "{resolved:?}");
    events.extend(asked);
    let rest = drain(&mut stream);
    let failed: ActionCallPayload = of_type(&rest, EnvelopeType::ACTION_CALL_FAILED)
        .decode_payload()
        .unwrap();
    assert_eq!(
        failed.error.map(|error| error.code),
        Some("lookup_failed".into())
    );
    assert_eq!(
        rest.last().unwrap().envelope_type,
        EnvelopeType::RUN_STATUS_UPDATED
    );
    answer(&session, &run, &rest);
    events.extend(rest);
    events.extend(drain(&mut stream));
    assert_flow(&providing(), &[&events]);
}

#[test]
fn a_refused_resolution_reports_the_highest_reason_on_the_ladder() {
    let Called { session, call, .. } = called();
    let stranger = ActionCallResolveRequest {
        interaction_id: "no-such-interaction".into(),
        responded_by: "someone-else".into(),
        ..result_arm(&call)
    };
    let (answer, _) = resolve_call(&session, "resolve-1", stranger);
    assert_eq!(refused(&answer), ResolveReason::UNKNOWN_INTERACTION);
    let armless = ActionCallResolveRequest {
        result: None,
        ..result_arm(&call)
    };
    let (answer, _) = resolve_call(&session, "resolve-2", armless);
    assert_eq!(refused(&answer), ResolveReason::UNKNOWN_INTERACTION);
    let both = ActionCallResolveRequest {
        started: Some(ResolveArmStarted {}),
        ..result_arm(&call)
    };
    let (answer, _) = resolve_call(&session, "resolve-3", both);
    assert_eq!(refused(&answer), ResolveReason::UNKNOWN_INTERACTION);

    let (answer, _) = resolve_call(&session, "resolve-4", acknowledgement(&call));
    assert!(answer.accepted);
    let (answer, _) = resolve_call(&session, "resolve-5", acknowledgement(&call));
    assert_eq!(refused(&answer), ResolveReason::REPEATED_ACKNOWLEDGEMENT);

    let Called {
        session,
        mut stream,
        call,
        ..
    } = called();
    let (answer, _) = resolve_call(&session, "resolve-1", result_arm(&call));
    assert!(answer.accepted);
    let rest = drain(&mut stream);
    let terminal = of_type(&rest, EnvelopeType::ACTION_CALL_COMPLETED);
    let foreign = ActionCallResolveRequest {
        responded_by: "someone-else".into(),
        ..result_arm(&call)
    };
    let (answer, _) = resolve_call(&session, "resolve-2", foreign);
    assert_eq!(refused(&answer), ResolveReason::WRONG_RESPONDER);
    assert_eq!(answer.details, None);
    for (id, again) in [
        ("resolve-3", result_arm(&call)),
        ("resolve-4", acknowledgement(&call)),
    ] {
        let (answer, _) = resolve_call(&session, id, again);
        assert_eq!(refused(&answer), ResolveReason::ALREADY_RESOLVED);
        assert_eq!(
            answer.details.and_then(|details| details.settlement_id),
            Some(terminal.id.clone())
        );
    }
}

#[test]
fn a_session_without_a_pending_call_knows_no_interaction_and_a_closed_one_refuses() {
    let session = session(64);
    let (admission, mut stream) = started(&session);
    let scripted = of_type(&drain(&mut stream), EnvelopeType::ACTION_CALL_REQUESTED).clone();
    let request = ActionCallResolveRequest {
        interaction_id: "call-1".into(),
        session_id: "session-1".into(),
        run_id: admission.run_id.clone().unwrap(),
        tool_call_id: scripted.tool_call_id.unwrap(),
        requested_by: "reference.memory".into(),
        responded_by: "user".into(),
        result: raw("{}"),
        ..ActionCallResolveRequest::default()
    };
    for interaction in ["call-1", ""] {
        let named = ActionCallResolveRequest {
            interaction_id: interaction.into(),
            ..request.clone()
        };
        let (answer, _) = resolve_call(&session, "resolve-1", named);
        assert_eq!(refused(&answer), ResolveReason::UNKNOWN_INTERACTION);
    }

    let idle = support::session(64);
    idle.close().unwrap();
    let resolver = Session::call_resolver(&idle).unwrap();
    let outcome = block_on(resolver.resolve_call(CallResolution {
        request_id: "resolve-2".into(),
        request,
    }));
    assert_eq!(outcome.err(), Some(Error::SessionClosed));
}

#[test]
fn a_cancel_closes_a_pending_provided_call() {
    let Called {
        session,
        run,
        mut stream,
        mut events,
        ..
    } = called();
    let cancelled = cancel(&session, &run).unwrap();
    let request = RunCancelRequest {
        session_id: "session-1".into(),
        run_id: run.clone(),
        ..RunCancelRequest::default()
    };
    events.extend(exchange(
        [
            EnvelopeType::RUN_CANCEL_REQUEST,
            EnvelopeType::RUN_CANCEL_RESPONSE,
        ],
        "cancel-request",
        Some(&run),
        &request,
        &cancelled,
    ));
    let rest = drain(&mut stream);
    of_type(&rest, EnvelopeType::ACTION_CALL_CANCELLED);
    assert_eq!(
        rest.last().unwrap().envelope_type,
        EnvelopeType::RUN_CANCELLED
    );
    events.extend(rest);
    assert_flow(&providing(), &[&events]);
}
