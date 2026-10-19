use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

use crate::serde_util::{is_false, is_zero_i64, is_zero_u64};
use crate::vocabulary::vocabulary;
use crate::{
    EndpointId, EnvelopeId, FeatureSupport, ParticipantId, RunId, SessionId, SupportLevel,
    ToolDefinition, ToolSourceDescriptor,
};

vocabulary!(RequestedDeliveryMode {
    AUTO = "auto",
    QUEUE = "queue",
    STEER = "steer",
    BTW = "btw",
});

vocabulary!(EffectiveDeliveryMode {
    START = "start",
    QUEUE = "queue",
    STEER = "steer",
    BTW = "btw",
});

impl RequestedDeliveryMode {
    pub fn feature_key(&self) -> String {
        format!("session.message.delivery.{}", self.as_str())
    }
}

vocabulary!(ReasoningLevel {
    OFF = "off",
    MINIMAL = "minimal",
    LOW = "low",
    MEDIUM = "medium",
    HIGH = "high",
    XHIGH = "xhigh",
    MAX = "max",
});

vocabulary!(CompactionKind {
    AUTO = "auto",
    OFF = "off",
    SHARE = "share",
    TOKENS = "tokens",
});

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct CompactionPolicy {
    pub kind: CompactionKind,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub share_percent: i64,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub tokens: i64,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct EndpointDescriptor {
    pub id: EndpointId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub adapter: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Participant {
    pub id: ParticipantId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct InitializeRequest {
    pub protocol_versions: Vec<String>,
    pub profiles: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub participant: Option<Participant>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct InitializeResponse {
    pub protocol_version: String,
    pub profile: String,
    pub endpoint: EndpointDescriptor,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct CapabilityLayer {
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub features: BTreeMap<String, FeatureSupport>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub requested_delivery_modes: Vec<RequestedDeliveryMode>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub effective_delivery_modes: Vec<EffectiveDeliveryMode>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<ToolDefinition>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<ToolSourceDescriptor>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct CapabilityDescriptor {
    pub endpoint: EndpointDescriptor,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub protocol_versions: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub profiles: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub bindings: Vec<Binding>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub features: BTreeMap<String, FeatureSupport>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub layers: BTreeMap<String, CapabilityLayer>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tools: Vec<ToolDefinition>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<ToolSourceDescriptor>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub degradation: Vec<Degradation>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub limits: Option<CapabilityLimits>,
}

pub type CapabilitiesResponse = CapabilityDescriptor;

impl CapabilityDescriptor {
    pub fn effective_support(&self, key: &str) -> Option<&FeatureSupport> {
        self.features.get(key).or_else(|| {
            self.layers
                .values()
                .find_map(|layer| layer.features.get(key))
        })
    }

    pub fn effective_sources(&self) -> Vec<ToolSourceDescriptor> {
        self.sources
            .iter()
            .chain(self.layers.values().flat_map(|layer| &layer.sources))
            .cloned()
            .collect()
    }
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct CapabilityLimits {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_active_runs_per_session: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_queued_runs_per_session: Option<i64>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Binding {
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub serialization: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Degradation {
    pub feature: String,
    #[serde(skip_serializing_if = "SupportLevel::is_empty")]
    pub from: SupportLevel,
    pub to: SupportLevel,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub mode: String,
    pub reason: String,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct CapabilitiesRequest {}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct CapabilitiesUpdated {
    pub previous_revision: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub reason: String,
}

pub const WIRE_OPENAI_RESPONSES: &str = "openai-responses";
pub const WIRE_ANTHROPIC_MESSAGES: &str = "anthropic-messages";
pub const WIRE_OPENAI_CHAT_COMPLETIONS: &str = "openai-chat-completions";

pub const PROVIDER_DIRECT: &str = "direct";
pub const PROVIDER_GATEWAY: &str = "gateway";

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ModelsRequest {
    pub session_id: SessionId,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
}

impl ModelsRequest {
    pub fn allows_degraded(&self, key: &str) -> bool {
        self.allow_degraded_features
            .iter()
            .any(|allowed| allowed == key)
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ModelDescriptor {
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub provider_id: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub context_window: i64,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub features: BTreeMap<String, FeatureSupport>,
    #[serde(skip_serializing_if = "is_false")]
    pub default: bool,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ModelEventPosition {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub run_id: Option<RunId>,
    #[serde(skip_serializing_if = "is_zero_u64")]
    pub sequence: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub switch_request_id: Option<EnvelopeId>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ProviderDescriptor {
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub wire: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub endpoint: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub service_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub upstream_provider_id: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct ModelsResponse {
    pub session_id: SessionId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub current_model_id: String,
    pub models: Vec<ModelDescriptor>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub providers: Vec<ProviderDescriptor>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub as_of_model_event: Option<ModelEventPosition>,
}
