use oap_adapter::gojson::{go_quote, zero};
use serde::Deserialize;
use serde_json::value::RawValue;

use super::stream::{
    carries_into_a_trace, strict_object, valid_block_raw, valid_llm_failure, valid_usage, INVALID,
};
use super::{
    decode_strict, observed_only, raw_string, AssistantAttempt, AssistantMessageEvent, EpochHeader,
    Event, InboxSpliced, MessageSource, Raw, RequestContext, RequestHeader, StepBoundary,
    TodoWrite, ToolCall, ToolError, ToolResult, TurnEnd, TurnStart, UserMessage,
};

type Object = std::collections::BTreeMap<String, Box<RawValue>>;

fn invalid(detail: impl std::fmt::Display) -> String {
    format!("{INVALID}: {detail}")
}

fn bytes<R: AsRef<RawValue>>(raw: Option<&R>) -> &[u8] {
    raw.map_or(&[], |raw| raw.as_ref().get().as_bytes())
}

fn has(object: &Object, keys: &[&str]) -> bool {
    keys.iter().all(|key| object.contains_key(*key))
}

fn strict_array(data: &[u8]) -> Option<Vec<Box<RawValue>>> {
    let text = std::str::from_utf8(data).ok()?;
    if oap_adapter::gojson::reject_duplicate_keys(data).is_err() {
        return None;
    }
    serde_json::from_str::<Option<Vec<Box<RawValue>>>>(text)
        .ok()
        .map(Option::unwrap_or_default)
}

pub(crate) fn valid_blocks_raw(raw: &[u8]) -> bool {
    let text = std::str::from_utf8(raw).map(str::trim).unwrap_or_default();
    if text.is_empty() || text == "null" {
        return false;
    }
    strict_array(raw).is_some_and(|elements| {
        elements
            .iter()
            .all(|element| valid_block_raw(element.get().as_bytes()))
    })
}

fn blocks_of(fields: Option<&Object>, key: &str) -> bool {
    fields.is_some_and(|fields| valid_blocks_raw(bytes(fields.get(key))))
}

fn message_blocks(fields: Option<&Object>) -> bool {
    let message = fields
        .and_then(|fields| fields.get("message"))
        .and_then(|message| strict_object(message.get().as_bytes()));
    blocks_of(message.as_ref(), "content")
}

impl Event {
    pub fn validate(&self) -> Result<(), String> {
        if self.kind.is_empty()
            || self.seq < 0
            || self.time < 0
            || self.data.is_none()
            || self.ignorable == Some(false)
        {
            return Err(invalid("invalid event envelope"));
        }
        if observed_only(&self.kind) {
            return Ok(());
        }
        let surface = matches!(
            self.kind.as_str(),
            "user/message" | "assistant/message" | "tool/result"
        );
        if !surface && (self.source_event_seqs.is_some() || self.surface_op.is_some()) {
            return Err(invalid("surface metadata on non-surface event"));
        }
        if self.source_event_seqs.iter().flatten().any(|seq| *seq < 0) {
            return Err(invalid("invalid sourceEventSeqs"));
        }
        if let Some(op) = &self.surface_op {
            if !valid_surface_op(op.get()) {
                return Err(invalid("invalid surfaceOp"));
            }
        }
        let data = self.data_bytes();
        let kind = self.kind.as_str();
        let wrap = |error: String| invalid(format!("{kind} data: {error}"));
        let fields = || strict_object(data).filter(|_| !is_null(data));
        match kind {
            "turn/start" => {
                let value: TurnStart = decode_strict(data).map_err(wrap)?;
                if value.turn <= 0 {
                    return Err(invalid("invalid turn/start"));
                }
            }
            "turn/end" => {
                let value: TurnEnd = decode_strict(data).map_err(wrap)?;
                if value.turn <= 0 || !valid_turn_end_reason(bytes(value.reason.as_ref())) {
                    return Err(invalid("invalid turn/end"));
                }
            }
            "step/start" | "step/end" => {
                let value: StepBoundary = decode_strict(data).map_err(wrap)?;
                if value.turn <= 0 || value.step <= 0 {
                    return Err(invalid("invalid step boundary"));
                }
            }
            "user/message" => {
                let value: UserMessage = decode_strict(data).map_err(wrap)?;
                if value.id.is_empty()
                    || value.role != "user"
                    || !valid_user_source(&value.source)
                    || !blocks_of(fields().as_ref(), "content")
                {
                    return Err(invalid("invalid user/message"));
                }
            }
            "agent/inbox/spliced" => {
                let value: InboxSpliced = decode_strict(data).map_err(wrap)?;
                let fields = fields();
                if (value.target != "next-turn" && value.target != "next-step")
                    || value.start < 0
                    || (!value.outcome.is_empty() && value.outcome != "canceled")
                    || fields.is_none()
                {
                    return Err(invalid("invalid agent/inbox/spliced"));
                }
                for message in &value.inserted {
                    if message.id.is_empty()
                        || message.role != "user"
                        || !valid_user_source(&message.source)
                    {
                        return Err(invalid("invalid inserted user message"));
                    }
                }
                let inserted = fields
                    .as_ref()
                    .and_then(|fields| fields.get("inserted"))
                    .and_then(|raw| strict_array(raw.get().as_bytes()))
                    .ok_or_else(|| invalid("invalid agent/inbox/spliced inserted"))?;
                for element in inserted {
                    let message = strict_object(element.get().as_bytes());
                    if !blocks_of(message.as_ref(), "content") {
                        return Err(invalid("invalid inserted user message content"));
                    }
                }
            }
            "assistant/attempt" => {
                let value: AssistantAttempt = decode_strict(data).map_err(wrap)?;
                if value.turn <= 0 || value.step <= 0 {
                    return Err(invalid("invalid assistant/attempt"));
                }
            }
            "assistant/message" => {
                let value: AssistantMessageEvent = decode_strict(data).map_err(wrap)?;
                let fields = fields();
                let has_stream = fields
                    .as_ref()
                    .is_some_and(|fields| fields.contains_key("stream"));
                if value.turn <= 0
                    || value.step <= 0
                    || !has_stream
                    || value.message.id.is_empty()
                    || value.message.role != "assistant"
                    || value.message.source.kind != "model"
                    || !valid_source(&value.message.source)
                    || !message_blocks(fields.as_ref())
                    || value
                        .usage
                        .as_ref()
                        .is_some_and(|usage| !valid_usage(usage))
                {
                    return Err(invalid("invalid assistant/message"));
                }
            }
            "tool/call" => {
                let value: ToolCall = decode_strict(data).map_err(wrap)?;
                if value.turn <= 0
                    || value.step <= 0
                    || value.call_id.is_empty()
                    || value.name.is_empty()
                    || !carries_into_a_trace(&value.arguments)
                {
                    return Err(invalid("invalid tool/call"));
                }
            }
            "tool/result" => {
                let value: ToolResult = decode_strict(data).map_err(wrap)?;
                let answers = value.message.tool_call_id == value.message.source.call_id;
                if value.turn <= 0
                    || value.step <= 0
                    || value.message.id.is_empty()
                    || value.message.role != "tool"
                    || value.message.source.kind != "tool"
                    || !valid_source(&value.message.source)
                    || !message_blocks(fields().as_ref())
                    || !answers
                    || !valid_tool_error(value.error.as_ref())
                {
                    return Err(invalid("invalid tool/result"));
                }
            }
            "todo/write" => {
                let value: TodoWrite = decode_strict(data).map_err(wrap)?;
                let Some(todos) = value.todos else {
                    return Err(invalid("invalid todo/write"));
                };
                for todo in todos {
                    if todo.content.is_empty()
                        || !matches!(
                            todo.status.as_str(),
                            "pending" | "in_progress" | "completed"
                        )
                    {
                        return Err(invalid("invalid todo"));
                    }
                }
            }
            "request/header" => {
                let value: RequestHeader = decode_strict(data).map_err(wrap)?;
                if !valid_epoch_header(&value.header)
                    || !matches!(
                        value.reason.as_str(),
                        "initial" | "resume" | "change" | "series"
                    )
                    || value
                        .starts_series
                        .as_deref()
                        .is_some_and(|raw| raw.get().trim() != "true")
                {
                    return Err(invalid("invalid request/header"));
                }
            }
            "request/context" => {
                let value: RequestContext = decode_strict(data).map_err(wrap)?;
                if value.provider.is_empty()
                    || value.model.is_empty()
                    || value.context_window.is_some_and(|window| window <= 0)
                {
                    return Err(invalid("invalid request/context"));
                }
            }
            "session/end-seed" => end_seed(data).map_err(wrap)?,
            other => {
                if self.ignorable == Some(true) {
                    return Ok(());
                }
                return Err(invalid(format!(
                    "unknown required event {}",
                    go_quote(other)
                )));
            }
        }
        Ok(())
    }
}

fn is_null(data: &[u8]) -> bool {
    std::str::from_utf8(data).is_ok_and(|text| text.trim() == "null")
}

fn end_seed(data: &[u8]) -> Result<(), String> {
    #[derive(Default, Deserialize)]
    #[serde(deny_unknown_fields)]
    struct Empty {}
    decode_strict::<Empty>(data).map(drop).map_err(|error| {
        error.replace(
            "Go value of type native.Empty",
            "Go value of type struct {}",
        )
    })
}

fn valid_epoch_header(header: &EpochHeader) -> bool {
    if header.config.is_none() || header.system.is_some() {
        return false;
    }
    let Some(config) = strict_object(bytes(header.config.as_ref())) else {
        return false;
    };
    let provider = raw_string(config.get("provider").map(AsRef::as_ref));
    let model = raw_string(config.get("model").map(AsRef::as_ref));
    if !matches!((&provider, &model), (Ok(p), Ok(m)) if !p.is_empty() && !m.is_empty()) {
        return false;
    }
    let Some(tools) = &header.tools else {
        return true;
    };
    strict_array(tools.get().as_bytes()).is_some_and(|tools| {
        tools.iter().all(|tool| {
            strict_object(tool.get().as_bytes())
                .is_some_and(|fields| has(&fields, &["name", "description", "parameters"]))
        })
    })
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct TurnEndReason {
    #[serde(default, deserialize_with = "zero")]
    kind: String,
    #[serde(default, deserialize_with = "oap_adapter::gojson::raw")]
    reason: Raw,
    #[serde(default, deserialize_with = "oap_adapter::gojson::raw")]
    error: Raw,
}

fn valid_turn_end_reason(raw: &[u8]) -> bool {
    let Ok(reason) = decode_strict::<TurnEndReason>(raw) else {
        return false;
    };
    match reason.kind.as_str() {
        "completed" | "blocked" | "max-tokens" | "interrupted" | "forked" => {
            reason.reason.is_none() && reason.error.is_none()
        }
        "aborted" => valid_cancel_cause(bytes(reason.reason.as_ref())),
        "error" => valid_llm_failure(bytes(reason.error.as_ref())),
        _ => false,
    }
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct CancelCause {
    #[serde(default, deserialize_with = "zero")]
    kind: String,
    #[serde(default, deserialize_with = "zero")]
    reason: String,
}

fn valid_cancel_cause(raw: &[u8]) -> bool {
    let Ok(cause) = decode_strict::<CancelCause>(raw) else {
        return false;
    };
    match cause.kind.as_str() {
        "user" | "parent" | "disposed" | "legacy" => cause.reason.is_empty(),
        "hook" => !cause.reason.is_empty(),
        _ => false,
    }
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct Replace {
    #[serde(default, deserialize_with = "zero")]
    op: String,
    #[serde(default, deserialize_with = "zero")]
    start: i64,
    #[serde(default, deserialize_with = "zero")]
    end: i64,
}

fn valid_surface_op(raw: &str) -> bool {
    if let Ok(value) = serde_json::from_str::<Option<String>>(raw) {
        return value.unwrap_or_default() == "append";
    }
    decode_strict::<Replace>(raw.as_bytes()).is_ok_and(|replace| {
        replace.op == "replace" && replace.start >= 0 && replace.end >= replace.start
    })
}

pub(crate) fn valid_source(source: &MessageSource) -> bool {
    match source.kind.as_str() {
        "" => false,
        "model" => !source.provider.is_empty() && !source.model.is_empty(),
        "tool" => !source.call_id.is_empty(),
        _ => true,
    }
}

fn valid_user_source(source: &MessageSource) -> bool {
    valid_source(source) && !matches!(source.kind.as_str(), "model" | "tool" | "system-prompt")
}

fn valid_tool_error(error: Option<&ToolError>) -> bool {
    let Some(error) = error else {
        return true;
    };
    !error.name.is_empty() && !error.code.is_empty() && error.reason_text().is_some()
}
