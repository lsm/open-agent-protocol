use std::sync::{Arc, Mutex, MutexGuard};

use oap_adapter::journal::Journal;
use oap_adapter::Error;
use oap_protocol::{RecoveryMetadata, SessionState, SessionStatus};
use serde_json::value::RawValue;

use crate::client::Client;
use crate::native::SessionInfoPayload;

pub(crate) const REOPEN_RECOVERY_REASON: &str = "Hermes restored the bound conversation and reports the model it last ran under; OAP runs and cursors remain process-local";

struct Inner {
    state: SessionState,
    closed: bool,
    unusable: bool,
}

pub struct Session {
    client: Arc<dyn Client>,
    stored_id: String,
    journal: Journal,
    inner: Mutex<Inner>,
}

fn hold<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

impl Session {
    pub(crate) fn new(client: Arc<dyn Client>, capacity: usize, state: SessionState) -> Self {
        Self {
            stored_id: client.stored_session_id(),
            client,
            journal: Journal::new(capacity),
            inner: Mutex::new(Inner {
                state,
                closed: false,
                unusable: false,
            }),
        }
    }

    pub(crate) fn restore_state(&self, info: Option<&RawValue>) {
        let mut inner = hold(&self.inner);
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
        self.stored_id.clone()
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = hold(&self.inner);
        if inner.closed || inner.unusable {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn close(&self) -> Result<(), Error> {
        {
            let mut inner = hold(&self.inner);
            if inner.closed {
                return Ok(());
            }
            inner.closed = true;
            inner.state.status = SessionStatus::CLOSED;
        }
        let closed = self.client.close().map_err(Error::Harness);
        self.journal.close();
        closed
    }
}
