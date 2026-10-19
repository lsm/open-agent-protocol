use serde::{Deserialize, Serialize};

use crate::vocabulary::vocabulary;
use crate::{AuthFlowId, ProtocolError};

vocabulary!(CredentialKind {
    API_KEY = "api_key",
    OAUTH = "oauth",
    NONE = "none",
});

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthProvider {
    pub id: String,
    pub name: String,
    pub auth_kinds: Vec<CredentialKind>,
    pub auth_status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub last_error: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub override_host: String,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct AuthProvidersRequest {}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthProvidersResponse {
    pub providers: Vec<AuthProvider>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthLoginStartRequest {
    pub provider_id: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthLoginStartResponse {
    pub flow_id: AuthFlowId,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthLoginEvent {
    pub flow_id: AuthFlowId,
    pub provider_id: String,
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub instructions: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub message: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthLoginCancelRequest {
    pub flow_id: AuthFlowId,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthLoginCancelResponse {
    pub flow_id: AuthFlowId,
    pub accepted: bool,
}

#[derive(Clone, Debug, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct AuthLoginCompleted {
    pub flow_id: AuthFlowId,
    pub provider_id: String,
    pub status: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<ProtocolError>,
}
