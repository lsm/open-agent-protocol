use oap_adapter::gojson::go_quote;
use oap_adapter::jsonrpc::NotificationMessage;
use oap_adapter::subscriber::Subscriber;
use oap_adapter::{Error, EventStream};
use oap_protocol::{
    ContentDeltaPayload, ContentPart, ContentPartType, Envelope, EnvelopeType, Message,
    MessageContent, MessageId, MessageRole, ProtocolError, RunCompletedPayload, RunFailedPayload,
    RunId, RunStartedPayload, RunStatus, SessionId, SETTLED_BY_INFERRED,
};
use serde::Serialize;

use crate::native::{
    decode, AgentMessageChunk, ConfigOptionUpdate, PromptResult, SessionUpdateParams, UpdateHeader,
    METHOD_SESSION_UPDATE,
};

pub struct TextRunContext {
    pub session_id: SessionId,
    pub native_id: String,
    pub run_id: RunId,
    pub message_id: MessageId,
    pub capability_revision: Option<String>,
}

#[derive(Clone, Debug, Default, Serialize)]
pub struct TextRunState {
    pub status: RunStatus,
    pub sequence: u64,
    pub updated_at_ms: i64,
    pub closed: bool,
}

pub struct TextRun {
    context: TextRunContext,
    ids: Box<dyn FnMut(&str) -> String + Send>,
    state: TextRunState,
    text: String,
    native_message: Option<String>,
    subscriber: Subscriber,
}

impl TextRun {
    pub fn new(context: TextRunContext, ids: impl FnMut(&str) -> String + Send + 'static) -> Self {
        Self {
            context,
            ids: Box::new(ids),
            state: TextRunState::default(),
            text: String::new(),
            native_message: None,
            subscriber: Subscriber::new(64),
        }
    }

    pub fn state(&self) -> TextRunState {
        self.state.clone()
    }

    fn emit(
        &mut self,
        kind: EnvelopeType,
        payload: &impl Serialize,
        status: RunStatus,
        now_ms: i64,
    ) -> Result<(), Error> {
        let mut event =
            Envelope::new(kind, (self.ids)("event"), payload).map_err(Error::harness)?;
        event.sequence = Some(self.state.sequence + 1);
        event.timestamp_ms = Some(now_ms);
        event.session_id = Some(self.context.session_id.clone());
        event.run_id = Some(self.context.run_id.clone());
        event.capability_revision = self.context.capability_revision.clone();
        self.state.sequence += 1;
        self.state.updated_at_ms = now_ms;
        self.state.closed = status != RunStatus::RUNNING;
        self.state.status = status;
        self.subscriber.offer(event, self.state.closed);
        Ok(())
    }

    pub fn start(&mut self, now_ms: i64) -> Result<EventStream, Error> {
        if self.state.closed {
            return Err(Error::SessionClosed);
        }
        if !self.state.status.is_empty() {
            return Err(Error::RunActive);
        }
        self.emit(
            EnvelopeType::RUN_STARTED,
            &RunStartedPayload {
                session_id: self.context.session_id.clone(),
                run_id: self.context.run_id.clone(),
                status: RunStatus::RUNNING,
                started_at_ms: now_ms,
                ..RunStartedPayload::default()
            },
            RunStatus::RUNNING,
            now_ms,
        )?;
        Ok(self.subscriber.stream())
    }

    pub fn fail(
        &mut self,
        code: &str,
        message: &str,
        inferred: bool,
        now_ms: i64,
    ) -> Result<(), Error> {
        if self.state.closed {
            return Ok(());
        }
        if self.state.status.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        self.emit(
            EnvelopeType::RUN_FAILED,
            &RunFailedPayload {
                session_id: self.context.session_id.clone(),
                run_id: self.context.run_id.clone(),
                error: ProtocolError {
                    code: code.into(),
                    message: message.into(),
                    ..ProtocolError::default()
                },
                settled_by: if inferred {
                    SETTLED_BY_INFERRED.into()
                } else {
                    String::new()
                },
                ..RunFailedPayload::default()
            },
            RunStatus::FAILED,
            now_ms,
        )
    }

    pub fn observe(&mut self, notification: NotificationMessage, now_ms: i64) -> Result<(), Error> {
        if self.state.closed || notification.method != METHOD_SESSION_UPDATE {
            return Ok(());
        }
        if self.state.status.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        let params = notification
            .params
            .as_deref()
            .map_or(b"null".as_slice(), |raw| raw.get().as_bytes());
        let update = match decode::<SessionUpdateParams>(params) {
            Ok(update) if update.session_id == self.context.native_id => update,
            _ => {
                return self.fail(
                    "acp_invalid_update",
                    "malformed or foreign session/update",
                    false,
                    now_ms,
                )
            }
        };
        let raw = update
            .update
            .as_deref()
            .map_or(b"null".as_slice(), |raw| raw.get().as_bytes());
        let header = match decode::<UpdateHeader>(raw) {
            Ok(header) => header,
            Err(_) => {
                return self.fail(
                    "acp_invalid_update",
                    "malformed session update",
                    false,
                    now_ms,
                )
            }
        };
        match header.session_update.as_str() {
            "agent_message_chunk" => {
                let chunk = match decode::<AgentMessageChunk>(raw) {
                    Ok(chunk) if chunk.content.kind == "text" => chunk,
                    _ => {
                        return self.fail(
                            "acp_invalid_message_chunk",
                            "unsupported assistant chunk",
                            false,
                            now_ms,
                        )
                    }
                };
                if !chunk.message_id.is_empty() {
                    let message = self.native_message.get_or_insert(chunk.message_id.clone());
                    if *message != chunk.message_id {
                        return self.fail(
                            "acp_unavailable_message_identity",
                            "multiple native message ids are unavailable in the text-run engine",
                            false,
                            now_ms,
                        );
                    }
                }
                self.text.push_str(&chunk.content.text);
                self.emit(
                    EnvelopeType::CONTENT_DELTA,
                    &ContentDeltaPayload {
                        session_id: self.context.session_id.clone(),
                        run_id: self.context.run_id.clone(),
                        message_id: Some(self.context.message_id.clone()),
                        part: ContentPart {
                            part_type: ContentPartType::TEXT,
                            text: chunk.content.text,
                            ..ContentPart::default()
                        },
                    },
                    RunStatus::RUNNING,
                    now_ms,
                )
            }
            "config_option_update" if decode::<ConfigOptionUpdate>(raw).is_err() => self.fail(
                "acp_invalid_update",
                "malformed config option update",
                false,
                now_ms,
            ),
            "user_message_chunk"
            | "agent_thought_chunk"
            | "plan"
            | "plan_update"
            | "plan_removed"
            | "available_commands_update"
            | "current_mode_update"
            | "session_info_update"
            | "config_option_update"
            | "usage_update" => Ok(()),
            "tool_call" | "tool_call_update" => self.fail(
                "acp_unavailable_update",
                "tool updates are unavailable in the text-run engine",
                false,
                now_ms,
            ),
            name if name.starts_with('_') => Ok(()),
            _ => self.fail(
                "acp_unknown_update",
                "unknown stable ACP session update",
                false,
                now_ms,
            ),
        }
    }

    pub fn complete(&mut self, result: PromptResult, now_ms: i64) -> Result<(), Error> {
        if self.state.closed {
            return Ok(());
        }
        if self.state.status.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        match result.stop_reason.as_str() {
            "end_turn" | "max_tokens" | "max_turn_requests" => self.emit(
                EnvelopeType::RUN_COMPLETED,
                &RunCompletedPayload {
                    session_id: self.context.session_id.clone(),
                    run_id: self.context.run_id.clone(),
                    final_response: Message {
                        id: Some(self.context.message_id.clone()),
                        role: MessageRole::ASSISTANT,
                        content: MessageContent::from_text(&self.text),
                        ..Message::default()
                    },
                    stop_reason: result.stop_reason,
                    ..RunCompletedPayload::default()
                },
                RunStatus::COMPLETED,
                now_ms,
            ),
            "cancelled" => self.fail(
                "acp_unavailable_cancellation",
                "cancellation evidence is unavailable in the text-run engine",
                false,
                now_ms,
            ),
            "refusal" => self.fail("refusal", "agent refused the prompt", false, now_ms),
            other => {
                let detail = format!("unsupported ACP stop reason {}", go_quote(other));
                self.fail("acp_invalid_stop_reason", &detail, false, now_ms)
            }
        }
    }

    pub fn close(&mut self) {
        self.state.closed = true;
        self.subscriber.close();
    }
}

impl Drop for TextRun {
    fn drop(&mut self) {
        self.close();
    }
}
