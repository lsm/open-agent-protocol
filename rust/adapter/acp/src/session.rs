use std::collections::{BTreeMap, BTreeSet, VecDeque};
use std::sync::{Arc, Mutex, MutexGuard};
use std::thread::JoinHandle;

use futures::{FutureExt, StreamExt};
use futures_util::lock::Mutex as AsyncMutex;
use oap_adapter::journal::Journal;
use oap_adapter::jsonrpc::Inbound;
use oap_adapter::subscriber::Subscriber;
use oap_adapter::{
    BoxFuture, Catalog, Descriptor, Error, EventStream, InteractionResolution, ModelLister,
    ModelSwitcher, OpenRequest, Recovery, ResumeRequest, Session as SessionContract, SubmitRequest,
};
use oap_protocol::{
    InteractionId, MessageSubmitResponse, ModelDescriptor, ModelsRequest, ModelsResponse,
    RecoveryMetadata, RunCancelResponse, RunId, SessionState, SessionStatus, FEATURE_MODELS_LIST,
    VERSION,
};

use crate::adapter::Config;
use crate::native::{
    decode, ConfigOption, McpServer, PermissionOutcome, PermissionRequest, PermissionResponse,
    SessionNewParams, SessionNewResult, METHOD_SESSION_NEW, METHOD_SESSION_REQUEST_PERMISSION,
};
use crate::process::{Implementation, Process};
use crate::submit::Run;
use crate::{Permissions, PromptBridge, UsageScope};

type Shutdown = (Arc<PromptBridge>, Vec<JoinHandle<()>>);
const RESOLVED_PERMISSION_CAPACITY: usize = 64;

pub(crate) struct Inner {
    pub(crate) process: Option<Arc<PromptBridge>>,
    pub(crate) permissions: Option<Arc<Permissions>>,
    permission_runs: BTreeMap<InteractionId, RunId>,
    resolved_permissions: VecDeque<InteractionId>,
    pub(crate) state: SessionState,
    pub(crate) model_option: Option<ConfigOption>,
    pub(crate) active: Option<Arc<Run>>,
    pub(crate) workers: Vec<JoinHandle<()>>,
    pub(crate) usage: Arc<Mutex<UsageScope>>,
    pub(crate) tool_history: Arc<Mutex<BTreeSet<String>>>,
    pub(crate) journal: Journal,
    pub(crate) runtime: crate::Runtime,
}

impl Inner {
    fn remember_resolved(&mut self, id: InteractionId) {
        if !self.resolved_permissions.contains(&id) {
            self.resolved_permissions.push_back(id);
            if self.resolved_permissions.len() > RESOLVED_PERMISSION_CAPACITY {
                self.resolved_permissions.pop_front();
            }
        }
    }

    pub(crate) fn settle_permission_routes(&mut self) {
        for (id, _) in std::mem::take(&mut self.permission_runs) {
            self.remember_resolved(id);
        }
    }
}

pub(crate) struct Session {
    pub(crate) inner: Arc<Mutex<Inner>>,
    closing: AsyncMutex<()>,
    pub(crate) native_id: String,
    pub(crate) switching: AsyncMutex<()>,
    pub(crate) revision: String,
    pub(crate) submitting: AsyncMutex<()>,
    pub(crate) runtime: crate::Runtime,
    abort_active_on_close: bool,
}

fn native_error(error: impl std::fmt::Display) -> Error {
    Error::Harness(format!("create ACP session: {error}"))
}

impl Session {
    pub(crate) async fn open(
        config: &Config,
        request: OpenRequest,
        descriptor: Descriptor,
        mcp_servers: Vec<McpServer>,
    ) -> Result<Box<dyn SessionContract>, Error> {
        let runtime = config.runtime.clone().expect("adapter initializes runtime");
        let opening = SessionNewParams {
            cwd: config.process.dir.clone(),
            mcp_servers,
        };
        let params = serde_json::value::to_raw_value(&opening).map_err(native_error)?;
        let mut process_config = config.process.clone();
        process_config
            .client_info
            .get_or_insert_with(|| Implementation {
                name: "open-agent-protocol".into(),
                version: VERSION.into(),
                ..Implementation::default()
            });
        let process = Process::start_async(process_config, None)
            .await
            .map_err(|error| Error::Harness(error.to_string()))?;
        let (process, raw) = if request.reopen {
            crate::reload::call(process, &request, config, &opening.mcp_servers).await?
        } else {
            process
                .call_async(METHOD_SESSION_NEW.into(), Some(params), None)
                .await
                .map_err(native_error)?
        };
        let opened = decode::<SessionNewResult>(raw.get().as_bytes())
            .map_err(|error| {
                if request.reopen {
                    crate::reload::refusal(format!("the ACP agent could not load the bound session: {error}"))
                } else {
                    native_error(format!("decode session/new response: {error}"))
                }
            })
            .and_then(|mut opened| {
                if request.reopen {
                    let value: serde_json::Value = serde_json::from_str(raw.get())
                        .expect("decoded session response");
                    if value.get("sessionId").is_none_or(serde_json::Value::is_null) {
                        opened.session_id.clone_from(&request.native_session_id);
                    }
                    if opened.session_id != request.native_session_id {
                        return Err(crate::reload::refusal("the ACP agent returned a different session id"));
                    }
                }
                if opened.session_id.is_empty() {
                    Err(Error::Harness("acp adapter: invalid native protocol observation: session/new returned no session id".into()))
                } else {
                    Ok(opened)
                }
            });
        let opened = match opened {
            Ok(opened) => opened,
            Err(error) => {
                let _ = process.close_async().await;
                return Err(error);
            }
        };
        let (process, opened) =
            crate::settings::configure(process, opened, config, &request).await?;
        let mut usage = UsageScope::default();
        if request.reopen {
            usage.consume_prompt();
        }
        let model_option = opened
            .config_options
            .iter()
            .find(|option| option.category == "model")
            .cloned();
        let current_model_id = model_option
            .as_ref()
            .map(|option| option.current_value.clone())
            .filter(|value| !matches!(value.as_str(), "" | "default" | "current"))
            .unwrap_or_default();
        let session_id = if request.session_id.as_str().is_empty() {
            runtime.new_id("session").into()
        } else {
            request.session_id
        };
        let inner = Arc::new(Mutex::new(Inner {
            process: None,
            permissions: None,
            permission_runs: BTreeMap::new(),
            resolved_permissions: VecDeque::new(),
            active: None,
            workers: Vec::new(),
            usage: Arc::new(Mutex::new(usage)),
            tool_history: Arc::default(),
            journal: Journal::new(config.journal_capacity),
            runtime: runtime.clone(),
            model_option,
            state: SessionState {
                session_id,
                status: SessionStatus::IDLE,
                current_model_id,
                reasoning_level: request.reasoning_level.clone(),
                recovery: request.reopen.then(|| RecoveryMetadata {
                    recovered: true,
                    reason: crate::reload::REASON.into(),
                    ..RecoveryMetadata::default()
                }),
                updated_at_ms: runtime.now_ms(),
                sources: descriptor
                    .capabilities
                    .sources
                    .into_iter()
                    .chain(
                        request
                            .tool_sources
                            .iter()
                            .map(oap_protocol::ToolSourceAttachment::descriptor),
                    )
                    .collect(),
                ..SessionState::default()
            },
        }));
        if request.reopen || !config.session_config.is_empty() {
            let mut state = inner.lock().unwrap_or_else(|p| p.into_inner());
            state.apply_config(&opened.config_options);
            if !request.reasoning_level.is_empty() {
                state
                    .state
                    .reasoning_level
                    .clone_from(&request.reasoning_level);
            }
        }
        #[cfg(test)]
        tests::LAST_SESSION.with(|slot| *slot.borrow_mut() = Some(Arc::downgrade(&inner)));
        let weak = Arc::downgrade(&inner);
        let done = process.client.done();
        let native_id = opened.session_id.clone();
        let (bound, binding) = crossbeam_channel::bounded::<()>(0);
        let bridge = Arc::new(PromptBridge::new(process, move |message| {
            crossbeam_channel::select! { recv(done) -> _ => return, recv(binding) -> _ => () }
            let Some(shared) = weak.upgrade() else { return };
            let (run, permissions, bridge) = {
                let mut inner = shared.lock().unwrap_or_else(|p| p.into_inner());
                if let Inbound::Notification(notification) = &message {
                    if inner.observe_config(notification, &native_id) {
                        return;
                    }
                    if inner
                        .usage
                        .lock()
                        .unwrap_or_else(|p| p.into_inner())
                        .observe(notification, &native_id)
                    {
                        return;
                    }
                }
                let (Some(permissions), Some(bridge)) = (&inner.permissions, &inner.process) else {
                    return;
                };
                (
                    inner
                        .active
                        .clone()
                        .filter(|run| !run.lock().state().closed),
                    permissions.clone(),
                    bridge.clone(),
                )
            };
            if let Some(run) = &run {
                crossbeam_channel::select! { recv(done) -> _ => return, recv(run.admitted) -> _ => () }
            }
            match message {
                Inbound::Request(request)
                    if request.method == METHOD_SESSION_REQUEST_PERMISSION =>
                {
                    if let Some(run) = &run {
                        if let Ok(id) = futures::executor::block_on(
                            permissions.request(run.text.clone(), request),
                        ) {
                            let mut inner = shared.lock().unwrap_or_else(|p| p.into_inner());
                            if inner.process.is_some() {
                                if run.lock().state().closed
                                    || inner.resolved_permissions.contains(&id)
                                {
                                    inner.remember_resolved(id);
                                } else {
                                    inner.permission_runs.insert(id, run.id.clone());
                                }
                            }
                        }
                    } else {
                        let valid = decode::<PermissionRequest>(
                            request
                                .params
                                .as_ref()
                                .map_or(b"null".as_slice(), |raw| raw.get().as_bytes()),
                        )
                        .is_ok_and(|params| {
                            params.session_id == native_id
                                && !params.tool_call.tool_call_id.is_empty()
                                && !params.options.is_empty()
                        });
                        let reply = if valid {
                            bridge.respond(
                                request,
                                serde_json::value::to_raw_value(&PermissionResponse {
                                    outcome: PermissionOutcome {
                                        outcome: "cancelled".into(),
                                        ..PermissionOutcome::default()
                                    },
                                })
                                .expect("native cancellation is JSON"),
                            )
                        } else {
                            bridge.respond_error(
                                request,
                                -32602,
                                "invalid permission request".into(),
                                None,
                            )
                        };
                        let _ = futures::executor::block_on(reply);
                    }
                }
                Inbound::Request(request) => {
                    let _ = futures::executor::block_on(bridge.respond_error(
                        request,
                        -32601,
                        "method not supported".into(),
                        None,
                    ));
                }
                Inbound::Notification(notification) => {
                    if let Some(run) = &run {
                        let _ = futures::executor::block_on(
                            permissions.observe(run.text.clone(), notification),
                        );
                    }
                }
                Inbound::Barrier(_) => (),
            }
            shared.lock().unwrap_or_else(|p| p.into_inner()).refresh();
        }));
        let clock = runtime.clone();
        let permissions = Permissions::new(bridge.clone(), request.participant.id, move || {
            clock.now_ms()
        })?;
        {
            let mut state = inner.lock().unwrap_or_else(|p| p.into_inner());
            state.process = Some(bridge);
            state.permissions = Some(permissions);
        }
        drop(bound);
        Ok(Box::new(Self {
            inner,
            closing: AsyncMutex::new(()),
            switching: AsyncMutex::new(()),
            native_id: opened.session_id,
            revision: descriptor.capability_revision,
            submitting: AsyncMutex::new(()),
            runtime,
            abort_active_on_close: config.abort_active_on_close,
        }))
    }

    pub(crate) fn lock(&self) -> MutexGuard<'_, Inner> {
        self.inner
            .lock()
            .unwrap_or_else(|poison| poison.into_inner())
    }

    fn shutdown(&self, abort: bool) -> Result<Option<Shutdown>, Error> {
        let mut inner = self.lock();
        if !abort
            && inner
                .active
                .as_ref()
                .is_some_and(|run| !run.lock().state().closed)
        {
            return Err(Error::RunActive);
        }
        let Some(process) = inner.process.take() else {
            return Ok(None);
        };
        inner.permissions.take();
        inner.permission_runs.clear();
        if let Some(run) = inner.active.take() {
            run.lock().close();
        }
        inner.journal.close();
        inner.state.status = SessionStatus::CLOSED;
        inner.state.active_run_id = None;
        inner.state.updated_at_ms = inner.runtime.now_ms();
        Ok(Some((process, std::mem::take(&mut inner.workers))))
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        if let Ok(Some((process, workers))) = self.shutdown(true) {
            let _ = process.close_workers(workers);
        }
    }
}

impl SessionContract for Session {
    fn state(&self) -> BoxFuture<'_, SessionState> {
        Box::pin(async {
            let mut inner = self.lock();
            if inner.process.is_none() {
                return Err(Error::SessionClosed);
            }
            inner.refresh();
            Ok(inner.state.clone())
        })
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        Box::pin(async {
            let _closing = self.closing.lock().await;
            let Some((process, workers)) = self.shutdown(self.abort_active_on_close)? else {
                return Ok(());
            };
            process
                .close_joining(workers)
                .await
                .map_err(|error| Error::Harness(error.to_string()))
        })
    }

    fn native_session_id(&self) -> Option<String> {
        Some(self.native_id.clone())
    }
    fn model_lister(&self) -> Option<&dyn ModelLister> {
        Some(self)
    }
    fn model_switcher(&self) -> Option<&dyn ModelSwitcher> {
        Some(self)
    }

    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        Box::pin(self.submit_text(request))
    }
    fn resolve(&self, resolution: InteractionResolution) -> BoxFuture<'_, ()> {
        Box::pin(async move {
            let (permissions, run, id) = {
                let inner = self.lock();
                let permissions = inner.permissions.clone().ok_or(Error::SessionClosed)?;
                let answer = resolution
                    .permission
                    .as_ref()
                    .filter(|_| resolution.input.is_none())
                    .ok_or(Error::InvalidResolution)?;
                if inner.resolved_permissions.contains(&answer.interaction_id) {
                    return Err(Error::InteractionResolved);
                }
                let run = inner.active.clone().ok_or(Error::InteractionNotFound)?;
                if inner
                    .permission_runs
                    .get(&answer.interaction_id)
                    .is_some_and(|id| *id != run.id)
                {
                    return Err(Error::InteractionResolved);
                }
                (permissions, run, answer.interaction_id.clone())
            };
            let result = permissions.resolve(run.text.clone(), resolution).await;
            let mut inner = self.lock();
            if result.is_ok() || result == Err(Error::InteractionResolved) {
                inner.permission_runs.remove(&id);
                inner.remember_resolved(id);
            }
            inner.refresh();
            result
        })
    }
    fn cancel(&self, id: RunId) -> BoxFuture<'_, RunCancelResponse> {
        Box::pin(async move {
            let _submitting = self.submitting.lock().await;
            let (permissions, run) = {
                let inner = self.lock();
                let permissions = inner.permissions.clone().ok_or(Error::SessionClosed)?;
                let run = inner
                    .active
                    .clone()
                    .filter(|run| run.id == id)
                    .ok_or(Error::RunNotFound)?;
                (permissions, run)
            };
            let result = permissions.cancel(run.text.clone()).await;
            let mut inner = self.lock();
            if inner.process.is_none() {
                return Err(Error::SessionClosed);
            }
            if result.is_ok() {
                inner.settle_permission_routes();
            }
            inner.refresh();
            result
        })
    }
    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        Box::pin(async move {
            let mut inner = self.lock();
            if inner.process.is_none() {
                return Err(Error::SessionClosed);
            }
            let active = inner.active.clone();
            let mut run = active.as_ref().map(|run| run.lock());
            if let (Some(active), Some(run)) = (&active, &run) {
                inner.refresh_run(&active.id, run.state());
            }
            let current = active
                .as_ref()
                .is_some_and(|active| active.id == request.run_id);
            let latest = if current {
                run.as_ref().expect("active run").state().sequence
            } else {
                inner
                    .journal
                    .ended(request.run_id.as_str())
                    .ok_or(Error::RunNotFound)?
            };
            if request.after_sequence > latest {
                return Err(Error::ReplayCursorFuture);
            }
            let (recovery, mut replay) = inner.journal.resume(
                inner.state.clone(),
                request.run_id.as_str(),
                request.after_sequence,
                latest,
            );
            if let Some(gap) = recovery.replay_gap {
                return Err(Error::ReplayGap(gap));
            }
            let retained = recovery.replayed_through - request.after_sequence;
            let follower = Subscriber::new(retained as usize + 64);
            for _ in 0..retained {
                let event = replay
                    .next()
                    .now_or_never()
                    .expect("locked journal snapshot is ready")
                    .expect("retained journal event")?;
                follower.replay(event);
            }
            if current && !run.as_ref().expect("active run").state().closed {
                run.as_mut()
                    .expect("active run")
                    .followers
                    .push(follower.clone());
            } else {
                follower.close();
            }
            Ok((recovery, follower.stream()))
        })
    }
}

impl ModelLister for Session {
    fn models(&self, request: ModelsRequest) -> BoxFuture<'_, Catalog> {
        Box::pin(async move {
            if !request.allows_degraded(FEATURE_MODELS_LIST) {
                return Err(Error::DegradedControl(FEATURE_MODELS_LIST.into()));
            }
            let inner = self.lock();
            if inner.process.is_none() {
                return Err(Error::SessionClosed);
            }
            if !request.session_id.as_str().is_empty()
                && request.session_id != inner.state.session_id
            {
                return Err(Error::RunNotFound);
            }
            let option = inner
                .model_option
                .as_ref()
                .ok_or_else(|| crate::models::missing_model(FEATURE_MODELS_LIST))?;
            Ok(Catalog {
                revision: self.revision.clone(),
                models: ModelsResponse {
                    session_id: inner.state.session_id.clone(),
                    current_model_id: inner.state.current_model_id.clone(),
                    models: option
                        .values()
                        .into_iter()
                        .map(|value| ModelDescriptor {
                            default: value.value == inner.state.current_model_id,
                            id: value.value,
                            display_name: value.name,
                            ..ModelDescriptor::default()
                        })
                        .collect(),
                    ..ModelsResponse::default()
                },
            })
        })
    }
}

#[cfg(test)]
#[path = "../tests/support/session_permissions.rs"]
mod tests;
