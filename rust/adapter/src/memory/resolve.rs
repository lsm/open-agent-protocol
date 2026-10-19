use oap_protocol::*;

use super::admit::history_tokens;
use super::session::{golden_input_questions, surfaced, Emit, Inner, Stage};
use super::{raw, FIXED_RESULT};
use crate::{validate_input_answer, Error, InteractionResolution};

impl Inner {
    pub(super) fn resolve(&mut self, resolution: InteractionResolution) -> Result<(), Error> {
        if self.closed {
            return Err(Error::SessionClosed);
        }
        let session = self.session_id();
        let run_id = resolution.run_id.as_str().to_owned();
        let run = self.runs.get(&run_id).ok_or(Error::RunNotFound)?;
        if run.terminal {
            return Err(Error::InteractionResolved);
        }
        if !run.started {
            return Err(Error::InteractionNotFound);
        }
        if resolution.responded_by.as_str() != run.responded_by {
            return Err(Error::WrongResponder);
        }
        let owned = |interaction: &InteractionId,
                     s: &SessionId,
                     r: &RunId,
                     requested: &ParticipantId,
                     responded: &ParticipantId,
                     id: &str| {
            interaction.as_str() == id
                && *s == session
                && r.as_str() == run.id
                && requested.as_str() == run.requested_by
                && responded.as_str() == run.responded_by
        };
        match run.stage {
            Stage::Permission => {
                let Some(permission) = resolution
                    .permission
                    .as_ref()
                    .filter(|_| resolution.input.is_none())
                else {
                    return Err(Error::InvalidResolution);
                };
                if !owned(
                    &permission.interaction_id,
                    &permission.session_id,
                    &permission.run_id,
                    &permission.requested_by,
                    &permission.responded_by,
                    &run.permission_id,
                ) {
                    return Err(Error::InvalidResolution);
                }
                let granted = match permission.choice_id.as_str() {
                    "approve" => true,
                    "deny" => false,
                    _ => return Err(Error::InvalidResolution),
                };
                if granted != permission.granted {
                    return Err(Error::InvalidResolution);
                }
                let permission = permission.clone();
                self.run_mut(&run_id).stage = Stage::Input;
                surfaced(self.resolve_permission(&run_id, permission))
            }
            Stage::Input => {
                let Some(input) = resolution
                    .input
                    .as_ref()
                    .filter(|_| resolution.permission.is_none())
                else {
                    return Err(Error::InvalidResolution);
                };
                if !owned(
                    &input.interaction_id,
                    &input.session_id,
                    &input.run_id,
                    &input.requested_by,
                    &input.responded_by,
                    &run.input_id,
                ) {
                    return Err(Error::InvalidResolution);
                }
                let [answer] = input.answers.as_slice() else {
                    return Err(Error::InvalidResolution);
                };
                validate_input_answer(&golden_input_questions()[0], answer)?;
                let input = input.clone();
                self.run_mut(&run_id).stage = Stage::Terminal;
                surfaced(self.resolve_input(&run_id, input))
            }
            Stage::Call => Err(Error::InteractionNotFound),
            Stage::Terminal => Err(Error::InteractionResolved),
        }
    }

    fn resolve_permission(
        &mut self,
        run_id: &str,
        request: PermissionResolveRequest,
    ) -> Result<(), Emit> {
        let run = self.run(run_id);
        let resolved = PermissionResolvedPayload {
            interaction_id: InteractionId::from(run.permission_id.as_str()),
            requested_by: ParticipantId::from(run.requested_by.as_str()),
            responded_by: ParticipantId::from(run.responded_by.as_str()),
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            tool_call_id: Some(ToolCallId::from(run.tool_call_id.as_str())),
            outcome: InteractionOutcome::RESOLVED,
            choice_id: request.choice_id.clone(),
            granted: Some(request.granted),
            reason: None,
        };
        self.emit(
            run_id,
            EnvelopeType::ACTION_PERMISSION_RESOLVED,
            resolved,
            false,
        )?;
        if !request.granted {
            self.emit(
                run_id,
                EnvelopeType::ACTION_CALL_CANCELLED,
                self.scripted_call(run_id, false, true),
                false,
            )?;
            let failure = RunFailedPayload {
                session_id: self.session_id(),
                run_id: RunId::from(run_id),
                error: ProtocolError {
                    code: "permission_denied".into(),
                    message: "scripted tool permission denied".into(),
                    ..ProtocolError::default()
                },
                ..RunFailedPayload::default()
            };
            return self.emit(run_id, EnvelopeType::RUN_FAILED, failure, true);
        }
        self.emit(
            run_id,
            EnvelopeType::ACTION_CALL_STARTED,
            self.scripted_call(run_id, true, false),
            false,
        )?;
        let completed = ActionCallPayload {
            result: Some(raw(r#"{"ok":true}"#)),
            ..self.scripted_call(run_id, false, false)
        };
        self.emit(
            run_id,
            EnvelopeType::ACTION_CALL_COMPLETED,
            completed,
            false,
        )?;
        self.request_input(run_id)
    }

    fn resolve_input(
        &mut self,
        run_id: &str,
        request: UserInputResolveRequest,
    ) -> Result<(), Emit> {
        let run = self.run(run_id);
        let resolved = UserInputResolvedPayload {
            interaction_id: InteractionId::from(run.input_id.as_str()),
            requested_by: ParticipantId::from(run.requested_by.as_str()),
            responded_by: ParticipantId::from(run.responded_by.as_str()),
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            status: UserInputResolutionStatus::SUBMITTED,
            answers: request.answers,
        };
        self.emit(run_id, EnvelopeType::USER_INPUT_RESOLVED, resolved, false)?;
        let final_text = "The golden script completed.";
        let message_id = MessageId::from(self.ids.new_id("message"));
        let delta = ContentDeltaPayload {
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            message_id: Some(message_id.clone()),
            part: ContentPart {
                part_type: ContentPartType::TEXT,
                text: final_text.into(),
                ..ContentPart::default()
            },
        };
        self.emit(run_id, EnvelopeType::CONTENT_DELTA, delta, false)?;
        let run = self.run(run_id);
        let final_response = Message {
            id: Some(message_id),
            role: MessageRole::ASSISTANT,
            content: MessageContent::from_text(final_text),
            ..Message::default()
        };
        let reply = history_tokens(std::slice::from_ref(&final_response));
        let completed = RunCompletedPayload {
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            final_response,
            stop_reason: "end_turn".into(),
            model_id: run.controls.model.clone(),
            result: run
                .controls
                .output_schema
                .as_ref()
                .map(|_| raw(FIXED_RESULT)),
            ..RunCompletedPayload::default()
        };
        self.history += reply;
        let outcome = self.emit(run_id, EnvelopeType::RUN_COMPLETED, completed, true);
        if matches!(outcome, Err(Emit::TerminalWon)) {
            self.history -= reply;
        }
        outcome
    }
}
