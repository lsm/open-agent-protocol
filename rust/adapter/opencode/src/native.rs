use std::collections::BTreeMap;
use std::fmt;

use oap_adapter::gojson::{go_quote, raw, reject_duplicate_keys, replace_lone_surrogates, zero};
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

pub type Raw = Option<Box<RawValue>>;

fn suffix_ok(rest: &str) -> bool {
    !rest.is_empty()
        && rest
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || byte == b'_' || byte == b'-')
}

pub fn valid_session_id(id: &str) -> bool {
    id.strip_prefix("ses").is_some_and(suffix_ok)
}

pub fn valid_message_id(id: &str) -> bool {
    id.strip_prefix("msg_").is_some_and(suffix_ok)
}

pub fn valid_event_id(id: &str) -> bool {
    id.strip_prefix("evt_").is_some_and(suffix_ok)
}

pub fn valid_delivery(delivery: &str) -> bool {
    matches!(delivery, "steer" | "queue")
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Error {
    InvalidWire(String),
    UnsupportedType(String),
    Api(ApiError),
    Transport(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidWire(detail) => {
                write!(f, "opencode native: invalid wire payload: {detail}")
            }
            Self::UnsupportedType(kind) => write!(
                f,
                "opencode native: unsupported event type: unsupported event type {}",
                go_quote(kind)
            ),
            Self::Api(error) => error.fmt(f),
            Self::Transport(text) => f.write_str(text),
        }
    }
}

impl std::error::Error for Error {}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct ApiError {
    pub status: u16,
    pub tag: String,
    pub fields: BTreeMap<String, String>,
}

impl fmt::Display for ApiError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.tag.is_empty() {
            write!(f, "opencode native: HTTP {}", self.status)
        } else {
            write!(f, "opencode native: HTTP {} {}", self.status, self.tag)
        }
    }
}

impl ApiError {
    pub fn is_session_not_found(&self) -> bool {
        self.tag == "SessionNotFoundError"
    }

    pub fn is_conflict(&self) -> bool {
        self.tag == "ConflictError" || self.tag == "PromptConflictError"
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ModelRef {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, rename = "providerID", deserialize_with = "zero")]
    pub provider_id: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub variant: String,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct CacheAccounting {
    #[serde(default, deserialize_with = "zero")]
    pub read: f64,
    #[serde(default, deserialize_with = "zero")]
    pub write: f64,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TokenAccounting {
    #[serde(default, deserialize_with = "zero")]
    pub input: f64,
    #[serde(default, deserialize_with = "zero")]
    pub output: f64,
    #[serde(default, deserialize_with = "zero")]
    pub reasoning: f64,
    #[serde(default, deserialize_with = "oap_adapter::gojson::structure")]
    pub cache: CacheAccounting,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct PromptRequest {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub id: String,
    pub text: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub files: Raw,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub agents: Raw,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub delivery: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub resume: Option<bool>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AdmittedTime {
    #[serde(default, deserialize_with = "zero")]
    pub created: i64,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Admitted {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, rename = "sessionID", deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "oap_adapter::gojson::structure")]
    pub time: AdmittedTime,
    #[serde(default, rename = "type", deserialize_with = "zero")]
    pub kind: String,
    #[serde(default, deserialize_with = "raw")]
    pub payload: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub delivery: String,
}

impl Admitted {
    pub fn validate(&self) -> Result<(), Error> {
        if !valid_message_id(&self.id)
            || !valid_session_id(&self.session_id)
            || !valid_delivery(&self.delivery)
            || self.kind != "user"
            || self.time.created < 0
        {
            return Err(Error::InvalidWire("invalid admitted receipt".into()));
        }
        Ok(())
    }
}

#[derive(Clone, Copy, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SessionTime {
    #[serde(default, deserialize_with = "zero")]
    pub created: i64,
    #[serde(default, deserialize_with = "zero")]
    pub updated: i64,
    #[serde(default)]
    pub idle: Option<i64>,
    #[serde(default)]
    pub viewed: Option<i64>,
    #[serde(default)]
    pub archived: Option<i64>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SessionInfo {
    #[serde(default, deserialize_with = "zero")]
    pub id: String,
    #[serde(default, rename = "parentID", deserialize_with = "zero")]
    pub parent_id: String,
    #[serde(default, deserialize_with = "raw")]
    pub fork: Raw,
    #[serde(default, rename = "projectID", deserialize_with = "zero")]
    pub project_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub outcome: String,
    #[serde(default, deserialize_with = "zero")]
    pub agent: String,
    #[serde(default)]
    pub model: Option<ModelRef>,
    #[serde(default, deserialize_with = "zero")]
    pub cost: f64,
    #[serde(default, deserialize_with = "oap_adapter::gojson::structure")]
    pub tokens: TokenAccounting,
    #[serde(default, deserialize_with = "oap_adapter::gojson::structure")]
    pub time: SessionTime,
    #[serde(default, deserialize_with = "zero")]
    pub title: String,
    #[serde(default, deserialize_with = "raw")]
    pub location: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub subpath: String,
    #[serde(default, deserialize_with = "raw")]
    pub revert: Raw,
    #[serde(default, deserialize_with = "raw")]
    pub metadata: Raw,
    #[serde(default, deserialize_with = "raw")]
    pub permissions: Raw,
}

impl SessionInfo {
    pub fn validate(&self) -> Result<(), Error> {
        if !valid_session_id(&self.id)
            || self.project_id.is_empty()
            || self.time.created < 0
            || self.time.updated < 0
        {
            return Err(Error::InvalidWire("invalid session info".into()));
        }
        Ok(())
    }

    pub fn directory(&self) -> String {
        #[derive(Default, Deserialize)]
        struct Location {
            #[serde(default, deserialize_with = "zero")]
            directory: String,
        }
        self.location
            .as_deref()
            .and_then(|raw| serde_json::from_str::<Location>(raw.get()).ok())
            .map(|location| location.directory)
            .unwrap_or_default()
    }
}

pub fn decode_api_error(status: u16, body: &[u8]) -> ApiError {
    #[derive(Default, Deserialize)]
    struct Tagged {
        #[serde(default, rename = "_tag")]
        tag: Option<String>,
    }
    let tag = serde_json::from_slice::<Tagged>(body)
        .ok()
        .and_then(|tagged| tagged.tag)
        .unwrap_or_default();
    let fields = serde_json::from_slice::<BTreeMap<String, Box<RawValue>>>(body)
        .map(|fields| {
            fields
                .into_iter()
                .filter(|(key, _)| key != "_tag")
                .map(|(key, value)| (key, value.get().to_owned()))
                .collect()
        })
        .unwrap_or_default();
    ApiError {
        status,
        tag,
        fields,
    }
}

pub const TYPE_INBOX_ENQUEUED: &str = "session.inbox.enqueued";
pub const TYPE_INBOX_DELIVERED: &str = "session.inbox.delivered";
pub const TYPE_INBOX_CANCELLED: &str = "session.inbox.cancelled";
pub const TYPE_INBOX_DELIVERY_CHANGED: &str = "session.inbox.delivery.changed";
pub const TYPE_EXECUTION_STARTED: &str = "session.execution.started";
pub const TYPE_EXECUTION_SUCCEEDED: &str = "session.execution.succeeded";
pub const TYPE_EXECUTION_FAILED: &str = "session.execution.failed";
pub const TYPE_EXECUTION_INTERRUPTED: &str = "session.execution.interrupted";
pub const TYPE_STEP_STARTED: &str = "session.step.started";
pub const TYPE_STEP_ENDED: &str = "session.step.ended";
pub const TYPE_STEP_FAILED: &str = "session.step.failed";
pub const TYPE_TEXT_DELTA: &str = "session.text.delta";
pub const TYPE_TEXT_ENDED: &str = "session.text.ended";
pub const TYPE_REASONING_DELTA: &str = "session.reasoning.delta";
pub const TYPE_REASONING_ENDED: &str = "session.reasoning.ended";
pub const TYPE_TOOL_INPUT_STARTED: &str = "session.tool.input.started";
pub const TYPE_TOOL_CALLED: &str = "session.tool.called";
pub const TYPE_TOOL_PROGRESS: &str = "session.tool.progress";
pub const TYPE_TOOL_SUCCESS: &str = "session.tool.success";
pub const TYPE_TOOL_FAILED: &str = "session.tool.failed";
pub const TYPE_PERMISSION_ASKED: &str = "permission.asked";
pub const TYPE_PERMISSION_REPLIED: &str = "permission.replied";

const SUPPORTED: &[&str] = &[
    "session.inbox.enqueued",
    "session.inbox.delivered",
    "session.inbox.cancelled",
    "session.inbox.delivery.changed",
    "session.execution.started",
    "session.execution.succeeded",
    "session.execution.failed",
    "session.execution.interrupted",
    "session.step.started",
    "session.step.streamed",
    "session.step.ended",
    "session.step.failed",
    "session.text.started",
    "session.text.delta",
    "session.text.ended",
    "session.reasoning.started",
    "session.reasoning.delta",
    "session.reasoning.ended",
    "session.tool.input.started",
    "session.tool.input.delta",
    "session.tool.input.ended",
    "session.tool.called",
    "session.tool.progress",
    "session.tool.success",
    "session.tool.failed",
    "session.created",
    "session.agent.selected",
    "session.model.selected",
    "session.moved",
    "session.renamed",
    "session.metadata.updated",
    "session.permissions",
    "session.viewed",
    "session.usage.updated",
    "session.deleted",
    "session.forked",
    "session.instructions.updated",
    "session.synthetic",
    "session.skill.activated",
    "session.shell.started",
    "session.shell.ended",
    "session.retry.scheduled",
    "session.compaction.started",
    "session.compaction.delta",
    "session.compaction.ended",
    "session.compaction.failed",
    "session.revert.staged",
    "session.revert.cleared",
    "session.revert.committed",
    "permission.asked",
    "permission.replied",
];

pub fn supported(kind: &str) -> bool {
    SUPPORTED.contains(&kind)
}

pub fn durable(kind: &str) -> bool {
    !matches!(
        kind,
        "session.text.delta"
            | "session.reasoning.delta"
            | "session.tool.input.delta"
            | "session.tool.progress"
            | "session.compaction.delta"
            | "session.usage.updated"
            | "permission.asked"
            | "permission.replied"
    ) && supported(kind)
}

pub fn session_scoped(kind: &str) -> bool {
    kind.starts_with("session.") || kind == TYPE_PERMISSION_ASKED || kind == TYPE_PERMISSION_REPLIED
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct DurablePosition {
    #[serde(default, rename = "aggregateID", deserialize_with = "zero")]
    pub aggregate_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub seq: i64,
    #[serde(default, deserialize_with = "zero")]
    pub version: i64,
}

#[derive(Clone, Debug, Default)]
pub struct Event {
    pub id: String,
    pub created: i64,
    pub kind: String,
    pub location: Raw,
    pub metadata: Raw,
    pub durable: Option<DurablePosition>,
    pub data: String,
    pub session_id: String,
}

pub fn decode_strict<T: DeserializeOwned>(data: &str) -> Result<T, String> {
    reject_duplicate_keys(data.as_bytes()).map_err(|error| error.to_string())?;
    serde_json::from_str(&replace_lone_surrogates(data)).map_err(|error| error.to_string())
}

pub fn decode_event(data: &str) -> Result<Event, Error> {
    #[derive(Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Envelope {
        #[serde(default, deserialize_with = "zero")]
        id: String,
        #[serde(default, deserialize_with = "zero")]
        created: i64,
        #[serde(default, rename = "type", deserialize_with = "zero")]
        kind: String,
        #[serde(default, deserialize_with = "raw")]
        location: Raw,
        #[serde(default, deserialize_with = "raw")]
        metadata: Raw,
        #[serde(default)]
        durable: Option<DurablePosition>,
        #[serde(default, deserialize_with = "raw")]
        data: Raw,
    }
    let envelope: Envelope =
        decode_strict(data).map_err(|error| Error::InvalidWire(format!("envelope: {error}")))?;
    if !valid_event_id(&envelope.id) {
        return Err(Error::InvalidWire("invalid event id".into()));
    }
    let mut event = Event {
        id: envelope.id,
        created: envelope.created,
        kind: envelope.kind.clone(),
        location: envelope.location,
        metadata: envelope.metadata,
        durable: envelope.durable.clone(),
        data: envelope
            .data
            .as_deref()
            .map_or_else(String::new, |raw| raw.get().to_owned()),
        session_id: String::new(),
    };
    if !session_scoped(&envelope.kind) {
        return Ok(event);
    }
    #[derive(Deserialize)]
    struct Scope {
        #[serde(default, rename = "sessionID", deserialize_with = "zero")]
        session_id: String,
    }
    let scope = serde_json::from_str::<Scope>(if event.data.is_empty() {
        "null"
    } else {
        &event.data
    })
    .ok()
    .filter(|scope| valid_session_id(&scope.session_id))
    .ok_or_else(|| Error::InvalidWire(format!("{} without a session id", envelope.kind)))?;
    event.session_id = scope.session_id.clone();
    if !supported(&envelope.kind) {
        return Err(Error::UnsupportedType(envelope.kind));
    }
    if durable(&envelope.kind) {
        if envelope
            .durable
            .as_ref()
            .is_none_or(|position| position.aggregate_id != scope.session_id || position.seq < 0)
        {
            return Err(Error::InvalidWire(format!(
                "{} without its durable position",
                envelope.kind
            )));
        }
    } else if envelope.durable.is_some() {
        return Err(Error::InvalidWire(format!(
            "ephemeral {} with a durable position",
            envelope.kind
        )));
    }
    Ok(event)
}

pub fn decode_data<T: DeserializeOwned>(event: &Event) -> Result<T, Error> {
    decode_strict(if event.data.is_empty() {
        "null"
    } else {
        &event.data
    })
    .map_err(|error| Error::InvalidWire(format!("{} data: {error}", event.kind)))
}
