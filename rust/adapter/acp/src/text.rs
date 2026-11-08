use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};

use oap_adapter::gojson::go_quote;
use oap_adapter::jsonrpc::NotificationMessage;
use oap_adapter::subscriber::Subscriber;
use oap_adapter::{Error, EventStream};
use oap_protocol::{
    ActionCallPayload, ContentDeltaPayload, ContentPart, ContentPartType, Envelope, EnvelopeId,
    EnvelopeType, Message, MessageContent, MessageId, MessageRole, ProtocolError,
    RunCompletedPayload, RunFailedPayload, RunId, RunStartedPayload, RunStatus, SessionId, Usage,
    SETTLED_BY_INFERRED,
};
use serde::Serialize;

use crate::native::{
    decode, AgentMessageChunk, ConfigOptionUpdate, PromptResult, SessionUpdateParams, UpdateHeader,
    METHOD_SESSION_UPDATE,
};
use crate::usage::{Extensions, UsageScope};

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
    pub(crate) context: TextRunContext,
    runtime: Option<crate::Runtime>,
    pub(crate) ids: Box<dyn FnMut(&str) -> String + Send>,
    state: TextRunState,
    texts: BTreeMap<MessageId, String>,
    native_messages: BTreeMap<String, MessageId>,
    subscriber: Subscriber,
    pub(crate) followers: Vec<Subscriber>,
    pub(crate) journal: Option<oap_adapter::journal::Journal>,
    usage_scope: Arc<Mutex<UsageScope>>,
    pub(crate) terminal_usage: Option<Usage>,
    extensions: Extensions,
    pub(crate) tools: crate::tools::Tools,
    pub(crate) permissions: Vec<crate::permissions::Gate>,
    pub(crate) pending: Option<Terminal>,
}

pub(crate) enum Terminal {
    Completed(String),
    Failed(String, String, bool),
    Cancelled(String),
}

impl TextRun {
    pub fn new(context: TextRunContext, ids: impl FnMut(&str) -> String + Send + 'static) -> Self {
        Self::new_in_scope(context, ids, Arc::default())
    }

    pub fn new_in_scope(
        context: TextRunContext,
        ids: impl FnMut(&str) -> String + Send + 'static,
        usage_scope: Arc<Mutex<UsageScope>>,
    ) -> Self {
        Self {
            context,
            runtime: None,
            ids: Box::new(ids),
            state: TextRunState::default(),
            texts: BTreeMap::new(),
            native_messages: BTreeMap::new(),
            subscriber: Subscriber::new(64),
            followers: Vec::new(),
            journal: None,
            usage_scope,
            terminal_usage: None,
            extensions: Extensions::new(),
            tools: crate::tools::Tools::default(),
            permissions: Vec::new(),
            pending: None,
        }
    }

    pub fn with_runtime(mut self, runtime: crate::Runtime) -> Self {
        self.set_runtime(runtime);
        self
    }

    pub(crate) fn set_runtime(&mut self, runtime: crate::Runtime) {
        let ids = runtime.clone();
        self.ids = Box::new(move |kind| ids.new_id(kind));
        self.runtime = Some(runtime);
    }

    pub(crate) fn input_time(&self, fallback: &dyn Fn() -> i64) -> i64 {
        if self.runtime.is_some() {
            self.state.updated_at_ms
        } else {
            fallback()
        }
    }

    pub(crate) fn timestamp(&self, fallback: i64) -> i64 {
        self.runtime
            .as_ref()
            .map_or(fallback, crate::Runtime::now_ms)
    }

    pub fn state(&self) -> TextRunState {
        self.state.clone()
    }

    pub(crate) fn emit(
        &mut self,
        kind: EnvelopeType,
        payload: &impl Serialize,
        status: RunStatus,
        reply: Option<EnvelopeId>,
        now_ms: i64,
    ) -> Result<EnvelopeId, Error> {
        if self.state.closed {
            return Err(Error::SessionClosed);
        }
        let mut event =
            Envelope::new(kind, (self.ids)("event"), payload).map_err(Error::harness)?;
        event.sequence = Some(self.state.sequence + 1);
        let now_ms = self.timestamp(now_ms);
        event.timestamp_ms = Some(now_ms);
        event.session_id = Some(self.context.session_id.clone());
        event.run_id = Some(self.context.run_id.clone());
        event.capability_revision = self.context.capability_revision.clone();
        if event.envelope_type.as_str().starts_with("action.") {
            event.tool_call_id = Some(
                event
                    .decode_payload::<ActionCallPayload>()
                    .map_err(Error::harness)?
                    .tool_call_id,
            );
        }
        self.state.sequence += 1;
        self.state.updated_at_ms = now_ms;
        let status = if status == RunStatus::RUNNING && self.state.status == RunStatus::CANCELLING {
            RunStatus::CANCELLING
        } else {
            status
        };
        self.state.closed = status != RunStatus::RUNNING && status != RunStatus::CANCELLING;
        self.state.status = status;
        event.in_reply_to = reply;
        if self.state.closed {
            event.extensions = self.extensions.clone();
        }
        let id = event.id.clone();
        if let Some(journal) = &self.journal {
            journal.append(event.clone(), self.state.closed);
        }
        self.followers
            .retain(|follower| follower.offer(event.clone(), self.state.closed));
        self.subscriber.offer(event, self.state.closed);
        Ok(id)
    }

    pub fn start(&mut self, now_ms: i64) -> Result<EventStream, Error> {
        if self.state.closed {
            return Err(Error::SessionClosed);
        }
        if !self.state.status.is_empty() {
            return Err(Error::RunActive);
        }
        let now_ms = self.timestamp(now_ms);
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
            None,
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
        if self.state.closed || self.pending.is_some() {
            return Ok(());
        }
        if self.state.status.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        if self.has_permissions() {
            self.pending = Some(Terminal::Failed(code.into(), message.into(), inferred));
            return Ok(());
        }
        let events = self.tools.settle(true);
        self.emit_tools(events, now_ms)?;
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
                usage: self.terminal_usage.clone(),
                ..RunFailedPayload::default()
            },
            RunStatus::FAILED,
            None,
            now_ms,
        )
        .map(|_| ())
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
        if self.pending.is_some() && header.session_update != "usage_update" {
            return Ok(());
        }
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
                    let message = match self.native_messages.get(&chunk.message_id) {
                        Some(message) => message.clone(),
                        None => {
                            let message = if self.native_messages.is_empty() {
                                self.context.message_id.clone()
                            } else {
                                format!("{}/{}", self.context.session_id, (self.ids)("message"))
                                    .into()
                            };
                            self.native_messages
                                .insert(chunk.message_id, message.clone());
                            message
                        }
                    };
                    self.context.message_id = message;
                }
                self.texts
                    .entry(self.context.message_id.clone())
                    .or_default()
                    .push_str(&chunk.content.text);
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
                    None,
                    now_ms,
                )
                .map(|_| ())
            }
            "config_option_update" if decode::<ConfigOptionUpdate>(raw).is_err() => self.fail(
                "acp_invalid_update",
                "malformed config option update",
                false,
                now_ms,
            ),
            "usage_update" => {
                self.usage_scope
                    .lock()
                    .unwrap_or_else(|p| p.into_inner())
                    .update(std::str::from_utf8(raw).map_err(Error::harness)?);
                Ok(())
            }
            "user_message_chunk"
            | "agent_thought_chunk"
            | "plan"
            | "plan_update"
            | "plan_removed"
            | "available_commands_update"
            | "current_mode_update"
            | "session_info_update"
            | "config_option_update" => Ok(()),
            "tool_call" | "tool_call_update" => {
                let (events, fault) = self.tools.observe(
                    raw,
                    header.session_update == "tool_call_update",
                    &mut *self.ids,
                );
                self.emit_tools(events, now_ms)?;
                match fault {
                    Some((code, message)) => self.fail(code, message, false, now_ms),
                    None => Ok(()),
                }
            }
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
        self.settle_usage(result.usage.as_deref());
        if self.state.closed {
            return Ok(());
        }
        if self.state.status.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        if self.pending.is_some() {
            return Ok(());
        }
        if self.has_permissions() {
            self.pending = Some(Terminal::Completed(result.stop_reason));
            return Ok(());
        }
        self.complete_stop(result.stop_reason, now_ms)
    }

    fn complete_stop(&mut self, stop: String, now_ms: i64) -> Result<(), Error> {
        if stop == "cancelled" && self.state.status == RunStatus::CANCELLING {
            return self.cancelled("ACP prompt returned cancelled", now_ms);
        }
        let events = self.tools.settle(!matches!(
            stop.as_str(),
            "end_turn" | "max_tokens" | "max_turn_requests"
        ));
        self.emit_tools(events, now_ms)?;
        match stop.as_str() {
            "end_turn" | "max_tokens" | "max_turn_requests" => self
                .emit(
                    EnvelopeType::RUN_COMPLETED,
                    &RunCompletedPayload {
                        session_id: self.context.session_id.clone(),
                        run_id: self.context.run_id.clone(),
                        final_response: Message {
                            id: Some(self.context.message_id.clone()),
                            role: MessageRole::ASSISTANT,
                            content: MessageContent::from_text(
                                self.texts
                                    .get(&self.context.message_id)
                                    .map(String::as_str)
                                    .unwrap_or_default(),
                            ),
                            ..Message::default()
                        },
                        stop_reason: stop,
                        usage: self.terminal_usage.clone(),
                        ..RunCompletedPayload::default()
                    },
                    RunStatus::COMPLETED,
                    None,
                    now_ms,
                )
                .map(|_| ()),
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

    pub(crate) fn emit_tools(
        &mut self,
        events: Vec<(EnvelopeType, ActionCallPayload)>,
        now_ms: i64,
    ) -> Result<(), Error> {
        for (kind, mut payload) in events {
            payload.session_id = self.context.session_id.clone();
            payload.run_id = self.context.run_id.clone();
            self.emit(kind, &payload, RunStatus::RUNNING, None, now_ms)?;
        }
        Ok(())
    }

    pub(crate) fn settle_usage(&mut self, raw: Option<&serde_json::value::RawValue>) {
        if !self.state.status.is_empty() {
            let observed = self
                .usage_scope
                .lock()
                .unwrap_or_else(|p| p.into_inner())
                .settle(raw);
            if self.pending.is_none() {
                (self.terminal_usage, self.extensions) = observed;
            }
        }
    }

    pub(crate) fn finish_pending(&mut self, now_ms: i64) -> Result<(), Error> {
        match self.pending.take() {
            Some(Terminal::Completed(stop)) => self.complete_stop(stop, now_ms),
            Some(Terminal::Failed(code, message, inferred)) => {
                self.fail(&code, &message, inferred, now_ms)
            }
            Some(Terminal::Cancelled(reason)) => self.cancelled(&reason, now_ms),
            None => Ok(()),
        }
    }

    pub fn prompt_error(
        &mut self,
        message: &str,
        inferred: bool,
        now_ms: i64,
    ) -> Result<(), Error> {
        if self.state.status.is_empty() {
            self.usage_scope
                .lock()
                .unwrap_or_else(|p| p.into_inner())
                .consume_prompt();
            self.close();
            return Ok(());
        }
        self.settle_usage(None);
        self.fail("acp_prompt_error", message, inferred, now_ms)
    }

    pub fn close(&mut self) {
        self.state.closed = true;
        self.subscriber.close();
        for follower in self.followers.drain(..) {
            follower.close();
        }
    }
}

impl Drop for TextRun {
    fn drop(&mut self) {
        self.close();
    }
}
