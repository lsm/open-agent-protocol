#![doc = include_str!("../README.md")]

mod baseurl;
mod credential;
mod row;

use std::sync::OnceLock;

use serde::{Deserialize, Serialize};
use serde_json::Value;

pub use baseurl::{
    base_url_with_overrides, normalize_kimi_region, normalize_versioned_base_url, provider_arm,
    uses_versioned_route, BaseOverrides, GLOBAL_BASE_URL_ENV, VERSIONED_WIRES,
};
pub use credential::{api_key_from_names, Credential, EnvironmentValue, Source, StoredCredential};
pub use row::{is_responses_only_model, joins_wire, WIRES};

const SCHEMA: &str = include_str!("../../../providers/catalog.schema.json");
const CATALOG: &str = include_str!("../../../providers/catalog.json");

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("provider catalog schema: {0}")]
    Schema(String),
    #[error("provider catalog: {0}")]
    Invalid(String),
    #[error("provider catalog: {0}")]
    Decode(String),
    #[error("the provider catalog holds no provider")]
    Empty,
}

fn is_false(value: &bool) -> bool {
    !value
}

fn is_zero(value: &i64) -> bool {
    *value == 0
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Catalog {
    pub providers: Vec<Provider>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Provider {
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub auth: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub offering: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub credential_env: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub base_url_env: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub region_env: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub default_region: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub wires: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub base_url_source: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub endpoints: Vec<Endpoint>,
    #[serde(rename = "models_endpoint", skip_serializing_if = "String::is_empty")]
    pub models_path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub models_dev: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub credential_group: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub context_window: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub max_context_window: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub max_tokens: i64,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub models: Vec<Model>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub oauth_origin: Option<OAuthOrigin>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub docs: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Model {
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub name: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub context_window: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub max_context_window: i64,
    #[serde(skip_serializing_if = "is_zero")]
    pub max_tokens: i64,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Endpoint {
    pub wire: String,
    pub base_url: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub region: String,
    #[serde(skip_serializing_if = "is_false")]
    pub carries_version: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub session_header: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct OAuthOrigin {
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub exact: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub domain: String,
    #[serde(skip_serializing_if = "is_false")]
    pub credential_declares_origin: bool,
}

impl Catalog {
    pub fn load(schema: &str, catalog: &str) -> Result<Self, Error> {
        let schema: Value =
            serde_json::from_str(schema).map_err(|error| Error::Schema(error.to_string()))?;
        let validator = jsonschema::draft202012::new(&schema)
            .map_err(|error| Error::Schema(error.to_string()))?;
        let instance: Value =
            serde_json::from_str(catalog).map_err(|error| Error::Decode(error.to_string()))?;
        if let Some(error) = validator.iter_errors(&instance).next() {
            return Err(Error::Invalid(error.to_string()));
        }
        let parsed: Catalog =
            serde_json::from_str(catalog).map_err(|error| Error::Decode(error.to_string()))?;
        if parsed.providers.is_empty() {
            return Err(Error::Empty);
        }
        Ok(parsed)
    }

    pub fn embedded() -> &'static Catalog {
        static EMBEDDED: OnceLock<Catalog> = OnceLock::new();
        EMBEDDED.get_or_init(|| match Catalog::load(SCHEMA, CATALOG) {
            Ok(catalog) => catalog,
            Err(error) => panic!("the embedded provider catalog is invalid: {error}"),
        })
    }

    pub fn provider(&self, id: &str) -> Option<&Provider> {
        self.providers.iter().find(|provider| provider.id == id)
    }
}
