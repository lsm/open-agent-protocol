use oxap_protocol::{
    Envelope, EnvelopeType, InputAnswer, Message, MessageContent, MessageRole,
    MessageSubmitRequest, MessageSubmitResponse, PermissionRequestedPayload,
    PermissionResolveRequest, RequestedDeliveryMode, RunId, UserInputRequestedPayload,
    UserInputResolveRequest,
};
use serde::Serialize;

use super::{decode_payload, Check, Runner};

impl Runner<'_> {
    pub fn submit_run(&mut self, model: &str, switched: bool) -> Option<RunId> {
        let payload = MessageSubmitRequest {
            session_id: self.session.clone(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("drive one scripted run"),
                ..Message::default()
            }],
            model_id: (!switched && !model.is_empty()).then(|| model.to_owned()),
            ..MessageSubmitRequest::default()
        };
        let answer = self.request(EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST, &payload);
        let answer = self.record("session.message.submit.request is answered", answer)?;
        let admission: MessageSubmitResponse = self.decode(&answer, "the admission decodes")?;
        const ADMITTED: &str = "the submission is admitted and names its run";
        let run = admission.run_id.filter(|run| !run.as_str().is_empty());
        if !admission.accepted || run.is_none() {
            self.checks.push(Check::failed(
                ADMITTED,
                format!("accepted={} run={run:?}", admission.accepted),
            ));
            return None;
        }
        self.checks.push(Check::passed(ADMITTED));
        if switched {
            const MODEL: &str = "the switched model is used by the next run";
            self.checks.push(if admission.model_id == model {
                Check::passed(MODEL)
            } else {
                Check::failed(
                    MODEL,
                    format!("admission used {:?}, want {model:?}", admission.model_id),
                )
            });
        }
        const DELIVERY: &str =
            "the admission repeats requested_delivery and reports a concrete effective_delivery";
        self.checks.push(
            if admission.requested_delivery != RequestedDeliveryMode::AUTO {
                Check::failed(
                    DELIVERY,
                    format!(
                        "requested_delivery came back {:?}, want auto",
                        admission.requested_delivery
                    ),
                )
            } else if admission.effective_delivery.is_empty()
                || admission.effective_delivery.as_str() == RequestedDeliveryMode::AUTO.as_str()
            {
                Check::failed(
                    DELIVERY,
                    format!(
                        "effective_delivery is {:?}; auto must resolve to a concrete mode",
                        admission.effective_delivery
                    ),
                )
            } else {
                Check::passed(DELIVERY)
            },
        );
        run
    }

    pub fn consume_run(&mut self, run: &RunId) -> bool {
        let mut sequence = 0;
        loop {
            let event = self
                .client
                .event_for(run)
                .map_err(|error| error.to_string());
            let Some(event) = self.result("the run reaches a terminal event", event) else {
                return false;
            };
            if let Some(next) = event.sequence {
                if next <= sequence {
                    self.checks.push(Check::failed(
                        "run events carry an advancing per-run sequence",
                        format!("sequence {next} did not advance past {sequence}"),
                    ));
                    return false;
                }
                sequence = next;
            }
            let (name, outcome) = match &event.envelope_type {
                kind if *kind == EnvelopeType::ACTION_PERMISSION_REQUESTED => (
                    "a permission gate is resolvable from the stream",
                    self.answer_permission(&event),
                ),
                kind if *kind == EnvelopeType::USER_INPUT_REQUESTED => (
                    "a user input gate is resolvable from the stream",
                    self.answer_input(&event),
                ),
                kind if [
                    EnvelopeType::RUN_COMPLETED,
                    EnvelopeType::RUN_FAILED,
                    EnvelopeType::RUN_CANCELLED,
                ]
                .contains(kind) =>
                {
                    let mut check = Check::passed("the run reaches a terminal event");
                    check.detail = format!("settled {}", event.envelope_type);
                    self.checks.push(check);
                    return true;
                }
                _ => continue,
            };
            if self.result(name, outcome).is_none() {
                return false;
            }
            self.checks.push(Check::passed(name));
        }
    }

    fn answer_permission(&mut self, event: &Envelope) -> Result<Envelope, String> {
        let gate: PermissionRequestedPayload = decode_payload(event)?;
        let choice = gate
            .choices
            .first()
            .ok_or("the permission gate offers no choices, so it cannot be answered")?;
        let payload = PermissionResolveRequest {
            interaction_id: gate.interaction_id,
            requested_by: gate.requested_by,
            responded_by: gate.responded_by,
            session_id: gate.session_id,
            run_id: gate.run_id.clone(),
            choice_id: choice.id.clone(),
            granted: true,
            ..PermissionResolveRequest::default()
        };
        self.resolve_gate(
            EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST,
            &payload,
            gate.run_id,
        )
    }

    fn answer_input(&mut self, event: &Envelope) -> Result<Envelope, String> {
        let gate: UserInputRequestedPayload = decode_payload(event)?;
        let answers = gate
            .questions
            .into_iter()
            .map(|question| {
                let mut answer = InputAnswer {
                    question_id: question.id,
                    ..InputAnswer::default()
                };
                if let Some(option) = question.options.first() {
                    answer.selected_option_ids.push(option.id.clone());
                } else {
                    answer.text = "conformance".into();
                }
                answer
            })
            .collect();
        let payload = UserInputResolveRequest {
            interaction_id: gate.interaction_id,
            requested_by: gate.requested_by,
            responded_by: gate.responded_by,
            session_id: gate.session_id,
            run_id: gate.run_id.clone(),
            answers,
        };
        self.resolve_gate(
            EnvelopeType::USER_INPUT_RESOLVE_REQUEST,
            &payload,
            gate.run_id,
        )
    }

    fn resolve_gate(
        &mut self,
        kind: EnvelopeType,
        payload: &impl Serialize,
        run: RunId,
    ) -> Result<Envelope, String> {
        let mut envelope = self.envelope(kind, payload)?;
        envelope.run_id = Some(run);
        self.request_envelope(envelope)
    }
}
