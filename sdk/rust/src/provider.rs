//! The direct provider path: one request, one provider turn, no tool loop.

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use async_stream::try_stream;
use futures_core::Stream;
use serde_json::json;

use crate::auth::AuthApi;
use crate::error::{Error, Result};
use crate::events::{normalize_provider_frame, ProviderEvent, ToolCallBuffers};
use crate::execution::{provider_id_from_ref, provider_payload, ExecutionRequest};
use crate::ids::new_ulid;
use crate::transport::Transport;
use crate::types::{AuthRetryPolicy, CompletionResponse};
use crate::wire::{Envelope, Frame, PROVIDER_PROFILE};

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
        if self.transport.is_oap() {
            return self.complete_oap(&request).await;
        }
        let policy = request
            .options
            .auth_retry_policy
            .or(self.auth_retry_policy)
            .unwrap_or_default();
        let fallback_provider = provider_id_from_ref(&request.model_ref);

        match self.complete_once(&request).await {
            Ok(response) => Ok(response),
            Err(error) if error.is_retryable_auth() => {
                let provider_id = error
                    .provider_id()
                    .map(str::to_owned)
                    .or_else(|| fallback_provider.clone());
                let Some(provider_id) = provider_id else {
                    return Err(error);
                };
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
                self.complete_once(&request).await.map_err(|retry_error| {
                    promote_auth_error(retry_error, Some(provider_id.clone()))
                })
            }
            Err(error) => Err(error),
        }
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

    async fn complete_once(&self, request: &ExecutionRequest) -> Result<CompletionResponse> {
        let fallback_provider = provider_id_from_ref(&request.model_ref);
        let stream_id = new_ulid();
        let mut subscription = self.transport.subscribe_stream(&stream_id);
        let mut guard = StreamCancelGuard::new(Arc::clone(&self.transport), stream_id.clone());

        self.transport.send(&Envelope::for_stream(
            "complete_request",
            &stream_id,
            provider_payload(request, true, self.auth_retry_policy),
        ))?;

        loop {
            let frame = subscription
                .next_within(self.response_timeout, "provider complete_response")
                .await?;
            match frame.kind.as_str() {
                "ack" => continue,
                "nack" => {
                    guard.settle();
                    return Err(nack_to_error(&frame, fallback_provider.as_deref()));
                }
                "stream_error" => {
                    guard.settle();
                    return Err(frame_to_error(&frame));
                }
                "result" | "complete_response" => {
                    guard.settle();
                    return Ok(CompletionResponse::parse(frame.payload_object()?));
                }
                other => {
                    guard.settle();
                    return Err(Error::transport_stream(format!(
                        "unexpected frame type while awaiting provider result: {other}"
                    )));
                }
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
    /// Dropping the stream cancels it: the SDK sends `abort_request` for the
    /// stream, so the runtime stops the provider turn instead of finishing it
    /// into a queue nobody is reading.
    pub fn stream(
        &self,
        request: ExecutionRequest,
    ) -> impl Stream<Item = Result<ProviderEvent>> + Send + 'static {
        let this = self.clone();
        try_stream! {
            request.validate()?;
            if this.transport.is_oap() {
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
            let policy = request
                .options
                .auth_retry_policy
                .or(this.auth_retry_policy)
                .unwrap_or_default();
            let fallback_provider = provider_id_from_ref(&request.model_ref);

            let mut yielded = false;
            let mut retried = false;

            'attempt: loop {
                let stream_id = new_ulid();
                let mut subscription = this.transport.subscribe_stream(&stream_id);
                let mut guard =
                    StreamCancelGuard::new(Arc::clone(&this.transport), stream_id.clone());
                let mut buffers = ToolCallBuffers::default();

                this.transport.send(&Envelope::for_stream(
                    "stream_request",
                    &stream_id,
                    provider_payload(&request, false, this.auth_retry_policy),
                ))?;

                loop {
                    let frame = subscription
                        .next_within(this.response_timeout, "provider stream event")
                        .await?;

                    if frame.kind == "ack" {
                        continue;
                    }
                    if frame.kind == "nack" {
                        guard.settle();
                        let error = nack_to_error(&frame, fallback_provider.as_deref());
                        if !yielded && !retried && policy == AuthRetryPolicy::AutoOnce
                            && error.is_retryable_auth()
                        {
                            if let Some(provider_id) = error
                                .provider_id()
                                .map(str::to_owned)
                                .or_else(|| fallback_provider.clone())
                            {
                                this.auth.login(&provider_id, None).await.map_err(|_| {
                                    Error::auth_required(&provider_id, error.message().to_owned())
                                })?;
                                retried = true;
                                continue 'attempt;
                            }
                        }
                        Err(promote_auth_error(error, fallback_provider.clone()))?;
                        return;
                    }

                    let Some(event) = normalize_provider_frame(&frame, &mut buffers) else {
                        continue;
                    };

                    if let ProviderEvent::Error {
                        message,
                        code,
                        provider_id,
                    } = &event
                    {
                        guard.settle();
                        // An `auth_required` error event is a failure, not an
                        // event a consumer should have to inspect: it becomes
                        // the typed auth path like every other auth rejection.
                        if code.as_deref() == Some("auth_required") {
                            let error = Error::provider_stream(
                                message.clone(),
                                code.clone(),
                                provider_id.clone().or_else(|| fallback_provider.clone()),
                            );
                            if !yielded && !retried && policy == AuthRetryPolicy::AutoOnce {
                                if let Some(provider) = error.provider_id().map(str::to_owned) {
                                    this.auth.login(&provider, None).await.map_err(|_| {
                                        Error::auth_required(&provider, error.message().to_owned())
                                    })?;
                                    retried = true;
                                    continue 'attempt;
                                }
                            }
                            Err(promote_auth_error(error, fallback_provider.clone()))?;
                            return;
                        }
                        yield event;
                        return;
                    }

                    if event.is_terminal() {
                        guard.settle();
                        yield event;
                        return;
                    }

                    yielded = true;
                    yield event;
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
            .map(|tool| -> Result<serde_json::Value> {
                let mut value = tool.serialize_for_wire();
                if let Some(obj) = value.as_object_mut() {
                    if let Some(schema) = obj.remove("parameters_schema_json") {
                        let raw = schema.as_str().ok_or_else(|| {
                            Error::invalid_request("tool parameters_schema_json is not a string")
                        })?;
                        let parsed =
                            serde_json::from_str::<serde_json::Value>(raw).map_err(|err| {
                                Error::invalid_request(format!(
                                    "tool parameters_schema_json is not valid JSON: {err}"
                                ))
                            })?;
                        obj.insert("input_schema".to_owned(), parsed);
                    }
                }
                Ok(value)
            })
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

/// Sends `abort_request` if the stream is dropped before it settles.
pub(crate) struct StreamCancelGuard {
    transport: Arc<Transport>,
    stream_id: String,
    settled: bool,
}

impl StreamCancelGuard {
    pub(crate) fn new(transport: Arc<Transport>, stream_id: String) -> Self {
        Self {
            transport,
            stream_id,
            settled: false,
        }
    }

    pub(crate) fn settle(&mut self) {
        self.settled = true;
    }
}

impl Drop for StreamCancelGuard {
    fn drop(&mut self) {
        if self.settled {
            return;
        }
        // The abort carries sequence 2 because the request it cancels carried 1.
        // It is advisory: a request the server already rejected never advanced
        // its counter, so the abort may itself be rejected as a sequence gap —
        // which is harmless, since in that case there is nothing left to abort.
        self.transport.send_best_effort(&Envelope {
            kind: "abort_request".to_owned(),
            stream_id: Some(self.stream_id.clone()),
            session_id: None,
            message_id: new_ulid(),
            sequence: 2,
            timestamp: crate::ids::now_millis(),
            version: crate::wire::ENVELOPE_VERSION,
            in_reply_to: None,
            payload: json!({
                "target_stream_id": self.stream_id,
                "reason": "client aborted",
            }),
        });
    }
}

pub(crate) fn nack_to_error(frame: &Frame, fallback_provider: Option<&str>) -> Error {
    let code = frame.payload_non_empty("error_code");
    let mut provider_id = frame.payload_non_empty("provider_id");
    if provider_id.is_none() && code.as_deref() == Some("auth_required") {
        provider_id = fallback_provider.map(str::to_owned);
    }
    Error::provider_stream(
        frame
            .payload_non_empty("reason")
            .unwrap_or_else(|| "request rejected".to_owned()),
        code,
        provider_id,
    )
}

pub(crate) fn frame_to_error(frame: &Frame) -> Error {
    Error::provider_stream(
        frame
            .payload_non_empty("message")
            .or_else(|| frame.payload_non_empty("reason"))
            .unwrap_or_else(|| "stream error".to_owned()),
        frame
            .payload_non_empty("code")
            .or_else(|| frame.payload_non_empty("error_code")),
        frame.payload_non_empty("provider_id"),
    )
}

/// Turns a still-retryable `auth_required` into the terminal typed form once
/// the retry budget is gone.
pub(crate) fn promote_auth_error(error: Error, fallback_provider: Option<String>) -> Error {
    if !error.is_retryable_auth() {
        return error;
    }
    match error.provider_id().map(str::to_owned).or(fallback_provider) {
        Some(provider_id) => Error::auth_required(provider_id, error.message().to_owned()),
        None => error,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::Value;

    fn frame(value: Value) -> Frame {
        Frame::parse(&value.to_string()).expect("frame parses")
    }

    #[test]
    fn nacks_attribute_auth_failures_to_the_requests_provider() {
        // The shape the runtime answers an unauthenticated stream_request with.
        let error = nack_to_error(
            &frame(json!({
                "type": "nack",
                "payload": { "reason": "auth_required", "error_code": "auth_required" }
            })),
            Some("anthropic"),
        );
        assert_eq!(error.code(), Some("auth_required"));
        assert_eq!(error.provider_id(), Some("anthropic"));
        assert!(error.is_retryable_auth());
    }

    #[test]
    fn non_auth_nacks_do_not_borrow_the_fallback_provider() {
        let error = nack_to_error(
            &frame(json!({
                "type": "nack",
                "payload": { "reason": "bad filter", "error_code": "invalid_request" }
            })),
            Some("anthropic"),
        );
        assert_eq!(error.provider_id(), None);
        assert!(!error.is_retryable_auth());
    }

    #[test]
    fn nacks_without_a_reason_still_carry_a_message() {
        let error = nack_to_error(&frame(json!({ "type": "nack", "payload": {} })), None);
        assert_eq!(error.message(), "request rejected");
    }

    #[test]
    fn stream_error_frames_read_both_code_spellings() {
        let error = frame_to_error(&frame(json!({
            "type": "stream_error",
            "payload": { "message": "upstream exploded", "error_code": "provider_error", "provider_id": "openai" }
        })));
        assert_eq!(error.code(), Some("provider_error"));
        assert_eq!(error.provider_id(), Some("openai"));
        assert_eq!(error.message(), "upstream exploded");
    }

    #[test]
    fn promotion_only_touches_retryable_auth_errors() {
        let promoted = promote_auth_error(
            Error::provider_stream("auth_required", Some("auth_required".into()), None),
            Some("anthropic".to_owned()),
        );
        assert!(matches!(promoted, Error::AuthRequired { .. }));
        assert_eq!(promoted.provider_id(), Some("anthropic"));

        let untouched = promote_auth_error(
            Error::transport_stream("child exited"),
            Some("anthropic".to_owned()),
        );
        assert!(matches!(untouched, Error::Stream { .. }));

        // With no provider to attribute it to, the retryable form is kept as-is
        // rather than inventing one.
        let kept = promote_auth_error(
            Error::provider_stream("auth_required", Some("auth_required".into()), None),
            None,
        );
        assert!(matches!(kept, Error::Stream { .. }));
    }
}
