mod admit;
mod cancel;
mod catalogs;
mod compact;
mod controls;
mod descriptor;
mod pipe;
mod resolve;
mod resume;
mod session;
mod steer;

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::{SystemTime, UNIX_EPOCH};

use oap_protocol::*;
use serde_json::value::RawValue;

pub use admit::history_tokens;
use admit::{
    admit_provided_tools, admit_tool_sources, compaction_threshold, effective_policy,
    session_sources,
};
pub use catalogs::{model_catalog, provider_catalog};

use session::Inner;

use crate::{
    Adapter, Admitter, BoxFuture, Catalog, CompactRequest, Compactor, Descriptor, Error,
    EventStream, InteractionResolution, ModelLister, ModelSwitcher, OpenRequest, Recovery,
    ResumeRequest, Session, SettingsUpdater, SubmitRequest, ToolCatalog, ToolLister,
};

pub const DEFAULT_JOURNAL_CAPACITY: usize = 64;
pub const MODEL_PRIMARY: &str = "reference-model-a";
pub const MODEL_SECONDARY: &str = "reference-model-b";
pub const CAPABILITY_REVISION: &str = "reference-memory-v17";
pub const FIXED_RESULT: &str = r#"{"ok":true}"#;
pub const SCRIPTED_TOOL: &str = "scripted_tool";
pub const SCRIPTED_TOOL_OWNER: &str = "reference-adapter";
pub const SCRIPTED_SOURCE: &str = "reference-native";
pub const SYNTHETIC_MCP_SOURCE: &str = "reference-mcp";
pub const ENDPOINT_ID: &str = "reference.memory";

const MAX_PROVIDED_TOOLS: usize = 2;
const PROVIDED_NAME_PATTERN: &str = "^[a-z][a-z0-9_]*$";
const PROVIDED_DIALECT: &str = "https://json-schema.org/draft/2020-12/schema";
const ATTACH_TRANSPORTS: [&str; 2] = [TOOL_SOURCE_PROCESS, TOOL_SOURCE_LOCAL];
const MAX_ATTACHED_SOURCES: usize = 2;
const REFERENCE_WINDOW: u64 = 8192;

fn raw(text: &str) -> Box<RawValue> {
    RawValue::from_string(text.to_owned()).unwrap_or_else(|_| RawValue::NULL.to_owned())
}

fn json(value: impl serde::Serialize) -> Box<RawValue> {
    serde_json::value::to_raw_value(&value).unwrap_or_else(|_| RawValue::NULL.to_owned())
}

fn support(level: SupportLevel, reason: &str) -> FeatureSupport {
    FeatureSupport {
        level,
        reason: reason.to_owned(),
        ..FeatureSupport::default()
    }
}

pub(crate) fn provide_support() -> FeatureSupport {
    FeatureSupport {
        limits: BTreeMap::from([
            (LIMIT_MAX_TOOLS.to_owned(), json(MAX_PROVIDED_TOOLS)),
            (LIMIT_NAME_PATTERN.to_owned(), json(PROVIDED_NAME_PATTERN)),
            (LIMIT_SCHEMA_DIALECT.to_owned(), json(PROVIDED_DIALECT)),
        ]),
        ..support(
            SupportLevel::EMULATED,
            "provided tools are called by the script and executed by the control layer through the resolve pair",
        )
    }
}

pub(crate) fn attach_support() -> FeatureSupport {
    FeatureSupport {
        modes: vec![MODE_SESSION_OPEN.to_owned()],
        limits: BTreeMap::from([
            (LIMIT_MAX_SOURCES.to_owned(), json(MAX_ATTACHED_SOURCES)),
            (LIMIT_TRANSPORTS.to_owned(), json(ATTACH_TRANSPORTS)),
        ]),
        ..support(
            SupportLevel::EMULATED,
            "sources are described and published back; the reference adapter runs no client for them",
        )
    }
}

pub fn scripted_catalog() -> Vec<ToolDefinition> {
    vec![ToolDefinition {
        name: SCRIPTED_TOOL.to_owned(),
        description: "The deterministic scripted tool the reference adapter calls.".to_owned(),
        input_schema: Some(raw(
            r#"{"type":"object","properties":{"operation":{"type":"string"}}}"#,
        )),
        execution_owner: ParticipantId::from(SCRIPTED_TOOL_OWNER),
        source: SCRIPTED_SOURCE.to_owned(),
        features: BTreeMap::from([
            (
                "action.tools.execute".to_owned(),
                support(
                    SupportLevel::EMULATED,
                    "the reference adapter executes a fixed deterministic script",
                ),
            ),
            (
                "action.permissions".to_owned(),
                support(SupportLevel::EMULATED, "the scripted call is gated"),
            ),
        ]),
        ..ToolDefinition::default()
    }]
}

pub fn declared_sources() -> Vec<ToolSourceDescriptor> {
    vec![
        ToolSourceDescriptor {
            id: SCRIPTED_SOURCE.to_owned(),
            kind: TOOL_SOURCE_NATIVE.to_owned(),
            display_name: "Reference Adapter Script".to_owned(),
            ..ToolSourceDescriptor::default()
        },
        ToolSourceDescriptor {
            id: SYNTHETIC_MCP_SOURCE.to_owned(),
            kind: TOOL_SOURCE_PROCESS.to_owned(),
            protocol: TOOL_SOURCE_MCP.to_owned(),
            display_name: "Reference Synthetic MCP Source".to_owned(),
            endpoint: "stdio:reference-tool-source".to_owned(),
        },
    ]
}

pub trait Clock: Send + Sync {
    fn now_ms(&self) -> i64;
}

pub trait IdGenerator: Send + Sync {
    fn new_id(&self, kind: &str) -> String;
}

struct WallClock;

impl Clock for WallClock {
    fn now_ms(&self) -> i64 {
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map_or(0, |elapsed| elapsed.as_millis() as i64)
    }
}

#[derive(Default)]
struct SequenceIds(AtomicU64);

impl IdGenerator for SequenceIds {
    fn new_id(&self, kind: &str) -> String {
        format!("{kind}-{}", self.0.fetch_add(1, Ordering::SeqCst) + 1)
    }
}

#[derive(Default)]
pub struct Config {
    pub clock: Option<Arc<dyn Clock>>,
    pub ids: Option<Arc<dyn IdGenerator>>,
    pub journal_capacity: usize,
}

#[derive(Clone, Default)]
struct KeptSession {
    model: String,
    policy: Option<CompactionPolicy>,
}

pub struct Memory {
    clock: Arc<dyn Clock>,
    ids: Arc<dyn IdGenerator>,
    capacity: usize,
    closed: Arc<Mutex<HashMap<String, KeptSession>>>,
}

pub struct MemorySession(Arc<Mutex<Inner>>);

impl MemorySession {
    fn lock(&self) -> MutexGuard<'_, Inner> {
        self.0
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    pub fn snapshot(&self) -> SessionState {
        self.lock().state.clone()
    }

    pub fn participant(&self) -> ParticipantId {
        self.lock().participant.clone()
    }

    pub fn compaction_threshold(&self) -> u64 {
        self.lock().threshold
    }

    pub fn attached_sources(&self) -> Vec<ToolSourceAttachment> {
        self.lock().attached.clone()
    }

    pub fn provided_tools(&self) -> Vec<ToolDefinition> {
        self.lock().provided.clone()
    }

    pub fn submit(
        &self,
        request: SubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        self.lock().submit(request)
    }

    pub fn resolve(&self, resolution: InteractionResolution) -> Result<(), Error> {
        self.lock().resolve(resolution)
    }

    pub fn compact(
        &self,
        request: CompactRequest,
    ) -> Result<(SessionCompactResponse, EventStream), Error> {
        self.lock().compact(request)
    }

    pub fn cancel(&self, run_id: &RunId) -> Result<RunCancelResponse, Error> {
        self.lock().cancel(run_id)
    }

    pub fn resume(&self, request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        self.lock().resume(request)
    }

    pub fn close(&self) -> Result<(), Error> {
        self.lock().close()
    }

    pub fn models(&self, request: &ModelsRequest) -> Result<Catalog, Error> {
        self.lock().models(request)
    }

    pub fn switch_model(
        &self,
        request: &SessionModelSwitchRequest,
    ) -> Result<(SessionModelSwitchResponse, SessionState), Error> {
        self.lock().switch_model(request)
    }

    pub fn update_settings(
        &self,
        request: &SessionSettingsUpdateRequest,
    ) -> Result<(SessionSettingsUpdateResponse, SessionState), Error> {
        self.lock().update_settings(request)
    }

    pub fn tools(&self, request: &ToolsListRequest) -> Result<ToolCatalog, Error> {
        self.lock().tools(request)
    }
}

impl ModelLister for MemorySession {
    fn models(&self, request: ModelsRequest) -> BoxFuture<'_, Catalog> {
        let result = MemorySession::models(self, &request);
        Box::pin(async move { result })
    }
}

impl ModelSwitcher for MemorySession {
    fn switch_model(
        &self,
        request: SessionModelSwitchRequest,
    ) -> BoxFuture<'_, (SessionModelSwitchResponse, SessionState)> {
        let result = MemorySession::switch_model(self, &request);
        Box::pin(async move { result })
    }
}

impl SettingsUpdater for MemorySession {
    fn update_settings(
        &self,
        request: SessionSettingsUpdateRequest,
    ) -> BoxFuture<'_, (SessionSettingsUpdateResponse, SessionState)> {
        let result = MemorySession::update_settings(self, &request);
        Box::pin(async move { result })
    }
}

impl ToolLister for MemorySession {
    fn tools(&self, request: ToolsListRequest) -> BoxFuture<'_, ToolCatalog> {
        let result = MemorySession::tools(self, &request);
        Box::pin(async move { result })
    }
}

impl Compactor for MemorySession {
    fn compact(
        &self,
        request: CompactRequest,
    ) -> BoxFuture<'_, (SessionCompactResponse, EventStream)> {
        let result = MemorySession::compact(self, request);
        Box::pin(async move { result })
    }
}

impl Session for MemorySession {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        let result = MemorySession::submit(self, request);
        Box::pin(async move { result })
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        let state = self.snapshot();
        Box::pin(async move { Ok(state) })
    }

    fn resolve(&self, resolution: InteractionResolution) -> BoxFuture<'_, ()> {
        let result = MemorySession::resolve(self, resolution);
        Box::pin(async move { result })
    }

    fn cancel(&self, run_id: RunId) -> BoxFuture<'_, RunCancelResponse> {
        let result = MemorySession::cancel(self, &run_id);
        Box::pin(async move { result })
    }

    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        let result = MemorySession::resume(self, request);
        Box::pin(async move { result })
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        let result = MemorySession::close(self);
        Box::pin(async move { result })
    }

    fn model_lister(&self) -> Option<&dyn ModelLister> {
        Some(self)
    }

    fn model_switcher(&self) -> Option<&dyn ModelSwitcher> {
        Some(self)
    }

    fn settings_updater(&self) -> Option<&dyn SettingsUpdater> {
        Some(self)
    }

    fn tool_lister(&self) -> Option<&dyn ToolLister> {
        Some(self)
    }

    fn compactor(&self) -> Option<&dyn Compactor> {
        Some(self)
    }
}

impl Memory {
    pub fn new(config: Config) -> Self {
        let capacity = if config.journal_capacity == 0 {
            DEFAULT_JOURNAL_CAPACITY
        } else {
            config.journal_capacity
        };
        Self {
            clock: config.clock.unwrap_or_else(|| Arc::new(WallClock)),
            ids: config
                .ids
                .unwrap_or_else(|| Arc::new(SequenceIds::default())),
            capacity,
            closed: Arc::new(Mutex::new(HashMap::new())),
        }
    }

    pub fn check_admission(&self, request: &OpenRequest) -> Result<(), Error> {
        let attached = admit_tool_sources(request)?;
        admit_provided_tools(request, &attached)?;
        compaction_threshold(request.compaction_policy.as_ref()).map(|_| ())
    }

    fn claim(
        &self,
        id: &str,
        reopen: bool,
    ) -> Result<(KeptSession, Option<RecoveryMetadata>), Error> {
        let mut closed = self
            .closed
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        let kept = closed.remove(id);
        match (reopen, kept) {
            (false, _) => Ok((KeptSession::default(), None)),
            (true, None) => Err(Error::UnknownSession(SessionId::from(id))),
            (true, Some(kept)) => Ok((
                kept,
                Some(RecoveryMetadata {
                    recovered: true,
                    ..RecoveryMetadata::default()
                }),
            )),
        }
    }

    pub fn keep(&self, id: &str, model: &str, policy: Option<CompactionPolicy>) {
        let mut closed = self
            .closed
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner());
        closed.insert(
            id.to_owned(),
            KeptSession {
                model: model.to_owned(),
                policy,
            },
        );
    }

    pub fn open_session(&self, request: &OpenRequest) -> Result<MemorySession, Error> {
        if request.participant.id.as_str().is_empty() {
            return Err(Error::InvalidParticipant);
        }
        let attached = admit_tool_sources(request)?;
        let provided = admit_provided_tools(request, &attached)?;
        let id = if request.session_id.as_str().is_empty() {
            self.ids.new_id("session")
        } else {
            request.session_id.as_str().to_owned()
        };
        let mut policy = request.compaction_policy.clone();
        let mut threshold = compaction_threshold(policy.as_ref())?;
        let (kept, recovery) = self.claim(&id, request.reopen)?;
        if policy.is_none() && kept.policy.is_some() {
            policy = kept.policy;
            threshold = compaction_threshold(policy.as_ref())?;
        }
        let state = SessionState {
            session_id: SessionId::from(id),
            status: SessionStatus::IDLE,
            updated_at_ms: self.clock.now_ms(),
            sources: session_sources(&attached),
            current_model_id: kept.model,
            recovery,
            compaction_policy: Some(effective_policy(policy.as_ref())),
            ..SessionState::default()
        };
        Ok(MemorySession(Arc::new(Mutex::new(Inner {
            clock: self.clock.clone(),
            ids: self.ids.clone(),
            kept: self.closed.clone(),
            capacity: self.capacity,
            participant: request.participant.id.clone(),
            state,
            attached,
            provided,
            closed: false,
            active: None,
            reserved: None,
            runs: HashMap::new(),
            journal: VecDeque::new(),
            settled: Vec::new(),
            threshold,
            history: 0,
        }))))
    }
}

impl Admitter for Memory {
    fn admit(&self, request: &OpenRequest) -> BoxFuture<'_, ()> {
        let result = self.check_admission(request);
        Box::pin(async move { result })
    }
}

impl Adapter for Memory {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        let descriptor = self.describe();
        Box::pin(async move { Ok(descriptor) })
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        let result = self
            .open_session(&request)
            .map(|session| Box::new(session) as Box<dyn Session>);
        Box::pin(async move { result })
    }

    fn admitter(&self) -> Option<&dyn Admitter> {
        Some(self)
    }
}
