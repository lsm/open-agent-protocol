use std::collections::BTreeMap;

use oap_adapter::gojson::{
    go_quote, reject_duplicate_keys, replace_lone_surrogates, strings, structure, zero,
};
use serde::{Deserialize, Deserializer};
use serde_json::value::RawValue;

use super::{decode_strict, raw_string, AssistantMessage, ContentBlock, Raw, TokenUsage};

pub const INVALID: &str = "deepseek native: invalid pinned message";

type Object = BTreeMap<String, Box<RawValue>>;

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AssistantAttempt {
    #[serde(default, deserialize_with = "zero")]
    pub turn: i64,
    #[serde(default, deserialize_with = "zero")]
    pub step: i64,
    #[serde(default, deserialize_with = "records")]
    pub stream: Vec<AssistantStreamRecord>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct AssistantMessageEvent {
    #[serde(default, deserialize_with = "zero")]
    pub turn: i64,
    #[serde(default, deserialize_with = "zero")]
    pub step: i64,
    #[serde(default, deserialize_with = "structure")]
    pub message: AssistantMessage,
    #[serde(default, deserialize_with = "records")]
    pub stream: Vec<AssistantStreamRecord>,
    #[serde(default)]
    pub usage: Option<TokenUsage>,
}

#[derive(Clone, Debug, Default)]
pub struct AssistantStreamRecord {
    pub kind: String,
    pub index: i64,
    pub texts: Vec<String>,
    pub id: String,
    pub name: String,
    pub args: Vec<String>,
    pub chunk: Raw,
}

fn records<'de, D: Deserializer<'de>>(
    deserializer: D,
) -> Result<Vec<AssistantStreamRecord>, D::Error> {
    let items = Option::<Vec<Box<RawValue>>>::deserialize(deserializer)?;
    items
        .unwrap_or_default()
        .iter()
        .map(|item| stream_record(item.get()).map_err(serde::de::Error::custom))
        .collect()
}

fn ints<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Vec<i64>, D::Error> {
    let items = Option::<Vec<Option<i64>>>::deserialize(deserializer)?;
    Ok(items
        .unwrap_or_default()
        .into_iter()
        .map(Option::unwrap_or_default)
        .collect())
}

fn refused(detail: impl std::fmt::Display) -> String {
    format!("{INVALID}: {detail}")
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct Chunks {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    _kind: String,
    #[serde(default, deserialize_with = "zero")]
    time0: i64,
    #[serde(default, deserialize_with = "zero")]
    index: i64,
    #[serde(default, deserialize_with = "ints")]
    dt: Vec<i64>,
    #[serde(default, deserialize_with = "strings")]
    texts: Vec<String>,
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct Calls {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    _kind: String,
    #[serde(default, deserialize_with = "zero")]
    time0: i64,
    #[serde(default, deserialize_with = "zero")]
    index: i64,
    #[serde(default, deserialize_with = "ints")]
    dt: Vec<i64>,
    #[serde(default, deserialize_with = "zero")]
    id: String,
    #[serde(default, deserialize_with = "zero")]
    name: String,
    #[serde(default, deserialize_with = "strings")]
    args: Vec<String>,
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct Chunk {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    _kind: String,
    #[serde(default, deserialize_with = "zero")]
    time: i64,
    #[serde(default, deserialize_with = "oap_adapter::gojson::raw")]
    chunk: Raw,
}

pub fn stream_record(data: &str) -> Result<AssistantStreamRecord, String> {
    #[derive(Default, Deserialize)]
    struct Head {
        #[serde(default, rename = "type", deserialize_with = "zero")]
        kind: String,
    }
    let text = replace_lone_surrogates(data);
    let head =
        if text.trim_matches(|character| matches!(character, ' ' | '\t' | '\r' | '\n')) == "null" {
            Head::default()
        } else {
            serde_json::from_str::<Head>(&text).map_err(|error| error.to_string())?
        };
    let kind = head.kind;
    match kind.as_str() {
        "text-chunks" | "reasoning-chunks" => {
            let value: Chunks = decode_strict(data.as_bytes())?;
            if value.time0 < 0
                || value.index < 0
                || value.texts.is_empty()
                || value.dt.len() != value.texts.len() - 1
            {
                return Err(refused(format!("invalid {kind} record")));
            }
            Ok(AssistantStreamRecord {
                kind,
                index: value.index,
                texts: value.texts,
                ..AssistantStreamRecord::default()
            })
        }
        "tool-call-chunks" => {
            let value: Calls = decode_strict(data.as_bytes())?;
            if value.time0 < 0
                || value.index < 0
                || value.id.is_empty()
                || value.args.is_empty()
                || value.dt.len() != value.args.len() - 1
            {
                return Err(refused("invalid tool-call-chunks record"));
            }
            Ok(AssistantStreamRecord {
                kind,
                index: value.index,
                id: value.id,
                name: value.name,
                args: value.args,
                ..AssistantStreamRecord::default()
            })
        }
        "chunk" => {
            let value: Chunk = decode_strict(data.as_bytes())?;
            if value.time < 0 || !valid_stream_chunk(bytes(value.chunk.as_ref())) {
                return Err(refused("invalid chunk record"));
            }
            Ok(AssistantStreamRecord {
                kind,
                chunk: value.chunk,
                ..AssistantStreamRecord::default()
            })
        }
        other => Err(refused(format!(
            "unknown assistant stream record {}",
            go_quote(other)
        ))),
    }
}

fn bytes<R: AsRef<RawValue>>(raw: Option<&R>) -> &[u8] {
    raw.map_or(&[], |raw| raw.as_ref().get().as_bytes())
}

fn has(object: &Object, keys: &[&str]) -> bool {
    keys.iter().all(|key| object.contains_key(*key))
}

pub(crate) fn go_text(data: &[u8]) -> Option<String> {
    let text = super::go_lossy(data);
    reject_duplicate_keys(text.as_bytes()).ok()?;
    Some(replace_lone_surrogates(&text).into_owned())
}

pub(crate) fn is_json_null(text: &str) -> bool {
    text.trim_matches(|character| matches!(character, ' ' | '\t' | '\r' | '\n')) == "null"
}

pub(crate) fn strict_object(data: &[u8]) -> Option<Object> {
    let text = go_text(data)?;
    if is_json_null(&text) {
        return Some(Object::new());
    }
    serde_json::from_str(&text).ok()
}

pub(crate) fn carries_into_a_trace(raw: &str) -> bool {
    reject_duplicate_keys(raw.as_bytes()).is_ok()
}

pub(crate) fn valid_block_raw(element: &[u8]) -> bool {
    let Some(object) = strict_object(element) else {
        return false;
    };
    let Ok(kind) = raw_string(object.get("type").map(AsRef::as_ref)) else {
        return false;
    };
    let Ok(block) = decode_strict::<ContentBlock>(element) else {
        return false;
    };
    if !valid_block(&block) {
        return false;
    }
    match kind.as_str() {
        "text" | "reasoning" => has(&object, &["text"]),
        "image" => {
            has(&object, &["attachment"]) && valid_attachment(bytes(object.get("attachment")))
        }
        "tool-call" => has(&object, &["id", "name", "arguments"]),
        _ => false,
    }
}

fn offloaded_rides_image(raw: &Raw) -> bool {
    raw.as_deref().is_none_or(|raw| raw.get().trim() == "true")
}

pub(crate) fn valid_block(block: &ContentBlock) -> bool {
    let bare = block.tool_call_id.is_empty() && block.content.is_none() && block.is_error.is_none();
    match block.kind.as_str() {
        "text" | "reasoning" => {
            bare && block.attachment.is_none()
                && block.id.is_empty()
                && block.name.is_empty()
                && block.arguments.is_empty()
                && block.offloaded.is_none()
        }
        "image" => {
            bare && block.text.is_empty()
                && block.id.is_empty()
                && block.name.is_empty()
                && block.arguments.is_empty()
                && offloaded_rides_image(&block.offloaded)
        }
        "tool-call" => {
            bare && block.text.is_empty()
                && block.attachment.is_none()
                && block.offloaded.is_none()
                && !block.id.is_empty()
                && !block.name.is_empty()
                && carries_into_a_trace(&block.arguments)
        }
        _ => false,
    }
}

#[derive(Default, Deserialize)]
#[serde(rename_all = "camelCase", deny_unknown_fields)]
struct Attachment {
    #[serde(default, deserialize_with = "zero")]
    attachment_id: String,
    #[serde(default, deserialize_with = "zero")]
    media_type: String,
}

fn valid_attachment(raw: &[u8]) -> bool {
    decode_strict::<Attachment>(raw).is_ok_and(|attachment| {
        !attachment.attachment_id.is_empty() && !attachment.media_type.is_empty()
    })
}

pub(crate) fn valid_usage(usage: &TokenUsage) -> bool {
    usage.input_tokens >= 0
        && usage.output_tokens >= 0
        && [
            usage.total_tokens,
            usage.cache_read_tokens,
            usage.cache_write_tokens,
            usage.reasoning_tokens,
        ]
        .iter()
        .flatten()
        .all(|value| *value >= 0)
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct LlmFailure {
    #[serde(default, deserialize_with = "zero")]
    message: String,
    #[serde(default, deserialize_with = "zero")]
    code: String,
}

pub(crate) fn valid_llm_failure(raw: &[u8]) -> bool {
    decode_strict::<LlmFailure>(raw)
        .is_ok_and(|failure| !failure.message.is_empty() && !failure.code.is_empty())
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct UsageChunk {
    #[serde(default, rename = "type", deserialize_with = "zero")]
    _kind: String,
    #[serde(default, deserialize_with = "structure")]
    usage: TokenUsage,
}

pub(crate) fn valid_stream_chunk(raw: &[u8]) -> bool {
    let Some(object) = strict_object(raw) else {
        return false;
    };
    let kind = match raw_string(object.get("type").map(AsRef::as_ref)) {
        Ok(kind) if !kind.is_empty() => kind,
        _ => return false,
    };
    let index_ok = || {
        object
            .get("index")
            .and_then(|index| serde_json::from_str::<Option<i64>>(index.get()).ok())
            .is_some_and(|index| index.unwrap_or_default() >= 0)
    };
    let non_empty = |key: &str| {
        raw_string(object.get(key).map(AsRef::as_ref)).is_ok_and(|value| !value.is_empty())
    };
    match kind.as_str() {
        "block-start" => {
            has(&object, &["index", "blockType"]) && index_ok() && non_empty("blockType")
        }
        "text-delta" | "reasoning-delta" => has(&object, &["index", "text"]) && index_ok(),
        "tool-call-delta" => {
            has(&object, &["index", "id", "argumentsDelta"]) && index_ok() && non_empty("id")
        }
        "block-end" => {
            has(&object, &["index", "block"])
                && index_ok()
                && valid_block_raw(bytes(object.get("block")))
        }
        "usage" => {
            has(&object, &["usage"])
                && decode_strict::<UsageChunk>(raw).is_ok_and(|chunk| valid_usage(&chunk.usage))
        }
        "finish" => has(&object, &["reason"]) && valid_finish_reason(bytes(object.get("reason"))),
        _ => false,
    }
}

fn valid_finish_reason(raw: &[u8]) -> bool {
    let Some(object) = strict_object(raw) else {
        return false;
    };
    let Ok(kind) = raw_string(object.get("kind").map(AsRef::as_ref)) else {
        return false;
    };
    match kind.as_str() {
        "stop" | "tool-calls" | "max-tokens" => object.len() == 1,
        "aborted" | "error" => {
            object.len() == 2
                && has(&object, &["failure"])
                && valid_llm_failure(bytes(object.get("failure")))
        }
        _ => false,
    }
}
