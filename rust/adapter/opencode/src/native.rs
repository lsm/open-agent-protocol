use std::collections::BTreeMap;
use std::fmt;

use oap_adapter::gojson::{go_quote, raw, zero};
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
    #[serde(skip_serializing_if = "String::is_empty")]
    pub delivery: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub resume: Option<bool>,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct AdmittedTime {
    #[serde(default, deserialize_with = "zero")]
    pub created: i64,
}

#[derive(Clone, Debug, Default, Deserialize)]
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
