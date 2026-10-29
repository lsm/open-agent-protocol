use std::collections::{BTreeMap, BTreeSet};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError, Weak};

use oap_adapter::{Error, OpenRequest};
use oap_protocol::{SessionId, SessionState, SessionStatus};

use crate::Registry;

pub const DEFAULT_PARTICIPANT: &str = "user";

#[derive(Debug, thiserror::Error)]
pub enum HubError {
    #[error("no adapter {0:?}")]
    UnknownAdapter(String),
    #[error("unknown session {0:?}")]
    UnknownSession(SessionId),
    #[error("session {0:?} already exists")]
    SessionExists(SessionId),
    #[error("serve: adapter reported state scoped to session {got:?}, want {want:?}")]
    StateScope { got: SessionId, want: SessionId },
    #[error("the session is closed")]
    ClosedAtOpen(Box<SessionState>),
    #[error(transparent)]
    Adapter(#[from] Error),
}

#[derive(Default)]
struct Sessions {
    live: BTreeMap<SessionId, Arc<HubSession>>,
    opening: BTreeSet<SessionId>,
}

struct Shared {
    registry: Registry,
    sessions: Mutex<Sessions>,
}

impl Shared {
    fn sessions(&self) -> MutexGuard<'_, Sessions> {
        self.sessions.lock().unwrap_or_else(PoisonError::into_inner)
    }

    fn release(&self, entry: &HubSession) {
        let mut sessions = self.sessions();
        if sessions
            .live
            .get(&entry.id)
            .is_some_and(|held| std::ptr::eq(Arc::as_ptr(held), entry))
        {
            sessions.live.remove(&entry.id);
        }
    }
}

pub struct Hub {
    shared: Arc<Shared>,
}

pub struct Opened {
    pub session: Arc<HubSession>,
    pub state: SessionState,
}

struct Reservation<'a> {
    shared: &'a Shared,
    id: Option<SessionId>,
}

impl Drop for Reservation<'_> {
    fn drop(&mut self) {
        if let Some(id) = self.id.take() {
            self.shared.sessions().opening.remove(&id);
        }
    }
}

impl Hub {
    pub fn new(registry: Registry) -> Self {
        Self {
            shared: Arc::new(Shared {
                registry,
                sessions: Mutex::default(),
            }),
        }
    }

    pub fn registry(&self) -> &Registry {
        &self.shared.registry
    }

    pub async fn open(&self, adapter: &str, mut request: OpenRequest) -> Result<Opened, HubError> {
        let implementation = self
            .shared
            .registry
            .lookup(adapter)
            .ok_or_else(|| HubError::UnknownAdapter(adapter.into()))?;
        if request.participant.id.as_str().is_empty() {
            request.participant.id = DEFAULT_PARTICIPANT.into();
        }
        if let Some(admitter) = implementation.admitter() {
            admitter.admit(&request).await?;
        }
        let _reservation = self.reserve(&request.session_id)?;
        let requested = request.session_id.clone();
        let session = implementation.open(request).await?;
        let state = match session.state().await {
            Ok(state) => state,
            Err(Error::SessionClosed) => {
                return Err(HubError::ClosedAtOpen(Box::new(SessionState {
                    session_id: requested,
                    status: SessionStatus::CLOSED,
                    ..SessionState::default()
                })))
            }
            Err(err) => {
                let _ = session.close().await;
                return Err(err.into());
            }
        };
        if state.status == SessionStatus::CLOSED {
            return Err(HubError::ClosedAtOpen(Box::new(state)));
        }
        let entry = Arc::new(HubSession {
            id: state.session_id.clone(),
            adapter: adapter.into(),
            session,
            closed: AtomicBool::new(false),
            hub: Arc::downgrade(&self.shared),
        });
        let added = {
            let mut sessions = self.shared.sessions();
            if sessions.live.contains_key(&entry.id) {
                false
            } else {
                sessions.live.insert(entry.id.clone(), entry.clone());
                true
            }
        };
        if !added {
            let _ = entry.session.close().await;
            return Err(HubError::SessionExists(entry.id.clone()));
        }
        Ok(Opened {
            session: entry,
            state,
        })
    }

    fn reserve(&self, id: &SessionId) -> Result<Reservation<'_>, HubError> {
        if id.as_str().is_empty() {
            return Ok(Reservation {
                shared: &self.shared,
                id: None,
            });
        }
        let mut sessions = self.shared.sessions();
        if sessions.live.contains_key(id) || !sessions.opening.insert(id.clone()) {
            return Err(HubError::SessionExists(id.clone()));
        }
        Ok(Reservation {
            shared: &self.shared,
            id: Some(id.clone()),
        })
    }

    pub fn session(&self, id: &SessionId) -> Result<Arc<HubSession>, HubError> {
        self.shared
            .sessions()
            .live
            .get(id)
            .cloned()
            .ok_or_else(|| HubError::UnknownSession(id.clone()))
    }

    pub fn sessions(&self) -> Vec<Arc<HubSession>> {
        self.shared.sessions().live.values().cloned().collect()
    }
}

pub struct HubSession {
    id: SessionId,
    adapter: String,
    session: Box<dyn oap_adapter::Session>,
    closed: AtomicBool,
    hub: Weak<Shared>,
}

impl HubSession {
    pub fn id(&self) -> &SessionId {
        &self.id
    }

    pub fn adapter(&self) -> &str {
        &self.adapter
    }

    pub fn is_closed(&self) -> bool {
        self.closed.load(Ordering::SeqCst)
    }

    fn mark_closed(&self) {
        if !self.closed.swap(true, Ordering::SeqCst) {
            if let Some(hub) = self.hub.upgrade() {
                hub.release(self);
            }
        }
    }

    pub async fn state(&self) -> Result<SessionState, HubError> {
        let state = self.session.state().await.inspect_err(|err| {
            if matches!(err, Error::SessionClosed) {
                self.mark_closed();
            }
        })?;
        if state.session_id != self.id {
            return Err(HubError::StateScope {
                got: state.session_id,
                want: self.id.clone(),
            });
        }
        Ok(state)
    }

    pub async fn close(&self) -> Result<(), HubError> {
        self.session.close().await?;
        self.mark_closed();
        Ok(())
    }
}
