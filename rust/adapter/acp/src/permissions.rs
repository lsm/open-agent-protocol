use std::{collections::BTreeMap, sync::Arc};

use oap_adapter::{Error, InteractionResolution};
use oap_protocol::{
    EnvelopeId, EnvelopeType, InteractionId, ParticipantId, PermissionChoice,
    PermissionRequestedPayload, PermissionResolvedPayload, ProtocolError, RunStatus,
};

use crate::native::{PermissionOutcome, PermissionRequest, PermissionResponse};
use crate::TextRun;

pub(crate) struct Gate {
    requested: PermissionRequestedPayload,
    event: EnvelopeId,
    options: BTreeMap<String, bool>,
    resolved: bool,
    identity: Arc<()>,
}

pub struct PermissionReply {
    id: InteractionId,
    identity: Arc<()>,
    response: PermissionResponse,
}

impl PermissionReply {
    pub fn interaction_id(&self) -> &InteractionId {
        &self.id
    }
    pub fn response(&self) -> &PermissionResponse {
        &self.response
    }
}

impl TextRun {
    pub fn permission_request(
        &mut self,
        request: PermissionRequest,
        participant: ParticipantId,
        now_ms: i64,
    ) -> Result<InteractionId, Error> {
        if self.state().closed {
            return Err(Error::SessionClosed);
        }
        if self.state().status.is_empty() || self.pending.is_some() {
            return Err(Error::InvalidSubmission);
        }
        if request.session_id != self.context.native_id
            || request.tool_call.tool_call_id.is_empty()
            || request.options.is_empty()
        {
            return self.permission_error(
                "acp_invalid_permission",
                "malformed permission request",
                "invalid permission request",
                now_ms,
            );
        }
        let raw = serde_json::to_vec(&request.tool_call).expect("native tool is JSON");
        let (events, fault) = self.tools.observe(&raw, false, &mut *self.ids);
        self.emit_tools(events, now_ms)?;
        if let Some((code, message)) = fault {
            return self.permission_error(code, message, "invalid tool call", now_ms);
        }
        let tool = self
            .tools
            .payload(&request.tool_call.tool_call_id)
            .expect("observed tool exists");
        let id: InteractionId = (self.ids)("interaction").into();
        let mut options = BTreeMap::new();
        let mut choices = Vec::new();
        for option in request.options {
            if option.option_id.is_empty()
                || option.name.is_empty()
                || !matches!(
                    option.kind.as_str(),
                    "allow_once" | "allow_always" | "reject_once" | "reject_always"
                )
            {
                continue;
            }
            options.insert(option.option_id.clone(), option.kind.starts_with("allow_"));
            choices.push(PermissionChoice {
                id: option.option_id,
                label: option.name,
                description: option.kind,
            });
        }
        if choices.is_empty() {
            return self.permission_error(
                "acp_invalid_permission",
                "empty permission options",
                "permission options required",
                now_ms,
            );
        }
        if self.state().status == RunStatus::CANCELLING {
            return Err(Error::InteractionResolved);
        }
        let requested = PermissionRequestedPayload {
            interaction_id: id.clone(),
            requested_by: "acp.v1".into(),
            responded_by: participant,
            session_id: self.context.session_id.clone(),
            run_id: self.context.run_id.clone(),
            tool_call_id: Some(tool.tool_call_id),
            title: request.tool_call.title,
            choices,
            arguments_json: tool.arguments_json,
            ..PermissionRequestedPayload::default()
        };
        let event = self.emit(
            EnvelopeType::ACTION_PERMISSION_REQUESTED,
            &requested,
            RunStatus::RUNNING,
            None,
            now_ms,
        )?;
        self.permissions.push(Gate {
            requested,
            event,
            options,
            resolved: false,
            identity: Arc::new(()),
        });
        Ok(id)
    }

    fn permission_error(
        &mut self,
        code: &str,
        message: &str,
        reply: &str,
        now_ms: i64,
    ) -> Result<InteractionId, Error> {
        self.fail(code, message, false, now_ms)?;
        Err(Error::Harness(reply.into()))
    }

    pub fn permission_reply(
        &self,
        resolution: &InteractionResolution,
    ) -> Result<PermissionReply, Error> {
        let request = resolution
            .permission
            .as_ref()
            .filter(|_| resolution.input.is_none())
            .ok_or(Error::InvalidResolution)?;
        let gate = self
            .permissions
            .iter()
            .find(|gate| gate.requested.interaction_id == request.interaction_id)
            .ok_or(Error::InteractionNotFound)?;
        if gate.resolved {
            return Err(Error::InteractionResolved);
        }
        if self.pending.is_some() || self.state().closed {
            return Err(Error::InvalidResolution);
        }
        let payload = &gate.requested;
        if request.run_id != payload.run_id
            || resolution.run_id != payload.run_id
            || request.session_id != payload.session_id
        {
            return Err(Error::InvalidResolution);
        }
        if resolution.responded_by != payload.responded_by
            || request.responded_by != payload.responded_by
        {
            return Err(Error::WrongResponder);
        }
        if !request.requested_by.as_str().is_empty() && request.requested_by.as_str() != "acp.v1" {
            return Err(Error::InvalidResolution);
        }
        if gate.options.get(&request.choice_id) != Some(&request.granted) {
            return Err(Error::InvalidResolution);
        }
        Ok(gate.reply("selected", &request.choice_id))
    }

    pub fn permission_settlements(&self) -> Vec<PermissionReply> {
        if self.pending.is_none() && self.state().status != RunStatus::CANCELLING {
            return Vec::new();
        }
        self.permissions
            .iter()
            .filter(|gate| !gate.resolved)
            .map(|gate| gate.reply("cancelled", ""))
            .collect()
    }

    pub fn commit_permission(&mut self, reply: PermissionReply, now_ms: i64) -> Result<(), Error> {
        let index = self
            .permissions
            .iter()
            .position(|gate| gate.requested.interaction_id == reply.id)
            .ok_or(Error::InteractionNotFound)?;
        let gate = &self.permissions[index];
        if gate.resolved {
            return Err(Error::InteractionResolved);
        }
        if !Arc::ptr_eq(&gate.identity, &reply.identity) {
            return Err(Error::InvalidResolution);
        }
        let requested = &gate.requested;
        let choice = reply.response.outcome.option_id;
        let granted = (reply.response.outcome.outcome == "selected").then(|| gate.options[&choice]);
        let payload = PermissionResolvedPayload {
            interaction_id: reply.id,
            requested_by: requested.requested_by.clone(),
            responded_by: requested.responded_by.clone(),
            session_id: requested.session_id.clone(),
            run_id: requested.run_id.clone(),
            tool_call_id: requested.tool_call_id.clone(),
            outcome: match granted {
                Some(true) => "resolved".into(),
                Some(false) => "rejected".into(),
                None => "cancelled".into(),
            },
            choice_id: choice,
            granted,
            reason: granted.is_none().then(|| ProtocolError {
                code: if self.state().status == RunStatus::CANCELLING {
                    "run_cancelling"
                } else {
                    "run_settled"
                }
                .into(),
                message: if self.state().status == RunStatus::CANCELLING {
                    "the run's cancellation answered the permission request cancelled"
                } else {
                    "parent run settled the permission request"
                }
                .into(),
                ..ProtocolError::default()
            }),
        };
        self.emit(
            EnvelopeType::ACTION_PERMISSION_RESOLVED,
            &payload,
            RunStatus::RUNNING,
            Some(gate.event.clone()),
            now_ms,
        )?;
        self.permissions[index].resolved = true;
        if !self.has_permissions() {
            self.finish_pending(now_ms)?;
        }
        Ok(())
    }

    pub(crate) fn has_permissions(&self) -> bool {
        self.permissions.iter().any(|gate| !gate.resolved)
    }
}

impl Gate {
    fn reply(&self, outcome: &str, option: &str) -> PermissionReply {
        PermissionReply {
            id: self.requested.interaction_id.clone(),
            identity: self.identity.clone(),
            response: PermissionResponse {
                outcome: PermissionOutcome {
                    outcome: outcome.into(),
                    option_id: option.into(),
                },
            },
        }
    }
}
