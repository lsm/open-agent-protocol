use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::serde_util::{is_false, raw};
use crate::vocabulary::vocabulary;
use crate::{
    EffectiveDeliveryMode, Message, MessageId, RequestedDeliveryMode, RunId, RunStatus, SessionId,
    SubmissionId,
};

vocabulary!(Admission {
    STARTED = "started",
    QUEUED = "queued",
    STEERED = "steered",
    SIDE_STARTED = "side_started",
    REJECTED = "rejected",
});

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct MessageSubmitRequest {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_run_id: Option<RunId>,
    pub session_id: SessionId,
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

impl MessageSubmitRequest {
    pub fn allows_degraded(&self, key: &str) -> bool {
        self.allow_degraded_features
            .iter()
            .any(|allowed| allowed == key)
    }

    pub fn tool_choice_policy(&self) -> Result<Option<ToolChoice>, ToolChoiceError> {
        let Some(raw) = &self.tool_choice else {
            return Ok(None);
        };
        let value: serde_json::Value = serde_json::from_str(raw.get())
            .map_err(|err| ToolChoiceError::NotThePolicy(err.to_string()))?;
        let members = match value {
            serde_json::Value::Null => return Err(ToolChoiceError::Null),
            serde_json::Value::Object(members) => members,
            other => return Err(ToolChoiceError::NotThePolicy(format!("got {other}"))),
        };
        if let Some(unknown) = members
            .keys()
            .find(|key| *key != "allowed" && *key != "disallowed")
        {
            return Err(ToolChoiceError::NotThePolicy(format!(
                "unknown field {unknown:?}"
            )));
        }
        let (allowed, disallowed) = (members.get("allowed"), members.get("disallowed"));
        match (allowed, disallowed) {
            (None, None) => return Err(ToolChoiceError::Neither),
            (Some(_), Some(_)) => return Err(ToolChoiceError::Both),
            _ => {}
        }
        let names = |name: &'static str, member: Option<&serde_json::Value>| match member {
            None => Ok(None),
            Some(serde_json::Value::Null) => Err(ToolChoiceError::NullList(name)),
            Some(list) => serde_json::from_value::<Vec<String>>(list.clone())
                .map(Some)
                .map_err(|err| ToolChoiceError::NotThePolicy(err.to_string())),
        };
        Ok(Some(ToolChoice {
            allowed: names("allowed", allowed)?,
            disallowed: names("disallowed", disallowed)?,
        }))
    }
}

#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
pub enum ToolChoiceError {
    #[error("tool_choice is null, which is not the typed policy")]
    Null,
    #[error("tool_choice is not the typed policy: {0}")]
    NotThePolicy(String),
    #[error("tool_choice carries neither allowed nor disallowed")]
    Neither,
    #[error("tool_choice allowed and disallowed are mutually exclusive")]
    Both,
    #[error("tool_choice {0} is null, which is not a list of tool names")]
    NullList(&'static str),
}

fn absent_or_empty(names: &Option<Vec<String>>) -> bool {
    names.as_ref().is_none_or(Vec::is_empty)
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolChoice {
    #[serde(skip_serializing_if = "absent_or_empty")]
    pub allowed: Option<Vec<String>>,
    #[serde(skip_serializing_if = "absent_or_empty")]
    pub disallowed: Option<Vec<String>>,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ToolChoiceDefect {
    pub pointer: String,
    pub tool: String,
    pub reason: &'static str,
}

impl ToolChoice {
    pub fn unsatisfiable(&self, catalog: &[String], known: bool) -> Option<ToolChoiceDefect> {
        if !known {
            return None;
        }
        self.allowed
            .iter()
            .flatten()
            .enumerate()
            .find_map(|(index, name)| {
                (!catalog.contains(name)).then(|| ToolChoiceDefect {
                    pointer: format!("/payload/tool_choice/allowed/{index}"),
                    tool: name.clone(),
                    reason: "allowed names a tool outside the catalog",
                })
            })
    }

    pub fn filter(&self, catalog: &[String]) -> Vec<String> {
        catalog
            .iter()
            .filter(|name| self.lets(name))
            .cloned()
            .collect()
    }

    pub fn permits(&self, name: &str, catalog: &[String], known: bool) -> bool {
        if known && !catalog.iter().any(|listed| listed == name) {
            return false;
        }
        self.lets(name)
    }

    fn lets(&self, name: &str) -> bool {
        let allowed = self
            .allowed
            .as_ref()
            .is_none_or(|list| list.iter().any(|n| n == name));
        let disallowed = self.disallowed.iter().flatten().any(|n| n == name);
        allowed && !disallowed
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct MessageSubmitResponse {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_sequence: Option<u64>,
    pub session_id: SessionId,
    pub accepted: bool,
    pub submission_id: SubmissionId,
    pub requested_delivery: RequestedDeliveryMode,
    pub effective_delivery: EffectiveDeliveryMode,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub delivery_resolution: String,
    pub admission: Admission,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub run_id: Option<RunId>,
    #[serde(skip_serializing_if = "RunStatus::is_empty")]
    pub status: RunStatus,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model_id: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub message_ids: Vec<MessageId>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionCompactRequest {
    pub session_id: SessionId,
    #[serde(skip_serializing_if = "RequestedDeliveryMode::is_empty")]
    pub delivery: RequestedDeliveryMode,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub focus: Option<String>,
    #[serde(rename = "continue", skip_serializing_if = "is_false")]
    pub continue_after: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub metadata: BTreeMap<String, Box<RawValue>>,
}

impl SessionCompactRequest {
    pub fn allows_degraded(&self, key: &str) -> bool {
        self.allow_degraded_features
            .iter()
            .any(|allowed| allowed == key)
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SessionCompactResponse {
    pub session_id: SessionId,
    pub accepted: bool,
    pub submission_id: SubmissionId,
    pub requested_delivery: RequestedDeliveryMode,
    pub effective_delivery: EffectiveDeliveryMode,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub delivery_resolution: String,
    pub admission: Admission,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub run_id: Option<RunId>,
    #[serde(skip_serializing_if = "RunStatus::is_empty")]
    pub status: RunStatus,
}
