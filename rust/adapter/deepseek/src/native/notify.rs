use oap_adapter::gojson::go_quote;

use super::stream::{strict_object, valid_block, INVALID};
use super::validate::valid_blocks_raw;
use super::{
    decode_strict, InitializeParams, InitializeResult, SessionEventNotification,
    SessionPromptParams, SessionStatusNotification, SubagentFinishedNotification,
    SubagentStartedNotification, NOTIFY_SESSION_EVENT, NOTIFY_SESSION_STATUS,
    NOTIFY_SUBAGENT_FINISHED, NOTIFY_SUBAGENT_STARTED, SERVER_NAME, SERVER_VERSION,
};

const MAX_SAFE_INTEGER: i64 = 9_007_199_254_740_991;

fn invalid(detail: impl std::fmt::Display) -> String {
    format!("{INVALID}: {detail}")
}

fn require(ok: bool, what: &str) -> Result<(), String> {
    if ok {
        Ok(())
    } else {
        Err(invalid(what))
    }
}

#[derive(Debug)]
pub enum Notification {
    SessionEvent(SessionEventNotification),
    SessionStatus(SessionStatusNotification),
    SubagentStarted(SubagentStartedNotification),
    SubagentFinished(SubagentFinishedNotification),
}

pub fn decode_notification(method: &str, data: &[u8]) -> Result<Notification, String> {
    let notification = match method {
        NOTIFY_SESSION_EVENT => decode_strict(data).map(Notification::SessionEvent),
        NOTIFY_SESSION_STATUS => decode_strict(data).map(Notification::SessionStatus),
        NOTIFY_SUBAGENT_STARTED => decode_strict(data).map(Notification::SubagentStarted),
        NOTIFY_SUBAGENT_FINISHED => decode_strict(data).map(Notification::SubagentFinished),
        other => return Err(invalid(format!("unknown notification {}", go_quote(other)))),
    }
    .map_err(invalid)?;
    match &notification {
        Notification::SessionEvent(value) => {
            require(
                !value.session_id.is_empty(),
                "session.event sessionId is required",
            )?;
            value.event.validate()?;
        }
        Notification::SessionStatus(value) => require(
            !value.session_id.is_empty() && (value.status == "idle" || value.status == "running"),
            "invalid session.status",
        )?,
        Notification::SubagentStarted(value) => require(
            !value.parent_session_id.is_empty() && !value.child_session_id.is_empty(),
            "invalid subagent.started",
        )?,
        Notification::SubagentFinished(value) => {
            require(
                !value.provider.is_empty()
                    && !value.agent_id.is_empty()
                    && !value.parent_session_id.is_empty()
                    && !value.child_session_id.is_empty(),
                "invalid subagent.finished identity",
            )?;
            require(
                value.status == "ok" || value.status == "error",
                "invalid subagent status",
            )?;
            require(
                matches!(
                    value.stop_reason.as_str(),
                    "completed" | "aborted" | "error" | "max-tokens" | "refusal"
                ),
                "invalid subagent stopReason",
            )?;
            let object = strict_object(data).unwrap_or_default();
            if let Some(raw) = object.get("lastAssistantMessage") {
                require(
                    valid_blocks_raw(raw.get().as_bytes()),
                    "invalid subagent.finished lastAssistantMessage",
                )?;
            }
        }
    }
    Ok(notification)
}

pub fn validate_initialize_params(value: &InitializeParams) -> Result<(), String> {
    require(
        !value.cwd.is_empty() && !value.provider.is_empty() && !value.model.is_empty(),
        "initialize cwd, provider, and model are required",
    )?;
    require(
        value
            .max_tokens
            .is_none_or(|tokens| tokens > 0 && tokens <= MAX_SAFE_INTEGER),
        "maxTokens must be a positive safe integer",
    )
}

pub fn validate_initialize_result(value: &InitializeResult) -> Result<(), String> {
    if value.server_info.name != SERVER_NAME || value.server_info.version != *SERVER_VERSION {
        return Err(invalid(format!(
            "unexpected serverInfo {}/{}",
            go_quote(&value.server_info.name),
            go_quote(&value.server_info.version)
        )));
    }
    Ok(())
}

pub fn validate_prompt(value: &SessionPromptParams) -> Result<(), String> {
    let Some(blocks) = &value.content_blocks else {
        return Err(invalid("prompt sessionId and contentBlocks are required"));
    };
    require(
        !value.session_id.is_empty(),
        "prompt sessionId and contentBlocks are required",
    )?;
    require(
        blocks.iter().all(valid_block),
        "prompt contentBlocks are invalid",
    )
}
