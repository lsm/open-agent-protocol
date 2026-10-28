mod go;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

pub use go::{decode, Decode};
use go::{raw, slice, strings, structure, structures, zero};

pub const METHOD_SESSION_NEW: &str = "session/new";
pub const METHOD_SESSION_LOAD: &str = "session/load";
pub const METHOD_SESSION_RESUME: &str = "session/resume";
pub const METHOD_SESSION_SET_CONFIG_OPTION: &str = "session/set_config_option";
pub const METHOD_SESSION_PROMPT: &str = "session/prompt";
pub const METHOD_SESSION_CANCEL: &str = "session/cancel";
pub const METHOD_SESSION_UPDATE: &str = "session/update";
pub const METHOD_SESSION_REQUEST_PERMISSION: &str = "session/request_permission";

pub const CATEGORY_THOUGHT_LEVEL: &str = "thought_level";

type Raw = Option<Box<RawValue>>;

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionNewParams {
    #[serde(default, deserialize_with = "zero")]
    pub cwd: String,
    #[serde(default, deserialize_with = "structures")]
    pub mcp_servers: Vec<McpServer>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionReopenParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub cwd: String,
    #[serde(default, deserialize_with = "structures")]
    pub mcp_servers: Vec<McpServer>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct McpServer {
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(default, deserialize_with = "zero")]
    pub command: String,
    #[serde(
        default,
        deserialize_with = "strings",
        skip_serializing_if = "Vec::is_empty"
    )]
    pub args: Vec<String>,
    #[serde(
        default,
        deserialize_with = "structures",
        skip_serializing_if = "Vec::is_empty"
    )]
    pub env: Vec<EnvVariable>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
pub struct EnvVariable {
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(default, deserialize_with = "zero")]
    pub value: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionNewResult {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(
        default,
        deserialize_with = "structures",
        skip_serializing_if = "Vec::is_empty"
    )]
    pub config_options: Vec<ConfigOption>,
}

impl Decode for SessionNewResult {
    fn from_null() -> Result<Self, serde_json::Error> {
        Err(serde::de::Error::custom(
            "ACP session response is not an object",
        ))
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConfigOption {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub category: String,
    #[serde(
        default,
        rename = "type",
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub kind: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub current_value: String,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub options: Raw,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ConfigValue {
    #[serde(default, deserialize_with = "zero")]
    pub value: String,
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
}

#[derive(Default, Deserialize)]
struct ConfigEntry {
    #[serde(default, deserialize_with = "zero")]
    value: String,
    #[serde(default, deserialize_with = "zero")]
    name: String,
    #[serde(default, deserialize_with = "structures")]
    options: Vec<ConfigValue>,
}

impl ConfigOption {
    pub fn values(&self) -> Vec<ConfigValue> {
        let Some(options) = self.options.as_deref() else {
            return Vec::new();
        };
        let Ok(flat) = slice::<ConfigEntry, serde_json::Error>(options) else {
            return Vec::new();
        };
        let mut values = Vec::new();
        for entry in flat {
            if !entry.value.is_empty() {
                values.push(ConfigValue {
                    value: entry.value,
                    name: entry.name,
                });
            }
            values.extend(entry.options);
        }
        values
    }
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SetConfigOptionParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub config_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub value: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SetConfigOptionResult {
    #[serde(default, deserialize_with = "structures")]
    pub config_options: Vec<ConfigOption>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ConfigOptionUpdate {
    #[serde(default, deserialize_with = "zero")]
    pub session_update: String,
    #[serde(default, deserialize_with = "structures")]
    pub config_options: Vec<ConfigOption>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
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
    pub data: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub mime_type: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub uri: String,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "structures")]
    pub prompt: Vec<ContentBlock>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PromptResult {
    #[serde(default, deserialize_with = "zero")]
    pub stop_reason: String,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub usage: Raw,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CancelParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionUpdateParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "raw")]
    pub update: Raw,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct UpdateHeader {
    #[serde(default, deserialize_with = "zero")]
    pub session_update: String,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct AgentMessageChunk {
    #[serde(default, deserialize_with = "zero")]
    pub session_update: String,
    #[serde(default, deserialize_with = "structure")]
    pub content: ContentBlock,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub message_id: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolCall {
    #[serde(default, deserialize_with = "zero")]
    pub session_update: String,
    #[serde(default, deserialize_with = "zero")]
    pub tool_call_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub title: String,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub name: Raw,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub kind: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub status: String,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub raw_input: Raw,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub raw_output: Raw,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub content: Raw,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub locations: Raw,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ToolCallUpdate {
    #[serde(default, deserialize_with = "zero")]
    pub session_update: String,
    #[serde(default, deserialize_with = "zero")]
    pub tool_call_id: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub name: Raw,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub kind: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub status: Option<String>,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub raw_input: Raw,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub raw_output: Raw,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub content: Raw,
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub locations: Raw,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PermissionRequest {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "structure")]
    pub tool_call: ToolCall,
    #[serde(default, deserialize_with = "structures")]
    pub options: Vec<PermissionOption>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PermissionOption {
    #[serde(default, deserialize_with = "zero")]
    pub option_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(default, deserialize_with = "zero")]
    pub kind: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct PermissionResponse {
    #[serde(default, deserialize_with = "structure")]
    pub outcome: PermissionOutcome,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct PermissionOutcome {
    #[serde(default, deserialize_with = "zero")]
    pub outcome: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub option_id: String,
}

impl Decode for SessionNewParams {}
impl Decode for SessionReopenParams {}
impl Decode for SetConfigOptionParams {}
impl Decode for SetConfigOptionResult {}
impl Decode for ConfigOptionUpdate {}
impl Decode for PromptParams {}
impl Decode for PromptResult {}
impl Decode for CancelParams {}
impl Decode for SessionUpdateParams {}
impl Decode for UpdateHeader {}
impl Decode for AgentMessageChunk {}
impl Decode for ToolCall {}
impl Decode for ToolCallUpdate {}
impl Decode for PermissionRequest {}
impl Decode for PermissionResponse {}
