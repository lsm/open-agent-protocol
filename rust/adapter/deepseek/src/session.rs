use std::sync::{Arc, Mutex, MutexGuard};

use crossbeam_channel::{select, Receiver};
use oap_adapter::journal::Journal;
use oap_adapter::{Error, EventStream, InteractionResolution, Recovery, ResumeRequest};
use oap_protocol::{
    MessageSubmitRequest, MessageSubmitResponse, RunCancelResponse, RunId, SessionState,
    SessionStatus,
};

use crate::native::{decode_notification, Notification, NOTIFY_SESSION_STATUS};
use crate::process::Process;
use crate::rpc::{Inbound, NotificationMessage};

pub struct Session {
    shared: Arc<Shared>,
}

struct Shared {
    inner: Mutex<Inner>,
    journal: Journal,
    native_id: String,
}

struct Inner {
    process: Option<Process>,
    state: SessionState,
    unusable: bool,
}

impl Session {
    pub(crate) fn new(process: Process, state: SessionState, journal_capacity: usize) -> Self {
        let inbound = process.client.inbound();
        let done = process.client.done();
        let shared = Arc::new(Shared {
            native_id: state.session_id.as_str().into(),
            inner: Mutex::new(Inner {
                process: Some(process),
                state,
                unusable: false,
            }),
            journal: Journal::new(journal_capacity),
        });
        let reader = shared.clone();
        std::thread::spawn(move || reader.dispatch(&inbound, &done));
        Self { shared }
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.shared.lock();
        if inner.process.is_none() || inner.unusable {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn submit(
        &self,
        _: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        Err(Error::UnsupportedInput)
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
        let inner = self.shared.lock();
        if inner.process.is_none() {
            return Err(Error::SessionClosed);
        }
        let journal = &self.shared.journal;
        let run = request.run_id.as_str();
        let latest = journal.ended(run).ok_or(Error::RunNotFound)?;
        if request.after_sequence > latest {
            return Err(Error::ReplayCursorFuture);
        }
        let (recovery, replay) =
            journal.resume(inner.state.clone(), run, request.after_sequence, latest);
        if let Some(gap) = recovery.replay_gap {
            return Err(Error::ReplayGap(gap));
        }
        Ok((recovery, replay))
    }

    pub fn close(&self) -> Result<(), Error> {
        let process = {
            let mut inner = self.shared.lock();
            let Some(process) = inner.process.take() else {
                return Ok(());
            };
            inner.state.status = SessionStatus::CLOSED;
            process
        };
        let closed = process.close();
        self.shared.journal.close();
        closed.map_err(Error::Harness)
    }

    pub fn native_session_id(&self) -> String {
        self.shared.native_id.clone()
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        let _ = self.close();
    }
}

impl Shared {
    fn lock(&self) -> MutexGuard<'_, Inner> {
        self.inner
            .lock()
            .unwrap_or_else(|poison| poison.into_inner())
    }

    fn dispatch(&self, inbound: &Receiver<Inbound>, done: &Receiver<()>) {
        loop {
            select! {
                recv(inbound) -> message => match message {
                    Ok(message) => self.reduce(message),
                    Err(_) => break,
                },
                recv(done) -> _ => break,
            }
        }
        while let Ok(message) = inbound.try_recv() {
            self.reduce(message);
        }
        let mut inner = self.lock();
        if inner.process.is_some() {
            inner.unusable = true;
        }
    }

    fn reduce(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(_) => self.lock().unusable = true,
            Inbound::Notification(notification) => self.apply_notification(&notification),
        }
    }

    fn apply_notification(&self, notification: &NotificationMessage) {
        let mut inner = self.lock();
        if !inner.unusable && notification.method == NOTIFY_SESSION_STATUS {
            let params = notification.params.as_ref().map_or("", |raw| raw.get());
            if let Ok(Notification::SessionStatus(status)) =
                decode_notification(&notification.method, params.as_bytes())
            {
                if status.session_id == self.native_id {
                    return;
                }
            }
        }
        inner.unusable = true;
    }
}

#[cfg(test)]
#[path = "../tests/support/session_journal.rs"]
mod tests;
