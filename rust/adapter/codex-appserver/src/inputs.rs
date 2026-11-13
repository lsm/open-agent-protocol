use std::collections::HashSet;
use std::sync::Arc;

use oap_adapter::gojson::decode;
use oap_protocol::{
    EnvelopeType, InputOption, InputQuestion, InputQuestionKind, InteractionId, RunId, RunStatus,
    RunStatusUpdatedPayload, SessionStatus, ToolCallId, UserInputRequestedPayload,
    UserInputResolutionStatus, UserInputResolvedPayload,
};
use serde_json::value::RawValue;

use crate::adapter::ENDPOINT_ID;
use crate::native::UserInputRequestParams;
use crate::rpc::IncomingRequest;
use crate::session::{Inner, Interaction, Shared};

fn questions(params: &UserInputRequestParams) -> Result<Vec<InputQuestion>, &'static str> {
    let mut seen = HashSet::new();
    let mut questions = Vec::with_capacity(params.questions.len());
    for question in &params.questions {
        if question.id.is_empty() || question.question.is_empty() || question.is_secret {
            return Err("invalid user input question");
        }
        if !seen.insert(question.id.as_str()) {
            return Err("duplicate user input question id");
        }
        let options: Vec<InputOption> = question
            .options
            .iter()
            .flatten()
            .enumerate()
            .map(|(index, option)| InputOption {
                id: format!("option-{}", index + 1),
                label: option.label.clone(),
                description: option.description.clone(),
            })
            .collect();
        questions.push(InputQuestion {
            id: question.id.clone(),
            prompt: question.question.clone(),
            kind: if options.is_empty() {
                InputQuestionKind::TEXT
            } else {
                InputQuestionKind::SINGLE_CHOICE
            },
            required: true,
            options,
        });
    }
    Ok(questions)
}

impl Shared {
    pub(crate) fn user_input(&self, request: &Arc<IncomingRequest>) {
        let refuse = |message: &str| {
            let _ = request.respond_error(-32602, message);
        };
        let params = request.params.as_deref().map_or("null", RawValue::get);
        let Some(params) = decode::<UserInputRequestParams>(params.as_bytes())
            .ok()
            .filter(|params| {
                params.thread_id == self.thread_id
                    && !params.turn_id.is_empty()
                    && !params.questions.is_empty()
            })
        else {
            return refuse("invalid user input request");
        };
        let mut inner = self.lock();
        let Some(id) = self
            .run_for(&inner, &params.thread_id, &params.turn_id)
            .filter(|id| !inner.runs[id].terminal)
        else {
            return refuse("user input does not target the active run");
        };
        let questions = match questions(&params) {
            Ok(questions) => questions,
            Err(message) => return refuse(message),
        };
        let interaction = InteractionId::from(self.runtime.new_id("interaction"));
        let order = inner.next_order;
        inner.next_order += 1;
        inner.interactions.insert(
            interaction.clone(),
            Interaction {
                run: id.clone(),
                tool_call: ToolCallId::from(params.item_id),
                request: request.clone(),
                resolved: false,
                choices: Vec::new(),
                questions: Some(questions.clone()),
                order,
            },
        );
        inner.state.status = SessionStatus::WAITING_FOR_INPUT;
        inner.state.updated_at_ms = self.runtime.now_ms();
        let payload = UserInputRequestedPayload {
            interaction_id: interaction.clone(),
            requested_by: ENDPOINT_ID.into(),
            responded_by: self.participant.clone(),
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            title: "Codex needs input".into(),
            questions,
            ..UserInputRequestedPayload::default()
        };
        self.emit(
            &mut inner,
            &id,
            EnvelopeType::USER_INPUT_REQUESTED,
            &payload,
            false,
        );
        self.status(
            &mut inner,
            &id,
            RunStatus::WAITING_FOR_INPUT,
            Some(interaction),
        );
    }

    pub(crate) fn cancel_input(&self, inner: &mut Inner, id: &RunId, interaction: InteractionId) {
        let payload = UserInputResolvedPayload {
            interaction_id: interaction,
            requested_by: ENDPOINT_ID.into(),
            responded_by: self.participant.clone(),
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            status: UserInputResolutionStatus::CANCELLED,
            answers: Vec::new(),
        };
        self.emit(
            inner,
            id,
            EnvelopeType::USER_INPUT_RESOLVED,
            &payload,
            false,
        );
    }

    fn status(
        &self,
        inner: &mut Inner,
        id: &RunId,
        status: RunStatus,
        pending: Option<InteractionId>,
    ) {
        let payload = RunStatusUpdatedPayload {
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            status,
            pending_user_input_id: pending,
            updated_at_ms: self.runtime.now_ms(),
        };
        self.emit(inner, id, EnvelopeType::RUN_STATUS_UPDATED, &payload, false);
    }
}
