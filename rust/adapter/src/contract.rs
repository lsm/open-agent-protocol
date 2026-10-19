use std::future::Future;
use std::pin::Pin;

use futures_core::Stream;
use oap_protocol::{
    ActionCallResolveRequest, ActionCallResolveResponse, CapabilityDescriptor, EnvelopeId,
    MessageSubmitRequest, MessageSubmitResponse, ModelsRequest, ModelsResponse, ParticipantId,
    PermissionResolveRequest, RunCancelResponse, RunId, SessionCompactRequest,
    SessionCompactResponse, SessionModelSwitchRequest, SessionModelSwitchResponse,
    SessionSettingsUpdateRequest, SessionSettingsUpdateResponse, SessionState, SupportLevel,
    ToolsListRequest, ToolsListResponse, TranscriptLoadResponse, UserInputResolveRequest,
};

use crate::{Error, OpenRequest, ReplayGap};

pub type BoxFuture<'a, T> = Pin<Box<dyn Future<Output = Result<T, Error>> + Send + 'a>>;

pub type EventStream = Pin<Box<dyn Stream<Item = Result<oap_protocol::Envelope, Error>> + Send>>;

#[derive(Clone, Debug, Default)]
pub struct JournalDescriptor {
    pub scope: String,
    pub persistence: String,
    pub replay: SupportLevel,
    pub capacity: usize,
}

#[derive(Clone, Debug, Default)]
pub struct Descriptor {
    pub capabilities: CapabilityDescriptor,
    pub capability_revision: String,
    pub journal: JournalDescriptor,
    pub max_active_runs_per_session: usize,
    pub interactive_gates: bool,
    pub cancellation_target: String,
    pub cancellation_implementation: String,
}

#[derive(Clone, Debug, Default)]
pub struct SubmitRequest {
    pub request: MessageSubmitRequest,
    pub envelope_id: EnvelopeId,
}

#[derive(Clone, Debug, Default)]
pub struct CompactRequest {
    pub request: SessionCompactRequest,
    pub envelope_id: EnvelopeId,
}

#[derive(Clone, Debug, Default)]
pub struct Catalog {
    pub revision: String,
    pub models: ModelsResponse,
}

#[derive(Clone, Debug, Default)]
pub struct ToolCatalog {
    pub revision: String,
    pub tools: ToolsListResponse,
}

#[derive(Clone, Debug, Default)]
pub struct InteractionResolution {
    pub run_id: RunId,
    pub responded_by: ParticipantId,
    pub permission: Option<PermissionResolveRequest>,
    pub input: Option<UserInputResolveRequest>,
}

#[derive(Clone, Debug, Default)]
pub struct CallResolution {
    pub request_id: EnvelopeId,
    pub request: ActionCallResolveRequest,
}

#[derive(Clone, Debug, Default)]
pub struct ResumeRequest {
    pub run_id: RunId,
    pub after_sequence: u64,
}

#[derive(Clone, Debug, Default)]
pub struct Recovery {
    pub state: SessionState,
    pub run_id: RunId,
    pub requested_after: u64,
    pub replayed_from: u64,
    pub replayed_through: u64,
    pub replay_gap: Option<ReplayGap>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct NativeListing {
    pub native_id: String,
    pub title: String,
    pub directory: String,
    pub updated_at_ms: i64,
    pub running: bool,
    pub link: String,
}

#[derive(Clone, Debug, Default)]
pub struct NativeListRequest {
    pub directory: String,
    pub limit: usize,
}

#[derive(Clone, Debug, Default)]
pub struct NativeSearchRequest {
    pub directory: String,
    pub limit: usize,
    pub term: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct NativeTurn {
    pub role: String,
    pub text: String,
    pub at_ms: i64,
}

#[derive(Clone, Debug, Default)]
pub struct NativeReadRequest {
    pub native_id: String,
    pub directory: String,
    pub max_turns: usize,
}

#[derive(Clone, Debug, Default)]
pub struct TranscriptRequest {
    pub native_id: String,
    pub directory: String,
    pub cursor: String,
    pub limit: usize,
}

pub trait Adapter: Send + Sync {
    fn probe(&self) -> BoxFuture<'_, Descriptor>;
    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>>;

    fn admitter(&self) -> Option<&dyn Admitter> {
        None
    }
    fn native_lister(&self) -> Option<&dyn NativeLister> {
        None
    }
    fn native_searcher(&self) -> Option<&dyn NativeSearcher> {
        None
    }
    fn native_reader(&self) -> Option<&dyn NativeReader> {
        None
    }
    fn transcript_loader(&self) -> Option<&dyn TranscriptLoader> {
        None
    }
    fn native_linker(&self) -> Option<&dyn NativeLinker> {
        None
    }
}

pub trait Session: Send + Sync {
    fn submit(&self, request: SubmitRequest)
        -> BoxFuture<'_, (MessageSubmitResponse, EventStream)>;
    fn state(&self) -> BoxFuture<'_, SessionState>;
    fn resolve(&self, resolution: InteractionResolution) -> BoxFuture<'_, ()>;
    fn cancel(&self, run_id: RunId) -> BoxFuture<'_, RunCancelResponse>;
    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)>;
    fn close(&self) -> BoxFuture<'_, ()>;

    fn native_session_id(&self) -> Option<String> {
        None
    }
    fn model_lister(&self) -> Option<&dyn ModelLister> {
        None
    }
    fn model_switcher(&self) -> Option<&dyn ModelSwitcher> {
        None
    }
    fn settings_updater(&self) -> Option<&dyn SettingsUpdater> {
        None
    }
    fn compactor(&self) -> Option<&dyn Compactor> {
        None
    }
    fn tool_lister(&self) -> Option<&dyn ToolLister> {
        None
    }
    fn call_resolver(&self) -> Option<&dyn CallResolver> {
        None
    }
}

pub trait Admitter: Send + Sync {
    fn admit(&self, request: &OpenRequest) -> BoxFuture<'_, ()>;
}

pub trait NativeLister: Send + Sync {
    fn native_list(&self, request: NativeListRequest) -> BoxFuture<'_, Vec<NativeListing>>;
}

pub trait NativeSearcher: Send + Sync {
    fn native_search(&self, request: NativeSearchRequest) -> BoxFuture<'_, Vec<NativeListing>>;
}

pub trait NativeReader: Send + Sync {
    fn native_read(&self, request: NativeReadRequest) -> BoxFuture<'_, Vec<NativeTurn>>;
}

pub trait TranscriptLoader: Send + Sync {
    fn transcript_load(&self, request: TranscriptRequest) -> BoxFuture<'_, TranscriptLoadResponse>;
}

pub trait NativeLinker: Send + Sync {
    fn native_link(&self, native_id: &str) -> String;
}

pub trait ModelLister: Send + Sync {
    fn models(&self, request: ModelsRequest) -> BoxFuture<'_, Catalog>;
}

pub trait ModelSwitcher: Send + Sync {
    fn switch_model(
        &self,
        request: SessionModelSwitchRequest,
    ) -> BoxFuture<'_, (SessionModelSwitchResponse, SessionState)>;
}

pub trait SettingsUpdater: Send + Sync {
    fn update_settings(
        &self,
        request: SessionSettingsUpdateRequest,
    ) -> BoxFuture<'_, (SessionSettingsUpdateResponse, SessionState)>;
}

pub trait Compactor: Send + Sync {
    fn compact(
        &self,
        request: CompactRequest,
    ) -> BoxFuture<'_, (SessionCompactResponse, EventStream)>;
}

pub trait ToolLister: Send + Sync {
    fn tools(&self, request: ToolsListRequest) -> BoxFuture<'_, ToolCatalog>;
}

pub trait CallResolver: Send + Sync {
    fn resolve_call(&self, resolution: CallResolution) -> BoxFuture<'_, ActionCallResolveResponse>;
}
