use oap_adapter::gojson::go_quote;
use oap_protocol::{ContentPart, ContentPartType, MessageContent, ToolCallId};
use serde::Deserialize;
use serde_json::value::RawValue;

use crate::native::{decode_strict, AssistantStreamRecord, ContentBlock};

#[derive(Default, Deserialize)]
#[serde(default, deny_unknown_fields)]
struct Chunk {
    #[serde(rename = "type")]
    kind: String,
    #[allow(dead_code)]
    index: i64,
    text: String,
}

fn text(text: String) -> ContentPart {
    ContentPart {
        part_type: ContentPartType::TEXT,
        text,
        ..ContentPart::default()
    }
}

fn reasoning(reasoning: String) -> ContentPart {
    ContentPart {
        part_type: ContentPartType::REASONING,
        reasoning,
        ..ContentPart::default()
    }
}

pub fn chunk_part(raw: &RawValue) -> Option<ContentPart> {
    let chunk: Chunk = decode_strict(raw.get().as_bytes()).ok()?;
    match chunk.kind.as_str() {
        "text-delta" => Some(text(chunk.text)),
        "reasoning-delta" => Some(reasoning(chunk.text)),
        _ => None,
    }
}

pub fn stream_parts(records: &[AssistantStreamRecord]) -> Vec<ContentPart> {
    let mut parts = Vec::new();
    for record in records {
        match record.kind.as_str() {
            "text-chunks" => parts.extend(record.texts.iter().cloned().map(text)),
            "reasoning-chunks" => parts.extend(record.texts.iter().cloned().map(reasoning)),
            "chunk" => parts.extend(record.chunk.as_deref().and_then(chunk_part)),
            _ => {}
        }
    }
    parts
}

pub fn final_content(
    blocks: &[ContentBlock],
    tool: &dyn Fn(&str) -> Option<(ToolCallId, String)>,
) -> Result<MessageContent, String> {
    let mut parts = Vec::new();
    for block in blocks {
        match block.kind.as_str() {
            "text" => parts.push(text(block.text.clone())),
            "reasoning" => parts.push(reasoning(block.text.clone())),
            "tool-call" => {
                let unknown = || {
                    format!(
                        "final message references unknown tool {}",
                        go_quote(&block.id)
                    )
                };
                let (id, _) = tool(&block.id)
                    .filter(|(_, name)| *name == block.name)
                    .ok_or_else(unknown)?;
                let arguments = match block.arguments.as_str() {
                    "" => None,
                    raw => Some(RawValue::from_string(raw.into()).map_err(|_| {
                        format!(
                            "final message carries invalid arguments for tool {}",
                            go_quote(&block.id)
                        )
                    })?),
                };
                parts.push(ContentPart {
                    part_type: ContentPartType::TOOL_CALL,
                    tool_call_id: Some(id),
                    name: block.name.clone(),
                    arguments_json: arguments,
                    ..ContentPart::default()
                });
            }
            "tool-result" | "image" => {
                return Err(format!(
                    "assistant message contains invalid {} block",
                    block.kind
                ))
            }
            other => {
                return Err(format!(
                    "unknown assistant content block {}",
                    go_quote(other)
                ))
            }
        }
    }
    Ok(match parts.as_slice() {
        [] => MessageContent::from_text(""),
        [only] if only.part_type == ContentPartType::TEXT => MessageContent::from_text(&only.text),
        _ => MessageContent::from_parts(&parts),
    })
}
