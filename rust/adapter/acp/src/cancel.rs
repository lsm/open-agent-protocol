use oap_adapter::Error;
use oap_protocol::{
    EnvelopeType, RunCancelResponse, RunCancelledPayload, RunStatus, RunStatusUpdatedPayload,
};

use crate::text::Terminal;
use crate::TextRun;

impl TextRun {
    pub(crate) fn cancel_state(&self) -> Result<RunCancelResponse, Error> {
        let state = self.state();
        if state.status.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        if state.closed && state.status != RunStatus::CANCELLED {
            if state.status != RunStatus::COMPLETED && state.status != RunStatus::FAILED {
                return Err(Error::SessionClosed);
            }
            return Err(Error::RunAlreadyTerminal {
                run_id: Some(self.context.run_id.clone()),
                status: Some(state.status),
            });
        }
        Ok(RunCancelResponse {
            session_id: self.context.session_id.clone(),
            run_id: self.context.run_id.clone(),
            accepted: true,
            status: state.status,
        })
    }

    pub(crate) fn request_cancel(&mut self, now: i64) -> Result<(), Error> {
        if self.state().closed || self.pending.is_some() {
            return Ok(());
        }
        let now = self.timestamp(now);
        self.emit(
            EnvelopeType::RUN_STATUS_UPDATED,
            &RunStatusUpdatedPayload {
                session_id: self.context.session_id.clone(),
                run_id: self.context.run_id.clone(),
                status: RunStatus::CANCELLING,
                updated_at_ms: now,
                ..RunStatusUpdatedPayload::default()
            },
            RunStatus::CANCELLING,
            None,
            now,
        )
        .map(|_| ())
    }

    pub(crate) fn cancelled(&mut self, reason: &str, now: i64) -> Result<(), Error> {
        if self.state().closed || self.pending.is_some() {
            return Ok(());
        }
        if self.has_permissions() {
            self.pending = Some(Terminal::Cancelled(reason.into()));
            return Ok(());
        }
        let events = self.tools.settle(true);
        self.emit_tools(events, now)?;
        self.emit(
            EnvelopeType::RUN_CANCELLED,
            &RunCancelledPayload {
                session_id: self.context.session_id.clone(),
                run_id: self.context.run_id.clone(),
                reason: reason.into(),
                usage: self.terminal_usage.clone(),
                ..RunCancelledPayload::default()
            },
            RunStatus::CANCELLED,
            None,
            now,
        )
        .map(|_| ())
    }
}
