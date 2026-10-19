use std::collections::BTreeMap;

use serde::ser::SerializeMap;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use serde_json::value::RawValue;
use serde_json::Value;

use crate::serde_util::{is_false, is_zero_u64, raw};
use crate::vocabulary::vocabulary;
use crate::{MessageId, RunId, SessionId, ToolCallId};

vocabulary!(MessageRole {
    SYSTEM = "system",
    DEVELOPER = "developer",
    USER = "user",
    ASSISTANT = "assistant",
    TOOL = "tool",
});

vocabulary!(ContentPartType {
    TEXT = "text",
    REASONING = "reasoning",
    IMAGE = "image",
    TOOL_CALL = "tool_call",
    TOOL_RESULT = "tool_result",
});

#[derive(Clone, Debug)]
pub struct MessageContent(Box<RawValue>);

impl MessageContent {
    pub fn from_text(text: &str) -> Self {
        Self(serde_json::value::to_raw_value(text).unwrap_or_else(|_| null()))
    }

    pub fn from_parts(parts: &[ContentPart]) -> Self {
        Self(serde_json::value::to_raw_value(parts).unwrap_or_else(|_| null()))
    }

    pub fn text(&self) -> Option<String> {
        serde_json::from_str(self.0.get()).ok()
    }

    pub fn parts(&self) -> Option<Vec<ContentPart>> {
        serde_json::from_str(self.0.get()).ok()
    }

    pub fn raw(&self) -> &RawValue {
        &self.0
    }
}

fn null() -> Box<RawValue> {
    RawValue::NULL.to_owned()
}

impl Default for MessageContent {
    fn default() -> Self {
        Self(null())
    }
}

impl Serialize for MessageContent {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        self.0.serialize(serializer)
    }
}

impl<'de> Deserialize<'de> for MessageContent {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        Box::<RawValue>::deserialize(deserializer).map(Self)
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Message {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<MessageId>,
    pub role: MessageRole,
    pub content: MessageContent,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub metadata: BTreeMap<String, Box<RawValue>>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(default)]
pub struct ContentPart {
    #[serde(rename = "type")]
    pub part_type: ContentPartType,
    pub text: String,
    pub reasoning: String,
    pub image: Option<ImageContent>,
    pub tool_call_id: Option<ToolCallId>,
    pub name: String,
    #[serde(deserialize_with = "raw")]
    pub arguments_json: Option<Box<RawValue>>,
    #[serde(deserialize_with = "raw")]
    pub result: Option<Box<RawValue>>,
    pub is_error: Option<bool>,
    pub carry: String,
}

impl Serialize for ContentPart {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(None)?;
        map.serialize_entry("type", &self.part_type)?;
        if self.part_type == ContentPartType::TEXT {
            map.serialize_entry("text", &self.text)?;
        }
        if self.part_type == ContentPartType::REASONING {
            map.serialize_entry("reasoning", &self.reasoning)?;
        }
        if let Some(image) = &self.image {
            map.serialize_entry("image", image)?;
        }
        if let Some(id) = &self.tool_call_id {
            map.serialize_entry("tool_call_id", id)?;
        }
        if !self.name.is_empty() {
            map.serialize_entry("name", &self.name)?;
        }
        if let Some(arguments) = &self.arguments_json {
            map.serialize_entry("arguments_json", arguments)?;
        }
        if let Some(result) = &self.result {
            map.serialize_entry("result", result)?;
        }
        if let Some(is_error) = self.is_error {
            map.serialize_entry("is_error", &is_error)?;
        }
        if !self.carry.is_empty() {
            map.serialize_entry("carry", &self.carry)?;
        }
        map.end()
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ImageContent {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub data: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub media_type: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Usage {
    #[serde(skip_serializing_if = "is_zero_u64")]
    pub input_tokens: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cache_read_input_tokens: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cache_write_input_tokens: Option<u64>,
    #[serde(skip_serializing_if = "is_zero_u64")]
    pub output_tokens: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub reasoning_tokens: Option<u64>,
    #[serde(skip_serializing_if = "is_zero_u64")]
    pub total_tokens: u64,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ProtocolError {
    pub code: String,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub retriable: Option<bool>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub details: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ErrorResponse {
    pub error: ProtocolError,
}

pub const INTERRUPTED_BY_RESTART: &str = "restart";

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct RecoveryMetadata {
    #[serde(skip_serializing_if = "is_false")]
    pub recovered: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub previous_session_id: Option<SessionId>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub previous_run_id: Option<RunId>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub resume_cursor: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub interrupted_runs: Vec<InterruptedRun>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct InterruptedRun {
    pub run_id: RunId,
    pub cause: String,
    pub settled_by: String,
}
