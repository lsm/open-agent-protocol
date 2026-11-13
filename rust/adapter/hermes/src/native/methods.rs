use oap_adapter::gojson::{raw, zero};
use serde::{Deserialize, Deserializer, Serialize};

use super::Raw;

#[derive(Clone, Debug, Default, Serialize)]
pub struct SessionCreateParams {
    pub title: String,
    pub cwd: String,
    pub model: String,
    pub reasoning_effort: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SessionCreateResult {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub stored_session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub message_count: i64,
    #[serde(default, deserialize_with = "raw")]
    pub messages: Raw,
    #[serde(default, deserialize_with = "raw")]
    pub info: Raw,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct SessionResumeParams {
    pub session_id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SessionResumeResult {
    #[serde(default, deserialize_with = "zero")]
    pub session_id: String,
    #[serde(default, deserialize_with = "zero")]
    pub resumed: String,
    #[serde(default, deserialize_with = "zero")]
    pub message_count: i64,
    #[serde(default, deserialize_with = "raw")]
    pub info: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub running: bool,
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default, deserialize_with = "raw")]
    pub auto_continue: Raw,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct ConfigSetParams {
    pub session_id: String,
    pub key: String,
    pub value: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConfigSetResult {
    #[serde(default, deserialize_with = "zero")]
    pub key: String,
    #[serde(default, deserialize_with = "zero")]
    pub value: String,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct PromptSubmitParams {
    pub session_id: String,
    pub text: String,
}

fn row_ids<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Vec<Option<i64>>, D::Error> {
    Option::<Vec<Option<i64>>>::deserialize(deserializer).map(Option::unwrap_or_default)
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PromptSubmitResult {
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default, deserialize_with = "row_ids")]
    pub survivor_user_row_ids: Vec<Option<i64>>,
    #[serde(default, deserialize_with = "raw")]
    pub survivor_row_id_map: Raw,
    #[serde(default, deserialize_with = "zero")]
    pub voice_stopped: bool,
    #[serde(default, deserialize_with = "zero")]
    pub turn_isolation: bool,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct InterruptParams {
    pub session_id: String,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InterruptResult {
    #[serde(default, deserialize_with = "zero")]
    pub status: String,
    #[serde(default, deserialize_with = "zero")]
    pub interrupted: bool,
    #[serde(default, deserialize_with = "zero")]
    pub turn_isolation: bool,
}
