//! Shared request building for the `provider` and `agent` namespaces.

use crate::error::{Error, Result};
use crate::types::{ChatMessage, RunOptions, Tool};

const MAX_MODEL_REF_LEN: usize = 4096;
const MAX_IDENTIFIER_LEN: usize = 256;
const MAX_MODEL_FIELD_LEN: usize = 512;

/// A `provider.complete` / `provider.stream` / `agent.run` / `agent.stream` request.
#[derive(Debug, Clone, Default)]
pub struct ExecutionRequest {
    /// The opaque model handle from [`crate::ModelDescriptor::model_ref`].
    pub model_ref: String,
    /// The conversation. `system` and `developer` turns are hoisted into the
    /// request's system prompt rather than sent as messages.
    pub messages: Vec<ChatMessage>,
    /// Tools the model may call. On the agent path, tools with a handler execute
    /// in this process.
    pub tools: Vec<Tool>,
    /// Per-request knobs.
    pub options: RunOptions,
}

impl ExecutionRequest {
    /// A request for `model_ref` with `messages`.
    pub fn new(model_ref: impl Into<String>, messages: Vec<ChatMessage>) -> Self {
        Self {
            model_ref: model_ref.into(),
            messages,
            tools: Vec::new(),
            options: RunOptions::default(),
        }
    }

    /// A single-turn user request.
    pub fn prompt(model_ref: impl Into<String>, prompt: impl Into<String>) -> Self {
        Self::new(model_ref, vec![ChatMessage::user(prompt.into())])
    }

    /// Attaches tools.
    pub fn with_tools(mut self, tools: Vec<Tool>) -> Self {
        self.tools = tools;
        self
    }

    /// Attaches one tool.
    pub fn with_tool(mut self, tool: Tool) -> Self {
        self.tools.push(tool);
        self
    }

    /// Replaces the request options.
    pub fn with_options(mut self, options: RunOptions) -> Self {
        self.options = options;
        self
    }

    /// Sets the output token ceiling.
    pub fn with_max_tokens(mut self, max_tokens: u32) -> Self {
        self.options.max_tokens = Some(max_tokens);
        self
    }

    pub(crate) fn validate(&self) -> Result<()> {
        if self.model_ref.is_empty() {
            return Err(Error::invalid_request("request requires a model_ref"));
        }
        if self.model_ref.len() > MAX_MODEL_REF_LEN {
            return Err(Error::protocol(
                format!("model_ref exceeds maximum length of {MAX_MODEL_REF_LEN} characters"),
                Some("invalid_request"),
            ));
        }
        match split_model_ref(&self.model_ref) {
            Some((provider, api, model_id)) => validate_segments(provider, api, model_id)?,
            None if self.model_ref.len() > MAX_MODEL_FIELD_LEN => {
                return Err(Error::protocol(
                    format!(
                        "model_ref exceeds maximum length of {MAX_MODEL_FIELD_LEN} characters for opaque refs"
                    ),
                    Some("invalid_request"),
                ))
            }
            None => {}
        }
        self.options.validate()
    }
}

fn validate_segments(provider: &str, api: &str, model_id: &str) -> Result<()> {
    if provider.len() > MAX_IDENTIFIER_LEN {
        return Err(Error::protocol(
            format!("model_ref provider segment exceeds maximum length of {MAX_IDENTIFIER_LEN} characters"),
            Some("invalid_request"),
        ));
    }
    if api.len() > MAX_IDENTIFIER_LEN {
        return Err(Error::protocol(
            format!(
                "model_ref api segment exceeds maximum length of {MAX_IDENTIFIER_LEN} characters"
            ),
            Some("invalid_request"),
        ));
    }
    if model_id.len() > MAX_MODEL_FIELD_LEN {
        return Err(Error::protocol(
            format!(
                "model_ref model_id segment exceeds maximum length of {MAX_MODEL_FIELD_LEN} characters"
            ),
            Some("invalid_request"),
        ));
    }
    Ok(())
}

/// Splits `provider/api@model_id`, without decoding the percent-encoded model id.
///
/// `model_ref` is opaque to *consumers*, and nothing in this crate's public API
/// parses one; the SDK reads only the provider id from it, to name the
/// provider in a typed auth error. A ref that does not split yields `None`.
fn split_model_ref(model_ref: &str) -> Option<(&str, &str, &str)> {
    let slash = model_ref.find('/')?;
    if slash == 0 {
        return None;
    }
    let at = model_ref[slash + 1..].find('@')? + slash + 1;
    let provider = &model_ref[..slash];
    let api = &model_ref[slash + 1..at];
    let model_id = &model_ref[at + 1..];
    if provider.is_empty() || api.is_empty() {
        return None;
    }
    Some((provider, api, model_id))
}

/// The provider a failure without an explicit `provider_id` is attributed to.
pub(crate) fn provider_id_from_ref(model_ref: &str) -> Option<String> {
    split_model_ref(model_ref).map(|(provider, _, _)| provider.to_owned())
}

#[cfg(test)]
mod tests {

    use super::*;

    #[test]
    fn refs_split_into_their_three_segments() {
        assert_eq!(
            split_model_ref("anthropic/anthropic-messages@claude-sonnet-4-5"),
            Some(("anthropic", "anthropic-messages", "claude-sonnet-4-5"))
        );
        assert_eq!(split_model_ref("opaque-handle"), None);
        assert_eq!(split_model_ref("/api@model"), None);
        assert_eq!(split_model_ref("provider/@model"), None);
        assert_eq!(split_model_ref("provider/api"), None);
    }

    #[test]
    fn requests_are_validated_before_the_wire() {
        assert!(ExecutionRequest::new("", vec![]).validate().is_err());

        let long = "x".repeat(MAX_MODEL_REF_LEN + 1);
        let err = ExecutionRequest::new(long, vec![]).validate().unwrap_err();
        assert_eq!(err.code(), Some("invalid_request"));

        let long_model_id = format!("p/a@{}", "x".repeat(MAX_MODEL_FIELD_LEN + 1));
        let err = ExecutionRequest::new(long_model_id, vec![])
            .validate()
            .unwrap_err();
        assert!(err.message().contains("model_id segment"), "{err}");

        let long_opaque = "x".repeat(MAX_MODEL_FIELD_LEN + 1);
        let err = ExecutionRequest::new(long_opaque, vec![])
            .validate()
            .unwrap_err();
        assert!(err.message().contains("opaque refs"), "{err}");

        assert!(ExecutionRequest::prompt("p/a@m", "hi").validate().is_ok());
    }

    #[test]
    fn provider_attribution_falls_back_to_the_refs_first_segment() {
        assert_eq!(
            provider_id_from_ref("anthropic/anthropic-messages@m"),
            Some("anthropic".to_owned())
        );
        assert_eq!(provider_id_from_ref("opaque"), None);
    }
}
