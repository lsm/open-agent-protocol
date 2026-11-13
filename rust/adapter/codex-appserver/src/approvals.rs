use std::sync::Arc;

use oap_adapter::gojson::decode;
use oap_protocol::{
    EnvelopeType, InteractionId, InteractionOutcome, PermissionChoice, PermissionRequestedPayload,
    PermissionResolvedPayload, ProtocolError, RunId,
};
use serde_json::value::RawValue;

use crate::adapter::ENDPOINT_ID;
use crate::native::{
    CommandApprovalParams, FileApprovalParams, METHOD_COMMAND_APPROVAL, METHOD_FILE_APPROVAL,
};
use crate::rpc::IncomingRequest;
use crate::session::{Inner, Interaction, Shared};

const DECISIONS: [(&str, &str); 4] = [
    ("accept", "Approve once"),
    ("acceptForSession", "Approve for session"),
    ("decline", "Deny"),
    ("cancel", "Deny and cancel run"),
];

struct Scope {
    thread: String,
    turn: String,
    item: String,
    reason: String,
    decisions: Vec<String>,
}

fn decisions(available: Option<&RawValue>) -> Option<Vec<String>> {
    let standard = DECISIONS.map(|(id, _)| id.to_owned()).to_vec();
    let Some(available) = available.filter(|raw| raw.get() != "null") else {
        return Some(standard);
    };
    let listed: Vec<Option<String>> = serde_json::from_str(available.get()).ok()?;
    let mut decisions = Vec::new();
    for listed in listed {
        let listed = listed.unwrap_or_default();
        if !standard.contains(&listed) {
            return None;
        }
        if !decisions.contains(&listed) {
            decisions.push(listed);
        }
    }
    (!decisions.is_empty()).then_some(decisions)
}

fn scope(method: &str, params: &[u8]) -> Option<Scope> {
    let (thread, turn, item, reason, available) = if method == METHOD_COMMAND_APPROVAL {
        let params: CommandApprovalParams = decode(params).ok()?;
        let available = params.available_decisions;
        let reason = params.reason;
        (
            params.thread_id,
            params.turn_id,
            params.item_id,
            reason,
            available,
        )
    } else {
        let params: FileApprovalParams = decode(params).ok()?;
        let reason = params.reason;
        (
            params.thread_id,
            params.turn_id,
            params.item_id,
            reason,
            None,
        )
    };
    if thread.is_empty() || turn.is_empty() || item.is_empty() {
        return None;
    }
    Some(Scope {
        thread,
        turn,
        item,
        reason: reason.unwrap_or_default(),
        decisions: decisions(available.as_deref())?,
    })
}

impl Shared {
    pub(crate) fn approval(&self, request: &Arc<IncomingRequest>) {
        let refuse = |message: &str| {
            let _ = request.respond_error(-32602, message);
        };
        let params = request.params.as_deref().map_or("null", RawValue::get);
        let Some(scope) = scope(&request.method, params.as_bytes())
            .filter(|scope| scope.thread == self.thread_id)
        else {
            return refuse("invalid approval request scope");
        };
        let mut inner = self.lock();
        let Some(id) = self
            .run_for(&inner, &scope.thread, &scope.turn)
            .filter(|id| !inner.runs[id].terminal)
        else {
            return refuse("approval request does not target the active run");
        };
        let Some(item) = inner
            .items
            .get(&scope.item)
            .filter(|item| item.run == id && !item.terminal)
            .cloned()
        else {
            return refuse("approval request does not target an active action");
        };
        let interaction = InteractionId::from(self.runtime.new_id("interaction"));
        let order = inner.next_order;
        inner.next_order += 1;
        let choices = DECISIONS
            .iter()
            .filter(|(choice, _)| scope.decisions.iter().any(|offered| offered == choice))
            .map(|(choice, label)| PermissionChoice {
                id: (*choice).into(),
                label: (*label).into(),
                ..PermissionChoice::default()
            })
            .collect();
        inner.interactions.insert(
            interaction.clone(),
            Interaction {
                run: id.clone(),
                tool_call: item.tool_call.clone(),
                request: request.clone(),
                resolved: false,
                order,
            },
        );
        let payload = PermissionRequestedPayload {
            interaction_id: interaction,
            requested_by: ENDPOINT_ID.into(),
            responded_by: self.participant.clone(),
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            tool_call_id: Some(item.tool_call.clone()),
            title: if request.method == METHOD_FILE_APPROVAL {
                "Allow Codex file changes"
            } else {
                "Allow Codex action"
            }
            .into(),
            description: scope.reason,
            choices,
            arguments_json: Some(item.arguments),
        };
        self.emit_call(
            &mut inner,
            &id,
            EnvelopeType::ACTION_PERMISSION_REQUESTED,
            &payload,
            &item.tool_call,
        );
    }

    pub(crate) fn close_interactions(&self, inner: &mut Inner, id: &RunId, interrupted: bool) {
        let mut pending: Vec<(InteractionId, Interaction)> = inner
            .interactions
            .iter_mut()
            .filter(|(_, interaction)| &interaction.run == id && !interaction.resolved)
            .map(|(key, interaction)| {
                interaction.resolved = true;
                (key.clone(), interaction.clone())
            })
            .collect();
        pending.sort_by_key(|(_, interaction)| interaction.order);
        for (key, interaction) in pending {
            let _ = interaction
                .request
                .respond_error(-32800, "OAP run terminated before interaction resolution");
            let payload = PermissionResolvedPayload {
                interaction_id: key,
                requested_by: ENDPOINT_ID.into(),
                responded_by: self.participant.clone(),
                session_id: inner.state.session_id.clone(),
                run_id: id.clone(),
                tool_call_id: Some(interaction.tool_call.clone()),
                outcome: if interrupted {
                    InteractionOutcome::CANCELLED
                } else {
                    InteractionOutcome::FAILED
                },
                reason: Some(ProtocolError {
                    code: "run_terminated".into(),
                    message: "run terminated before permission resolution".into(),
                    ..ProtocolError::default()
                }),
                ..PermissionResolvedPayload::default()
            };
            self.emit_call(
                inner,
                id,
                EnvelopeType::ACTION_PERMISSION_RESOLVED,
                &payload,
                &interaction.tool_call,
            );
        }
    }
}
