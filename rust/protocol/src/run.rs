use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::serde_util::{is_zero_i64, raw};
use crate::vocabulary::vocabulary;
use crate::{
    CompactionId, ContentPart, EnvelopeId, FeatureSupport, InteractionId, Message, MessageId,
    ParticipantId, ProtocolError, RecoveryMetadata, RunId, SessionId, SubmissionId, ToolCallId,
    Usage,
};

vocabulary!(RunStatus {
    QUEUED = "queued",
    RUNNING = "running",
    WAITING_FOR_INPUT = "waiting_for_input",
    CANCELLING = "cancelling",
    COMPLETED = "completed",
    FAILED = "failed",
    CANCELLED = "cancelled",
});

vocabulary!(ResolveReason {
    UNKNOWN_INTERACTION = "unknown_interaction",
    WRONG_RESPONDER = "wrong_responder",
    ALREADY_RESOLVED = "already_resolved",
    REPEATED_ACKNOWLEDGEMENT = "repeated_acknowledgement",
    LATE_ACKNOWLEDGEMENT = "late_acknowledgement",
});

vocabulary!(InteractionOutcome {
    RESOLVED = "resolved",
    REJECTED = "rejected",
    CANCELLED = "cancelled",
    FAILED = "failed",
});

vocabulary!(SteerBoundary {
    IMMEDIATE = "immediate",
    TURN = "turn",
    TOOL_RESULT = "tool_result",
    UNKNOWN = "unknown",
});

vocabulary!(CompactionReason {
    REQUESTED = "requested",
    THRESHOLD = "threshold",
    OVERFLOW = "overflow",
});

vocabulary!(CompactionOutcome {
    COMPLETED = "completed",
    FAILED = "failed",
    CANCELLED = "cancelled",
});

pub const SETTLED_BY_OBSERVED: &str = "observed";
pub const SETTLED_BY_INFERRED: &str = "inferred";

pub const TOOL_SOURCE_NATIVE: &str = "native";
pub const TOOL_SOURCE_LOCAL: &str = "local";
pub const TOOL_SOURCE_PROCESS: &str = "process";
pub const TOOL_SOURCE_REMOTE: &str = "remote";
pub const TOOL_SOURCE_HOSTED: &str = "hosted";
pub const TOOL_SOURCE_MCP: &str = "mcp";

pub fn is_tool_source_kind(kind: &str) -> bool {
    [
        TOOL_SOURCE_NATIVE,
        TOOL_SOURCE_LOCAL,
        TOOL_SOURCE_PROCESS,
        TOOL_SOURCE_REMOTE,
        TOOL_SOURCE_HOSTED,
    ]
    .contains(&kind)
}

pub const RESOLVE_ARM_ACKNOWLEDGE: &str = "started";
pub const RESOLVE_ARM_RESULT: &str = "result";
pub const RESOLVE_ARM_ERROR: &str = "error";

const RESOLVE_REASON_LADDER: [ResolveReason; 5] = [
    ResolveReason::UNKNOWN_INTERACTION,
    ResolveReason::WRONG_RESPONDER,
    ResolveReason::ALREADY_RESOLVED,
    ResolveReason::REPEATED_ACKNOWLEDGEMENT,
    ResolveReason::LATE_ACKNOWLEDGEMENT,
];

impl ResolveReason {
    pub fn rank(&self) -> Option<usize> {
        RESOLVE_REASON_LADDER.iter().position(|known| known == self)
    }

    pub fn highest<'a>(
        reasons: impl IntoIterator<Item = &'a ResolveReason>,
    ) -> Option<ResolveReason> {
        reasons
            .into_iter()
            .filter_map(|reason| reason.rank().map(|rank| (rank, reason)))
            .min_by_key(|(rank, _)| *rank)
            .map(|(_, reason)| reason.clone())
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunCancelRequest {
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunCancelResponse {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub accepted: bool,
    pub status: RunStatus,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunStartedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub status: RunStatus,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model_id: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub started_at_ms: i64,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunStatusUpdatedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub status: RunStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub pending_user_input_id: Option<InteractionId>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub updated_at_ms: i64,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ContentDeltaPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message_id: Option<MessageId>,
    pub part: ContentPart,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ContentCompletedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub message_id: MessageId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<Usage>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct RunCompletedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub final_response: Message,
    pub stop_reason: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model_id: String,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub result: Option<Box<RawValue>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<Usage>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub settled_by: String,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunFailedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub error: ProtocolError,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<Usage>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub recovery: Option<RecoveryMetadata>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub settled_by: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunCancelledPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub usage: Option<Usage>,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub duration_ms: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub settled_by: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolDefinition {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(deserialize_with = "raw")]
    pub input_schema: Option<Box<RawValue>>,
    pub execution_owner: ParticipantId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub source: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub features: BTreeMap<String, FeatureSupport>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub annotations: BTreeMap<String, Box<RawValue>>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolSourceDescriptor {
    pub id: String,
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub protocol: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub endpoint: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolsListRequest {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub session_id: Option<SessionId>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
}

impl ToolsListRequest {
    pub fn allows_degraded(&self, key: &str) -> bool {
        self.allow_degraded_features
            .iter()
            .any(|allowed| allowed == key)
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolsListResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub session_id: Option<SessionId>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<ToolSourceDescriptor>,
    pub tools: Vec<ToolDefinition>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ActionCallPayload {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub interaction_id: Option<InteractionId>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub request_id: Option<EnvelopeId>,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub tool_call_id: ToolCallId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub requested_by: Option<ParticipantId>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub responded_by: Option<ParticipantId>,
    pub execution_owner: ParticipantId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub source: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub arguments_json: Option<Box<RawValue>>,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub progress: Option<Box<RawValue>>,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub result: Option<Box<RawValue>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ProtocolError>,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ResolveArmStarted {}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ActionCallResolveRequest {
    pub interaction_id: InteractionId,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub tool_call_id: ToolCallId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub started: Option<ResolveArmStarted>,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub result: Option<Box<RawValue>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ProtocolError>,
}

impl ActionCallResolveRequest {
    pub fn arm(&self) -> Option<&'static str> {
        let arms = [
            self.started.map(|_| RESOLVE_ARM_ACKNOWLEDGE),
            self.result.as_ref().map(|_| RESOLVE_ARM_RESULT),
            self.error.as_ref().map(|_| RESOLVE_ARM_ERROR),
        ];
        match arms.iter().flatten().collect::<Vec<_>>().as_slice() {
            [arm] => Some(arm),
            _ => None,
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ActionCallResolveDetails {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub settlement_id: Option<EnvelopeId>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ActionCallResolveResponse {
    pub interaction_id: InteractionId,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub tool_call_id: ToolCallId,
    pub accepted: bool,
    #[serde(skip_serializing_if = "ResolveReason::is_empty")]
    pub reason: ResolveReason,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub details: Option<ActionCallResolveDetails>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct PermissionChoice {
    pub id: String,
    pub label: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct PermissionRequestedPayload {
    pub interaction_id: InteractionId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<ToolCallId>,
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub choices: Vec<PermissionChoice>,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub arguments_json: Option<Box<RawValue>>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct PermissionResolveRequest {
    pub interaction_id: InteractionId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub choice_id: String,
    pub granted: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(deserialize_with = "raw", skip_serializing_if = "Option::is_none")]
    pub updated_arguments_json: Option<Box<RawValue>>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct PermissionResolveResponse {
    pub interaction_id: InteractionId,
    pub session_id: SessionId,
    pub run_id: RunId,
    pub accepted: bool,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct PermissionResolvedPayload {
    pub interaction_id: InteractionId,
    pub requested_by: ParticipantId,
    pub responded_by: ParticipantId,
    pub session_id: SessionId,
    pub run_id: RunId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<ToolCallId>,
    pub outcome: InteractionOutcome,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub choice_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub granted: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reason: Option<ProtocolError>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunSteerAppliedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub submission_id: SubmissionId,
    pub request_id: EnvelopeId,
    pub message_ids: Vec<MessageId>,
    pub boundary: SteerBoundary,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunSteerDroppedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub submission_id: SubmissionId,
    pub request_id: EnvelopeId,
    pub reason: ProtocolError,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RunCompactionStartedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub compaction_id: CompactionId,
    pub reason: CompactionReason,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub history_tokens: Option<u64>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct RunCompactionEndedPayload {
    pub session_id: SessionId,
    pub run_id: RunId,
    pub compaction_id: CompactionId,
    pub outcome: CompactionOutcome,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub summary: Option<Message>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub history_tokens: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ProtocolError>,
}
