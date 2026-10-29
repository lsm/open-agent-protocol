use std::collections::BTreeMap;

use oap_adapter::gojson::{raw, slice, structure, structures, zero};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use serde_json::Value;

pub type Raw = Option<Box<RawValue>>;

pub const METHOD_THREAD_START: &str = "thread/start";
pub const METHOD_THREAD_RESUME: &str = "thread/resume";
pub const METHOD_TURN_START: &str = "turn/start";
pub const METHOD_TURN_INTERRUPT: &str = "turn/interrupt";
pub const METHOD_TURN_STARTED: &str = "turn/started";
pub const METHOD_TURN_COMPLETED: &str = "turn/completed";
pub const METHOD_ITEM_STARTED: &str = "item/started";
pub const METHOD_ITEM_COMPLETED: &str = "item/completed";
pub const METHOD_AGENT_DELTA: &str = "item/agentMessage/delta";
pub const METHOD_COMMAND_APPROVAL: &str = "item/commandExecution/requestApproval";
pub const METHOD_FILE_APPROVAL: &str = "item/fileChange/requestApproval";
pub const METHOD_USER_INPUT: &str = "item/tool/requestUserInput";
pub const METHOD_TOKEN_USAGE: &str = "thread/tokenUsage/updated";

pub const TURN_COMPLETED: &str = "completed";
pub const TURN_INTERRUPTED: &str = "interrupted";
pub const TURN_FAILED: &str = "failed";
pub const TURN_IN_PROGRESS: &str = "inProgress";

fn none(value: &Raw) -> bool {
    value.is_none()
}

fn optional_structures<'de, D, T>(deserializer: D) -> Result<Option<Vec<T>>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: serde::de::DeserializeOwned + Default,
{
    let raw = Box::<RawValue>::deserialize(deserializer)?;
    if raw.get() == "null" {
        return Ok(None);
    }
    slice(&raw).map(Some)
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ThreadTokenUsageUpdatedNotification {
    #[serde(default, rename = "threadId", deserialize_with = "zero")]
    pub thread_id: String,
    #[serde(default, rename = "turnId", deserialize_with = "zero")]
    pub turn_id: String,
    #[serde(default, rename = "tokenUsage", deserialize_with = "structure")]
    pub token_usage: ThreadTokenUsage,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ThreadTokenUsage {
    #[serde(default, deserialize_with = "structure")]
    pub total: TokenUsageBreakdown,
    #[serde(default, deserialize_with = "structure")]
    pub last: TokenUsageBreakdown,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Deserialize)]
pub struct TokenUsageBreakdown {
    #[serde(default, rename = "totalTokens", deserialize_with = "zero")]
    pub total_tokens: i64,
    #[serde(default, rename = "inputTokens", deserialize_with = "zero")]
    pub input_tokens: i64,
    #[serde(default, rename = "cachedInputTokens", deserialize_with = "zero")]
    pub cached_input_tokens: i64,
    #[serde(default, rename = "outputTokens", deserialize_with = "zero")]
    pub output_tokens: i64,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Thread {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Turn {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default)]
    pub error: Option<TurnError>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct TurnError {
    #[serde(default, deserialize_with = "zero")]
    pub message: String,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct ThreadStartParams {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub cwd: String,
    #[serde(rename = "approvalPolicy", skip_serializing_if = "String::is_empty")]
    pub approval_policy: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub sandbox: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub config: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ThreadStartResponse {
    #[serde(default, deserialize_with = "structure")]
    pub thread: Thread,
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct ThreadResumeParams {
    #[serde(rename = "threadId")]
    pub thread_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub cwd: String,
    #[serde(rename = "approvalPolicy", skip_serializing_if = "String::is_empty")]
    pub approval_policy: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub sandbox: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub config: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ThreadResumeResponse {
    #[serde(default, deserialize_with = "structure")]
    pub thread: Thread,
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
    #[serde(default, rename = "modelProvider", deserialize_with = "zero")]
    pub model_provider: String,
    #[serde(default, deserialize_with = "zero")]
    pub cwd: String,
    #[serde(default, rename = "approvalPolicy", deserialize_with = "raw")]
    pub approval_policy: Raw,
    #[serde(default, rename = "approvalsReviewer", deserialize_with = "raw")]
    pub approvals_reviewer: Raw,
    #[serde(default, deserialize_with = "raw")]
    pub sandbox: Raw,
    #[serde(default, rename = "reasoningEffort", deserialize_with = "zero")]
    pub reasoning_effort: String,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct UserInput {
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub text: String,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct TurnStartParams {
    #[serde(rename = "threadId")]
    pub thread_id: String,
    pub input: Vec<UserInput>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub effort: String,
    #[serde(rename = "outputSchema", skip_serializing_if = "none")]
    pub output_schema: Raw,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct TurnStartResponse {
    #[serde(default, deserialize_with = "structure")]
    pub turn: Turn,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct TurnInterruptParams {
    #[serde(rename = "threadId")]
    pub thread_id: String,
    #[serde(rename = "turnId")]
    pub turn_id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct TurnNotification {
    #[serde(default, rename = "threadId", deserialize_with = "zero")]
    pub thread_id: String,
    #[serde(default, deserialize_with = "structure")]
    pub turn: Turn,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct AgentMessageDeltaNotification {
    #[serde(default, rename = "threadId", deserialize_with = "zero")]
    pub thread_id: String,
    #[serde(default, rename = "turnId", deserialize_with = "zero")]
    pub turn_id: String,
    #[serde(default, rename = "itemId", deserialize_with = "zero")]
    pub item_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub delta: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ItemNotification {
    #[serde(default, rename = "threadId", deserialize_with = "zero")]
    pub thread_id: String,
    #[serde(default, rename = "turnId", deserialize_with = "zero")]
    pub turn_id: String,
    #[serde(default, deserialize_with = "structure")]
    pub item: Item,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct Item {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    pub kind: String,
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
    #[serde(default, deserialize_with = "zero")]
    pub command: String,
    #[serde(default, deserialize_with = "zero")]
    pub cwd: String,
    #[serde(default, deserialize_with = "zero")]
    pub path: String,
    #[serde(default, deserialize_with = "optional_structures")]
    pub changes: Option<Vec<FileChange>>,
    #[serde(default, deserialize_with = "zero")]
    pub server: String,
    #[serde(default, deserialize_with = "zero")]
    pub tool: String,
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default, deserialize_with = "raw")]
    pub arguments: Raw,
    #[serde(default, rename = "aggregatedOutput")]
    pub output: Option<String>,
    #[serde(default, rename = "exitCode")]
    pub exit_code: Option<i64>,
    #[serde(default, rename = "durationMs")]
    pub duration_ms: Option<i64>,
    #[serde(default, deserialize_with = "raw")]
    pub result: Raw,
    #[serde(default)]
    pub error: Option<ToolError>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct FileChange {
    #[serde(default, deserialize_with = "zero")]
    pub path: String,
    #[serde(default, deserialize_with = "raw")]
    pub kind: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub diff: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ToolError {
    #[serde(default, deserialize_with = "zero")]
    pub message: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct CommandApprovalParams {
    #[serde(default, rename = "threadId", deserialize_with = "zero")]
    pub thread_id: String,
    #[serde(default, rename = "turnId", deserialize_with = "zero")]
    pub turn_id: String,
    #[serde(default, rename = "itemId", deserialize_with = "zero")]
    pub item_id: String,
    #[serde(default)]
    pub reason: Option<String>,
    #[serde(default, rename = "availableDecisions", deserialize_with = "raw")]
    pub available_decisions: Raw,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct FileApprovalParams {
    #[serde(default, rename = "threadId", deserialize_with = "zero")]
    pub thread_id: String,
    #[serde(default, rename = "turnId", deserialize_with = "zero")]
    pub turn_id: String,
    #[serde(default, rename = "itemId", deserialize_with = "zero")]
    pub item_id: String,
    #[serde(default)]
    pub reason: Option<String>,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct ApprovalResponse {
    pub decision: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct UserInputRequestParams {
    #[serde(default, rename = "threadId", deserialize_with = "zero")]
    pub thread_id: String,
    #[serde(default, rename = "turnId", deserialize_with = "zero")]
    pub turn_id: String,
    #[serde(default, rename = "itemId", deserialize_with = "zero")]
    pub item_id: String,
    #[serde(default, deserialize_with = "structures")]
    pub questions: Vec<UserInputQuestion>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct UserInputQuestion {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, deserialize_with = "zero")]
    pub header: String,
    #[serde(default, deserialize_with = "zero")]
    pub question: String,
    #[serde(default, rename = "isOther", deserialize_with = "zero")]
    pub is_other: bool,
    #[serde(default, rename = "isSecret", deserialize_with = "zero")]
    pub is_secret: bool,
    #[serde(default, deserialize_with = "optional_structures")]
    pub options: Option<Vec<UserInputOption>>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct UserInputOption {
    #[serde(default, deserialize_with = "zero")]
    pub label: String,
    #[serde(default, deserialize_with = "zero")]
    pub description: String,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct UserInputResponse {
    pub answers: BTreeMap<String, UserInputAnswer>,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct UserInputAnswer {
    pub answers: Vec<String>,
}
