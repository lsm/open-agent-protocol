use std::sync::{Mutex, MutexGuard};

use oap_adapter::journal::Journal;
use oap_adapter::{Error, EventStream, InteractionResolution, Recovery, ResumeRequest};
use oap_protocol::{RunCancelResponse, RunId, SessionState, SessionStatus};

use crate::process::Process;

pub struct Session {
    inner: Mutex<Inner>,
    journal: Journal,
    native_id: String,
}

struct Inner {
    process: Option<Process>,
    state: SessionState,
}

impl Session {
    pub(crate) fn new(process: Process, state: SessionState, journal_capacity: usize) -> Self {
        Self {
            native_id: state.session_id.as_str().into(),
            inner: Mutex::new(Inner {
                process: Some(process),
                state,
            }),
            journal: Journal::new(journal_capacity),
        }
    }

    fn lock(&self) -> MutexGuard<'_, Inner> {
        self.inner
            .lock()
            .unwrap_or_else(|poison| poison.into_inner())
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.lock();
        if inner.process.is_none() {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn resolve(&self, _: InteractionResolution) -> Result<(), Error> {
        Err(Error::InteractionNotFound)
    }

    pub fn cancel(&self, _: &RunId) -> Result<RunCancelResponse, Error> {
        Err(Error::Harness(
            "deepseek adapter: operation unavailable".into(),
        ))
    }

    pub fn resume(&self, request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        let inner = self.lock();
        if inner.process.is_none() {
            return Err(Error::SessionClosed);
        }
        let run = request.run_id.as_str();
        let latest = self.journal.ended(run).ok_or(Error::RunNotFound)?;
        if request.after_sequence > latest {
            return Err(Error::ReplayCursorFuture);
        }
        Ok(self
            .journal
            .resume(inner.state.clone(), run, request.after_sequence, latest))
    }

    pub fn close(&self) -> Result<(), Error> {
        let process = {
            let mut inner = self.lock();
            let Some(process) = inner.process.take() else {
                return Ok(());
            };
            inner.state.status = SessionStatus::CLOSED;
            process
        };
        let closed = process.close();
        self.journal.close();
        closed.map_err(Error::Harness)
    }

    pub fn native_session_id(&self) -> String {
        self.native_id.clone()
    }
}
