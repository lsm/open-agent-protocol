use std::fmt;

use oap_adapter::gojson::{go_quote, raw, reject_duplicate_keys, zero};
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

pub type Raw = Option<Box<RawValue>>;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Invalid(pub String);

impl fmt::Display for Invalid {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "pi native: invalid message: {}", self.0)
    }
}

impl std::error::Error for Invalid {}

fn invalid(detail: impl Into<String>) -> Invalid {
    Invalid(detail.into())
}

pub const COMMAND_PROMPT: &str = "prompt";
pub const COMMAND_STEER: &str = "steer";
pub const COMMAND_FOLLOW_UP: &str = "follow_up";
pub const COMMAND_ABORT: &str = "abort";
pub const COMMAND_NEW_SESSION: &str = "new_session";
pub const COMMAND_GET_STATE: &str = "get_state";
pub const COMMAND_SET_MODEL: &str = "set_model";
pub const COMMAND_SET_THINKING_LEVEL: &str = "set_thinking_level";
pub const COMMAND_SET_STEERING_MODE: &str = "set_steering_mode";
pub const COMMAND_SET_FOLLOW_UP_MODE: &str = "set_follow_up_mode";
pub const COMMAND_COMPACT: &str = "compact";
pub const COMMAND_SET_AUTO_COMPACTION: &str = "set_auto_compaction";
pub const COMMAND_SET_AUTO_RETRY: &str = "set_auto_retry";
pub const COMMAND_BASH: &str = "bash";
pub const COMMAND_EXPORT_HTML: &str = "export_html";
pub const COMMAND_SWITCH_SESSION: &str = "switch_session";
pub const COMMAND_FORK: &str = "fork";
pub const COMMAND_GET_ENTRIES: &str = "get_entries";
pub const COMMAND_SET_SESSION_NAME: &str = "set_session_name";
pub const COMMAND_GET_MESSAGES: &str = "get_messages";

const COMMANDS: &[&str] = &[
    "prompt",
    "steer",
    "follow_up",
    "abort",
    "clear_queue",
    "new_session",
    "get_state",
    "set_model",
    "cycle_model",
    "get_available_models",
    "set_thinking_level",
    "cycle_thinking_level",
    "get_available_thinking_levels",
    "set_steering_mode",
    "set_follow_up_mode",
    "compact",
    "set_auto_compaction",
    "set_auto_retry",
    "abort_retry",
    "bash",
    "abort_bash",
    "get_session_stats",
    "export_html",
    "switch_session",
    "fork",
    "clone",
    "get_fork_messages",
    "get_entries",
    "get_tree",
    "get_last_assistant_text",
    "set_session_name",
    "get_messages",
    "get_commands",
];

pub fn valid_command_type(kind: &str) -> bool {
    COMMANDS.contains(&kind)
}

pub fn valid_thinking(level: &str) -> bool {
    matches!(
        level,
        "off" | "minimal" | "low" | "medium" | "high" | "xhigh" | "max"
    )
}

fn valid_mode(mode: &str) -> bool {
    matches!(mode, "all" | "one-at-a-time")
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ImageContent {
    #[serde(rename = "type")]
    pub kind: String,
    pub data: String,
    #[serde(rename = "mimeType")]
    pub mime_type: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Command {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub id: String,
    #[serde(rename = "type")]
    pub kind: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub images: Vec<ImageContent>,
    #[serde(rename = "streamingBehavior", skip_serializing_if = "String::is_empty")]
    pub streaming_behavior: String,
    #[serde(rename = "parentSession", skip_serializing_if = "Option::is_none")]
    pub parent_session: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub provider: Option<String>,
    #[serde(rename = "modelId", skip_serializing_if = "Option::is_none")]
    pub model_id: Option<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub level: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mode: String,
    #[serde(rename = "customInstructions", skip_serializing_if = "Option::is_none")]
    pub custom_instructions: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub enabled: Option<bool>,
    #[serde(rename = "command", skip_serializing_if = "Option::is_none")]
    pub bash_command: Option<String>,
    #[serde(rename = "excludeFromContext", skip_serializing_if = "Option::is_none")]
    pub exclude_from_context: Option<bool>,
    #[serde(rename = "outputPath", skip_serializing_if = "Option::is_none")]
    pub output_path: Option<String>,
    #[serde(rename = "sessionPath", skip_serializing_if = "Option::is_none")]
    pub session_path: Option<String>,
    #[serde(rename = "entryId", skip_serializing_if = "Option::is_none")]
    pub entry_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub since: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
}

impl Command {
    pub fn of(kind: &str) -> Self {
        Self {
            kind: kind.into(),
            ..Self::default()
        }
    }

    pub fn validate(&self) -> Result<(), Invalid> {
        let kind = self.kind.as_str();
        if !valid_command_type(kind) {
            return Err(invalid(format!("unknown command type {}", go_quote(kind))));
        }
        self.validate_fields()?;
        let require = |value: &Option<String>, name: &str| {
            if value.is_none() {
                Err(invalid(format!("{kind} requires {name}")))
            } else {
                Ok(())
            }
        };
        match kind {
            "prompt" | "steer" | "follow_up" => require(&self.message, "message"),
            "set_model" => {
                require(&self.provider, "provider")?;
                require(&self.model_id, "modelId")
            }
            "set_thinking_level" if !valid_thinking(&self.level) => Err(invalid(format!(
                "invalid thinking level {}",
                go_quote(&self.level)
            ))),
            "set_steering_mode" | "set_follow_up_mode" if !valid_mode(&self.mode) => Err(invalid(
                format!("invalid queue mode {}", go_quote(&self.mode)),
            )),
            "set_auto_compaction" | "set_auto_retry" if self.enabled.is_none() => {
                Err(invalid(format!("{kind} requires enabled")))
            }
            "bash" => require(&self.bash_command, "command"),
            "switch_session" => require(&self.session_path, "sessionPath"),
            "fork" => require(&self.entry_id, "entryId"),
            "set_session_name" => require(&self.name, "name"),
            _ => Ok(()),
        }
    }

    fn validate_fields(&self) -> Result<(), Invalid> {
        let extra: &[&str] = match self.kind.as_str() {
            "prompt" => &["message", "images", "streamingBehavior"],
            "steer" | "follow_up" => &["message", "images"],
            "new_session" => &["parentSession"],
            "set_model" => &["provider", "modelId"],
            "set_thinking_level" => &["level"],
            "set_steering_mode" | "set_follow_up_mode" => &["mode"],
            "compact" => &["customInstructions"],
            "set_auto_compaction" | "set_auto_retry" => &["enabled"],
            "bash" => &["command", "excludeFromContext"],
            "export_html" => &["outputPath"],
            "switch_session" => &["sessionPath"],
            "fork" => &["entryId"],
            "get_entries" => &["since"],
            "set_session_name" => &["name"],
            _ => &[],
        };
        let value = serde_json::to_value(self).map_err(|error| invalid(error.to_string()))?;
        for name in value
            .as_object()
            .into_iter()
            .flat_map(|object| object.keys())
        {
            if name != "id" && name != "type" && !extra.contains(&name.as_str()) {
                return Err(invalid(format!(
                    "field {name} is not valid for {}",
                    self.kind
                )));
            }
        }
        Ok(())
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Response {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, rename = "type", deserialize_with = "zero")]
    pub kind: String,
    #[serde(default, deserialize_with = "zero")]
    pub command: String,
    #[serde(default, deserialize_with = "zero")]
    pub success: bool,
    #[serde(default, deserialize_with = "raw")]
    pub data: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub error: String,
}

impl Response {
    pub fn validate(&self) -> Result<(), Invalid> {
        if self.kind != "response" || !valid_command_type(&self.command) {
            return Err(invalid("invalid response discriminator"));
        }
        if self.success && !self.error.is_empty() {
            return Err(invalid("successful response has error"));
        }
        if !self.success && self.error.is_empty() {
            return Err(invalid("failed response requires error"));
        }
        Ok(())
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SessionState {
    #[serde(
        default,
        deserialize_with = "raw",
        skip_serializing_if = "Option::is_none"
    )]
    pub model: Raw,
    #[serde(default, rename = "thinkingLevel", deserialize_with = "zero")]
    pub thinking_level: String,
    #[serde(default, rename = "isStreaming", deserialize_with = "zero")]
    pub is_streaming: bool,
    #[serde(default, rename = "isCompacting", deserialize_with = "zero")]
    pub is_compacting: bool,
    #[serde(default, rename = "steeringMode", deserialize_with = "zero")]
    pub steering_mode: String,
    #[serde(default, rename = "followUpMode", deserialize_with = "zero")]
    pub follow_up_mode: String,
    #[serde(
        default,
        rename = "sessionFile",
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub session_file: String,
    #[serde(default, rename = "sessionId", deserialize_with = "zero")]
    pub session_id: String,
    #[serde(
        default,
        rename = "sessionName",
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub session_name: String,
    #[serde(default, rename = "autoCompactionEnabled", deserialize_with = "zero")]
    pub auto_compaction_enabled: bool,
    #[serde(default, rename = "messageCount", deserialize_with = "zero")]
    pub message_count: i64,
    #[serde(default, rename = "pendingMessageCount", deserialize_with = "zero")]
    pub pending_message_count: i64,
}

pub fn validate_state(state: &SessionState) -> Result<(), String> {
    if state.session_id.is_empty() || state.message_count < 0 || state.pending_message_count < 0 {
        return Err("missing identity or negative counts".into());
    }
    if !valid_mode(&state.steering_mode) || !valid_mode(&state.follow_up_mode) {
        return Err("invalid queue mode".into());
    }
    if !valid_thinking(&state.thinking_level) {
        return Err("invalid thinking level".into());
    }
    Ok(())
}

pub fn decode_strict<T: DeserializeOwned>(data: &str) -> Result<T, String> {
    reject_duplicate_keys(data.as_bytes()).map_err(|error| error.to_string())?;
    serde_json::from_str(data).map_err(|error| error.to_string())
}
