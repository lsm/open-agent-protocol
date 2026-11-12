use oap_protocol::{ContentPart, MessageContent, ToolCallId};
use serde_json::value::RawValue;

use crate::native::{AssistantStreamRecord, ContentBlock};

pub fn chunk_part(_raw: &RawValue) -> Option<ContentPart> {
    None
}

pub fn stream_parts(_records: &[AssistantStreamRecord]) -> Vec<ContentPart> {
    Vec::new()
}

pub fn final_content(
    _blocks: &[ContentBlock],
    _tool: &dyn Fn(&str) -> Option<(ToolCallId, String)>,
) -> Result<MessageContent, String> {
    Err(String::new())
}
