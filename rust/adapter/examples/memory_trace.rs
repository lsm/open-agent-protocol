use std::error::Error;

use futures::executor::block_on;
use futures::{FutureExt, StreamExt};
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{
    Adapter, CallResolution, CompactRequest, EventStream, InteractionResolution, OpenRequest,
    SubmitRequest,
};
use oap_protocol::*;

type Failure = Box<dyn Error>;

fn drain(stream: &mut EventStream) -> Result<Vec<Envelope>, Failure> {
    let mut events = Vec::new();
    while let Some(Some(item)) = stream.next().now_or_never() {
        events.push(item?);
    }
    Ok(events)
}

fn find<T: serde::de::DeserializeOwned>(
    events: &[Envelope],
    wanted: EnvelopeType,
) -> Result<T, Failure> {
    let event = events
        .iter()
        .find(|event| event.envelope_type == wanted)
        .ok_or_else(|| format!("no {}", wanted.as_str()))?;
    Ok(event.decode_payload()?)
}

fn reply(
    kinds: [EnvelopeType; 2],
    id: &str,
    stamp: Option<(&str, &str)>,
    request: &impl serde::Serialize,
    response: &impl serde::Serialize,
) -> Result<[Envelope; 2], Failure> {
    let [asked, answered] = kinds;
    let mut request = Envelope::new(asked, id, request)?;
    let mut response = Envelope::new(answered, format!("{id}-response"), response)?;
    response.in_reply_to = Some(request.id.clone());
    for half in [&mut request, &mut response] {
        if let Some((session, revision)) = stamp {
            half.session_id = Some(session.into());
            half.capability_revision = Some(revision.to_owned());
        }
    }
    Ok([request, response])
}

fn main() -> Result<(), Failure> {
    let memory = Memory::new(Config::default());
    let descriptor = block_on(memory.probe())?;
    let revision = descriptor.capability_revision.as_str();
    let participant = Participant {
        id: "user".into(),
        ..Participant::default()
    };
    let mode = std::env::args().nth(1).unwrap_or_default();
    let tools = if mode == "call" {
        vec![ToolDefinition {
            name: "lookup".into(),
            description: "A tool the control layer executes.".into(),
            input_schema: Some(serde_json::value::RawValue::from_string(
                r#"{"type":"object","properties":{"operation":{"type":"string"}}}"#.into(),
            )?),
            execution_owner: "user".into(),
            source: "reference-native".into(),
            ..ToolDefinition::default()
        }]
    } else {
        Vec::new()
    };
    let open = OpenRequest {
        session_id: "session-1".into(),
        participant: participant.clone(),
        tools: tools.clone(),
        ..OpenRequest::default()
    };
    let session = block_on(memory.open(open))?;
    let mut trace = Vec::new();
    trace.extend(reply(
        [
            EnvelopeType::PROTOCOL_INITIALIZE_REQUEST,
            EnvelopeType::PROTOCOL_INITIALIZE_RESPONSE,
        ],
        "initialize",
        None,
        &InitializeRequest {
            protocol_versions: vec![VERSION.into()],
            profiles: vec![PROFILE.into()],
            participant: Some(participant),
        },
        &InitializeResponse {
            protocol_version: VERSION.into(),
            profile: PROFILE.into(),
            endpoint: descriptor.capabilities.endpoint.clone(),
        },
    )?);
    let mut capabilities = reply(
        [
            EnvelopeType::CAPABILITIES_REQUEST,
            EnvelopeType::CAPABILITIES_RESPONSE,
        ],
        "capabilities",
        None,
        &CapabilitiesRequest {},
        &descriptor.capabilities,
    )?;
    capabilities[1].capability_revision = Some(revision.to_owned());
    trace.extend(capabilities);
    let stamp = Some(("session-1", revision));
    trace.extend(reply(
        [
            EnvelopeType::SESSION_OPEN_REQUEST,
            EnvelopeType::SESSION_OPEN_RESPONSE,
        ],
        "open",
        stamp,
        &SessionOpenRequest {
            session_id: Some("session-1".into()),
            tools,
            ..SessionOpenRequest::default()
        },
        &block_on(session.state())?,
    )?);

    if mode == "compact" {
        let compactor = session.compactor().ok_or("the session does not compact")?;
        let request = SessionCompactRequest {
            session_id: "session-1".into(),
            focus: Some("the parser".into()),
            ..SessionCompactRequest::default()
        };
        let (admission, mut stream) = block_on(compactor.compact(CompactRequest {
            request: request.clone(),
            envelope_id: "compact".into(),
        }))?;
        let kinds = [
            EnvelopeType::SESSION_COMPACT_REQUEST,
            EnvelopeType::SESSION_COMPACT_RESPONSE,
        ];
        trace.extend(reply(kinds, "compact", stamp, &request, &admission)?);
        trace.extend(drain(&mut stream)?);
        println!("{}", serde_json::to_string_pretty(&trace)?);
        return Ok(());
    }

    let submitted = MessageSubmitRequest {
        session_id: "session-1".into(),
        delivery: RequestedDeliveryMode::AUTO,
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text("go"),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    };
    let submit = |request: &MessageSubmitRequest, id: &str| {
        block_on(session.submit(SubmitRequest {
            request: request.clone(),
            envelope_id: id.into(),
        }))
    };
    let kinds = [
        EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
        EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE,
    ];
    let (admission, mut stream) = submit(&submitted, "submit")?;
    trace.extend(reply(
        kinds.clone(),
        "submit",
        stamp,
        &submitted,
        &admission,
    )?);
    let run = admission.run_id.ok_or("the submission names no run")?;
    let initial = drain(&mut stream)?;
    trace.extend(initial.iter().cloned());

    if mode == "call" {
        let resolver = session
            .call_resolver()
            .ok_or("the session resolves no calls")?;
        let call: ActionCallPayload = find(&initial, EnvelopeType::ACTION_CALL_REQUESTED)?;
        let started = ActionCallResolveRequest {
            interaction_id: call.interaction_id.ok_or("the call opens no interaction")?,
            session_id: "session-1".into(),
            run_id: run.clone(),
            tool_call_id: call.tool_call_id,
            requested_by: call.requested_by.ok_or("the call names no requester")?,
            responded_by: call.responded_by.ok_or("the call names no responder")?,
            started: Some(ResolveArmStarted {}),
            ..ActionCallResolveRequest::default()
        };
        let finished = ActionCallResolveRequest {
            started: None,
            result: Some(serde_json::value::RawValue::from_string(
                r#"{"found":true}"#.into(),
            )?),
            ..started.clone()
        };
        let kinds = [
            EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
            EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE,
        ];
        for (id, request) in [("call-started", started), ("call-result", finished)] {
            let answer = block_on(resolver.resolve_call(CallResolution {
                request_id: id.into(),
                request: request.clone(),
            }))?;
            let mut exchange = reply(kinds.clone(), id, stamp, &request, &answer)?;
            for half in &mut exchange {
                half.run_id = Some(run.clone());
            }
            trace.extend(exchange);
            trace.extend(drain(&mut stream)?);
        }
    } else {
        let guidance = MessageSubmitRequest {
            delivery: RequestedDeliveryMode::STEER,
            target_run_id: Some(run.clone()),
            messages: vec![Message {
                id: Some("guidance".into()),
                role: MessageRole::USER,
                content: MessageContent::from_text("wait"),
                ..Message::default()
            }],
            ..submitted.clone()
        };
        let (steered, _) = submit(&guidance, "steer")?;
        trace.extend(reply(kinds, "steer", stamp, &guidance, &steered)?);

        let permission: PermissionRequestedPayload =
            find(&initial, EnvelopeType::ACTION_PERMISSION_REQUESTED)?;
        block_on(session.resolve(InteractionResolution {
            run_id: run.clone(),
            responded_by: permission.responded_by.clone(),
            permission: Some(PermissionResolveRequest {
                interaction_id: permission.interaction_id,
                session_id: "session-1".into(),
                run_id: run.clone(),
                requested_by: permission.requested_by,
                responded_by: permission.responded_by,
                choice_id: "approve".into(),
                granted: true,
                ..PermissionResolveRequest::default()
            }),
            input: None,
        }))?;
        trace.extend(drain(&mut stream)?);
    }
    let input: UserInputRequestedPayload = find(&trace, EnvelopeType::USER_INPUT_REQUESTED)?;
    block_on(session.resolve(InteractionResolution {
        run_id: run.clone(),
        responded_by: input.responded_by.clone(),
        permission: None,
        input: Some(UserInputResolveRequest {
            interaction_id: input.interaction_id,
            session_id: "session-1".into(),
            run_id: run,
            requested_by: input.requested_by,
            responded_by: input.responded_by,
            answers: vec![InputAnswer {
                question_id: "choice".into(),
                selected_option_ids: vec!["yes".into()],
                ..InputAnswer::default()
            }],
        }),
    }))?;
    trace.extend(drain(&mut stream)?);
    println!("{}", serde_json::to_string_pretty(&trace)?);
    Ok(())
}
