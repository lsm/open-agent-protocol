//! The agent path: a multi-turn loop with client-side tool execution.
//!
//! # Session lifecycle
//!
//! A run opens (or reuses) an OAP session with `session.open.request`, submits
//! with `session.message.submit.request`, and reads the run's events until it
//! settles. Dropping a stream before it settles sends `run.cancel.request`.
//!
//! # Tool execution
//!
//! Tools run in this process. Over OAP the tools are provided at session open
//! and the endpoint publishes `action.call.requested` for each call the SDK
//! owns, which it answers with `action.call.resolve.request`. A
//! [`crate::Tool`] with no handler answers with an error rather than stalling
//! the loop.

use std::sync::Arc;
use std::time::Duration;

use async_stream::try_stream;
use futures_core::Stream;
use serde_json::json;

use crate::auth::AuthApi;
use crate::error::{Error, Result};
use crate::events::{AgentEvent, ProviderEvent};
use crate::execution::{provider_id_from_ref, ExecutionRequest};
use crate::ids::new_session_id;
use crate::models::ModelsApi;
use crate::transport::{Subscription, Transport};
use crate::types::{AuthRetryPolicy, ChatMessage, CompletionResponse, ToolInvocation, Usage};
use crate::wire::{Frame, AGENT_PROFILE, SDK_PARTICIPANT};

/// The agent namespace.
///
/// Cheap to clone; every clone shares one transport.
#[derive(Clone)]
pub struct AgentApi {
    pub(crate) transport: Arc<Transport>,
    pub(crate) auth: AuthApi,
    pub(crate) auth_retry_policy: Option<AuthRetryPolicy>,
    pub(crate) response_timeout: Duration,
    pub(crate) models: ModelsApi,
}

impl std::fmt::Debug for AgentApi {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("AgentApi")
            .field("auth_retry_policy", &self.auth_retry_policy)
            .field("response_timeout", &self.response_timeout)
            .finish()
    }
}

impl AgentApi {
    /// Model discovery over the same transport, for chaining with a run.
    ///
    /// The same API as [`crate::Client::models`], reached through this
    /// namespace; the two return identical results.
    pub fn models(&self) -> ModelsApi {
        self.models.clone()
    }

    /// Runs the agent loop to completion and returns the final assistant message.
    pub async fn run(&self, request: ExecutionRequest) -> Result<CompletionResponse> {
        validate_oap_agent_request(&request)?;
        self.run_oap(&request).await
    }

    async fn run_oap(&self, request: &ExecutionRequest) -> Result<CompletionResponse> {
        let provider_id = provider_id_from_ref(&request.model_ref);
        let mut tools_ran = false;
        match self.run_oap_once(request, &mut tools_ran).await {
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
                if policy != AuthRetryPolicy::AutoOnce || tools_ran {
                    return Err(Error::auth_required(
                        provider_id,
                        error.message().to_owned(),
                    ));
                }
                self.auth
                    .login(&provider_id, None)
                    .await
                    .map_err(|_| Error::auth_required(&provider_id, error.message().to_owned()))?;
                self.run_oap_once(request, &mut tools_ran)
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

    async fn run_oap_once(
        &self,
        request: &ExecutionRequest,
        tools_ran: &mut bool,
    ) -> Result<CompletionResponse> {
        let (session_id, run_id, mut subscription) = self.begin_oap(request).await?;
        let mut guard = OapRunGuard::new(Arc::clone(&self.transport), session_id, run_id.clone());
        loop {
            let frame = subscription
                .next_within(self.response_timeout, "run.completed")
                .await?;
            if frame.run_id.as_deref().is_some_and(|id| id != run_id) {
                continue;
            }
            match frame.kind.as_str() {
                "action.call.requested" | "action.call.resolve.response" => {
                    *tools_ran |= self.answer_call(&frame, request, &run_id).await?;
                }
                "run.completed" => {
                    guard.settle();
                    let message = frame.payload().get("final_response").ok_or_else(|| {
                        Error::protocol(
                            "run.completed has no final_response",
                            Some("malformed_response"),
                        )
                    })?;
                    return crate::oap::response(
                        message,
                        frame.payload(),
                        if request.model_ref.is_empty() {
                            frame
                                .payload()
                                .get("model_id")
                                .and_then(serde_json::Value::as_str)
                                .unwrap_or_default()
                        } else {
                            &request.model_ref
                        },
                    );
                }
                "run.failed" => {
                    guard.settle();
                    let err = frame.payload().get("error").unwrap_or(frame.payload());
                    return Err(Error::provider_stream(
                        err.get("message")
                            .and_then(serde_json::Value::as_str)
                            .unwrap_or("agent run failed"),
                        err.get("code")
                            .and_then(serde_json::Value::as_str)
                            .map(str::to_owned),
                        provider_id_from_ref(&request.model_ref),
                    ));
                }
                "run.cancelled" => {
                    guard.settle();
                    return Err(Error::stream(
                        crate::error::StreamErrorKind::Aborted,
                        "agent run cancelled",
                    ));
                }
                _ => {}
            }
        }
    }

    async fn begin_oap(
        &self,
        request: &ExecutionRequest,
    ) -> Result<(String, String, Subscription)> {
        if request.options.temperature.is_some() {
            return Err(crate::oap::unsupported("temperature on an agent run"));
        }
        let session_id = request
            .options
            .session_id
            .clone()
            .unwrap_or_else(new_session_id);
        let opened = self
            .transport
            .request_oap(
                AGENT_PROFILE,
                "session.open.request",
                open_payload(
                    &session_id,
                    request,
                    &self.transport.agent_endpoint(),
                    self.transport.advertises("action.tools.provide"),
                    &self.transport.agent_source(),
                )?,
                Some(("session_id", &session_id)),
                self.response_timeout,
            )
            .await?;
        if opened.kind != "session.open.response" {
            return Err(Error::protocol(
                "unexpected OAP session response",
                Some("malformed_response"),
            ));
        }
        let subscription = self.transport.subscribe_session(&session_id);
        if !subscription.owns_session() {
            return Err(Error::protocol(
                "session is already in use",
                Some("session_busy"),
            ));
        }
        let mut submit_payload = json!({
            "session_id": session_id,
            "messages": crate::oap::messages(request)?,
            "delivery": "auto",
        });
        if !request.model_ref.is_empty() {
            if let Some(fields) = submit_payload.as_object_mut() {
                fields.insert("model_id".to_owned(), json!(request.model_ref));
            }
        }
        let submitted = self
            .transport
            .request_oap(
                AGENT_PROFILE,
                "session.message.submit.request",
                submit_payload,
                Some(("session_id", &session_id)),
                self.response_timeout,
            )
            .await?;
        if submitted.kind != "session.message.submit.response"
            || submitted
                .payload()
                .get("accepted")
                .and_then(serde_json::Value::as_bool)
                != Some(true)
        {
            return Err(Error::protocol(
                "OAP submission was not admitted",
                Some("malformed_response"),
            ));
        }
        let run_id = submitted
            .payload()
            .get("run_id")
            .and_then(serde_json::Value::as_str)
            .filter(|id| !id.is_empty())
            .ok_or_else(|| {
                Error::protocol("OAP admission has no run_id", Some("malformed_response"))
            })?
            .to_owned();
        Ok((session_id, run_id, subscription))
    }

    /// Switches the selected model of an open OAP session; the next admitted run uses it.
    pub async fn switch_model(&self, session_id: &str, model_id: &str) -> Result<()> {
        let response = self
            .transport
            .request_oap(
                AGENT_PROFILE,
                "session.model.switch.request",
                json!({ "session_id": session_id, "model_id": model_id }),
                Some(("session_id", session_id)),
                self.response_timeout,
            )
            .await?;
        if response.kind != "session.model.switch.response" {
            return Err(Error::protocol(
                "unexpected OAP model switch response",
                Some("malformed_response"),
            ));
        }
        Ok(())
    }

    /// Starts a run on an open session using the model selected by `switch_model`.
    pub async fn run_selected(
        &self,
        session_id: impl Into<String>,
        messages: Vec<ChatMessage>,
    ) -> Result<CompletionResponse> {
        let mut request = ExecutionRequest::new("", messages);
        request.options.session_id = Some(session_id.into());
        self.run(request).await
    }

    /// Streams a run on an open OAP session using its selected model.
    pub fn stream_selected(
        &self,
        session_id: impl Into<String>,
        messages: Vec<ChatMessage>,
    ) -> impl Stream<Item = Result<AgentEvent>> + Send + 'static {
        let mut request = ExecutionRequest::new("", messages);
        request.options.session_id = Some(session_id.into());
        self.stream(request)
    }

    /// Runs the agent loop, yielding events as they arrive.
    ///
    /// The run ends with exactly one terminal event: [`AgentEvent::AgentEnd`],
    /// or a [`ProviderEvent::Error`] wrapped in [`AgentEvent::Provider`]
    /// (spec §3.5). Failures outside the event plane arrive as an `Err` item.
    ///
    /// Dropping the stream cancels the run: the SDK sends `run.cancel.request`, so the
    /// runtime stops the loop rather than running it out against a queue nobody
    /// is reading.
    pub fn stream(
        &self,
        request: ExecutionRequest,
    ) -> impl Stream<Item = Result<AgentEvent>> + Send + 'static {
        let this = self.clone();
        try_stream! {
            validate_oap_agent_request(&request)?;
            let policy = request.options.auth_retry_policy.or(this.auth_retry_policy).unwrap_or_default();
            let provider_id = provider_id_from_ref(&request.model_ref);
            let mut retried = false;
            'oap_attempt: loop {
                let (session_id, run_id, mut subscription) = match this.begin_oap(&request).await {
                    Ok(started) => started,
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
                let mut guard = OapRunGuard::new(Arc::clone(&this.transport), session_id.clone(), run_id.clone());
                let mut start: Option<AgentEvent> = None;
                let mut yielded_content = false;
                loop {
                    let frame = subscription.next_within(this.response_timeout, "agent run event").await?;
                    if frame.run_id.as_deref().is_some_and(|id| id != run_id) { continue; }
                    let data = frame.payload();
                    match frame.kind.as_str() {
                        "action.call.requested" | "action.call.resolve.response" => {
                            if this.answer_call(&frame, &request, &run_id).await? { yielded_content = true; }
                        }
                        "action.call.started" => {
                            if let Some(start) = start.take() { yield start; }
                            yield AgentEvent::ToolExecutionStart {
                                tool_call_id: data.get("tool_call_id").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned(),
                                tool_name: data.get("name").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned(),
                            };
                        }
                        "action.call.completed" | "action.call.failed" | "action.call.cancelled" => {
                            if let Some(start) = start.take() { yield start; }
                            yield AgentEvent::ToolExecutionEnd {
                                tool_call_id: data.get("tool_call_id").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned(),
                                is_error: Some(frame.kind != "action.call.completed"),
                            };
                        }
                        "run.started" => start = Some(AgentEvent::AgentStart { session_id: Some(session_id.clone()) }),
                        "content.delta" => {
                            if let Some(part) = data.get("part") {
                                let delta = match part.get("type").and_then(serde_json::Value::as_str) {
                                    Some("text") => Some(ProviderEvent::TextDelta { delta: part.get("text").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned() }),
                                    Some("reasoning") => Some(ProviderEvent::ThinkingDelta { delta: part.get("reasoning").and_then(serde_json::Value::as_str).unwrap_or_default().to_owned() }),
                                    _ => None,
                                };
                                if let Some(delta) = delta {
                                    if let Some(start) = start.take() { yield start; }
                                    yielded_content = true;
                                    yield AgentEvent::Provider(delta);
                                }
                            }
                        }
                        "run.completed" => {
                            guard.settle();
                            if let Some(start) = start.take() { yield start; }
                            let identity = request.model_ref.split_once('/').and_then(|(provider, rest)| rest.split_once('@').map(|(api, _)| (provider, api)));
                            yield AgentEvent::AgentEnd {
                                usage: data.get("usage").and_then(Usage::parse),
                                stop_reason: data.get("stop_reason").and_then(serde_json::Value::as_str).map(str::to_owned),
                                error_message: None,
                                provider_id: identity.map(|parts| parts.0.to_owned()),
                                api: identity.map(|parts| parts.1.to_owned()),
                            };
                            return;
                        }
                        "run.failed" => {
                            guard.settle();
                            let err = data.get("error").unwrap_or(data);
                            let error = Error::provider_stream(
                                err.get("message").and_then(serde_json::Value::as_str).unwrap_or("agent run failed"),
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
                        "run.cancelled" => {
                            guard.settle();
                            Err(Error::stream(crate::error::StreamErrorKind::Aborted, "agent run cancelled"))?;
                        }
                        _ => {}
                    }
                }
            }

        }
    }
}

const OAPX_AGENT_ENDPOINT: &str = "oapx.agent";

pub(crate) fn open_payload(
    session_id: &str,
    request: &ExecutionRequest,
    endpoint: &str,
    provides_tools: bool,
    source: &str,
) -> Result<serde_json::Value> {
    let mut payload = json!({ "session_id": session_id });
    let fields = payload
        .as_object_mut()
        .ok_or_else(|| Error::invalid_request("session open payload"))?;
    if !request.tools.is_empty() && !provides_tools {
        return Err(crate::oap::unsupported(
            "client tools on an endpoint that does not advertise action.tools.provide",
        ));
    }
    if !request.tools.is_empty() {
        let mut provided = Vec::with_capacity(request.tools.len());
        for tool in &request.tools {
            let mut definition = crate::oap::tool_definition(tool)?;
            if let Some(entry) = definition.as_object_mut() {
                entry.insert("execution_owner".to_owned(), json!(SDK_PARTICIPANT));
                if !source.is_empty() {
                    entry.insert("source".to_owned(), json!(source));
                }
            }
            provided.push(definition);
        }
        fields.insert("tools".to_owned(), serde_json::Value::Array(provided));
    }
    if request.options.reasoning_effort == Some(crate::types::ReasoningEffort::Minimal) {
        return Err(crate::oap::unsupported(
            "minimal reasoning, which the agent loop runs as low",
        ));
    }
    if let Some(effort) = &request.options.reasoning_effort {
        fields.insert(
            "reasoning_level".to_owned(),
            serde_json::to_value(effort).map_err(|err| Error::invalid_request(err.to_string()))?,
        );
    }
    let mut settings = serde_json::Map::new();
    settings.insert("user_input".to_owned(), json!(false));
    if let Some(max_tokens) = request.options.max_tokens {
        if max_tokens == 0 {
            return Err(Error::invalid_request("max_tokens must be at least 1"));
        }
        if endpoint != OAPX_AGENT_ENDPOINT {
            return Err(crate::oap::unsupported(
                "an output limit on an endpoint other than the oapx agent loop",
            ));
        }
        settings.insert("output".to_owned(), json!(max_tokens));
    }
    if endpoint == OAPX_AGENT_ENDPOINT {
        fields.insert("metadata".to_owned(), json!({ "oapx": settings }));
    }
    Ok(payload)
}

impl AgentApi {
    async fn answer_call(
        &self,
        frame: &Frame,
        request: &ExecutionRequest,
        run_id: &str,
    ) -> Result<bool> {
        let data = frame.payload();
        let text = |key: &str| {
            data.get(key)
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default()
                .to_owned()
        };
        if frame.kind == "action.call.resolve.response" {
            if data.get("accepted").and_then(serde_json::Value::as_bool) == Some(false)
                && text("reason") != "already_resolved"
            {
                return Err(Error::protocol(
                    format!("the endpoint refused a tool result: {}", text("reason")),
                    Some("malformed_response"),
                ));
            }
            return Ok(false);
        }
        if text("execution_owner") != SDK_PARTICIPANT {
            return Ok(false);
        }
        let session_id = text("session_id");
        let invocation = ToolInvocation {
            tool_call_id: text("tool_call_id"),
            tool_name: text("name"),
            args_json: match data.get("arguments_json") {
                Some(serde_json::Value::String(raw)) => raw.clone(),
                Some(value) => value.to_string(),
                None => "{}".to_owned(),
            },
        };
        let mut answer = json!({
            "interaction_id": text("interaction_id"),
            "session_id": session_id,
            "run_id": run_id,
            "tool_call_id": invocation.tool_call_id,
            "requested_by": text("requested_by"),
            "responded_by": SDK_PARTICIPANT,
        });
        let tool_name = invocation.tool_name.clone();
        let outcome = match request.tools.iter().find(|tool| tool.name() == tool_name) {
            Some(tool) => tool.call(invocation).await,
            None => None,
        };
        if let Some(fields) = answer.as_object_mut() {
            match outcome {
                Some(Ok(result)) => {
                    fields.insert("result".to_owned(), json!(result));
                }
                Some(Err(message)) => {
                    let message = if message.is_empty() {
                        "the tool failed".to_owned()
                    } else {
                        message
                    };
                    fields.insert(
                        "error".to_owned(),
                        json!({ "code": "tool_failed", "message": message }),
                    );
                }
                None => {
                    fields.insert("error".to_owned(), json!({ "code": "tool_unavailable", "message": format!("Tool '{tool_name}' is not executable by this client") }));
                }
            }
        }
        self.transport.send_oap_scoped(
            AGENT_PROFILE,
            "action.call.resolve.request",
            &crate::ids::new_ulid(),
            answer,
            &[("session_id", &session_id), ("run_id", run_id)],
        )?;
        Ok(true)
    }
}

fn validate_oap_agent_request(request: &ExecutionRequest) -> Result<()> {
    if request.model_ref.is_empty() {
        if request
            .options
            .session_id
            .as_deref()
            .is_none_or(str::is_empty)
        {
            return Err(Error::invalid_request(
                "session-selected run requires a nonempty session_id",
            ));
        }
        return Ok(());
    }
    // OAP session IDs are opaque strings, not NanoIDs.
    let mut validated = request.clone();
    validated.options.session_id = None;
    validated.validate()?;
    if request
        .options
        .session_id
        .as_deref()
        .is_some_and(str::is_empty)
    {
        return Err(Error::invalid_request("OAP session_id cannot be empty"));
    }
    Ok(())
}

struct OapRunGuard {
    transport: Arc<Transport>,
    session_id: String,
    run_id: String,
    settled: bool,
}

impl OapRunGuard {
    fn new(transport: Arc<Transport>, session_id: String, run_id: String) -> Self {
        Self {
            transport,
            session_id,
            run_id,
            settled: false,
        }
    }
    fn settle(&mut self) {
        self.settled = true;
    }
}

impl Drop for OapRunGuard {
    fn drop(&mut self) {
        if !self.settled {
            let _ = self.transport.send_oap(
                AGENT_PROFILE,
                "run.cancel.request",
                &crate::ids::new_ulid(),
                json!({ "session_id": self.session_id, "run_id": self.run_id }),
                Some(("session_id", &self.session_id)),
            );
        }
    }
}

#[cfg(test)]
mod open_payload_tests {
    use super::*;
    use crate::types::Tool;

    #[test]
    fn an_endpoint_that_would_ignore_tools_or_an_output_limit_refuses_them_client_side() {
        let with_tool = ExecutionRequest::prompt("fixture/openai-responses@mock", "hello")
            .with_tool(Tool::new("lookup", "look", "{}"));
        assert!(open_payload("s", &with_tool, "oapx.agent-control", false, "").is_err());
        let limited =
            ExecutionRequest::prompt("fixture/openai-responses@mock", "hello").with_max_tokens(10);
        assert!(open_payload("s", &limited, "oapx.agent-control", true, "").is_err());
        let plain = ExecutionRequest::prompt("fixture/openai-responses@mock", "hello");
        let payload =
            open_payload("s", &plain, "oapx.agent-control", false, "").expect("plain open");
        assert!(payload.get("metadata").is_none());
    }
}
