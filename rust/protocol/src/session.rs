use std::collections::BTreeMap;

use serde::ser::SerializeStruct;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use serde_json::value::RawValue;

use crate::serde_util::{is_false, is_zero_i64, raw};
use crate::vocabulary::vocabulary;
use crate::{
    CompactionPolicy, EnvelopeId, InteractionId, Message, MessageSubmitRequest, ReasoningLevel,
    RecoveryMetadata, RequestedDeliveryMode, RunId, RunStatus, SessionId, SubmissionId,
    ToolDefinition, ToolSourceDescriptor,
};

vocabulary!(SessionStatus {
    IDLE = "idle",
    QUEUED = "queued",
    RUNNING = "running",
    WAITING_FOR_INPUT = "waiting_for_input",
    CLOSED = "closed",
    ERROR = "error",
});

vocabulary!(ShutdownMode {
    GRACEFUL = "graceful",
    FORCE = "force",
});

vocabulary!(ShutdownOutcome {
    CHOOSE = "choose",
    DRAINING = "draining",
    STOPPING = "stopping",
});

vocabulary!(SessionListState {
    LIVE = "live",
    CLOSED = "closed",
});

pub const SESSION_LIST_DEFAULT_LIMIT: i64 = 50;
pub const RELATIONSHIP_PRIMARY: &str = "primary";

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolSourceAttachment {
    pub id: String,
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub protocol: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub endpoint: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub command: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub args: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub environment: Vec<String>,
}

impl ToolSourceAttachment {
    pub fn descriptor(&self) -> ToolSourceDescriptor {
        ToolSourceDescriptor {
            id: self.id.clone(),
            kind: self.kind.clone(),
            display_name: self.display_name.clone(),
            protocol: self.protocol.clone(),
            endpoint: self.endpoint.clone(),
        }
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct OpenMessage {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_run_id: Option<RunId>,
    pub messages: Vec<Message>,
    pub delivery: RequestedDeliveryMode,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub instructions: Option<String>,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub tool_choice: Option<Box<RawValue>>,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub output_schema: Option<Box<RawValue>>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub metadata: BTreeMap<String, Box<RawValue>>,
}

impl OpenMessage {
    pub fn submit(&self, session_id: SessionId) -> MessageSubmitRequest {
        MessageSubmitRequest {
            target_run_id: self.target_run_id.clone(),
            session_id,
            messages: self.messages.clone(),
            delivery: self.delivery.clone(),
            model_id: self.model_id.clone(),
            instructions: self.instructions.clone(),
            tool_choice: self.tool_choice.clone(),
            output_schema: self.output_schema.clone(),
            allow_degraded_features: self.allow_degraded_features.clone(),
            metadata: self.metadata.clone(),
        }
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionOpenRequest {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub session_id: Option<SessionId>,
    #[serde(skip_serializing_if = "is_false")]
    pub subscribe: bool,
    #[serde(skip_serializing_if = "is_false")]
    pub reopen: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<OpenMessage>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub metadata: BTreeMap<String, Box<RawValue>>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tool_sources: Vec<ToolSourceAttachment>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<ToolDefinition>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub recovery: Option<RecoveryMetadata>,
    #[serde(skip_serializing_if = "ReasoningLevel::is_empty")]
    pub reasoning_level: ReasoningLevel,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub compaction_policy: Option<CompactionPolicy>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionStateRequest {
    pub session_id: SessionId,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct EndpointShutdownRequest {
    #[serde(skip_serializing_if = "ShutdownMode::is_empty")]
    pub mode: ShutdownMode,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ShutdownRun {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub status: RunStatus,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub pending_interactions: Vec<InteractionId>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ShutdownQueuedRun {
    pub session_id: SessionId,
    pub run_id: RunId,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct EndpointShutdownResponse {
    pub outcome: ShutdownOutcome,
    pub active_runs: Vec<ShutdownRun>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub held: Vec<ShutdownQueuedRun>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub lost: Vec<ShutdownQueuedRun>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct EndpointShutdownStarted {
    pub mode: ShutdownMode,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub held: Vec<ShutdownQueuedRun>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub lost: Vec<ShutdownQueuedRun>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionListRequest {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub cursor: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub limit: i64,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionListEntry {
    pub session_id: SessionId,
    pub adapter: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub harness_version: String,
    pub state: SessionListState,
    pub updated_at_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub directory: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionListResponse {
    pub sessions: Vec<SessionListEntry>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub next_cursor: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionModelSwitchRequest {
    pub session_id: SessionId,
    pub model_id: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionModelSwitchResponse {
    pub session_id: SessionId,
    pub model_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub previous_model_id: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionSettingsUpdateRequest {
    pub session_id: SessionId,
    #[serde(skip_serializing_if = "ReasoningLevel::is_empty")]
    pub reasoning_level: ReasoningLevel,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub compaction_policy: Option<CompactionPolicy>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionSettingsUpdateResponse {
    pub session_id: SessionId,
    #[serde(skip_serializing_if = "ReasoningLevel::is_empty")]
    pub reasoning_level: ReasoningLevel,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub compaction_policy: Option<CompactionPolicy>,
    #[serde(skip_serializing_if = "ReasoningLevel::is_empty")]
    pub previous_reasoning_level: ReasoningLevel,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub previous_compaction_policy: Option<CompactionPolicy>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ProviderAttachment {
    pub id: String,
    pub provider_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub service_id: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionProviderAttachRequest {
    pub session_id: SessionId,
    pub provider: ProviderAttachment,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionProviderAttachResponse {
    pub session_id: SessionId,
    pub provider_id: String,
}

macro_rules! allows_degraded {
    ($($request:ty),+) => {$(
        impl $request {
            pub fn allows_degraded(&self, key: &str) -> bool {
                self.allow_degraded_features.iter().any(|allowed| allowed == key)
            }
        }
    )+};
}

allows_degraded!(
    SessionOpenRequest,
    SessionListRequest,
    SessionModelSwitchRequest,
    SessionSettingsUpdateRequest,
    SessionProviderAttachRequest
);

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct PendingSteer {
    pub submission_id: SubmissionId,
    pub request_id: EnvelopeId,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ActiveRun {
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub pending_steers: Vec<PendingSteer>,
    pub run_id: RunId,
    pub status: RunStatus,
    pub relationship: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub queue_position: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub as_of_sequence: Option<u64>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub admitted_submit_requests: Vec<EnvelopeId>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub pending_interactions: Vec<InteractionId>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub acknowledged_interactions: Vec<InteractionId>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SettledRun {
    pub run_id: RunId,
    pub sequence: u64,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct RunPosition {
    pub run_id: Option<RunId>,
    pub sequence: u64,
}

impl RunPosition {
    pub fn genesis(&self) -> bool {
        self.run_id.as_ref().is_none_or(|id| id.as_str().is_empty()) && self.sequence == 0
    }
}

impl Serialize for RunPosition {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut position = serializer.serialize_struct("RunPosition", 2)?;
        position.serialize_field(
            "run_id",
            &self.run_id.as_ref().filter(|id| !id.as_str().is_empty()),
        )?;
        position.serialize_field("sequence", &self.sequence)?;
        position.end()
    }
}

impl<'de> Deserialize<'de> for RunPosition {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        #[derive(Deserialize)]
        struct Wire {
            #[serde(default)]
            run_id: Option<RunId>,
            #[serde(default)]
            sequence: u64,
        }
        let wire = Wire::deserialize(deserializer)?;
        Ok(Self {
            run_id: wire.run_id,
            sequence: wire.sequence,
        })
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionCapture {
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub admitted_submit_requests: Vec<EnvelopeId>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub settled: Vec<SettledRun>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub model_run_sequence: Option<RunPosition>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionState {
    pub session_id: SessionId,
    pub status: SessionStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub active_run_id: Option<RunId>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub active_runs: Vec<ActiveRun>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub current_model_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub transcript_cursor: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub updated_at_ms: i64,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub metadata: BTreeMap<String, Box<RawValue>>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<ToolSourceDescriptor>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub recovery: Option<RecoveryMetadata>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub as_of: Option<SessionCapture>,
    #[serde(skip_serializing_if = "ReasoningLevel::is_empty")]
    pub reasoning_level: ReasoningLevel,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub compaction_policy: Option<CompactionPolicy>,
}

pub type SessionOpenResponse = SessionState;
pub type SessionStateResponse = SessionState;
pub type SessionStateUpdated = SessionState;
