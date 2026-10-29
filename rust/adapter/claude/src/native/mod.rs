mod decode;

use oap_adapter::gojson::{raw, slice, strings, structure, zero};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

pub use decode::{decode_observation, Invalid, Observation};

pub type Raw = Option<Box<RawValue>>;

pub const TYPE_USER: &str = "user";
pub const TYPE_ASSISTANT: &str = "assistant";
pub const TYPE_SYSTEM: &str = "system";
pub const TYPE_RESULT: &str = "result";
pub const TYPE_STREAM_EVENT: &str = "stream_event";
pub const TYPE_TOOL_PROGRESS: &str = "tool_progress";
pub const TYPE_COMMAND_LIFECYCLE: &str = "command_lifecycle";
pub const TYPE_KEEP_ALIVE: &str = "keep_alive";
pub const TYPE_CONVERSATION: &str = "conversation_reset";
pub const TYPE_RATE_LIMIT: &str = "rate_limit_event";

pub const SYSTEM_INIT: &str = "init";
pub const SYSTEM_STATUS: &str = "status";
pub const SYSTEM_SESSION_STATE: &str = "session_state_changed";
pub const SYSTEM_TASK_STARTED: &str = "task_started";
pub const SYSTEM_TASK_PROGRESS: &str = "task_progress";
pub const SYSTEM_TASK_NOTIFICATION: &str = "task_notification";
pub const SYSTEM_TASK_UPDATED: &str = "task_updated";
pub const SYSTEM_PERMISSION_DENIED: &str = "permission_denied";
pub const SYSTEM_INFORMATIONAL: &str = "informational";
pub const SYSTEM_COMPACT_BOUNDARY: &str = "compact_boundary";
pub const SYSTEM_BACKGROUND_CHANGED: &str = "background_tasks_changed";

pub const RESULT_SUCCESS: &str = "success";
pub const RESULT_ERROR_DURING_EXECUTION: &str = "error_during_execution";
pub const RESULT_ERROR_MAX_TURNS: &str = "error_max_turns";
pub const RESULT_ERROR_MAX_BUDGET_USD: &str = "error_max_budget_usd";
pub const RESULT_ERROR_STRUCTURED_OUTPUT: &str = "error_max_structured_output_retries";

pub const TERMINAL_ABORTED_STREAMING: &str = "aborted_streaming";
pub const TERMINAL_ABORTED_TOOLS: &str = "aborted_tools";
pub const TERMINAL_COMPLETED: &str = "completed";
pub const TERMINAL_MAX_TURNS: &str = "max_turns";

pub const COMMAND_QUEUED: &str = "queued";
pub const COMMAND_STARTED: &str = "started";
pub const COMMAND_COMPLETED: &str = "completed";
pub const COMMAND_CANCELLED: &str = "cancelled";

pub const CONTROL_INITIALIZE: &str = "initialize";
pub const CONTROL_APPLY_FLAGS: &str = "apply_flag_settings";
pub const CONTROL_INTERRUPT: &str = "interrupt";
pub const CONTROL_CAN_USE_TOOL: &str = "can_use_tool";
pub const CONTROL_HOOK_CALLBACK: &str = "hook_callback";
pub const CONTROL_MCP_MESSAGE: &str = "mcp_message";

pub const HOOK_PRE_TOOL_USE: &str = "PreToolUse";
pub const TOOL_SELECTION_HOOK: &str = "oap_tool_selection";

pub const STATUS_COMPACTING: &str = "compacting";
pub const COMPACT_RESULT_SUCCESS: &str = "success";
pub const COMPACT_RESULT_FAILED: &str = "failed";

pub fn terminal_task_status(status: &str) -> bool {
    matches!(status, "completed" | "failed" | "stopped" | "killed")
}

fn empty(value: &Raw) -> bool {
    value.is_none()
}

fn optional_blocks<'de, D>(deserializer: D) -> Result<Option<Vec<ContentBlock>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let raw = Box::<RawValue>::deserialize(deserializer)?;
    if raw.get() == "null" {
        return Ok(None);
    }
    slice(&raw).map(Some)
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct Origin {
    #[serde(default, deserialize_with = "zero")]
    pub kind: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub from: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct ContentBlock {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    pub kind: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub text: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub thinking: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub id: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub name: String,
    #[serde(default, deserialize_with = "raw", skip_serializing_if = "empty")]
    pub input: Raw,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub tool_use_id: String,
    #[serde(default, deserialize_with = "raw", skip_serializing_if = "empty")]
    pub content: Raw,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub is_error: Option<bool>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct UserMessage {
    #[serde(default, deserialize_with = "zero")]
    pub role: String,
    #[serde(default, deserialize_with = "raw")]
    pub content: Raw,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct UserFrame {
    #[serde(default, deserialize_with = "structure")]
    pub message: UserMessage,
    #[serde(default)]
    pub parent_tool_use_id: Option<String>,
    #[serde(default, deserialize_with = "raw")]
    pub tool_use_result: Raw,
    #[serde(default)]
    pub origin: Option<Origin>,
    #[serde(default, deserialize_with = "zero")]
    pub uuid: String,
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
}

impl UserFrame {
    pub fn blocks(&self) -> Option<Vec<ContentBlock>> {
        slice::<ContentBlock, serde_json::Error>(self.message.content.as_deref()?).ok()
    }

    pub fn text_content(&self) -> Option<String> {
        let raw = self.message.content.as_deref()?;
        serde_json::from_str::<Option<String>>(raw.get())
            .ok()
            .map(Option::unwrap_or_default)
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct AssistantMessage {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
    #[serde(default, deserialize_with = "optional_blocks")]
    pub content: Option<Vec<ContentBlock>>,
    #[serde(default)]
    pub stop_reason: Option<String>,
    #[serde(default, deserialize_with = "raw")]
    pub usage: Raw,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct AssistantFrame {
    #[serde(default, deserialize_with = "structure")]
    pub message: AssistantMessage,
    #[serde(default)]
    pub parent_tool_use_id: Option<String>,
    #[serde(default, deserialize_with = "zero")]
    pub error: String,
    #[serde(default, deserialize_with = "zero")]
    pub user_message_uuid: String,
    #[serde(default, deserialize_with = "strings")]
    pub user_message_uuids: Vec<String>,
    #[serde(default, deserialize_with = "zero")]
    pub is_api_error_message: bool,
    #[serde(default, deserialize_with = "zero")]
    pub uuid: String,
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
}
