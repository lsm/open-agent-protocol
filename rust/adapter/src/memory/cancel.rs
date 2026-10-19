use oap_protocol::*;

use super::session::{golden_arguments, Emit, Inner, Stage};
use super::{SCRIPTED_SOURCE, SCRIPTED_TOOL, SCRIPTED_TOOL_OWNER};
use crate::Error;

impl Inner {
    pub(super) fn scripted_call(
        &self,
        run_id: &str,
        arguments: bool,
        responded: bool,
    ) -> ActionCallPayload {
        let run = self.run(run_id);
        ActionCallPayload {
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            tool_call_id: ToolCallId::from(run.tool_call_id.as_str()),
            name: SCRIPTED_TOOL.to_owned(),
            arguments_json: if arguments { golden_arguments() } else { None },
            requested_by: Some(ParticipantId::from(run.requested_by.as_str())),
            responded_by: responded.then(|| ParticipantId::from(run.responded_by.as_str())),
            execution_owner: ParticipantId::from(SCRIPTED_TOOL_OWNER),
            source: SCRIPTED_SOURCE.to_owned(),
            ..ActionCallPayload::default()
        }
    }

    pub(super) fn cancel(&mut self, run_id: &RunId) -> Result<RunCancelResponse, Error> {
        if self.closed {
            return Err(Error::SessionClosed);
        }
        let session = self.session_id();
        let accepted = |status: RunStatus| RunCancelResponse {
            session_id: session.clone(),
            run_id: run_id.clone(),
            accepted: true,
            status,
        };
        let id = run_id.as_str().to_owned();
        let run = self.runs.get(&id).ok_or(Error::RunNotFound)?;
        if run.terminal {
            if run.status == RunStatus::CANCELLED {
                return Ok(accepted(RunStatus::CANCELLED));
            }
            return Err(Error::RunAlreadyTerminal {
                run_id: Some(run_id.clone()),
                status: Some(run.status.clone()),
            });
        }
        if run.status == RunStatus::CANCELLING {
            return Ok(accepted(RunStatus::CANCELLING));
        }
        let reservation = !run.started;
        let stage = run.stage;
        self.run_mut(&id).status = RunStatus::CANCELLING;
        let cancelled = |reason: &str| RunCancelledPayload {
            session_id: session.clone(),
            run_id: run_id.clone(),
            reason: reason.into(),
            ..RunCancelledPayload::default()
        };
        if reservation {
            let _ = self.emit(
                &id,
                EnvelopeType::RUN_CANCELLED,
                cancelled("reservation cancelled before promotion"),
                true,
            );
            return Ok(accepted(RunStatus::CANCELLING));
        }
        let status = RunStatusUpdatedPayload {
            session_id: session.clone(),
            run_id: run_id.clone(),
            status: RunStatus::CANCELLING,
            updated_at_ms: self.clock.now_ms(),
            ..RunStatusUpdatedPayload::default()
        };
        if let Err(Emit::Failed(err)) =
            self.emit(&id, EnvelopeType::RUN_STATUS_UPDATED, status, false)
        {
            return Err(err);
        }
        let run = self.run(&id);
        let (requested_by, responded_by) = (
            ParticipantId::from(run.requested_by.as_str()),
            ParticipantId::from(run.responded_by.as_str()),
        );
        if stage == Stage::Call {
            let provided = ActionCallPayload {
                interaction_id: Some(InteractionId::from(run.call_id.as_str())),
                session_id: session.clone(),
                run_id: run_id.clone(),
                tool_call_id: ToolCallId::from(run.tool_call_id.as_str()),
                name: run.provided_tool.name.clone(),
                requested_by: Some(requested_by.clone()),
                responded_by: Some(responded_by.clone()),
                execution_owner: run.provided_tool.execution_owner.clone(),
                source: run.provided_tool.source.clone(),
                ..ActionCallPayload::default()
            };
            let _ = self.emit(&id, EnvelopeType::ACTION_CALL_CANCELLED, provided, false);
        }
        if stage == Stage::Permission {
            let run = self.run(&id);
            let resolved = PermissionResolvedPayload {
                interaction_id: InteractionId::from(run.permission_id.as_str()),
                requested_by: requested_by.clone(),
                responded_by: responded_by.clone(),
                session_id: session.clone(),
                run_id: run_id.clone(),
                tool_call_id: Some(ToolCallId::from(run.tool_call_id.as_str())),
                outcome: InteractionOutcome::CANCELLED,
                reason: Some(ProtocolError {
                    code: "run_cancelled".into(),
                    message: "run cancellation closed the permission request".into(),
                    ..ProtocolError::default()
                }),
                ..PermissionResolvedPayload::default()
            };
            let _ = self.emit(
                &id,
                EnvelopeType::ACTION_PERMISSION_RESOLVED,
                resolved,
                false,
            );
            let _ = self.emit(
                &id,
                EnvelopeType::ACTION_CALL_CANCELLED,
                self.scripted_call(&id, false, true),
                false,
            );
        } else if stage == Stage::Input {
            let resolved = UserInputResolvedPayload {
                interaction_id: InteractionId::from(self.run(&id).input_id.as_str()),
                requested_by,
                responded_by,
                session_id: session.clone(),
                run_id: run_id.clone(),
                status: UserInputResolutionStatus::CANCELLED,
                ..UserInputResolvedPayload::default()
            };
            let _ = self.emit(&id, EnvelopeType::USER_INPUT_RESOLVED, resolved, false);
        }
        let _ = self.emit(
            &id,
            EnvelopeType::RUN_CANCELLED,
            cancelled("cancel confirmed"),
            true,
        );
        Ok(accepted(RunStatus::CANCELLING))
    }
}
