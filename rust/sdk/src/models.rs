use std::marker::PhantomData;
use std::sync::Arc;
use std::time::Duration;

use serde::de::{self, Visitor};
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{json, Value};

use crate::error::{Error, Result};
use crate::transport::Transport;

const MAX_PROVIDER_ID_LEN: usize = 256;
const MAX_MODEL_ID_LEN: usize = 256;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthKind {
    ApiKey,
    #[serde(rename = "oauth")]
    OAuth,
    None,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthStatus {
    Authenticated,
    LoginRequired,
    Expired,
    Refreshing,
    LoginInProgress,
    Failed,
    Unknown,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ModelLifecycle {
    Stable,
    Preview,
    Deprecated,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ModelCapability {
    Chat,
    Streaming,
    Tools,
    Vision,
    Reasoning,
    PromptCache,
    AudioInput,
    AudioOutput,
}

fn optional_member<'de, D, T>(
    deserializer: D,
    member: &'static str,
) -> std::result::Result<Option<T>, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de>,
{
    struct OptionalVisitor<T>(PhantomData<T>, &'static str);

    impl<'de, T: Deserialize<'de>> Visitor<'de> for OptionalVisitor<T> {
        type Value = Option<T>;

        fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
            write!(
                formatter,
                "a {member} string when the key is present",
                member = self.1
            )
        }

        fn visit_str<E: de::Error>(self, value: &str) -> std::result::Result<Self::Value, E> {
            T::deserialize(de::value::StrDeserializer::<E>::new(value)).map(Some)
        }

        fn visit_string<E: de::Error>(self, value: String) -> std::result::Result<Self::Value, E> {
            self.visit_str(&value)
        }

        fn visit_unit<E: de::Error>(self) -> std::result::Result<Self::Value, E> {
            Err(E::custom(format!(
                "{member} must be a string when present, not null",
                member = self.1
            )))
        }

        fn visit_none<E: de::Error>(self) -> std::result::Result<Self::Value, E> {
            Err(E::custom(format!(
                "{member} must be a string when present, not null",
                member = self.1
            )))
        }
    }

    deserializer.deserialize_any(OptionalVisitor(PhantomData, member))
}

fn optional_model_source<'de, D>(
    deserializer: D,
) -> std::result::Result<Option<ModelSource>, D::Error>
where
    D: Deserializer<'de>,
{
    optional_member(deserializer, "source")
}

fn optional_model_lifecycle<'de, D>(
    deserializer: D,
) -> std::result::Result<Option<ModelLifecycle>, D::Error>
where
    D: Deserializer<'de>,
{
    optional_member(deserializer, "lifecycle")
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ModelSource {
    Dynamic,
    StaticFallback,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ReasoningLevel {
    Off,
    Minimal,
    Low,
    Medium,
    High,
    Xhigh,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ModelDescriptor {
    pub model_ref: String,
    pub model_id: String,
    pub display_name: String,
    pub provider_id: String,
    pub api: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub base_url: Option<String>,
    pub auth_status: AuthStatus,
    #[serde(
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "optional_model_lifecycle"
    )]
    pub lifecycle: Option<ModelLifecycle>,
    pub capabilities: Vec<ModelCapability>,
    #[serde(
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "optional_model_source"
    )]
    pub source: Option<ModelSource>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_window: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub max_output_tokens: Option<u32>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reasoning_default: Option<ReasoningLevel>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub metadata: Option<std::collections::BTreeMap<String, String>>,
}

#[derive(Debug, Clone, Default)]
pub struct ListModelsRequest {
    pub provider_id: Option<String>,
    pub api: Option<String>,
    pub model_id: Option<String>,
    pub include_deprecated: Option<bool>,
    pub include_login_required: Option<bool>,
}

#[derive(Debug, Clone, PartialEq)]
pub struct ListModelsResponse {
    pub models: Vec<ModelDescriptor>,
    pub fetched_at_ms: i64,
    pub cache_max_age_ms: u64,
}

#[derive(Clone)]
pub struct ModelsApi {
    transport: Arc<Transport>,
    response_timeout: Duration,
}

impl std::fmt::Debug for ModelsApi {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ModelsApi")
            .field("response_timeout", &self.response_timeout)
            .finish()
    }
}

impl ModelsApi {
    pub(crate) fn new(transport: Arc<Transport>, response_timeout: Duration) -> Self {
        Self {
            transport,
            response_timeout,
        }
    }

    pub async fn list(&self, request: ListModelsRequest) -> Result<ListModelsResponse> {
        if let Some(provider_id) = &request.provider_id {
            if provider_id.len() > MAX_PROVIDER_ID_LEN {
                return Err(Error::protocol(
                    format!(
                        "provider_id exceeds maximum length of {MAX_PROVIDER_ID_LEN} characters"
                    ),
                    Some("invalid_request"),
                ));
            }
        }
        if let Some(model_id) = &request.model_id {
            if model_id.len() > MAX_MODEL_ID_LEN {
                return Err(Error::protocol(
                    format!("model_id exceeds maximum length of {MAX_MODEL_ID_LEN} characters"),
                    Some("invalid_request"),
                ));
            }
        }
        self.list_oap(&request).await
    }

    async fn list_oap(&self, request: &ListModelsRequest) -> Result<ListModelsResponse> {
        let frame = self
            .transport
            .request_oap(
                crate::wire::PROVIDER_PROFILE,
                "provider.models.list.request",
                match &request.provider_id {
                    Some(provider_id) => json!({ "provider_id": provider_id }),
                    None => json!({}),
                },
                None,
                self.response_timeout,
            )
            .await?;
        if frame.kind != "provider.models.list.response" {
            return Err(Error::protocol(
                "unexpected OAP models response",
                Some("malformed_response"),
            ));
        }
        let entries = frame
            .payload()
            .get("models")
            .and_then(Value::as_array)
            .ok_or_else(|| {
                Error::protocol(
                    "OAP models response has no models",
                    Some("malformed_response"),
                )
            })?;
        let mut models = Vec::new();
        for entry in entries {
            let mut mapped = entry.clone();
            let Some(obj) = mapped.as_object_mut() else {
                return Err(Error::protocol(
                    "OAP model entry is not an object",
                    Some("malformed_response"),
                ));
            };
            let wire = obj
                .get("wire")
                .cloned()
                .unwrap_or(Value::String(String::new()));
            obj.insert("api".to_owned(), wire);
            if !obj.contains_key("display_name") {
                let model_id = obj
                    .get("model_id")
                    .cloned()
                    .unwrap_or(Value::String(String::new()));
                obj.insert("display_name".to_owned(), model_id);
            }
            if !obj.contains_key("auth_status") {
                obj.insert(
                    "auth_status".to_owned(),
                    Value::String("unknown".to_owned()),
                );
            }
            match obj.get("source") {
                None => {}
                Some(Value::String(name)) => {
                    if name != "discovered" && name != "fallback" {
                        return Err(Error::protocol(
                            format!("OAP model entry has an unknown source: {name}"),
                            Some("malformed_response"),
                        ));
                    }
                }
                Some(_) => {
                    return Err(Error::protocol(
                        "OAP model entry source must be a string when present".to_owned(),
                        Some("malformed_response"),
                    ))
                }
            }
            if obj.get("source").and_then(Value::as_str) == Some("discovered") {
                obj.insert("source".to_owned(), Value::String("dynamic".to_owned()));
            } else if obj.get("source").and_then(Value::as_str) == Some("fallback") {
                obj.insert(
                    "source".to_owned(),
                    Value::String("static_fallback".to_owned()),
                );
            }
            let model: ModelDescriptor = serde_json::from_value(mapped).map_err(|err| {
                Error::protocol(
                    format!("OAP model entry is malformed: {err}"),
                    Some("malformed_response"),
                )
            })?;
            if request.api.as_deref().is_some_and(|api| api != model.api)
                || request
                    .model_id
                    .as_deref()
                    .is_some_and(|id| id != model.model_id)
                || request.include_deprecated != Some(true)
                    && model.lifecycle == Some(ModelLifecycle::Deprecated)
                || request.include_login_required != Some(true)
                    && model.auth_status == AuthStatus::LoginRequired
            {
                continue;
            }
            models.push(model);
        }
        Ok(ListModelsResponse {
            models,
            fetched_at_ms: crate::ids::now_millis(),
            cache_max_age_ms: 0,
        })
    }

    pub async fn resolve(
        &self,
        provider_id: impl Into<String>,
        api: Option<&str>,
        model_id: impl Into<String>,
    ) -> Result<ModelDescriptor> {
        let provider_id = provider_id.into();
        let model_id = model_id.into();
        if provider_id.is_empty() {
            return Err(Error::protocol(
                "resolve requires provider_id",
                Some("invalid_request"),
            ));
        }
        if model_id.is_empty() {
            return Err(Error::protocol(
                "resolve requires model_id",
                Some("invalid_request"),
            ));
        }

        let response = self
            .list(ListModelsRequest {
                provider_id: Some(provider_id.clone()),
                api: api.map(str::to_owned),
                model_id: Some(model_id.clone()),
                ..Default::default()
            })
            .await?;

        match response.models.len() {
            0 => {
                return Err(Error::protocol("model not found", Some("invalid_request")));
            }
            1 => {}
            count => {
                return Err(Error::protocol(
                    format!("resolve returned {count} matches; expected exactly 1"),
                    Some("invalid_request"),
                ));
            }
        }

        let model = response
            .models
            .into_iter()
            .next()
            .ok_or_else(|| Error::protocol("model not found", Some("invalid_request")))?;
        if model.provider_id != provider_id {
            return Err(Error::protocol(
                "resolved model provider_id mismatch",
                Some("invalid_request"),
            ));
        }
        if model.model_id != model_id {
            return Err(Error::protocol(
                "resolved model_id mismatch",
                Some("invalid_request"),
            ));
        }
        if let Some(api) = api {
            if model.api != api {
                return Err(Error::protocol(
                    "resolved model api mismatch",
                    Some("invalid_request"),
                ));
            }
        }
        Ok(model)
    }
}
