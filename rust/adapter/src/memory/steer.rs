use oap_protocol::*;

use super::pipe::Pipe;
use super::session::{Emit, Inner, Run, Steer};
use crate::STEER_UNKNOWN_TARGET;
use crate::{Error, EventStream, SubmitRequest, UnsupportedControl, CONTROL_UNSATISFIABLE};
use crate::{STEER_NOT_STEERABLE, STEER_NO_ACTIVE_RUN, STEER_QUEUED, STEER_TERMINAL};

type Refusal<'a> = (Option<&'a Run>, &'static str);

fn refuse_steer_controls(request: &MessageSubmitRequest) -> Result<(), Error> {
    let carried = [
        (request.instructions.is_some(), FEATURE_INSTRUCTIONS),
        (request.model_id.is_some(), FEATURE_MODEL_SELECTION),
        (request.output_schema.is_some(), FEATURE_STRUCTURED_OUTPUT),
        (request.tool_choice.is_some(), FEATURE_TOOL_SELECTION),
    ];
    let lowest = carried
        .into_iter()
        .filter_map(|(present, key)| present.then_some(key))
        .min();
    match lowest {
        Some(key) => Err(Error::UnsupportedControl(Box::new(UnsupportedControl {
            feature: key.to_owned(),
            reason: CONTROL_UNSATISFIABLE,
            ..UnsupportedControl::default()
        }))),
        None => Ok(()),
    }
}

impl Inner {
    fn steer_target(&self, target: &str) -> Result<&Run, Refusal<'_>> {
        if !target.is_empty() {
            let Some(run) = self.runs.get(target) else {
                return Err((None, STEER_UNKNOWN_TARGET));
            };
            let reason = if run.terminal {
                STEER_TERMINAL
            } else if run.status == RunStatus::CANCELLING {
                STEER_NOT_STEERABLE
            } else if !run.started {
                STEER_QUEUED
            } else {
                return Ok(run);
            };
            return Err((Some(run), reason));
        }
        for id in [&self.active, &self.reserved].into_iter().flatten() {
            let Some(run) = self.runs.get(id) else {
                continue;
            };
            if !run.started || run.terminal {
                continue;
            }
            if run.status == RunStatus::CANCELLING {
                return Err((Some(run), STEER_NOT_STEERABLE));
            }
            return Ok(run);
        }
        Err((None, STEER_NO_ACTIVE_RUN))
    }

    pub(super) fn steer(
        &mut self,
        submit: SubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        let request = submit.request;
        if request.session_id.as_str().is_empty() || request.messages.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        refuse_steer_controls(&request)?;
        if self.closed {
            return Err(Error::SessionClosed);
        }
        if request.session_id != self.state.session_id {
            return Err(Error::RunNotFound);
        }
        let target = request.target_run_id.unwrap_or_default();
        let run = self
            .steer_target(target.as_str())
            .map_err(|(run, reason)| Error::InvalidSteerTarget {
                run_id: target.clone(),
                reason,
                target_sequence: run.map(|run| run.next_sequence - 1),
            })?;
        let (run_id, status, sequence) =
            (run.id.clone(), run.status.clone(), run.next_sequence - 1);
        let messages: Vec<MessageId> = request
            .messages
            .iter()
            .map(|message| {
                message
                    .id
                    .clone()
                    .unwrap_or_else(|| MessageId::from(self.ids.new_id("message")))
            })
            .collect();
        let submission_id = SubmissionId::from(self.ids.new_id("submission"));
        let run = self.run_mut(&run_id);
        run.steers.push(Steer {
            submission_id: submission_id.clone(),
            request_id: submit.envelope_id.clone(),
            messages: messages.clone(),
        });
        run.admitted_steers.push(submit.envelope_id);
        self.state.updated_at_ms = self.clock.now_ms();
        self.refresh_state();
        let admission = MessageSubmitResponse {
            session_id: self.session_id(),
            accepted: true,
            submission_id,
            requested_delivery: RequestedDeliveryMode::STEER,
            effective_delivery: EffectiveDeliveryMode::STEER,
            admission: Admission::STEERED,
            run_id: Some(RunId::from(run_id.as_str())),
            status,
            target_sequence: Some(sequence),
            message_ids: messages,
            ..MessageSubmitResponse::default()
        };
        let pipe = Pipe::default();
        pipe.close();
        Ok((admission, pipe.stream()))
    }

    pub(super) fn settle_steers(
        &mut self,
        run_id: &str,
        boundary: SteerBoundary,
    ) -> Result<(), Emit> {
        for steer in self.run(run_id).steers.clone() {
            let applied = RunSteerAppliedPayload {
                session_id: self.session_id(),
                run_id: RunId::from(run_id),
                submission_id: steer.submission_id.clone(),
                request_id: steer.request_id,
                message_ids: steer.messages,
                boundary: boundary.clone(),
            };
            let settles = Some(&steer.submission_id);
            self.publish(
                run_id,
                EnvelopeType::RUN_STEER_APPLIED,
                applied,
                false,
                settles,
            )?;
        }
        Ok(())
    }

    pub(super) fn drop_steers(&mut self, run_id: &str) -> Result<(), Emit> {
        for steer in self.run(run_id).steers.clone() {
            let dropped = RunSteerDroppedPayload {
                session_id: self.session_id(),
                run_id: RunId::from(run_id),
                submission_id: steer.submission_id.clone(),
                request_id: steer.request_id,
                reason: ProtocolError {
                    code: "run_terminated".into(),
                    message: "the run terminated before the guidance was applied".into(),
                    ..ProtocolError::default()
                },
            };
            let settles = Some(&steer.submission_id);
            self.publish(
                run_id,
                EnvelopeType::RUN_STEER_DROPPED,
                dropped,
                false,
                settles,
            )?;
        }
        Ok(())
    }
}
