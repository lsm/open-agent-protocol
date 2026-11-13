use oap_adapter::gojson::zero;
use serde::Deserialize;

use super::*;

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MessageStartPayload {
    #[serde(default, deserialize_with = "zero")]
    pub display_kind: String,
    #[serde(default, deserialize_with = "zero")]
    pub display_text: String,
    #[serde(default, deserialize_with = "zero")]
    pub notification_category: String,
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
    #[serde(default, deserialize_with = "zero")]
    pub turn_id: String,
}

pub const OBSERVED_EVENTS: &[&str] = &[];

pub fn is_run_scoped(_: &str) -> bool {
    false
}

pub fn decode_notification(_: &str, params: &[u8]) -> Result<Event, String> {
    decode_strict(params)
}
