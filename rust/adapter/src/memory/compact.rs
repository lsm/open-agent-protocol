use oap_protocol::*;

use super::admit::history_tokens;
use super::pipe::Pipe;
use super::session::{surfaced, Emit, Inner, Run, Stage};
use super::ENDPOINT_ID;
use crate::{CompactRequest, Error, EventStream, UnsupportedControl, CONTROL_UNADVERTISED};

impl Inner {
    pub(super) fn compact(
        &mut self,
        compact: CompactRequest,
    ) -> Result<(SessionCompactResponse, EventStream), Error> {
        let request = compact.request;
        let queue = request.delivery == RequestedDeliveryMode::QUEUE;
        if !(request.delivery.is_empty()
            || request.delivery == RequestedDeliveryMode::AUTO
            || queue)
        {
            return Err(Error::UnsupportedControl(Box::new(UnsupportedControl {
                feature: format!("session.message.delivery.{}", request.delivery.as_str()),
                reason: CONTROL_UNADVERTISED,
                detail: "a compaction takes auto or queue delivery".into(),
                ..UnsupportedControl::default()
            })));
        }
        if request.session_id.as_str().is_empty() {
            return Err(Error::InvalidSubmission);
        }
        if self.closed {
            return Err(Error::SessionClosed);
        }
        if request.session_id != self.state.session_id {
            return Err(Error::RunNotFound);
        }
        let busy = self.live(&self.active);
        if busy && self.live(&self.reserved) {
            return Err(Error::RunActive);
        }
        let id = self.ids.new_id("run");
        let reservation = busy || queue;
        let mut run = Run {
            id: id.clone(),
            status: RunStatus::RUNNING,
            queued_admission: reservation,
            stage: Stage::Input,
            next_sequence: 1,
            input_id: self.ids.new_id("input"),
            requested_by: ENDPOINT_ID.to_owned(),
            responded_by: self.participant.as_str().to_owned(),
            compaction: true,
            compact_continue: request.continue_after,
            compact_focus: request.focus.unwrap_or_default(),
            ..Run::default()
        };
        if reservation {
            run.status = RunStatus::QUEUED;
        } else {
            run.controls.model = self.state.current_model_id.clone();
        }
        let pipe = Pipe::default();
        run.subscribers.push(pipe.clone());
        self.runs.insert(id.clone(), run);
        if busy {
            self.reserved = Some(id.clone());
        } else {
            self.active = Some(id.clone());
        }
        self.refresh_state();
        self.state.updated_at_ms = self.clock.now_ms();
        let mut admission = SessionCompactResponse {
            session_id: self.session_id(),
            accepted: true,
            submission_id: SubmissionId::from(self.ids.new_id("submission")),
            requested_delivery: if request.delivery.is_empty() {
                RequestedDeliveryMode::AUTO
            } else {
                request.delivery
            },
            effective_delivery: EffectiveDeliveryMode::START,
            delivery_resolution: "session_idle".into(),
            admission: Admission::STARTED,
            run_id: Some(RunId::from(id.as_str())),
            status: RunStatus::RUNNING,
        };
        if reservation {
            admission.effective_delivery = EffectiveDeliveryMode::QUEUE;
            admission.admission = Admission::QUEUED;
            admission.status = RunStatus::QUEUED;
            if busy {
                admission.delivery_resolution = "session_busy".into();
            }
        }
        let started = if busy {
            Ok(())
        } else {
            surfaced(self.emit_initial(&id))
        };
        self.run_mut(&id).answered = true;
        self.refresh_state();
        started.map(|()| (admission, pipe.stream()))
    }

    pub(super) fn emit_compaction(
        &mut self,
        run_id: &str,
        reason: CompactionReason,
    ) -> Result<bool, Emit> {
        let compaction = CompactionId::from(self.ids.new_id("compaction"));
        let session = self.session_id();
        let requested = reason == CompactionReason::REQUESTED;
        let started = RunCompactionStartedPayload {
            session_id: session.clone(),
            run_id: RunId::from(run_id),
            compaction_id: compaction.clone(),
            reason,
            history_tokens: Some(self.history),
        };
        self.emit(run_id, EnvelopeType::RUN_COMPACTION_STARTED, started, false)?;
        let focus = &self.run(run_id).compact_focus;
        let text = if focus.is_empty() {
            "The session so far, compacted.".to_owned()
        } else {
            format!("The session so far, compacted with attention to: {focus}")
        };
        let summary = Message {
            id: Some(MessageId::from(self.ids.new_id("message"))),
            role: MessageRole::ASSISTANT,
            content: MessageContent::from_text(&text),
            ..Message::default()
        };
        let after = history_tokens(std::slice::from_ref(&summary));
        let ended = RunCompactionEndedPayload {
            session_id: session.clone(),
            run_id: RunId::from(run_id),
            compaction_id: compaction,
            outcome: CompactionOutcome::COMPLETED,
            summary: Some(summary.clone()),
            history_tokens: Some(after),
            error: None,
        };
        self.emit(run_id, EnvelopeType::RUN_COMPACTION_ENDED, ended, false)?;
        self.history = after;
        let run = self.run(run_id);
        if !requested || run.compact_continue {
            return Ok(false);
        }
        let completed = RunCompletedPayload {
            session_id: session,
            run_id: RunId::from(run_id),
            final_response: summary,
            stop_reason: "compacted".into(),
            model_id: run.controls.model.clone(),
            ..RunCompletedPayload::default()
        };
        match self.emit(run_id, EnvelopeType::RUN_COMPLETED, completed, true) {
            Ok(()) | Err(Emit::TerminalWon) => Ok(true),
            Err(err) => Err(err),
        }
    }
}
