use std::sync::{Arc, Mutex, MutexGuard};

use crossbeam_channel::{Receiver, Sender};
use oap_adapter::journal::Journal;
use oap_adapter::Error;
use oap_protocol::{RecoveryMetadata, SessionState, SessionStatus};
use serde_json::value::RawValue;

use crate::client::Client;
use crate::native::SessionInfoPayload;

pub(crate) const REOPEN_RECOVERY_REASON: &str = "Hermes restored the bound conversation and reports the model it last ran under; OAP runs and cursors remain process-local";

pub(crate) struct Inner {
    pub(crate) state: SessionState,
    pub(crate) closed: bool,
    pub(crate) unusable: bool,
    pub(crate) last_seq: i64,
}

pub(crate) struct Shared {
    pub(crate) client: Arc<dyn Client>,
    pub(crate) native_id: String,
    stored_id: String,
    journal: Journal,
    inner: Mutex<Inner>,
    stop: Mutex<Option<Sender<()>>>,
    pub(crate) stopped: Receiver<()>,
}

pub struct Session {
    shared: Arc<Shared>,
}

pub(crate) fn hold<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

impl Shared {
    pub(crate) fn lock(&self) -> MutexGuard<'_, Inner> {
        hold(&self.inner)
    }

    fn close(&self) -> Result<(), Error> {
        {
            let mut inner = self.lock();
            if inner.closed {
                return Ok(());
            }
            inner.closed = true;
            inner.state.status = SessionStatus::CLOSED;
        }
        let closed = self.client.close().map_err(Error::Harness);
        hold(&self.stop).take();
        self.journal.close();
        closed
    }
}

impl Session {
    pub(crate) fn new(
        client: Arc<dyn Client>,
        native_id: String,
        capacity: usize,
        state: SessionState,
    ) -> Self {
        let (stop, stopped) = crossbeam_channel::bounded(0);
        let shared = Arc::new(Shared {
            stored_id: client.stored_session_id(),
            client,
            native_id,
            journal: Journal::new(capacity),
            inner: Mutex::new(Inner {
                state,
                closed: false,
                unusable: false,
                last_seq: 0,
            }),
            stop: Mutex::new(Some(stop)),
            stopped,
        });
        let dispatching = shared.clone();
        std::thread::spawn(move || dispatching.dispatch());
        Self { shared }
    }

    pub(crate) fn restore_state(&self, info: Option<&RawValue>) {
        let mut inner = self.shared.lock();
        inner.state.recovery = Some(RecoveryMetadata {
            recovered: true,
            reason: REOPEN_RECOVERY_REASON.into(),
            ..RecoveryMetadata::default()
        });
        let info = info.and_then(|raw| serde_json::from_str::<SessionInfoPayload>(raw.get()).ok());
        if let Some(info) = info.filter(|info| !info.model.is_empty()) {
            inner.state.current_model_id = info.model;
        }
    }

    pub fn native_session_id(&self) -> String {
        self.shared.stored_id.clone()
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.shared.lock();
        if inner.closed || inner.unusable {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn close(&self) -> Result<(), Error> {
        self.shared.close()
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        hold(&self.shared.stop).take();
    }
}
