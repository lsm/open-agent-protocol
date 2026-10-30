use std::sync::Arc;

use futures::executor::block_on;
use futures::{FutureExt, StreamExt};
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    PermissionRequestedPayload, PermissionResolveRequest, RequestedDeliveryMode, RunCancelRequest,
    RunId, SessionStateRequest,
};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubSession, Registry};
use serde_json::Value;

fn opened() -> (Hub, Arc<HubSession>) {
    let hub = Hub::new(Registry::default_registry());
    let session = block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap()
    .session;
    (hub, session)
}

fn started(session: &Arc<HubSession>) -> RunId {
    block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "s".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("go"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap()
    .run_id
    .unwrap()
}

fn envelope(kind: EnvelopeType, payload: &impl serde::Serialize) -> String {
    let mut envelope = Envelope::new(kind, "req-1", payload).unwrap();
    envelope.capability_revision = Some("rev-cited".into());
    envelope.session_id = Some("s".into());
    let value = serde_json::to_value(payload).unwrap();
    envelope.run_id = value["run_id"].as_str().map(Into::into);
    serde_json::to_string(&envelope).unwrap()
}

fn answer(hub: &Hub, op: &str, request: &str) -> Value {
    let line = format!("{{\"id\":9,\"op\":\"{op}\",\"session_id\":\"s\",\"request\":{request}}}\n");
    let mut output = Vec::new();
    serve(hub, line.as_bytes(), &mut output, &StdioOptions::default()).unwrap();
    serde_json::from_slice(&output).unwrap()
}

#[test]
fn a_request_envelope_is_required_well_formed_schematic_and_of_the_ops_type() {
    let (hub, _) = opened();
    let missing = answer(&hub, "cancel", "null");
    assert_eq!(missing["error"]["code"], "invalid_request");
    assert_eq!(
        missing["error"]["message"],
        "the op requires a \"request\" envelope"
    );
    assert_eq!(
        answer(&hub, "cancel", "\"text\"")["error"]["code"],
        "malformed_json"
    );
    assert_eq!(
        answer(&hub, "cancel", "{\"type\":\"run.cancel.request\"}")["error"]["code"],
        "schema_invalid"
    );
    let state = envelope(
        EnvelopeType::SESSION_STATE_REQUEST,
        &SessionStateRequest {
            session_id: "s".into(),
        },
    );
    let mismatch = answer(&hub, "cancel", &state);
    assert_eq!(mismatch["error"]["code"], "type_mismatch");
    assert_eq!(
        mismatch["error"]["message"],
        "op expects run.cancel.request, got session.state.request"
    );
}

#[test]
fn cancel_answers_a_run_cancel_response_citing_the_request() {
    let (hub, session) = opened();
    let run = started(&session);
    let request = envelope(
        EnvelopeType::RUN_CANCEL_REQUEST,
        &RunCancelRequest {
            session_id: "s".into(),
            run_id: run.clone(),
            ..RunCancelRequest::default()
        },
    );
    let line = answer(&hub, "cancel", &request);
    let response = &line["result"];
    assert_eq!(line["ok"], true, "{line}");
    assert_eq!(response["type"], "run.cancel.response");
    assert_eq!(response["in_reply_to"], "req-1");
    assert_eq!(response["run_id"], run.as_str());
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["capability_revision"], "rev-cited");
    assert_eq!(response["payload"]["accepted"], true);
}

#[test]
fn cancel_refuses_a_payload_scoped_to_another_session_or_an_unknown_run() {
    let (hub, _) = opened();
    let elsewhere = envelope(
        EnvelopeType::RUN_CANCEL_REQUEST,
        &RunCancelRequest {
            session_id: "other".into(),
            run_id: "r".into(),
            ..RunCancelRequest::default()
        },
    );
    let refused = answer(&hub, "cancel", &elsewhere);
    assert_eq!(refused["error"]["code"], "scope_mismatch");
    assert_eq!(
        refused["error"]["message"],
        "payload session_id \"other\" does not match the addressed session \"s\""
    );
    let unknown = envelope(
        EnvelopeType::RUN_CANCEL_REQUEST,
        &RunCancelRequest {
            session_id: "s".into(),
            run_id: "missing".into(),
            ..RunCancelRequest::default()
        },
    );
    assert_eq!(
        answer(&hub, "cancel", &unknown)["error"]["code"],
        "run_not_found"
    );
}

#[test]
fn resolve_grants_a_pending_permission_and_refuses_one_already_resolved() {
    let (hub, session) = opened();
    let run = started(&session);
    let mut replay = block_on(hub.subscribe_after(&"s".into(), Some(run.clone()), 0)).unwrap();
    let mut permission = None;
    while let Some(Some(item)) = replay.next().now_or_never() {
        let envelope = item.unwrap();
        if envelope.envelope_type == EnvelopeType::ACTION_PERMISSION_REQUESTED {
            permission = Some(
                envelope
                    .decode_payload::<PermissionRequestedPayload>()
                    .unwrap(),
            );
        }
    }
    let permission = permission.unwrap();
    let request = envelope(
        EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST,
        &PermissionResolveRequest {
            interaction_id: permission.interaction_id.clone(),
            requested_by: permission.requested_by.clone(),
            responded_by: permission.responded_by.clone(),
            session_id: "s".into(),
            run_id: run.clone(),
            choice_id: "approve".into(),
            granted: true,
            ..PermissionResolveRequest::default()
        },
    );
    let line = answer(&hub, "resolve", &request);
    assert_eq!(line["ok"], true, "{line}");
    let response = &line["result"];
    assert_eq!(response["type"], "action.permission.resolve.response");
    assert_eq!(response["in_reply_to"], "req-1");
    assert_eq!(response["run_id"], run.as_str());
    assert_eq!(
        response["payload"]["interaction_id"],
        permission.interaction_id.as_str()
    );
    assert_eq!(response["payload"]["accepted"], true);
    assert_eq!(
        answer(&hub, "resolve", &request)["error"]["code"],
        "resolution_rejected"
    );
}
