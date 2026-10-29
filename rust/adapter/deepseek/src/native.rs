use std::sync::LazyLock;

use oap_adapter::gojson::{
    raw, reject_duplicate_keys, replace_lone_surrogates, slice, structure, structures, zero,
};
use serde::de::DeserializeOwned;
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::value::RawValue;

pub const SERVER_NAME: &str = "deepseek-harness-sdk-runtime";
pub const METHOD_INITIALIZE: &str = "initialize";
pub const METHOD_SESSION_PROMPT: &str = "session/prompt";
pub const METHOD_SHUTDOWN: &str = "shutdown";
pub const NOTIFY_SESSION_EVENT: &str = "session.event";
pub const NOTIFY_SESSION_STATUS: &str = "session.status";
pub const NOTIFY_SUBAGENT_STARTED: &str = "subagent.started";
pub const NOTIFY_SUBAGENT_FINISHED: &str = "subagent.finished";

pub static SERVER_VERSION: LazyLock<String> = LazyLock::new(|| {
    oap_harness::current("deepseek-harness")
        .and_then(|version| version.admits.first().cloned())
        .expect("the harness catalog pins deepseek-harness with an admitted server version")
});

const OBSERVED_ONLY_EVENTS: [&str; 47] = [
    "agent-preset/selected",
    "approval/asked",
    "approval/decided",
    "approval/policy",
    "command/done",
    "command/run",
    "compaction/end",
    "compaction/prune",
    "compaction/start",
    "compaction/summary",
    "deliverables/presented",
    "developer/message",
    "feedback/message-delete",
    "feedback/message-put",
    "feedback/record",
    "goal/change",
    "hook/invoked",
    "hook/result",
    "job/registered",
    "job/settled",
    "llm/retry",
    "llm/retry-started",
    "model/selection",
    "permission/preset",
    "plan/mode",
    "sandbox/mode",
    "schedule/change",
    "session-log-deepseek/delivery-accepted",
    "session/title",
    "session/title-llm-request",
    "subagent/catalog",
    "subagent/descriptor",
    "subagent/model-selection-policy",
    "system/message",
    "team/member",
    "team/message/delivered",
    "team/message/queued",
    "team/task",
    "tool-workflow/agent-end",
    "tool-workflow/agent-start",
    "tool-workflow/run-end",
    "tool-workflow/run-start",
    "tool/ptc-dispatch",
    "tool/ptc-dispatch-start",
    "web/deepseek-search-llm-request",
    "working-directory/change",
    "workspace/changes",
];

pub fn observed_only(event_type: &str) -> bool {
    OBSERVED_ONLY_EVENTS.contains(&event_type)
}

type Raw = Option<Box<RawValue>>;

fn none<T>(value: &Option<T>) -> bool {
    value.is_none()
}

fn empty<T>(value: &Option<Vec<T>>) -> bool {
    value.as_ref().is_none_or(Vec::is_empty)
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct InitializeParams {
    #[serde(default, deserialize_with = "zero")]
    pub cwd: String,
    #[serde(default, deserialize_with = "zero")]
    pub provider: String,
    #[serde(default, deserialize_with = "zero")]
    pub model: String,
    #[serde(default, skip_serializing_if = "none")]
    pub max_tokens: Option<i64>,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub reasoning_effort: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ServerInfo {
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(default, deserialize_with = "zero")]
    pub version: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeResult {
    #[serde(default, deserialize_with = "structure")]
    pub server_info: ServerInfo,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SessionPromptParams {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "nullable_blocks")]
    pub content_blocks: Option<Vec<ContentBlock>>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct SessionPromptResult {
    #[serde(default, deserialize_with = "zero")]
    pub message_id: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct ShutdownResult {}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct ContentBlock {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    pub kind: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub text: String,
    #[serde(default, deserialize_with = "raw", skip_serializing_if = "none")]
    pub attachment: Raw,
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
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub arguments: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub tool_call_id: String,
    #[serde(
        default,
        deserialize_with = "nullable_blocks",
        skip_serializing_if = "empty"
    )]
    pub content: Option<Vec<ContentBlock>>,
    #[serde(default, skip_serializing_if = "none")]
    pub is_error: Option<bool>,
    #[serde(default, deserialize_with = "raw", skip_serializing_if = "none")]
    pub offloaded: Raw,
}

fn nullable_blocks<'de, D: Deserializer<'de>>(
    deserializer: D,
) -> Result<Option<Vec<ContentBlock>>, D::Error> {
    let raw = Box::<RawValue>::deserialize(deserializer)?;
    if raw.get() == "null" {
        return Ok(None);
    }
    slice(&raw).map(Some)
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SessionEventNotification {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "structure")]
    pub event: Event,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SessionStatusNotification {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SubagentStartedNotification {
    #[serde(default, deserialize_with = "zero")]
    pub parent_session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub child_session_id: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct SubagentFinishedNotification {
    #[serde(default, deserialize_with = "zero")]
    pub provider: String,
    #[serde(default, deserialize_with = "zero")]
    pub agent_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub parent_session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub child_session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default, deserialize_with = "zero")]
    pub stop_reason: String,
    #[serde(
        default,
        deserialize_with = "structures",
        skip_serializing_if = "Vec::is_empty"
    )]
    pub last_assistant_message: Vec<ContentBlock>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
pub struct Event {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    pub kind: String,
    #[serde(default, deserialize_with = "zero")]
    pub seq: i64,
    #[serde(default, deserialize_with = "zero")]
    pub time: i64,
    #[serde(default, deserialize_with = "raw")]
    pub data: Raw,
    #[serde(default, skip_serializing_if = "none")]
    pub ignorable: Option<bool>,
    #[serde(default, deserialize_with = "seqs", skip_serializing_if = "empty")]
    pub source_event_seqs: Option<Vec<i64>>,
    #[serde(default, deserialize_with = "raw", skip_serializing_if = "none")]
    pub surface_op: Raw,
}

fn seqs<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Option<Vec<i64>>, D::Error> {
    let items = Option::<Vec<Option<i64>>>::deserialize(deserializer)?;
    Ok(items.map(|items| items.into_iter().map(Option::unwrap_or_default).collect()))
}

impl Event {
    pub fn data_bytes(&self) -> &[u8] {
        self.data.as_deref().map_or(&[], |raw| raw.get().as_bytes())
    }

    pub fn data_as<T: DeserializeOwned + Default>(&self) -> Result<T, String> {
        decode_strict(self.data_bytes())
    }
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

fn go_kind(text: &str) -> &'static str {
    match text.as_bytes().first() {
        Some(b'[') => "array",
        Some(b'"') => "string",
        Some(b't' | b'f') => "bool",
        _ => "number",
    }
}

pub fn decode_strict<T: DeserializeOwned + Default>(data: &[u8]) -> Result<T, String> {
    let text = go_lossy(data);
    let value = text.trim_matches(|character| matches!(character, ' ' | '\t' | '\r' | '\n'));
    if value.is_empty() {
        return Err("EOF".into());
    }
    reject_duplicate_keys(text.as_bytes()).map_err(|error| error.to_string())?;
    if value == "null" {
        return Ok(T::default());
    }
    if !value.starts_with('{') {
        let name = std::any::type_name::<T>()
            .rsplit("::")
            .next()
            .unwrap_or_default();
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type native.{name}",
            go_kind(value)
        ));
    }
    serde_json::from_str(&replace_lone_surrogates(&text)).map_err(|error| {
        let text = error.to_string();
        let suffix = format!(" at line {} column {}", error.line(), error.column());
        text.strip_suffix(&suffix)
            .map_or(text.clone(), str::to_owned)
    })
}
