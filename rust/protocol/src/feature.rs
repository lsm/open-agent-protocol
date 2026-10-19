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

pub const LIMIT_MAX_TOOLS: &str = "max_tools";
pub const LIMIT_NAME_PATTERN: &str = "name_pattern";
pub const LIMIT_SCHEMA_DIALECT: &str = "schema_dialect";

pub const FEATURE_MODEL_SELECTION: &str = "run.model_selection";
pub const FEATURE_INSTRUCTIONS: &str = "run.instructions";
pub const FEATURE_DELIVERY_QUEUE: &str = "session.message.delivery.queue";
pub const FEATURE_DELIVERY_STEER: &str = "session.message.delivery.steer";
pub const FEATURE_DELIVERY_BTW: &str = "session.message.delivery.btw";
pub const FEATURE_SESSION_COMPACT: &str = "session.compact";
pub const FEATURE_RUN_COMPACTION: &str = "run.compaction";
pub const FEATURE_TOOL_SELECTION: &str = "run.tool_selection";
pub const FEATURE_STRUCTURED_OUTPUT: &str = "run.structured_output";
pub const FEATURE_TOOLS_LIST: &str = "action.tools.list";
pub const FEATURE_TOOL_SOURCES_ATTACH: &str = "action.tool_sources.attach";
pub const FEATURE_OPEN_SUBSCRIBE: &str = "session.open.subscribe";
pub const FEATURE_OPEN_REOPEN: &str = "session.open.reopen";
pub const FEATURE_SESSION_LIST: &str = "session.list";
pub const FEATURE_SESSION_REASONING: &str = "session.reasoning";
pub const FEATURE_COMPACTION_POLICY: &str = "session.compaction.policy";
pub const FEATURE_TOOLS_PROVIDE: &str = "action.tools.provide";
pub const FEATURE_MODELS_LIST: &str = "models.list";
pub const FEATURE_CONTENT_IMAGE: &str = "content.image";
pub const FEATURE_SESSION_MODEL_SWITCH: &str = "session.model.switch";
pub const FEATURE_PROVIDERS_ATTACH: &str = "action.providers.attach";

pub const MODE_SESSION_OPEN: &str = "session_open";
pub const MODE_REMOTE: &str = "remote";
pub const MODE_SESSION_LIVE: &str = "session_live";

pub const SCOPE_RUN: &str = "run";
pub const SCOPE_SESSION: &str = "session";
pub const SCOPE_RESTART: &str = "restart";

pub const CONSTRAINT_FIXED_RESULT: &str = "fixed_result";

impl FeatureSupport {
    fn limit<T: serde::de::DeserializeOwned>(&self, name: &str) -> Option<T> {
        serde_json::from_str(self.limits.get(name)?.get()).ok()
    }

    pub fn max_tools(&self) -> Option<i64> {
        self.limit::<i64>(LIMIT_MAX_TOOLS)
            .filter(|value| *value >= 1)
    }

    pub fn max_sources(&self) -> Option<i64> {
        self.limit::<i64>(LIMIT_MAX_SOURCES)
            .filter(|value| *value >= 1)
    }

    pub fn name_pattern(&self) -> Option<String> {
        self.limit::<String>(LIMIT_NAME_PATTERN)
            .filter(|value| !value.is_empty())
    }

    pub fn schema_dialect(&self) -> Option<String> {
        self.limit::<String>(LIMIT_SCHEMA_DIALECT)
            .filter(|value| !value.is_empty())
    }

    pub fn transports(&self) -> Option<Vec<String>> {
        let kinds: Vec<String> = self
            .limit::<Vec<String>>(LIMIT_TRANSPORTS)?
            .into_iter()
            .filter(|kind| crate::is_tool_source_kind(kind))
            .collect();
        (!kinds.is_empty()).then_some(kinds)
    }

    pub fn discloses_mode(&self, mode: &str) -> bool {
        self.modes.iter().any(|disclosed| disclosed == mode)
    }
}
