use std::collections::BTreeSet;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::thread::JoinHandle;
use std::time::{SystemTime, UNIX_EPOCH};

use futures_util::lock::Mutex as AsyncMutex;
use oap_adapter::jsonrpc::Inbound;
use oap_adapter::{
    BoxFuture, Catalog, Descriptor, Error, EventStream, InteractionResolution, ModelLister,
    ModelSwitcher, OpenRequest, Recovery, ResumeRequest, Session as SessionContract, SubmitRequest,
};
use oap_protocol::{
    MessageSubmitResponse, ModelDescriptor, ModelsRequest, ModelsResponse, RunCancelResponse,
    RunId, SessionState, SessionStatus, FEATURE_MODELS_LIST, VERSION,
};

use crate::adapter::{unavailable, Config};
use crate::native::{decode, ConfigOption, SessionNewParams, SessionNewResult, METHOD_SESSION_NEW};
use crate::process::{Implementation, Process};
use crate::submit::Run;
use crate::{PromptBridge, UsageScope};

type Shutdown = (Arc<PromptBridge>, Vec<JoinHandle<()>>);

pub(crate) struct Inner {
    pub(crate) process: Option<Arc<PromptBridge>>,
    pub(crate) state: SessionState,
    pub(crate) active: Option<Arc<Run>>,
    pub(crate) workers: Vec<JoinHandle<()>>,
    pub(crate) usage: Arc<Mutex<UsageScope>>,
    pub(crate) tool_history: Arc<Mutex<BTreeSet<String>>>,
}

pub(crate) struct Session {
    pub(crate) inner: Arc<Mutex<Inner>>,
    closing: AsyncMutex<()>,
    pub(crate) native_id: String,
    pub(crate) model_option: Option<ConfigOption>,
    pub(crate) switching: AsyncMutex<()>,
    pub(crate) revision: String,
    pub(crate) submitting: AsyncMutex<()>,
    pub(crate) ids: Arc<AtomicU64>,
    abort_active_on_close: bool,
}

pub(crate) fn now_ms() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |time| time.as_millis() as i64)
}

fn native_error(error: impl std::fmt::Display) -> Error {
    Error::Harness(format!("create ACP session: {error}"))
}

impl Session {
    pub(crate) async fn open(
        config: &Config,
        request: OpenRequest,
        descriptor: Descriptor,
        next: &Arc<AtomicU64>,
    ) -> Result<Box<dyn SessionContract>, Error> {
        let params = serde_json::value::to_raw_value(&SessionNewParams {
            cwd: config.process.dir.clone(),
            mcp_servers: config.mcp_servers.clone(),
        })
        .map_err(native_error)?;
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
        let (process, raw) = process
            .call_async(METHOD_SESSION_NEW.into(), Some(params), None)
            .await
            .map_err(native_error)?;
        let opened = decode::<SessionNewResult>(raw.get().as_bytes())
            .map_err(|error| native_error(format!("decode session/new response: {error}")))
            .and_then(|opened| {
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
        let model_option = opened
            .config_options
            .into_iter()
            .find(|option| option.category == "model");
        let current_model_id = model_option
            .as_ref()
            .map(|option| option.current_value.clone())
            .filter(|value| !matches!(value.as_str(), "" | "default" | "current"))
            .unwrap_or_default();
        let session_id = if request.session_id.as_str().is_empty() {
            format!("session-{}", next.fetch_add(1, Ordering::SeqCst) + 1).into()
        } else {
            request.session_id
        };
        let inner = Arc::new(Mutex::new(Inner {
            process: None,
            active: None,
            workers: Vec::new(),
            usage: Arc::default(),
            tool_history: Arc::default(),
            state: SessionState {
                session_id,
                status: SessionStatus::IDLE,
                current_model_id,
                updated_at_ms: now_ms(),
                sources: descriptor.capabilities.sources,
                ..SessionState::default()
            },
        }));
        let weak = Arc::downgrade(&inner);
        let done = process.client.done();
        let native_id = opened.session_id.clone();
        let bridge = PromptBridge::new(process, move |message| match message {
            Inbound::Request(request) => {
                let _ = request.respond_error(-32601, "method not supported");
            }
            Inbound::Notification(notification) => {
                let Some(inner) = weak.upgrade() else {
                    return;
                };
                let run = {
                    let inner = inner.lock().unwrap_or_else(|p| p.into_inner());
                    if inner
                        .usage
                        .lock()
                        .unwrap_or_else(|p| p.into_inner())
                        .observe(&notification, &native_id)
                    {
                        return;
                    }
                    inner.active.clone()
                };
                if let Some(run) = run {
                    crossbeam_channel::select! {
                        recv(done) -> _ => return,
                        recv(run.admitted) -> _ => (),
                    }
                    inner
                        .lock()
                        .unwrap_or_else(|p| p.into_inner())
                        .update(&run, |text| text.observe(notification, now_ms()));
                }
            }
            Inbound::Barrier(_) => (),
        });
        inner.lock().unwrap_or_else(|p| p.into_inner()).process = Some(Arc::new(bridge));
        Ok(Box::new(Self {
            inner,
            closing: AsyncMutex::new(()),
            switching: AsyncMutex::new(()),
            native_id: opened.session_id,
            model_option,
            revision: descriptor.capability_revision,
            submitting: AsyncMutex::new(()),
            ids: next.clone(),
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
        if let Some(run) = inner.active.take() {
            run.lock().close();
        }
        inner.state.status = SessionStatus::CLOSED;
        inner.state.active_run_id = None;
        inner.state.updated_at_ms = now_ms();
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
            let inner = self.lock();
            if inner.process.is_none() {
                return Err(Error::SessionClosed);
            }
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
    fn resolve(&self, _: InteractionResolution) -> BoxFuture<'_, ()> {
        Box::pin(async { Err(unavailable("action.permissions")) })
    }
    fn cancel(&self, _: RunId) -> BoxFuture<'_, RunCancelResponse> {
        Box::pin(async { Err(unavailable("run.cancel")) })
    }
    fn resume(&self, _: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        Box::pin(async { Err(unavailable("run.resume")) })
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
            let option = self
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
