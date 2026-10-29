use oap_adapter::gojson::{raw, zero};
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::value::RawValue;

pub const NOTIFY_EVENT: &str = "event";
pub const METHOD_SESSION_CREATE: &str = "session.create";
pub const METHOD_SESSION_RESUME: &str = "session.resume";
pub const METHOD_CONFIG_SET: &str = "config.set";
pub const METHOD_SESSION_CLOSE: &str = "session.close";
pub const METHOD_SESSION_STEER: &str = "session.steer";
pub const METHOD_SESSION_INTERRUPT: &str = "session.interrupt";
pub const METHOD_SESSION_EVENTS_SINCE: &str = "session.events.since";
pub const METHOD_SESSION_EVENTS_STATS: &str = "session.events.stats";
pub const METHOD_PROMPT_SUBMIT: &str = "prompt.submit";
pub const METHOD_PROMPT_BTW: &str = "prompt.btw";
pub const METHOD_PROMPT_BACKGROUND: &str = "prompt.background";
pub const METHOD_SUBAGENT_STEER: &str = "subagent.steer";
pub const METHOD_SUBAGENT_INTERRUPT: &str = "subagent.interrupt";

pub const REQUEST_APPROVAL: &str = "approval";
pub const REQUEST_CLARIFY: &str = "clarify";
pub const REQUEST_SUDO: &str = "sudo";
pub const REQUEST_SECRET: &str = "secret";

pub const SUBMIT_STREAMING: &str = "streaming";
pub const SUBMIT_STEERED: &str = "steered";
pub const SUBMIT_REDIRECTED: &str = "redirected";
pub const SUBMIT_QUEUED: &str = "queued";

pub const EVENT_GATEWAY_READY: &str = "gateway.ready";
pub const EVENT_MESSAGE_START: &str = "message.start";
pub const EVENT_MESSAGE_DELTA: &str = "message.delta";
pub const EVENT_REASONING_DELTA: &str = "reasoning.delta";
pub const EVENT_THINKING_DELTA: &str = "thinking.delta";
pub const EVENT_MESSAGE_INTERIM: &str = "message.interim";
pub const EVENT_MESSAGE_COMPLETE: &str = "message.complete";
pub const EVENT_ERROR: &str = "error";
pub const EVENT_TOOL_START: &str = "tool.start";
pub const EVENT_TOOL_COMPLETE: &str = "tool.complete";
pub const EVENT_SESSION_USAGE: &str = "session.usage";
pub const EVENT_SESSION_INFO: &str = "session.info";
pub const EVENT_REQUEST_CANCEL: &str = "request.cancel";
pub const EVENT_SUBAGENT_COMPLETE: &str = "subagent.complete";
pub const EVENT_BTW_COMPLETE: &str = "btw.complete";
pub const EVENT_BACKGROUND_COMPLETE: &str = "background.complete";

type Raw = Option<Box<RawValue>>;

fn none<T>(value: &Option<T>) -> bool {
    value.is_none()
}

fn go_kind(text: &str) -> &'static str {
    match text.as_bytes().first() {
        Some(b'[') => "array",
        Some(b'"') => "string",
        Some(b't' | b'f') => "bool",
        _ => "number",
    }
}

fn args_in<'de, D: Deserializer<'de>>(deserializer: D, owner: &str) -> Result<Raw, D::Error> {
    let value = Box::<RawValue>::deserialize(deserializer)?;
    let text = value.get().trim_start();
    if text.starts_with('{') || text == "null" {
        return Ok(Some(value));
    }
    Err(serde::de::Error::custom(format!(
        "json: cannot unmarshal {} into Go struct field {owner}.args of type map[string]interface {{}}",
        go_kind(text)
    )))
}

fn start_args<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Raw, D::Error> {
    args_in(deserializer, "ToolStartPayload")
}

fn complete_args<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Raw, D::Error> {
    args_in(deserializer, "ToolCompletePayload")
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Event {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    pub kind: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero", skip_serializing_if = "is_zero")]
    pub seq: i64,
    #[serde(default, deserialize_with = "raw", skip_serializing_if = "none")]
    pub payload: Raw,
}

fn is_zero(value: &i64) -> bool {
    *value == 0
}

impl Event {
    pub fn payload_bytes(&self) -> &[u8] {
        self.payload
            .as_deref()
            .map_or(&[], |raw| raw.get().as_bytes())
    }
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ReadyPayload {
    #[serde(default, deserialize_with = "raw")]
    pub skin: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub change_events: bool,
    #[serde(default, deserialize_with = "zero")]
    pub replay_epoch: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DeltaPayload {
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
    #[serde(default, deserialize_with = "zero")]
    pub rendered: String,
    #[serde(default, deserialize_with = "zero")]
    pub verbose: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InterimPayload {
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
    #[serde(default, deserialize_with = "zero")]
    pub already_streamed: bool,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MessageCompletePayload {
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
    #[serde(default)]
    pub usage: Option<Usage>,
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default, deserialize_with = "zero")]
    pub reasoning: String,
    #[serde(default, deserialize_with = "zero")]
    pub warning: String,
    #[serde(default, deserialize_with = "zero")]
    pub response_previewed: bool,
    #[serde(default)]
    pub response_reused: Option<bool>,
    #[serde(default)]
    pub response_transformed: Option<bool>,
    #[serde(default, deserialize_with = "raw")]
    pub billing: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub failure_reason: String,
    #[serde(default, deserialize_with = "zero")]
    pub rendered: String,
    #[serde(default, deserialize_with = "zero")]
    pub error: String,
    #[serde(default, deserialize_with = "zero")]
    pub recoverable: bool,
    #[serde(default)]
    pub error_surface: Option<ErrorSurface>,
    #[serde(default, deserialize_with = "zero")]
    pub partial: bool,
    #[serde(default, deserialize_with = "raw")]
    pub persisted_turn: Raw,
}

#[derive(Clone, Debug, Default, PartialEq, Deserialize)]
pub struct ErrorSurface {
    #[serde(default, deserialize_with = "zero")]
    pub layer: String,
    #[serde(default, deserialize_with = "zero")]
    pub code: String,
    #[serde(default, deserialize_with = "zero")]
    pub retryable: bool,
    #[serde(default, deserialize_with = "zero")]
    pub provider: String,
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
    #[serde(default)]
    pub resets_at: Option<f64>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Deserialize)]
pub struct Usage {
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
    #[serde(default)]
    pub input: Option<i64>,
    #[serde(default)]
    pub output: Option<i64>,
    #[serde(default, deserialize_with = "zero")]
    pub reasoning: i64,
    #[serde(default, deserialize_with = "zero")]
    pub prompt: i64,
    #[serde(default, deserialize_with = "zero")]
    pub completion: i64,
    #[serde(default)]
    pub total: Option<i64>,
    #[serde(default, deserialize_with = "zero")]
    pub calls: i64,
    #[serde(default)]
    pub context_used: Option<i64>,
    #[serde(default)]
    pub context_max: Option<i64>,
    #[serde(default)]
    pub context_percent: Option<i64>,
    #[serde(default)]
    pub compressions: Option<i64>,
    #[serde(default)]
    pub active_subagents: Option<i64>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct UsageTickPayload {
    #[serde(default, deserialize_with = "oap_adapter::gojson::structure")]
    pub usage: Usage,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ToolStartPayload {
    #[serde(default, deserialize_with = "zero")]
    pub tool_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(default, deserialize_with = "zero")]
    pub context: String,
    #[serde(default, deserialize_with = "start_args")]
    pub args: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub args_text: String,
    #[serde(default, deserialize_with = "zero")]
    pub preview: String,
    #[serde(default, deserialize_with = "raw")]
    pub labels: Raw,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ToolCompletePayload {
    #[serde(default, deserialize_with = "zero")]
    pub tool_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(default, deserialize_with = "complete_args")]
    pub args: Raw,
    #[serde(default)]
    pub duration_s: Option<f64>,
    #[serde(default, deserialize_with = "raw")]
    pub result: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub summary: String,
    #[serde(default, deserialize_with = "zero")]
    pub result_text: String,
    #[serde(default, deserialize_with = "zero")]
    pub inline_diff: String,
    #[serde(default, deserialize_with = "raw")]
    pub todos: Raw,
    #[serde(default)]
    pub revision: Option<i64>,
    #[serde(default, deserialize_with = "raw")]
    pub labels: Raw,
}

fn go_lossy(data: &[u8]) -> String {
    let mut text = String::with_capacity(data.len());
    let mut rest = data;
    loop {
        match std::str::from_utf8(rest) {
            Ok(valid) => {
                text.push_str(valid);
                return text;
            }
            Err(error) => {
                let (valid, after) = rest.split_at(error.valid_up_to());
                text.push_str(std::str::from_utf8(valid).unwrap_or_default());
                let invalid = error.error_len().unwrap_or(after.len());
                text.extend(std::iter::repeat_n('\u{fffd}', invalid));
                rest = &after[invalid..];
            }
        }
    }
}

pub fn decode_strict<T: serde::de::DeserializeOwned + Default>(data: &[u8]) -> Result<T, String> {
    let text = go_lossy(data);
    let value = text.trim_matches(|character| matches!(character, ' ' | '\t' | '\r' | '\n'));
    if value.is_empty() {
        return Err("EOF".into());
    }
    if value == "null" {
        return Ok(T::default());
    }
    serde_json::from_str(&oap_adapter::gojson::replace_lone_surrogates(&text)).map_err(|error| {
        let text = error.to_string();
        let suffix = format!(" at line {} column {}", error.line(), error.column());
        text.strip_suffix(&suffix)
            .map_or(text.clone(), str::to_owned)
    })
}
