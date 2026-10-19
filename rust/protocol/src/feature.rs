use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::vocabulary::vocabulary;

vocabulary!(SupportLevel {
    NATIVE = "native",
    EMULATED = "emulated",
    DEGRADED = "degraded",
    UNAVAILABLE = "unavailable",
});

pub const LIMIT_MAX_SOURCES: &str = "max_sources";
pub const LIMIT_TRANSPORTS: &str = "transports";

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct FeatureSupport {
    pub level: SupportLevel,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub scope: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub modes: Vec<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub constraints: BTreeMap<String, Box<RawValue>>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub limits: BTreeMap<String, Box<RawValue>>,
}
