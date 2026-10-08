//! The direct provider path: one request, one provider turn, no tool loop.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use async_stream::try_stream;
use futures_core::Stream;
use serde_json::json;

use crate::auth::AuthApi;
use crate::error::{Error, Result};
use crate::events::ProviderEvent;
use crate::execution::{provider_id_from_ref, ExecutionRequest};
use crate::ids::new_ulid;
use crate::transport::Transport;
use crate::types::{AuthRetryPolicy, CompletionResponse};
use crate::wire::{Frame, PROVIDER_PROFILE};

/// The provider namespace.
///
/// Cheap to clone; every clone shares one transport.
#[derive(Clone)]
pub struct ProviderApi {
    pub(crate) transport: Arc<Transport>,
    pub(crate) auth: AuthApi,
    pub(crate) auth_retry_policy: Option<AuthRetryPolicy>,
    pub(crate) response_timeout: Duration,
}

impl std::fmt::Debug for ProviderApi {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ProviderApi")
            .field("auth_retry_policy", &self.auth_retry_policy)
            .field("response_timeout", &self.response_timeout)
            .finish()
    }
}

impl ProviderApi {
    /// Runs one provider turn and waits for the whole message.
    pub async fn complete(&self, request: ExecutionRequest) -> Result<CompletionResponse> {
        request.validate()?;
        return self.complete_oap(&request).await;
    }

    async fn complete_oap(&self, request: &ExecutionRequest) -> Result<CompletionResponse> {
        let provider_id = provider_id_from_ref(&request.model_ref);
        match self.complete_oap_once(request).await {
            Ok(response) => Ok(response),
            Err(error) if crate::oap::is_auth_failure(&error) => {
                let Some(provider_id) = provider_id else {
                    return Err(error);
                };
                let policy = request
                    .options
                    .auth_retry_policy
                    .or(self.auth_retry_policy)
                    .unwrap_or_default();
                if policy != AuthRetryPolicy::AutoOnce {
                    return Err(Error::auth_required(
                        provider_id,
                        error.message().to_owned(),
                    ));
                }
                self.auth
                    .login(&provider_id, None)
                    .await
                    .map_err(|_| Error::auth_required(&provider_id, error.message().to_owned()))?;
                self.complete_oap_once(request)
                    .await
                    .map_err(|retry_error| {
                        if crate::oap::is_auth_failure(&retry_error) {
                            Error::auth_required(provider_id, retry_error.message().to_owned())
                        } else {
                            retry_error
                        }
                    })
            }
            Err(error) => Err(error),
        }
    }

    async fn complete_oap_once(&self, request: &ExecutionRequest) -> Result<CompletionResponse> {
        let create = self
            .transport
            .request_oap(
                PROVIDER_PROFILE,
                "inference.create.request",
                oap_create_payload(request)?,
                None,
                self.response_timeout,
            )
            .await?;
        let inference_id = accepted_inference(&create)?;
        let mut subscription = self.transport.subscribe_inference(&inference_id);
        let mut guard = OapInferenceGuard::new(Arc::clone(&self.transport), inference_id);
        loop {
            let frame = subscription
                .next_within(self.response_timeout, "inference.completed")
                .await?;
            match frame.kind.as_str() {
                "inference.completed" => {
                    guard.settle();
                    let final_message = frame.payload().get("message").ok_or_else(|| {
                        Error::protocol(
                            "inference.completed has no message",
                            Some("malformed_response"),
                        )
                    })?;
                    return crate::oap::response(
                        final_message,
                        frame.payload(),
                        &request.model_ref,
                    );
                }
                "inference.failed" => {
                    guard.settle();
                    let err = frame.payload().get("error").unwrap_or(frame.payload());
                    return Err(Error::provider_stream(
                        err.get("message")
                            .and_then(serde_json::Value::as_str)
                            .unwrap_or("inference failed"),
                        err.get("code")
                            .and_then(serde_json::Value::as_str)
                            .map(str::to_owned),
                        provider_id_from_ref(&request.model_ref),
                    ));
                }
                _ => {}
            }
        }
    }

    /// Runs one provider turn, yielding events as they arrive.
    ///
    /// Exactly one terminal event ends the stream:
    /// [`ProviderEvent::MessageEnd`] or [`ProviderEvent::Error`] (spec §3.5).
    /// Failures that never reach the event plane — a rejected request, a dead
    /// transport, a timeout — arrive as an `Err` item instead.
    ///
    /// Dropping the stream cancels it: the SDK sends `inference.cancel.request`, so
    /// the runtime stops the provider turn instead of finishing it
    /// into a queue nobody is reading.
    pub fn stream(
        &self,
        request: ExecutionRequest,
    ) -> impl Stream<Item = Result<ProviderEvent>> + Send + 'static {
        let this = self.clone();
        try_stream! {
            request.validate()?;
            let policy = request.options.auth_retry_policy.or(this.auth_retry_policy).unwrap_or_default();
            let provider_id = provider_id_from_ref(&request.model_ref);
            let mut retried = false;
            'oap_attempt: loop {
                let inference_id = match this.transport.request_oap(
                    PROVIDER_PROFILE, "inference.create.request", oap_create_payload(&request)?, None, this.response_timeout,
                ).await.and_then(|frame| accepted_inference(&frame)) {
                    Ok(id) => id,
                    Err(error) if crate::oap::is_auth_failure(&error) => {
                        let Some(provider_id) = provider_id.as_deref() else { Err(error)?; unreachable!() };
                        if !retried && policy == AuthRetryPolicy::AutoOnce {
                            retried = true;
                            this.auth.login(provider_id, None).await
                                .map_err(|_| Error::auth_required(provider_id, error.message().to_owned()))?;
                            continue 'oap_attempt;
                        }
                        Err(Error::auth_required(provider_id, error.message().to_owned()))?
                    }
                    Err(error) => Err(error)?,
                };
                let mut subscription = this.transport.subscribe_inference(&inference_id);
                let mut guard = OapInferenceGuard::new(Arc::clone(&this.transport), inference_id);
                let mut kinds = HashMap::<u64, String>::new();
                let mut start: Option<ProviderEvent> = None;
                let mut yielded_content = false;
                loop {
                    let frame = subscription.next_within(this.response_timeout, "inference event").await?;
                    let data = frame.payload();
                    match frame.kind.as_str() {
                        "inference.started" => {
                            let ref_parts = request.model_ref.split_once('/').and_then(|(provider, rest)| rest.split_once('@').map(|(api, model)| (provider, api, model)));
                            start = Some(ProviderEvent::MessageStart {
                                provider_id: ref_parts.map(|parts| parts.0.to_owned()),
                                api: ref_parts.map(|parts| parts.1.to_owned()),
                                model_id: ref_parts.map(|parts| parts.2.to_owned()),
                            });
                        }
                        "inference.part.started" => {
                            if let (Some(index), Some(kind)) = (data.get("part_index").and_then(serde_json::Value::as_u64), data.get("part_kind").and_then(serde_json::Value::as_str)) {
                                kinds.insert(index, kind.to_owned());
                            }
                        }
                        "inference.part.delta" => {
                            if let Some(start) = start.take() { yield start; }
                            yielded_content = true;
                            let index = data.get("part_index").and_then(serde_json::Value::as_u64).unwrap_or(0);
                            let delta = data.get("delta").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned();
                            match kinds.get(&index).map(String::as_str) {
                                Some("reasoning") => yield ProviderEvent::ThinkingDelta { delta },
                                Some("text") => yield ProviderEvent::TextDelta { delta },
                                Some("tool_call") => {}, // Partial JSON is emitted only with the completed call.
                                _ => Err(Error::protocol("inference delta has no known part kind", Some("malformed_response")))?,
                            }
                        }
                        "inference.part.ended" if data.get("part_kind").and_then(serde_json::Value::as_str) == Some("tool_call") => {
                            if let Some(start) = start.take() { yield start; }
                            yielded_content = true;
                            let call = data.get("tool_call").unwrap_or(data);
                            yield ProviderEvent::ToolCall {
                                tool_call_id: call.get("tool_call_id").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned(),
                                name: call.get("name").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned(),
                                arguments_json: call.get("arguments_json").map(serde_json::Value::to_string).unwrap_or_else(|| "{}".to_owned()),
                            };
                        }
                        "inference.completed" => {
                            guard.settle();
                            if let Some(start) = start.take() { yield start; }
                            yield ProviderEvent::MessageEnd {
                                usage: data.get("usage").and_then(crate::types::Usage::parse),
                                stop_reason: data.get("stop_reason").and_then(serde_json::Value::as_str).map(str::to_owned),
                                error_message: None,
                            };
                            return;
                        }
                        "inference.failed" => {
                            guard.settle();
                            let err = data.get("error").unwrap_or(data);
                            let error = Error::provider_stream(
                                err.get("message").and_then(serde_json::Value::as_str).unwrap_or("inference failed"),
                                err.get("code").and_then(serde_json::Value::as_str).map(str::to_owned),
                                provider_id.clone(),
                            );
                            if crate::oap::is_auth_failure(&error) {
                                let Some(provider_id) = provider_id.as_deref() else { Err(error)?; unreachable!() };
                                if !yielded_content && !retried && policy == AuthRetryPolicy::AutoOnce {
                                    retried = true;
                                    this.auth.login(provider_id, None).await
                                        .map_err(|_| Error::auth_required(provider_id, error.message().to_owned()))?;
                                    continue 'oap_attempt;
                                }
                                Err(Error::auth_required(provider_id, error.message().to_owned()))?;
                            }
                            Err(error)?;
                        }
                        _ => {}
                    }
                }
            }

        }
    }
}

fn oap_create_payload(request: &ExecutionRequest) -> Result<serde_json::Value> {
    if request.tools.iter().any(crate::types::Tool::is_executable) {
        return Err(crate::oap::unsupported(
            "client-executed tools on direct provider inference",
        ));
    }
    let mut payload = json!({
        "model_ref": request.model_ref,
        "messages": crate::oap::messages(request)?,
        "stream": true,
    });
    let fields = payload
        .as_object_mut()
        .ok_or_else(|| Error::invalid_request("OAP inference payload is not an object"))?;
    if let Some(tokens) = request.options.max_tokens {
        fields.insert("max_output_tokens".to_owned(), json!(tokens));
    }
    if let Some(temperature) = request.options.temperature {
        fields.insert("temperature".to_owned(), json!(temperature));
    }
    if let Some(reasoning) = request.options.reasoning_effort {
        fields.insert(
            "reasoning".to_owned(),
            json!({ "enabled": true, "effort": format!("{reasoning:?}").to_lowercase() }),
        );
    }
    if let Some(metadata) = &request.options.metadata {
        fields.insert("metadata".to_owned(), json!(metadata));
    }
    if !request.tools.is_empty() {
        let tools = request
            .tools
            .iter()
            .map(crate::oap::tool_definition)
            .collect::<Result<Vec<_>>>()?;
        fields.insert("tools".to_owned(), json!(tools));
    }
    Ok(payload)
}

fn accepted_inference(frame: &Frame) -> Result<String> {
    if frame.kind != "inference.create.response" {
        return Err(Error::protocol(
            "unexpected OAP inference response",
            Some("malformed_response"),
        ));
    }
    if frame
        .payload()
        .get("accepted")
        .and_then(serde_json::Value::as_bool)
        != Some(true)
    {
        let err = frame.payload().get("error").unwrap_or(frame.payload());
        return Err(Error::provider_stream(
            err.get("message")
                .and_then(serde_json::Value::as_str)
                .unwrap_or("inference rejected"),
            err.get("code")
                .and_then(serde_json::Value::as_str)
                .map(str::to_owned),
            None,
        ));
    }
    frame.inference_id.clone().ok_or_else(|| {
        Error::protocol(
            "inference.create.response has no inference_id",
            Some("malformed_response"),
        )
    })
}

struct OapInferenceGuard {
    transport: Arc<Transport>,
    id: String,
    settled: bool,
}

impl OapInferenceGuard {
    fn new(transport: Arc<Transport>, id: String) -> Self {
        Self {
            transport,
            id,
            settled: false,
        }
    }
    fn settle(&mut self) {
        self.settled = true;
    }
}

impl Drop for OapInferenceGuard {
    fn drop(&mut self) {
        if !self.settled {
            let _ = self.transport.send_oap(
                PROVIDER_PROFILE,
                "inference.cancel.request",
                &new_ulid(),
                json!({ "reason": "client aborted" }),
                Some(("inference_id", &self.id)),
            );
        }
    }
}
