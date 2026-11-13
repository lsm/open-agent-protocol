use std::sync::{Arc, Mutex, MutexGuard};

use oap_adapter::{Error, EventStream};
use oap_protocol::{MessageSubmitRequest, MessageSubmitResponse, SessionState, SessionStatus};

use crate::client::Client;

struct Inner {
    state: SessionState,
    closed: bool,
}

pub struct Session {
    client: Arc<dyn Client>,
    thread_id: String,
    inner: Mutex<Inner>,
}

impl Session {
    pub(crate) fn new(client: Arc<dyn Client>, thread_id: String, state: SessionState) -> Self {
        Self {
            client,
            thread_id,
            inner: Mutex::new(Inner {
                state,
                closed: false,
            }),
        }
    }

    fn lock(&self) -> MutexGuard<'_, Inner> {
        self.inner
            .lock()
            .unwrap_or_else(|poison| poison.into_inner())
    }

    pub fn native_session_id(&self) -> String {
        self.thread_id.clone()
    }

    pub fn submit(
        &self,
        _request: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        Err(Error::Harness(
            "codex app-server adapter: submit unavailable".into(),
        ))
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.lock();
        if inner.closed {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn close(&self) -> Result<(), Error> {
        {
            let mut inner = self.lock();
            if inner.closed {
                return Ok(());
            }
            inner.closed = true;
            inner.state.status = SessionStatus::CLOSED;
            inner.state.active_run_id = None;
        }
        self.client.close().map_err(Error::harness)
    }
}
