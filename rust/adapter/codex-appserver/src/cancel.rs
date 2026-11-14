use oxap_adapter::Error;
use oxap_protocol::{RunCancelResponse, RunId, RunStatus};

use crate::adapter::call;
use crate::native::{TurnInterruptParams, TurnInterruptResponse, METHOD_TURN_INTERRUPT};
use crate::session::Shared;

impl Shared {
    pub(crate) fn cancel(&self, run_id: &RunId) -> Result<RunCancelResponse, Error> {
        loop {
            let operating = self.operate();
            let mut inner = self.lock();
            let accepted = RunCancelResponse {
                session_id: inner.state.session_id.clone(),
                run_id: run_id.clone(),
                accepted: true,
                status: RunStatus::CANCELLING,
            };
            let run = inner.runs.get_mut(run_id).ok_or(Error::RunNotFound)?;
            if let Some(status) = run.settled.clone() {
                if status == RunStatus::CANCELLED {
                    return Ok(RunCancelResponse { status, ..accepted });
                }
                return Err(Error::RunAlreadyTerminal {
                    run_id: Some(run_id.clone()),
                    status: Some(status),
                });
            }
            if run.cancel_pending {
                return Ok(accepted);
            }
            if let Some(flight) = run.flight.clone() {
                drop(inner);
                drop(operating);
                let _ = flight.recv();
                continue;
            }
            let (landed, flight) = crossbeam_channel::bounded::<()>(0);
            run.flight = Some(flight);
            let params = TurnInterruptParams {
                thread_id: self.thread_id.clone(),
                turn_id: run.turn.clone(),
            };
            drop(inner);
            drop(operating);
            let interrupted =
                call::<TurnInterruptResponse>(&*self.client, METHOD_TURN_INTERRUPT, &params);
            let _operating = self.operate();
            let mut inner = self.lock();
            drop(landed);
            let run = inner.runs.get_mut(run_id).ok_or(Error::RunNotFound)?;
            run.flight = None;
            if interrupted.is_ok() && !run.terminal {
                run.cancel_pending = true;
                self.status(&mut inner, run_id, RunStatus::CANCELLING, None);
            }
            interrupted
                .map_err(|failure| Error::Harness(format!("interrupt Codex turn: {failure}")))?;
            return Ok(accepted);
        }
    }
}
