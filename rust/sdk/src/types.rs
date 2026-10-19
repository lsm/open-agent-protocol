use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use serde::{Deserialize, Serialize};
use serde_json::{json, Value};

use crate::error::{Error, Result};
use crate::ids::is_session_id;
use crate::wire::{opt_string, opt_string_any, str_or_empty};

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Role {
    System,
    Developer,
    User,
    Assistant,
    Tool,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ContentPart {
    Text {
        text: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        text_signature: Option<String>,
    },
    Thinking {
        thinking: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        thinking_signature: Option<String>,
    },
    Image {
        data: String,
        mime_type: String,
    },
    ToolCall {
        tool_call_id: String,
        name: String,
        arguments_json: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        carry: Option<String>,
    },
    ToolResult {
        tool_call_id: String,
        tool_name: String,
        content: String,
        #[serde(default, skip_serializing_if = "Option::is_none")]
        is_error: Option<bool>,
    },
}

impl ContentPart {
    pub fn text(text: impl Into<String>) -> Self {
        Self::Text {
            text: text.into(),
            text_signature: None,
        }
    }

    pub fn as_text(&self) -> &str {
        match self {
            Self::Text { text, .. } => text,
            Self::Thinking { thinking, .. } => thinking,
            Self::ToolResult { content, .. } => content,
            Self::Image { .. } | Self::ToolCall { .. } => "",
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(untagged)]
pub enum Content {
    Text(String),
    Parts(Vec<ContentPart>),
}

impl Content {
    pub fn as_prompt_text(&self) -> String {
        match self {
            Self::Text(text) => text.clone(),
            Self::Parts(parts) => parts
                .iter()
                .map(ContentPart::as_text)
                .filter(|text| !text.is_empty())
                .collect::<Vec<_>>()
                .join("\n"),
        }
    }

    pub fn into_parts(self) -> Vec<ContentPart> {
        match self {
            Self::Text(text) => vec![ContentPart::text(text)],
            Self::Parts(parts) => parts,
        }
    }
}

impl From<String> for Content {
    fn from(value: String) -> Self {
        Self::Text(value)
    }
}

impl From<&str> for Content {
    fn from(value: &str) -> Self {
        Self::Text(value.to_owned())
    }
}

impl From<Vec<ContentPart>> for Content {
    fn from(value: Vec<ContentPart>) -> Self {
        Self::Parts(value)
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ChatMessage {
    pub role: Role,
    pub content: Content,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,
}

impl ChatMessage {
    pub fn user(content: impl Into<Content>) -> Self {
        Self::new(Role::User, content)
    }

    pub fn system(content: impl Into<Content>) -> Self {
        Self::new(Role::System, content)
    }

    pub fn assistant(content: impl Into<Content>) -> Self {
        Self::new(Role::Assistant, content)
    }

    pub fn new(role: Role, content: impl Into<Content>) -> Self {
        Self {
            role,
            content: content.into(),
            name: None,
            tool_call_id: None,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ReasoningEffort {
    Off,
    Minimal,
    Low,
    Medium,
    High,
    Xhigh,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum AuthRetryPolicy {
    #[default]
    Manual,
    AutoOnce,
}

#[derive(Debug, Clone, Default)]
pub struct RunOptions {
    pub temperature: Option<f64>,
    pub max_tokens: Option<u32>,
    pub reasoning_effort: Option<ReasoningEffort>,
    pub auth_retry_policy: Option<AuthRetryPolicy>,
    pub session_id: Option<String>,
    pub metadata: Option<std::collections::BTreeMap<String, String>>,
}

impl RunOptions {
    pub(crate) fn validate(&self) -> Result<()> {
        if let Some(session_id) = &self.session_id {
            if !is_session_id(session_id) {
                return Err(Error::invalid_request(
                    "options.session_id must be a 21-character alphanumeric NanoID",
                ));
            }
        }
        Ok(())
    }
}

#[derive(Clone)]
pub struct Tool {
    name: String,
    description: String,
    parameters_schema_json: String,
    handler: Option<ToolHandler>,
}

type ToolHandler =
    Arc<dyn Fn(ToolInvocation) -> Pin<Box<dyn Future<Output = ToolResult> + Send>> + Send + Sync>;

#[derive(Debug, Clone)]
pub struct ToolInvocation {
    pub tool_call_id: String,
    pub tool_name: String,
    pub args_json: String,
}

impl ToolInvocation {
    pub fn args<T: serde::de::DeserializeOwned>(&self) -> Result<T> {
        let text = if self.args_json.is_empty() {
            "{}"
        } else {
            &self.args_json
        };
        serde_json::from_str(text).map_err(|err| {
            Error::invalid_request(format!(
                "tool '{}' arguments did not deserialize: {err}",
                self.tool_name
            ))
        })
    }
}

pub type ToolResult = std::result::Result<String, String>;

impl std::fmt::Debug for Tool {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Tool")
            .field("name", &self.name)
            .field("description", &self.description)
            .field("parameters_schema_json", &self.parameters_schema_json)
            .field("executable", &self.handler.is_some())
            .finish()
    }
}

impl Tool {
    pub fn new(
        name: impl Into<String>,
        description: impl Into<String>,
        parameters_schema_json: impl Into<String>,
    ) -> Self {
        Self {
            name: name.into(),
            description: description.into(),
            parameters_schema_json: parameters_schema_json.into(),
            handler: None,
        }
    }

    pub fn on_call<F, Fut>(mut self, handler: F) -> Self
    where
        F: Fn(ToolInvocation) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = ToolResult> + Send + 'static,
    {
        self.handler = Some(Arc::new(move |invocation| Box::pin(handler(invocation))));
        self
    }

    pub fn name(&self) -> &str {
        &self.name
    }

    pub fn is_executable(&self) -> bool {
        self.handler.is_some()
    }

    pub(crate) async fn call(&self, invocation: ToolInvocation) -> Option<ToolResult> {
        let handler = self.handler.clone()?;
        Some(handler(invocation).await)
    }

    pub(crate) fn serialize_for_wire(&self) -> Value {
        json!({
            "name": self.name,
            "description": self.description,
            "parameters_schema_json": self.parameters_schema_json,
        })
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
pub struct Usage {
    pub input: u64,
    pub output: u64,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cache_read: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub cache_write: Option<u64>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reasoning: Option<u64>,
}

impl Usage {
    pub(crate) fn parse(raw: &Value) -> Option<Self> {
        let object = raw.as_object()?;
        let number = |keys: [&str; 2]| -> Option<u64> {
            keys.iter()
                .find_map(|key| object.get(*key).and_then(Value::as_u64))
        };
        let input = number(["input", "input_tokens"])?;
        let output = number(["output", "output_tokens"])?;
        Some(Self {
            input,
            output,
            cache_read: number(["cache_read_input_tokens", "cache_read"]),
            cache_write: number(["cache_write_input_tokens", "cache_write"]),
            reasoning: number(["reasoning_tokens", "reasoning"]),
        })
    }

    pub fn saturating_add(self, other: Self) -> Self {
        let add_option = |left: Option<u64>, right: Option<u64>| match (left, right) {
            (None, None) => None,
            _ => Some(left.unwrap_or(0).saturating_add(right.unwrap_or(0))),
        };
        Self {
            input: self.input.saturating_add(other.input),
            output: self.output.saturating_add(other.output),
            cache_read: add_option(self.cache_read, other.cache_read),
            cache_write: add_option(self.cache_write, other.cache_write),
            reasoning: add_option(self.reasoning, other.reasoning),
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
pub struct CompletionResponse {
    pub content: Content,
    pub usage: Option<Usage>,
    pub provider_id: String,
    pub api: String,
    pub model_id: String,
    pub stop_reason: Option<String>,
    pub error_message: Option<String>,
}

impl CompletionResponse {
    pub fn text(&self) -> String {
        self.content.as_prompt_text()
    }

    pub fn tool_calls(&self) -> Vec<&ContentPart> {
        match &self.content {
            Content::Text(_) => Vec::new(),
            Content::Parts(parts) => parts
                .iter()
                .filter(|part| matches!(part, ContentPart::ToolCall { .. }))
                .collect(),
        }
    }

    pub(crate) fn from_message_and_terminal(message: &Value, terminal: &Value) -> Self {
        let usage = message
            .get("usage")
            .and_then(Usage::parse)
            .or_else(|| terminal.get("usage").and_then(Usage::parse))
            .or_else(|| Usage::parse(message))
            .or_else(|| Usage::parse(terminal));

        Self {
            content: parse_content(message.get("content")),
            usage,
            provider_id: opt_string_any(message, &["provider_id", "provider"])
                .or_else(|| opt_string_any(terminal, &["provider_id", "provider"]))
                .unwrap_or_default(),
            api: opt_string(message, "api")
                .or_else(|| opt_string(terminal, "api"))
                .unwrap_or_default(),
            model_id: opt_string_any(message, &["model_id", "model"])
                .or_else(|| opt_string_any(terminal, &["model_id", "model"]))
                .unwrap_or_default(),
            stop_reason: opt_string_any(message, &["stop_reason", "reason"])
                .or_else(|| opt_string_any(terminal, &["stop_reason", "reason"])),
            error_message: opt_string(message, "error_message")
                .or_else(|| opt_string(terminal, "error_message")),
        }
    }
}

pub(crate) fn parse_content(raw: Option<&Value>) -> Content {
    match raw {
        Some(Value::String(text)) => Content::Text(text.clone()),
        Some(Value::Array(items)) => Content::Parts(
            items
                .iter()
                .map(
                    |item| match serde_json::from_value::<ContentPart>(item.clone()) {
                        Ok(part) => part,
                        Err(_) => normalize_loose_content_part(item),
                    },
                )
                .collect(),
        ),
        _ => Content::Text(String::new()),
    }
}

fn normalize_loose_content_part(item: &Value) -> ContentPart {
    match item.get("type").and_then(Value::as_str) {
        Some("tool_call") => ContentPart::ToolCall {
            tool_call_id: opt_string_any(item, &["tool_call_id", "id"]).unwrap_or_default(),
            name: str_or_empty(item, "name"),
            arguments_json: str_or_empty(item, "arguments_json"),
            carry: opt_string(item, "carry"),
        },
        Some("thinking") => ContentPart::Thinking {
            thinking: str_or_empty(item, "thinking"),
            thinking_signature: opt_string(item, "thinking_signature"),
        },
        Some("tool_result") => ContentPart::ToolResult {
            tool_call_id: opt_string_any(item, &["tool_call_id", "id"]).unwrap_or_default(),
            tool_name: str_or_empty(item, "tool_name"),
            content: match item.get("content") {
                Some(Value::String(text)) => text.clone(),
                Some(other) => parse_content(Some(other)).as_prompt_text(),
                None => String::new(),
            },
            is_error: item.get("is_error").and_then(Value::as_bool),
        },
        _ => ContentPart::Text {
            text: str_or_empty(item, "text"),
            text_signature: opt_string(item, "text_signature"),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn session_ids_are_validated_before_the_wire() {
        let bad = RunOptions {
            session_id: Some("too-short".to_owned()),
            ..Default::default()
        };
        assert!(bad.validate().is_err());

        let good = RunOptions {
            session_id: Some("abcdefghijklmnopqrstu".to_owned()),
            ..Default::default()
        };
        assert!(good.validate().is_ok());
    }

    #[test]
    fn usage_accepts_both_wire_spellings_and_needs_both_halves() {
        let usage = Usage::parse(&json!({"input_tokens": 3, "output_tokens": 5})).expect("usage");
        assert_eq!(usage.input, 3);
        assert_eq!(usage.output, 5);
        assert_eq!(usage.cache_read, None);

        assert!(Usage::parse(&json!({"input": 3})).is_none());

        let decision_0063 = Usage::parse(&json!({
            "input_tokens": 10,
            "output_tokens": 4,
            "cache_read_input_tokens": 6,
            "cache_write_input_tokens": 2,
            "reasoning_tokens": 3
        }))
        .expect("usage");
        assert_eq!(
            (
                decision_0063.cache_read,
                decision_0063.cache_write,
                decision_0063.reasoning
            ),
            (Some(6), Some(2), Some(3))
        );
        let vendor =
            Usage::parse(&json!({"input": 1, "output": 1, "cache_read": 7})).expect("usage");
        assert_eq!((vendor.cache_read, vendor.reasoning), (Some(7), None));
        assert!(Usage::parse(&json!("nope")).is_none());
    }

    #[test]
    fn usage_sums_preserve_absent_cache_fields() {
        let left = Usage {
            input: 1,
            output: 2,
            cache_read: None,
            cache_write: None,
            reasoning: Some(2),
        };
        let right = Usage {
            input: 3,
            output: 4,
            cache_read: Some(5),
            cache_write: None,
            reasoning: None,
        };
        let total = left.saturating_add(right);
        assert_eq!(total.input, 4);
        assert_eq!(total.output, 6);
        assert_eq!(total.cache_read, Some(5));
        assert_eq!(total.cache_write, None);
        assert_eq!(total.reasoning, Some(2));
    }

    #[test]
    fn tool_call_parts_accept_the_id_spelling() {
        let content = parse_content(Some(&json!([
            { "type": "tool_call", "id": "call-1", "name": "lookup", "arguments_json": "{}" }
        ])));
        match &content {
            Content::Parts(parts) => match &parts[0] {
                ContentPart::ToolCall { tool_call_id, .. } => assert_eq!(tool_call_id, "call-1"),
                other => panic!("unexpected part: {other:?}"),
            },
            other => panic!("unexpected content: {other:?}"),
        }
    }

    #[tokio::test]
    async fn tools_without_handlers_do_not_execute() {
        let tool = Tool::new("lookup", "look things up", "{}");
        assert!(!tool.is_executable());
        assert!(tool
            .call(ToolInvocation {
                tool_call_id: "c".into(),
                tool_name: "lookup".into(),
                args_json: "{}".into(),
            })
            .await
            .is_none());
    }

    #[tokio::test]
    async fn tool_handlers_receive_parsed_arguments() {
        let tool = Tool::new("lookup", "look things up", "{}").on_call(|invocation| async move {
            let args: serde_json::Map<String, Value> =
                invocation.args().map_err(|err| err.to_string())?;
            let city = args.get("city").and_then(Value::as_str).unwrap_or_default();
            Ok(format!("city={city}"))
        });
        let result = tool
            .call(ToolInvocation {
                tool_call_id: "c".into(),
                tool_name: "lookup".into(),
                args_json: r#"{"city":"SF"}"#.into(),
            })
            .await;
        assert_eq!(result, Some(Ok("city=SF".to_owned())));
    }

    #[test]
    fn tool_arguments_reject_non_objects_through_the_typed_reader() {
        let invocation = ToolInvocation {
            tool_call_id: "c".into(),
            tool_name: "lookup".into(),
            args_json: "[1,2,3]".into(),
        };
        let parsed: Result<serde_json::Map<String, Value>> = invocation.args();
        assert!(parsed.is_err());
    }

    #[test]
    fn empty_tool_arguments_read_as_an_empty_object() {
        let invocation = ToolInvocation {
            tool_call_id: "c".into(),
            tool_name: "lookup".into(),
            args_json: String::new(),
        };
        let parsed: serde_json::Map<String, Value> = invocation.args().expect("empty is an object");
        assert!(parsed.is_empty());
    }
}
