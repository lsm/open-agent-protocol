use std::collections::{BTreeMap, BTreeSet};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError, Weak};

use oap_adapter::{
    CompactRequest, Error, InteractionResolution, OpenRequest, ResumeRequest, SubmitRequest,
    UnsupportedControl, CONTROL_UNSATISFIABLE,
};
use oap_binding::{Action, Entry, Record, Store};
use oap_protocol::{
    Admission, MessageSubmitResponse, RunCancelResponse, RunId, SessionCompactResponse, SessionId,
    SessionState, SessionStatus, FEATURE_OPEN_REOPEN, FEATURE_SESSION_COMPACT,
};

use crate::live::{self, Live, Subscription, DEFAULT_STREAM_QUEUE};
use crate::Registry;

pub const DEFAULT_PARTICIPANT: &str = "user";

#[derive(Debug, thiserror::Error)]
pub enum HubError {
    #[error("serve: this hub keeps no session history")]
    NoSessionHistory,
    #[error("no adapter {0:?}")]
    UnknownAdapter(String),
    #[error("no session \"{0}\"")]
    UnknownSession(SessionId),
    #[error("session {:?} already exists", .0.as_str())]
    SessionExists(SessionId),
    #[error("serve: adapter reported state scoped to session {:?}, want {:?}", .got.as_str(), .want.as_str())]
    StateScope { got: SessionId, want: SessionId },
    #[error("the session is closed")]
    ClosedAtOpen(Box<SessionState>),
    #[error(
        "payload session_id {:?} does not match the addressed session {:?}",
        .payload.as_str(),
        .addressed.as_str()
    )]
    ScopeMismatch {
        payload: SessionId,
        addressed: SessionId,
    },
    #[error("the session has no run to replay")]
    NoRunToResume,
    #[error("the open cites a capability revision that is no longer current")]
    StaleRevision { expected: String, current: String },
    #[error("the session is closed")]
    SessionClosed(SessionId),
    #[error("serve: event stream overflowed behind sequence {last_sequence} (run {run_id}); resume with a cursor after it")]
    Overflow { run_id: RunId, last_sequence: u64 },
    #[error(transparent)]
    Adapter(#[from] Error),
    #[error(transparent)]
    Binding(#[from] oap_binding::Error),
}

#[derive(Default)]
struct Sessions {
    live: BTreeMap<SessionId, Arc<HubSession>>,
    opening: BTreeSet<SessionId>,
}

#[derive(Clone, Default)]
pub struct HubOptions {
    pub bindings: Option<Arc<dyn Store>>,
    pub home: String,
    pub stream_queue: usize,
    pub report: Option<Arc<dyn Fn(String) + Send + Sync>>,
}

impl HubOptions {
    fn record(&self, record: &Record, action: Action, time_ms: i64) {
        let Some(bindings) = &self.bindings else {
            return;
        };
        if record.session_id.is_empty() {
            return;
        }
        let appended = bindings.append(Entry {
            action,
            time_ms,
            record: record.clone(),
        });
        if let (Err(err), Some(report)) = (appended, &self.report) {
            report(format!(
                "binding: {} for {}: {err}",
                action.as_str(),
                record.session_id
            ));
        }
    }
}

struct Shared {
    registry: Registry,
    sessions: Mutex<Sessions>,
    options: HubOptions,
}

pub(crate) fn now_ms() -> i64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |elapsed| elapsed.as_millis() as i64)
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
        self.options.record(&entry.record, Action::Closed, now_ms());
    }
}

#[derive(Clone)]
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
        Self::with_options(registry, HubOptions::default())
    }

    pub fn with_options(registry: Registry, options: HubOptions) -> Self {
        Self {
            shared: Arc::new(Shared {
                registry,
                sessions: Mutex::default(),
                options,
            }),
        }
    }

    pub fn bindings(&self) -> Option<&Arc<dyn Store>> {
        self.shared.options.bindings.as_ref()
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
        let _reservation = self.reserve(adapter, &request.session_id)?;
        let requested = request.session_id.clone();
        let adopting = request.reopen && request.adopted && !request.native_session_id.is_empty();
        let mut directory = self.shared.registry.working_directory(adapter).to_owned();
        if let (true, Some(bindings), false) =
            (request.reopen, &self.shared.options.bindings, adopting)
        {
            let history = bindings.history(request.session_id.as_str())?;
            let bound = oap_binding::state(&history)
                .filter(|bound| bound.record.adapter == adapter)
                .ok_or_else(|| HubError::UnknownSession(request.session_id.clone()))?;
            request.native_session_id = bound.record.native_session_id.clone();
            request.adopted = bound.record.adopted;
            directory = bound.record.directory.clone();
        }
        let asked = request.clone();
        let reloads = asked.reopen && (self.shared.options.bindings.is_some() || adopting);
        let session = match implementation.open(request).await {
            Err(Error::UnknownSession(_)) if reloads => {
                return Err(Error::UnsupportedControl(Box::new(UnsupportedControl {
                    feature: FEATURE_OPEN_REOPEN.into(),
                    reason: CONTROL_UNSATISFIABLE,
                    detail: "the binding names a session the adapter can no longer load".into(),
                    ..UnsupportedControl::default()
                }))
                .into())
            }
            opened => opened?,
        };
        let (state, settled) = match session.state().await {
            Ok(state) => {
                let settled = state.status == SessionStatus::CLOSED;
                (state, settled)
            }
            Err(Error::SessionClosed) => (
                SessionState {
                    session_id: requested,
                    status: SessionStatus::CLOSED,
                    ..SessionState::default()
                },
                true,
            ),
            Err(err) => {
                let _ = session.close().await;
                return Err(err.into());
            }
        };
        let record = {
            let version = match self.shared.options.bindings {
                Some(_) => match implementation.probe().await {
                    Ok(descriptor) => descriptor.capabilities.endpoint.version,
                    Err(_) => String::new(),
                },
                None => String::new(),
            };
            let sources: Vec<String> = asked
                .tool_sources
                .iter()
                .map(|source| source.id.clone())
                .collect();
            let mut record = Record::from_open(
                state.session_id.as_str(),
                adapter,
                &version,
                &state.current_model_id,
                &self.shared.options.home,
                &directory,
                &sources,
            );
            record.reasoning_level = asked.reasoning_level.as_str().into();
            record.adopted = asked.adopted;
            record.compaction_policy = asked.compaction_policy.clone();
            record.native_session_id = session.native_session_id().unwrap_or_default();
            record
        };
        let began = if asked.reopen {
            Action::Reopened
        } else {
            Action::Opened
        };
        if settled {
            let _ = session.close().await;
            let _sessions = self.shared.sessions();
            self.shared
                .options
                .record(&record, began, state.updated_at_ms);
            self.shared
                .options
                .record(&record, Action::Closed, now_ms());
            return Err(HubError::ClosedAtOpen(Box::new(state)));
        }
        let entry = Arc::new(HubSession {
            id: state.session_id.clone(),
            adapter: adapter.into(),
            session,
            closed: AtomicBool::new(false),
            hub: Arc::downgrade(&self.shared),
            options: self.shared.options.clone(),
            record,
            live: Mutex::default(),
            created_ms: now_ms(),
        });
        let added = {
            let mut sessions = self.shared.sessions();
            if sessions.live.contains_key(&entry.id) {
                self.shared
                    .options
                    .record(&entry.record, Action::Refused, now_ms());
                false
            } else {
                sessions.live.insert(entry.id.clone(), entry.clone());
                self.shared
                    .options
                    .record(&entry.record, began, state.updated_at_ms);
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

    fn reserve(&self, adapter: &str, id: &SessionId) -> Result<Reservation<'_>, HubError> {
        if id.as_str().is_empty() {
            return Ok(Reservation {
                shared: &self.shared,
                id: None,
            });
        }
        let mut sessions = self.shared.sessions();
        if sessions.live.contains_key(id) || !sessions.opening.insert(id.clone()) {
            let refused = Record::from_open(
                id.as_str(),
                adapter,
                "",
                "",
                &self.shared.options.home,
                "",
                &[],
            );
            self.shared
                .options
                .record(&refused, Action::Refused, now_ms());
            return Err(HubError::SessionExists(id.clone()));
        }
        Ok(Reservation {
            shared: &self.shared,
            id: Some(id.clone()),
        })
    }

    pub fn session_history(
        &self,
        cursor: &str,
        limit: i64,
    ) -> Result<oap_protocol::SessionListResponse, HubError> {
        let bindings = self
            .shared
            .options
            .bindings
            .as_ref()
            .ok_or(HubError::NoSessionHistory)?;
        let page = oap_binding::list(&bindings.sessions()?, cursor, limit)?;
        let sessions = page
            .entries
            .into_iter()
            .map(|entry| oap_protocol::SessionListEntry {
                state: if entry.live() {
                    oap_protocol::SessionListState::LIVE
                } else {
                    oap_protocol::SessionListState::CLOSED
                },
                session_id: entry.record.session_id.as_str().into(),
                adapter: entry.record.adapter,
                harness_version: entry.record.harness_version,
                updated_at_ms: entry.time_ms,
                model: entry.record.model,
                directory: entry.record.directory,
            })
            .collect();
        Ok(oap_protocol::SessionListResponse {
            sessions,
            next_cursor: page.next_cursor,
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

    pub async fn subscribe_after(
        &self,
        id: &SessionId,
        run: Option<RunId>,
        after: u64,
    ) -> Result<Subscription, HubError> {
        let entry = self.session(id)?;
        if entry.is_closed() {
            return Err(HubError::SessionClosed(id.clone()));
        }
        let run = match run.filter(|run| !run.as_str().is_empty()) {
            Some(run) => run,
            None => {
                let active = entry
                    .session
                    .state()
                    .await
                    .ok()
                    .and_then(|state| state.active_run_id);
                active
                    .or_else(|| entry.current_run())
                    .ok_or(HubError::NoRunToResume)?
            }
        };
        let (_, stream) = entry
            .session
            .resume(ResumeRequest {
                run_id: run.clone(),
                after_sequence: after,
            })
            .await
            .inspect_err(|err| entry.closed_by(err))?;
        Ok(Subscription::replay(run, after, stream))
    }

    pub async fn probe(&self, name: &str) -> Result<oap_adapter::Descriptor, HubError> {
        let adapter = self
            .shared
            .registry
            .lookup(name)
            .ok_or_else(|| HubError::UnknownAdapter(name.into()))?;
        let descriptor = adapter.probe().await?;
        if descriptor.capability_revision.is_empty() {
            return Err(Error::harness("adapter descriptor carries no capability revision").into());
        }
        Ok(descriptor)
    }

    pub async fn adapters(&self) -> Vec<(String, Result<oap_adapter::Descriptor, HubError>)> {
        let mut statuses = Vec::new();
        for name in self.shared.registry.names() {
            let probed = self.probe(&name).await;
            statuses.push((name, probed));
        }
        statuses
    }

    pub fn subscribe(&self, id: &SessionId) -> Result<Subscription, HubError> {
        let entry = self.session(id)?;
        let (sub, joined) = entry
            .with_live(Live::subscribe)
            .ok_or_else(|| HubError::SessionClosed(id.clone()))?;
        Ok(Subscription::live(entry, sub, joined))
    }

    pub async fn session_statuses(&self) -> Vec<SessionListing> {
        let mut statuses = Vec::new();
        for entry in self.sessions() {
            let state = entry.state().await;
            if entry.is_closed() {
                continue;
            }
            let (status, active_run_id, active_runs) = match state {
                Ok(state) => (state.status, state.active_run_id, state.active_runs),
                Err(_) => (SessionStatus::ERROR, None, Vec::new()),
            };
            statuses.push(SessionListing {
                session_id: entry.id.clone(),
                adapter: entry.adapter.clone(),
                status,
                active_run_id,
                active_runs,
                created_ms: entry.created_ms,
            });
        }
        statuses
    }

    pub fn sessions(&self) -> Vec<Arc<HubSession>> {
        self.shared.sessions().live.values().cloned().collect()
    }

    pub fn close_sessions(&self) -> Vec<(SessionId, HubError)> {
        let mut failed = Vec::new();
        for entry in self.sessions() {
            let mut closed = crate::live::block_on(entry.close());
            for _ in 0..3 {
                if !matches!(closed, Err(HubError::Adapter(Error::RunActive))) {
                    break;
                }
                if let Ok(state) = crate::live::block_on(entry.state()) {
                    let mut runs: Vec<RunId> = state
                        .active_runs
                        .into_iter()
                        .map(|run| run.run_id)
                        .collect();
                    runs.extend(state.active_run_id.filter(|run| !runs.contains(run)));
                    for run in runs {
                        let _ = crate::live::block_on(entry.cancel(run));
                    }
                }
                std::thread::sleep(std::time::Duration::from_millis(100));
                closed = crate::live::block_on(entry.close());
            }
            match closed {
                Ok(()) | Err(HubError::Adapter(Error::SessionClosed)) => {}
                Err(err) => failed.push((entry.id().clone(), err)),
            }
        }
        failed
    }
}

struct Reserved<'a>(Option<&'a HubSession>);

impl Drop for Reserved<'_> {
    fn drop(&mut self) {
        if let Some(entry) = self.0.take() {
            live::finish(entry.with_live(Live::release));
        }
    }
}

pub struct HubSession {
    id: SessionId,
    adapter: String,
    session: Box<dyn oap_adapter::Session>,
    closed: AtomicBool,
    hub: Weak<Shared>,
    options: HubOptions,
    record: Record,
    live: Mutex<Live>,
    created_ms: i64,
}

#[derive(Clone, Debug)]
pub struct SessionListing {
    pub session_id: SessionId,
    pub adapter: String,
    pub status: SessionStatus,
    pub active_run_id: Option<RunId>,
    pub active_runs: Vec<oap_protocol::ActiveRun>,
    pub created_ms: i64,
}

impl HubSession {
    pub(crate) fn record(&self) -> &Record {
        &self.record
    }

    pub fn id(&self) -> &SessionId {
        &self.id
    }

    pub fn created_ms(&self) -> i64 {
        self.created_ms
    }

    pub fn adapter(&self) -> &str {
        &self.adapter
    }

    pub fn is_closed(&self) -> bool {
        self.closed.load(Ordering::SeqCst)
    }

    pub(crate) fn mark_closed(&self) {
        if !self.closed.swap(true, Ordering::SeqCst) {
            live::finish(self.with_live(Live::close));
            match self.hub.upgrade() {
                Some(hub) => hub.release(self),
                None => self.options.record(&self.record, Action::Closed, now_ms()),
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
        if state.status == SessionStatus::CLOSED {
            self.mark_closed();
        }
        Ok(state)
    }

    pub(crate) fn with_live<T>(&self, change: impl FnOnce(&mut Live) -> T) -> T {
        change(&mut self.live.lock().unwrap_or_else(PoisonError::into_inner))
    }

    pub(crate) fn stream_queue(&self) -> usize {
        match self.options.stream_queue {
            0 => DEFAULT_STREAM_QUEUE,
            queue => queue,
        }
    }

    fn closed_by(&self, err: &Error) {
        if matches!(err, Error::SessionClosed) {
            self.mark_closed();
        }
    }

    pub fn current_run(&self) -> Option<RunId> {
        self.with_live(|live| live.current())
    }

    pub async fn submit(
        self: &Arc<Self>,
        submit: SubmitRequest,
    ) -> Result<MessageSubmitResponse, HubError> {
        if submit.request.session_id != self.id {
            return Err(HubError::ScopeMismatch {
                payload: submit.request.session_id,
                addressed: self.id.clone(),
            });
        }
        let mut reserved = Reserved(Some(self));
        self.with_live(Live::reserve);
        let messages = submit.request.messages.clone();
        let (admission, stream) = match self.session.submit(submit).await {
            Ok(admitted) => admitted,
            Err(err) => {
                self.closed_by(&err);
                return Err(err.into());
            }
        };
        let at = now_ms();
        self.with_live(|live| live.work.submitted(&messages, admission.run_id.clone(), at));
        if admission.admission == Admission::STEERED {
            return Ok(admission);
        }
        reserved.0 = None;
        let current = admission.admission != Admission::QUEUED;
        let run = admission.run_id.clone();
        let serial = self.with_live(|live| live.start(run.as_ref(), current));
        live::read_run(self.clone(), run, serial, current, stream);
        Ok(admission)
    }

    pub async fn compact(
        self: &Arc<Self>,
        compact: CompactRequest,
    ) -> Result<SessionCompactResponse, HubError> {
        if compact.request.session_id != self.id {
            return Err(HubError::ScopeMismatch {
                payload: compact.request.session_id,
                addressed: self.id.clone(),
            });
        }
        let Some(compactor) = self.session.compactor() else {
            return Err(
                Error::UnsupportedControl(Box::new(UnsupportedControl::unadvertised(
                    FEATURE_SESSION_COMPACT,
                )))
                .into(),
            );
        };
        let mut reserved = Reserved(Some(self));
        self.with_live(Live::reserve);
        let (admission, stream) = match compactor.compact(compact).await {
            Ok(admitted) => admitted,
            Err(err) => {
                self.closed_by(&err);
                return Err(err.into());
            }
        };
        reserved.0 = None;
        let current = admission.admission != Admission::QUEUED;
        let run = admission.run_id.clone();
        let serial = self.with_live(|live| live.start(run.as_ref(), current));
        live::read_run(self.clone(), run, serial, current, stream);
        Ok(admission)
    }

    pub async fn models(
        &self,
        mut request: oap_protocol::ModelsRequest,
    ) -> Result<oap_adapter::Catalog, HubError> {
        if !request.session_id.as_str().is_empty() && request.session_id != self.id {
            return Err(HubError::ScopeMismatch {
                payload: request.session_id,
                addressed: self.id.clone(),
            });
        }
        request.session_id = self.id.clone();
        let lister = self.session.model_lister().ok_or_else(|| {
            Error::UnsupportedControl(Box::new(UnsupportedControl::unadvertised(
                oap_protocol::FEATURE_MODELS_LIST,
            )))
        })?;
        let catalog = lister
            .models(request)
            .await
            .inspect_err(|err| self.closed_by(err))?;
        if catalog.revision.is_empty() {
            return Err(Error::Harness(
                "serve: adapter served a model catalog with no capability revision".into(),
            )
            .into());
        }
        if catalog.models.session_id != self.id {
            return Err(Error::Harness(format!(
                "serve: adapter served a model catalog scoped to session {:?}, want {:?}",
                catalog.models.session_id.as_str(),
                self.id.as_str()
            ))
            .into());
        }
        Ok(catalog)
    }

    pub async fn tools(
        &self,
        mut request: oap_protocol::ToolsListRequest,
    ) -> Result<oap_adapter::ToolCatalog, HubError> {
        if let Some(payload) = request.session_id.as_ref().filter(|id| **id != self.id) {
            return Err(HubError::ScopeMismatch {
                payload: payload.clone(),
                addressed: self.id.clone(),
            });
        }
        request.session_id = Some(self.id.clone());
        let lister = self
            .session
            .tool_lister()
            .ok_or(Error::ToolCatalogUnavailable)?;
        let catalog = lister
            .tools(request)
            .await
            .inspect_err(|err| self.closed_by(err))?;
        if catalog.revision.is_empty() {
            return Err(Error::Harness(
                "serve: adapter served a tool catalog with no capability revision".into(),
            )
            .into());
        }
        if catalog.tools.session_id.as_ref() != Some(&self.id) {
            return Err(Error::Harness(format!(
                "serve: adapter served a tool catalog scoped to session {:?}, want {:?}",
                catalog
                    .tools
                    .session_id
                    .as_ref()
                    .map(|id| id.as_str())
                    .unwrap_or_default(),
                self.id.as_str()
            ))
            .into());
        }
        Ok(catalog)
    }

    pub async fn resolve(&self, resolution: InteractionResolution) -> Result<(), HubError> {
        let scoped = [
            resolution
                .permission
                .as_ref()
                .map(|permission| &permission.session_id),
            resolution.input.as_ref().map(|input| &input.session_id),
        ];
        if let Some(payload) = scoped.into_iter().flatten().find(|id| **id != self.id) {
            return Err(HubError::ScopeMismatch {
                payload: payload.clone(),
                addressed: self.id.clone(),
            });
        }
        self.session
            .resolve(resolution)
            .await
            .inspect_err(|err| self.closed_by(err))?;
        Ok(())
    }

    pub async fn resolve_call(
        &self,
        resolution: oap_adapter::CallResolution,
    ) -> Result<oap_protocol::ActionCallResolveResponse, HubError> {
        if resolution.request.session_id != self.id {
            return Err(HubError::ScopeMismatch {
                payload: resolution.request.session_id,
                addressed: self.id.clone(),
            });
        }
        let resolver = self.session.call_resolver().ok_or_else(|| {
            Error::UnsupportedControl(Box::new(UnsupportedControl::unadvertised(
                oap_protocol::FEATURE_TOOLS_PROVIDE,
            )))
        })?;
        Ok(resolver
            .resolve_call(resolution)
            .await
            .inspect_err(|err| self.closed_by(err))?)
    }

    pub async fn switch_model(
        &self,
        request: oap_protocol::SessionModelSwitchRequest,
    ) -> Result<(oap_protocol::SessionModelSwitchResponse, SessionState), HubError> {
        if request.session_id != self.id {
            return Err(HubError::ScopeMismatch {
                payload: request.session_id,
                addressed: self.id.clone(),
            });
        }
        let switcher = self.session.model_switcher().ok_or_else(|| {
            Error::UnsupportedControl(Box::new(UnsupportedControl::unadvertised(
                oap_protocol::FEATURE_SESSION_MODEL_SWITCH,
            )))
        })?;
        let (response, state) = switcher
            .switch_model(request)
            .await
            .inspect_err(|err| self.closed_by(err))?;
        if response.session_id != self.id
            || state.session_id != self.id
            || response.model_id != state.current_model_id
        {
            return Err(Error::Harness(format!(
                "serve: adapter reported a model switch outside session {:?}",
                self.id.as_str()
            ))
            .into());
        }
        Ok((response, state))
    }

    pub async fn update_settings(
        &self,
        request: oap_protocol::SessionSettingsUpdateRequest,
    ) -> Result<(oap_protocol::SessionSettingsUpdateResponse, SessionState), HubError> {
        if request.session_id != self.id {
            return Err(HubError::ScopeMismatch {
                payload: request.session_id,
                addressed: self.id.clone(),
            });
        }
        let keys = oap_adapter::open_setting_keys(
            &request.reasoning_level,
            request.compaction_policy.as_ref(),
        );
        let Some(first) = keys.first().copied() else {
            return Err(Error::InvalidSubmission.into());
        };
        let updater = self.session.settings_updater().ok_or_else(|| {
            Error::UnsupportedControl(Box::new(UnsupportedControl {
                field: oap_adapter::open_setting_field(first).to_owned(),
                ..UnsupportedControl::unadvertised(first)
            }))
        })?;
        let (response, state) = updater
            .update_settings(request)
            .await
            .inspect_err(|err| self.closed_by(err))?;
        if response.session_id != self.id || state.session_id != self.id {
            return Err(Error::Harness(format!(
                "serve: adapter reported a settings update outside session {:?}",
                self.id.as_str()
            ))
            .into());
        }
        Ok((response, state))
    }

    pub async fn cancel(&self, run: RunId) -> Result<RunCancelResponse, HubError> {
        Ok(self
            .session
            .cancel(run)
            .await
            .inspect_err(|err| self.closed_by(err))?)
    }

    pub async fn close(&self) -> Result<(), HubError> {
        self.session.close().await?;
        self.mark_closed();
        Ok(())
    }

    pub async fn rollback(&self) -> Result<(), HubError> {
        let closed = self.close().await;
        self.mark_closed();
        closed
    }
}
