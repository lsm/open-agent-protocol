use std::collections::BTreeMap;

use oap_adapter::gojson::go_marshal;
use oap_adapter::{index_input_answers, validate_input_answer, Error, InteractionResolution};
use oap_protocol::{
    EnvelopeType, InputAnswer, InputQuestion, InputQuestionKind, RunStatus,
    RunStatusUpdatedPayload, UserInputResolutionStatus, UserInputResolvedPayload,
};
use serde_json::value::{to_raw_value, RawValue};

use crate::adapter::ENDPOINT_ID;
use crate::native::{
    ApprovalResult, ClarifyAnswersResult, ValueResult, REQUEST_APPROVAL, REQUEST_CLARIFY,
};
use crate::session::Shared;

impl Shared {
    pub(crate) fn resolve(&self, resolution: InteractionResolution) -> Result<(), Error> {
        let input = resolution
            .input
            .as_ref()
            .ok_or(Error::InteractionNotFound)?;
        let mut inner = self.lock();
        let session_id = inner.state.session_id.clone();
        let run = inner.run.as_mut().ok_or(Error::InteractionNotFound)?;
        let run_id = run.id.clone();
        let gate = run
            .interactions
            .iter_mut()
            .find(|gate| gate.id == input.interaction_id)
            .filter(|gate| !gate.resolved && !gate.settling)
            .ok_or(Error::InteractionNotFound)?;
        let responder = match resolution.responded_by.as_str() {
            "" => &input.responded_by,
            _ => &resolution.responded_by,
        };
        if (!resolution.run_id.as_str().is_empty() && resolution.run_id != run_id)
            || (!input.session_id.as_str().is_empty() && input.session_id != session_id)
            || (!input.run_id.as_str().is_empty() && input.run_id != run_id)
            || (!responder.as_str().is_empty() && *responder != self.participant)
            || (!input.requested_by.as_str().is_empty()
                && input.requested_by.as_str() != ENDPOINT_ID)
        {
            return Err(Error::InvalidResolution);
        }
        let result =
            answer(&gate.kind, &gate.questions, &input.answers).ok_or(Error::InvalidResolution)?;
        gate.settling = true;
        let request = gate.request.clone();
        drop(inner);
        let written = request.respond(&result);
        let mut inner = self.lock();
        let Some(run) = inner.run.as_mut().filter(|run| run.id == run_id) else {
            return written.map_err(Error::harness);
        };
        let Some(gate) = run
            .interactions
            .iter_mut()
            .find(|gate| gate.id == input.interaction_id)
        else {
            return written.map_err(Error::harness);
        };
        gate.settling = false;
        let withdrawn = std::mem::take(&mut gate.withdrawn);
        let (id, requested) = (gate.id.clone(), gate.requested.clone());
        if let Err(error) = written {
            self.flush_deferred(&mut inner);
            return Err(Error::harness(error));
        }
        gate.resolved = true;
        let mut resolved = UserInputResolvedPayload {
            interaction_id: id.clone(),
            requested_by: ENDPOINT_ID.into(),
            responded_by: self.participant.clone(),
            session_id: session_id.clone(),
            run_id: run_id.clone(),
            status: UserInputResolutionStatus::CANCELLED,
            ..UserInputResolvedPayload::default()
        };
        if !withdrawn {
            resolved.status = UserInputResolutionStatus::SUBMITTED;
            resolved.answers = input.answers.clone();
        }
        let kind = EnvelopeType::USER_INPUT_RESOLVED;
        self.emit_input(&mut inner, kind, &resolved, &id, Some(requested));
        let running = RunStatusUpdatedPayload {
            session_id,
            run_id,
            status: RunStatus::RUNNING,
            updated_at_ms: self.runtime.now_ms(),
            ..RunStatusUpdatedPayload::default()
        };
        self.emit(
            &mut inner,
            EnvelopeType::RUN_STATUS_UPDATED,
            &running,
            false,
        );
        self.flush_deferred(&mut inner);
        match withdrawn {
            true => Err(Error::InteractionNotFound),
            false => Ok(()),
        }
    }
}

fn answer(
    kind: &str,
    questions: &[InputQuestion],
    answers: &[InputAnswer],
) -> Option<Box<RawValue>> {
    let single = || match (questions, answers) {
        ([question], [answer]) if validate_input_answer(question, answer).is_ok() => Some(answer),
        _ => None,
    };
    match kind {
        REQUEST_APPROVAL => {
            let choice = single()?.selected_option_ids.first()?.clone();
            to_raw_value(&ApprovalResult { choice }).ok()
        }
        REQUEST_CLARIFY => {
            let indexed = index_input_answers(questions, answers).ok()?;
            let mut values = BTreeMap::new();
            for question in questions {
                let answer = indexed.get(question.id.as_str())?;
                let value = if question.kind == InputQuestionKind::TEXT {
                    answer.text.clone()
                } else if question.kind == InputQuestionKind::MULTI_CHOICE {
                    go_marshal(&serde_json::to_string(&answer.selected_option_ids).ok()?)
                } else {
                    answer.selected_option_ids.first()?.clone()
                };
                values.insert(question.id.clone(), value);
            }
            to_raw_value(&ClarifyAnswersResult { answers: values }).ok()
        }
        _ => to_raw_value(&ValueResult {
            value: single()?.text.clone(),
        })
        .ok(),
    }
}
