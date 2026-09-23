//! Shared conversion between the SDK's public request types and OAP payloads.

use serde_json::{json, Value};

use crate::error::{Error, Result};
use crate::execution::ExecutionRequest;
use crate::types::{CompletionResponse, Content, ContentPart, Role, Usage};

pub(crate) fn messages(request: &ExecutionRequest) -> Result<Value> {
    let mut messages = Vec::with_capacity(request.messages.len());
    for message in &request.messages {
        let mut value = serde_json::to_value(message).map_err(|err| {
            Error::invalid_request(format!("message cannot be serialized: {err}"))
        })?;
        let Some(content) = value.get_mut("content") else {
            continue;
        };
        if let Some(parts) = content.as_array_mut() {
            for part in parts {
                let Some(obj) = part.as_object_mut() else {
                    continue;
                };
                match obj.get("type").and_then(Value::as_str) {
                    Some("thinking") => {
                        obj.insert("type".to_owned(), json!("reasoning"));
                        if let Some(text) = obj.remove("thinking") {
                            obj.insert("reasoning".to_owned(), text);
                        }
                        if let Some(signature) = obj.remove("thinking_signature") {
                            obj.insert("carry".to_owned(), signature);
                        }
                    }
                    Some("tool_call") => {
                        if let Some(Value::String(args)) = obj.get("arguments_json") {
                            let parsed: Value = serde_json::from_str(args).map_err(|err| {
                                Error::invalid_request(format!(
                                    "tool arguments are not JSON: {err}"
                                ))
                            })?;
                            obj.insert("arguments_json".to_owned(), parsed);
                        }
                    }
                    Some("tool_result") => {
                        if let Some(result) = obj.remove("content") {
                            obj.insert("result".to_owned(), result);
                        }
                        obj.remove("tool_name");
                    }
                    Some("image") => return Err(unsupported("inline image content")),
                    _ => {}
                }
            }
        }
        if let Some(obj) = value.as_object_mut() {
            if message.role == Role::Tool {
                let call_id = message.tool_call_id.as_deref().ok_or_else(|| {
                    Error::invalid_request("tool message requires tool_call_id on OAP")
                })?;
                let result = obj.remove("content").unwrap_or(Value::Null);
                let encoded = result
                    .as_array()
                    .and_then(|parts| parts.first())
                    .is_some_and(|part| {
                        result.as_array().is_some_and(|parts| parts.len() == 1)
                            && part.get("type").and_then(Value::as_str) == Some("tool_result")
                    });
                if encoded {
                    let existing_id = result
                        .as_array()
                        .and_then(|parts| parts.first())
                        .and_then(|part| part.get("tool_call_id"))
                        .and_then(Value::as_str);
                    if existing_id != Some(call_id) {
                        return Err(Error::invalid_request(
                            "tool result id disagrees with message tool_call_id",
                        ));
                    }
                    obj.insert("content".to_owned(), result);
                } else {
                    obj.insert("content".to_owned(), json!([{ "type": "tool_result", "tool_call_id": call_id, "result": result }]));
                }
            }
            obj.remove("name");
            obj.remove("tool_call_id");
        }
        messages.push(value);
    }
    Ok(Value::Array(messages))
}

pub(crate) fn unsupported(feature: &str) -> Error {
    Error::protocol(
        format!("OAP endpoint does not support {feature}"),
        Some("unsupported_feature"),
    )
}

/// Only explicit credential failures trigger standalone +auth and one-shot retry.
pub(crate) fn is_auth_failure(error: &Error) -> bool {
    matches!(
        error.code(),
        Some(
            "auth_required"
                | "credential_missing"
                | "credential_expired"
                | "credential_rejected"
                | "auth_expired"
        )
    )
}

pub(crate) fn response(
    final_message: &Value,
    terminal: &Value,
    model_ref: &str,
) -> Result<CompletionResponse> {
    let mut result = CompletionResponse::from_message_and_terminal(final_message, terminal);
    if let Some((provider, rest)) = model_ref.split_once('/') {
        if let Some((api, model)) = rest.split_once('@') {
            result.provider_id = provider.to_owned();
            result.api = api.to_owned();
            result.model_id = model.to_owned();
        }
    }
    result.content = content(final_message.get("content"))?;
    result.usage = terminal.get("usage").and_then(Usage::parse);
    Ok(result)
}

fn content(raw: Option<&Value>) -> Result<Content> {
    match raw {
        Some(Value::String(text)) => Ok(Content::Text(text.clone())),
        Some(Value::Array(parts)) if !parts.is_empty() => {
            let mut converted = Vec::with_capacity(parts.len());
            for part in parts {
                let required = |field: &str| -> Result<String> {
                    part.get(field)
                        .and_then(Value::as_str)
                        .map(str::to_owned)
                        .ok_or_else(|| {
                            Error::protocol(
                                format!("OAP content part is missing {field}"),
                                Some("malformed_response"),
                            )
                        })
                };
                converted.push(match part.get("type").and_then(Value::as_str) {
                    Some("text") => ContentPart::text(required("text")?),
                    Some("reasoning") => ContentPart::Thinking {
                        thinking: required("reasoning")?,
                        thinking_signature: part
                            .get("carry")
                            .and_then(Value::as_str)
                            .map(str::to_owned),
                    },
                    Some("tool_call") => ContentPart::ToolCall {
                        tool_call_id: required("tool_call_id")?,
                        name: required("name")?,
                        arguments_json: part
                            .get("arguments_json")
                            .ok_or_else(|| {
                                Error::protocol(
                                    "OAP tool call is missing arguments_json",
                                    Some("malformed_response"),
                                )
                            })?
                            .to_string(),
                        carry: part.get("carry").and_then(Value::as_str).map(str::to_owned),
                    },
                    Some("tool_result") => {
                        let result = part.get("result").ok_or_else(|| {
                            Error::protocol(
                                "OAP tool result is missing result",
                                Some("malformed_response"),
                            )
                        })?;
                        ContentPart::ToolResult {
                            tool_call_id: required("tool_call_id")?,
                            tool_name: String::new(),
                            content: result
                                .as_str()
                                .map(str::to_owned)
                                .unwrap_or_else(|| result.to_string()),
                            is_error: part.get("is_error").and_then(Value::as_bool),
                        }
                    }
                    Some("image") => {
                        let image = part.get("image").ok_or_else(|| {
                            Error::protocol(
                                "OAP image part is missing image",
                                Some("malformed_response"),
                            )
                        })?;
                        if image.get("url").is_some() {
                            return Err(unsupported("URL image content in an OAP response"));
                        }
                        ContentPart::Image {
                            data: image
                                .get("data")
                                .and_then(Value::as_str)
                                .ok_or_else(|| {
                                    Error::protocol(
                                        "OAP image is missing data",
                                        Some("malformed_response"),
                                    )
                                })?
                                .to_owned(),
                            mime_type: image
                                .get("media_type")
                                .and_then(Value::as_str)
                                .ok_or_else(|| {
                                    Error::protocol(
                                        "OAP image is missing media_type",
                                        Some("malformed_response"),
                                    )
                                })?
                                .to_owned(),
                        }
                    }
                    _ => return Err(unsupported("unknown OAP response content part")),
                });
            }
            Ok(Content::Parts(converted))
        }
        _ => Err(Error::protocol(
            "OAP assistant message has no valid content",
            Some("malformed_response"),
        )),
    }
}
