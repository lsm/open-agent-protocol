use oap_protocol::*;

use super::session::{golden_arguments, surfaced, Emit, Inner, Stage};
use crate::{CallResolution, Error};

type Refusal = (ResolveReason, Option<EnvelopeId>);

impl Inner {
    fn admit_call(&self, request: &ActionCallResolveRequest) -> Result<&'static str, Refusal> {
        let unknown = (ResolveReason::UNKNOWN_INTERACTION, None);
        let Some(run) = self.runs.get(request.run_id.as_str()) else {
            return Err(unknown);
        };
        if run.call_id.is_empty()
            || request.interaction_id.as_str() != run.call_id
            || request.session_id != self.state.session_id
        {
            return Err(unknown);
        }
        if request.responded_by.as_str() != run.responded_by
            || request.requested_by.as_str() != run.requested_by
            || request.tool_call_id.as_str() != run.tool_call_id
        {
            return Err((ResolveReason::WRONG_RESPONDER, None));
        }
        let Some(arm) = request.arm() else {
            return Err(unknown);
        };
        if run.settled_arm.is_some() {
            if arm == RESOLVE_ARM_ACKNOWLEDGE && run.settlement_id.is_none() {
                return Err((ResolveReason::LATE_ACKNOWLEDGEMENT, None));
            }
            let settlement = run
                .settlement_id
                .clone()
                .unwrap_or_else(|| run.settled_request_id.clone());
            return Err((ResolveReason::ALREADY_RESOLVED, Some(settlement)));
        }
        if run.terminal || run.stage != Stage::Call {
            return Err((ResolveReason::ALREADY_RESOLVED, run.settlement_id.clone()));
        }
        if arm == RESOLVE_ARM_ACKNOWLEDGE && run.acknowledged {
            return Err((ResolveReason::REPEATED_ACKNOWLEDGEMENT, None));
        }
        Ok(arm)
    }

    pub(super) fn resolve_call(
        &mut self,
        resolution: CallResolution,
    ) -> Result<ActionCallResolveResponse, Error> {
        if self.closed {
            return Err(Error::SessionClosed);
        }
        let request = resolution.request;
        let mut answer = ActionCallResolveResponse {
            interaction_id: request.interaction_id.clone(),
            session_id: request.session_id.clone(),
            run_id: request.run_id.clone(),
            tool_call_id: request.tool_call_id.clone(),
            ..ActionCallResolveResponse::default()
        };
        let arm = match self.admit_call(&request) {
            Ok(arm) => arm,
            Err((reason, settlement)) => {
                answer.details = (reason == ResolveReason::ALREADY_RESOLVED).then_some(
                    ActionCallResolveDetails {
                        settlement_id: settlement,
                    },
                );
                answer.reason = reason;
                return Ok(answer);
            }
        };
        answer.accepted = true;
        let run_id = request.run_id.as_str().to_owned();
        if arm == RESOLVE_ARM_ACKNOWLEDGE {
            self.run_mut(&run_id).acknowledged = true;
            let started = ActionCallPayload {
                arguments_json: golden_arguments(),
                ..self.call_payload(&run_id, &resolution.request_id)
            };
            surfaced(self.emit(&run_id, EnvelopeType::ACTION_CALL_STARTED, started, false))?;
            return Ok(answer);
        }
        let run = self.run_mut(&run_id);
        run.settled_arm = Some(arm);
        run.settled_request_id = resolution.request_id;
        run.settled_result = request.result;
        run.settled_error = request.error;
        run.stage = Stage::Input;
        let acknowledged = run.acknowledged;
        surfaced(self.settle_call(&run_id, acknowledged))?;
        Ok(answer)
    }

    fn call_payload(&self, run_id: &str, request_id: &EnvelopeId) -> ActionCallPayload {
        let run = self.run(run_id);
        ActionCallPayload {
            interaction_id: Some(InteractionId::from(run.call_id.as_str())),
            request_id: Some(request_id.clone()),
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            tool_call_id: ToolCallId::from(run.tool_call_id.as_str()),
            name: run.provided_tool.name.clone(),
            requested_by: Some(ParticipantId::from(run.requested_by.as_str())),
            responded_by: Some(ParticipantId::from(run.responded_by.as_str())),
            execution_owner: run.provided_tool.execution_owner.clone(),
            source: run.provided_tool.source.clone(),
            ..ActionCallPayload::default()
        }
    }

    fn settle_call(&mut self, run_id: &str, acknowledged: bool) -> Result<(), Emit> {
        let request_id = self.run(run_id).settled_request_id.clone();
        if !acknowledged {
            let started = ActionCallPayload {
                arguments_json: golden_arguments(),
                ..self.call_payload(run_id, &request_id)
            };
            self.emit(run_id, EnvelopeType::ACTION_CALL_STARTED, started, false)?;
        }
        let mut terminal = self.call_payload(run_id, &request_id);
        let run = self.run(run_id);
        let kind = if run.settled_arm == Some(RESOLVE_ARM_ERROR) {
            terminal.error = run.settled_error.clone();
            EnvelopeType::ACTION_CALL_FAILED
        } else {
            terminal.result = run.settled_result.clone();
            EnvelopeType::ACTION_CALL_COMPLETED
        };
        self.emit(run_id, kind, terminal, false)?;
        self.request_input(run_id)
    }
}
