use crossbeam_channel::select;
use oxap_adapter::gojson::{decode, Decode};
use oxap_protocol::{
    ActionCallPayload, ContentDeltaPayload, ContentPart, ContentPartType, Envelope, EnvelopeType,
    Message, MessageContent, MessageRole, ProtocolError, RunCancelledPayload, RunCompletedPayload,
    RunFailedPayload, RunId, RunStartedPayload, RunStatus, SessionStatus, ToolCallId, Usage,
    SETTLED_BY_INFERRED,
};
use oxap_validation::OutputSchema;
use serde::Serialize;
use serde_json::value::RawValue;

use crate::native::{
    AgentMessageDeltaNotification, ItemNotification, ThreadTokenUsageUpdatedNotification,
    TurnNotification, METHOD_AGENT_DELTA, METHOD_COMMAND_APPROVAL, METHOD_FILE_APPROVAL,
    METHOD_ITEM_COMPLETED, METHOD_ITEM_STARTED, METHOD_TOKEN_USAGE, METHOD_TURN_COMPLETED,
    METHOD_TURN_STARTED, METHOD_USER_INPUT, TURN_COMPLETED, TURN_FAILED, TURN_INTERRUPTED,
};
use crate::rpc::{Inbound, NotificationMessage};
use crate::session::{hold, Inner, Shared};

fn compact(text: &str) -> Option<String> {
    serde_json::from_str::<&RawValue>(text).ok()?;
    let mut compacted = String::with_capacity(text.len());
    let (mut quoted, mut escaped) = (false, false);
    for character in text.chars() {
        if quoted {
            compacted.push(character);
            if escaped {
                escaped = false;
            } else if character == '\\' {
                escaped = true;
            } else if character == '"' {
                quoted = false;
            }
        } else if !matches!(character, ' ' | '\t' | '\n' | '\r') {
            quoted = character == '"';
            compacted.push(character);
        }
    }
    Some(compacted)
}

fn structured(schema: &OutputSchema, text: Option<&str>) -> Result<Box<RawValue>, &'static str> {
    let text = text.ok_or(
        "the turn completed without a final assistant message to read the structured result from",
    )?;
    let compacted = compact(text.trim()).ok_or("the final assistant message is not JSON")?;
    schema
        .validate(&compacted)
        .map_err(|_| "the final assistant message does not conform to the output_schema")?;
    RawValue::from_string(compacted).map_err(|_| "the final assistant message is not JSON")
}

fn read<T: Decode>(notification: &NotificationMessage) -> Option<T> {
    decode(notification.params.as_deref()?.get().as_bytes()).ok()
}

impl Shared {
    pub(crate) fn dispatch(&self) {
        let (inbound, done, stop) = (self.client.inbound(), self.client.done(), &self.stopped);
        loop {
            select! {
                recv(inbound) -> message => match message {
                    Ok(message) => self.reduce(message),
                    Err(_) => break,
                },
                recv(done) -> _ => break,
                recv(stop) -> _ => return,
            }
        }
        let _operating = self.operate();
        let message = self.client.err().unwrap_or_else(|| {
            "Codex app-server transport closed before terminal settlement".into()
        });
        self.fail_active(
            &mut self.lock(),
            "native_transport_closed",
            &message,
            SETTLED_BY_INFERRED,
        );
    }

    fn reduce(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            message => {
                let mut operating = hold(&self.operating);
                if operating.starting {
                    operating.deferred.push(message);
                    return;
                }
                self.handle(message);
            }
        }
    }

    pub(crate) fn handle(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(request)
                if request.method == METHOD_COMMAND_APPROVAL
                    || request.method == METHOD_FILE_APPROVAL =>
            {
                self.approval(&request)
            }
            Inbound::Request(request) if request.method == METHOD_USER_INPUT => {
                self.user_input(&request)
            }
            Inbound::Request(request) => {
                let data =
                    serde_json::value::to_raw_value(&serde_json::json!({"method": request.method}))
                        .ok();
                let _ =
                    request.respond_error_with(-32601, "unsupported Codex server request", data);
            }
            Inbound::Notification(notification) => self.notification(&notification),
        }
    }

    fn notification(&self, notification: &NotificationMessage) {
        let mut inner = self.lock();
        let invalid = |inner: &mut Inner, message: &str| {
            self.fail_active(inner, "invalid_native_event", message, "");
        };
        match notification.method.as_str() {
            METHOD_TURN_STARTED => match read::<TurnNotification>(notification) {
                Some(value) => self.started(&mut inner, &value),
                None => invalid(&mut inner, "invalid turn/started payload"),
            },
            METHOD_AGENT_DELTA => match read::<AgentMessageDeltaNotification>(notification) {
                Some(value) => self.delta(&mut inner, &value),
                None => invalid(&mut inner, "invalid agent message delta"),
            },
            METHOD_ITEM_STARTED | METHOD_ITEM_COMPLETED => {
                match read::<ItemNotification>(notification) {
                    Some(value) => self.item(&mut inner, &notification.method, value),
                    None => invalid(&mut inner, "invalid item lifecycle payload"),
                }
            }
            METHOD_TOKEN_USAGE => match read::<ThreadTokenUsageUpdatedNotification>(notification) {
                Some(value) => self.usage(&mut inner, &value),
                None => invalid(&mut inner, "invalid thread/tokenUsage/updated payload"),
            },
            METHOD_TURN_COMPLETED => match read::<TurnNotification>(notification) {
                Some(value) => self.completed(&mut inner, &value),
                None => invalid(&mut inner, "invalid turn/completed payload"),
            },
            _ => {}
        }
    }

    pub(crate) fn run_for(&self, inner: &Inner, thread: &str, turn: &str) -> Option<RunId> {
        if thread != self.thread_id {
            return None;
        }
        let id = inner.turns.get(turn)?;
        inner.runs.contains_key(id).then(|| id.clone())
    }

    fn started(&self, inner: &mut Inner, value: &TurnNotification) {
        let Some(id) = self.run_for(inner, &value.thread_id, &value.turn.id) else {
            return;
        };
        let Some(run) = inner
            .runs
            .get_mut(&id)
            .filter(|run| !run.started && !run.terminal)
        else {
            return;
        };
        run.started = true;
        let model = run.model.clone();
        inner.state.status = SessionStatus::RUNNING;
        inner.state.updated_at_ms = self.runtime.now_ms();
        let payload = RunStartedPayload {
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            status: RunStatus::RUNNING,
            model_id: model,
            started_at_ms: self.runtime.now_ms(),
            ..RunStartedPayload::default()
        };
        self.emit(inner, &id, EnvelopeType::RUN_STARTED, &payload, false);
    }

    fn delta(&self, inner: &mut Inner, value: &AgentMessageDeltaNotification) {
        let Some(id) = self.run_for(inner, &value.thread_id, &value.turn_id) else {
            return;
        };
        let Some(run) = inner
            .runs
            .get_mut(&id)
            .filter(|run| run.started && !run.terminal)
        else {
            return;
        };
        run.text.push_str(&value.delta);
        let payload = ContentDeltaPayload {
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            message_id: Some(run.message_id.clone()),
            part: ContentPart {
                part_type: ContentPartType::TEXT,
                text: value.delta.clone(),
                ..ContentPart::default()
            },
        };
        self.emit(inner, &id, EnvelopeType::CONTENT_DELTA, &payload, false);
    }

    fn item(&self, inner: &mut Inner, method: &str, value: ItemNotification) {
        let Some(id) = self.run_for(inner, &value.thread_id, &value.turn_id) else {
            return;
        };
        let Some(run) = inner
            .runs
            .get_mut(&id)
            .filter(|run| run.started && !run.terminal)
        else {
            return;
        };
        if method == METHOD_ITEM_COMPLETED && value.item.kind == "agentMessage" {
            run.final_text = Some(value.item.text);
        } else {
            self.action(inner, &id, method, value.item);
        }
    }

    fn completed(&self, inner: &mut Inner, value: &TurnNotification) {
        let Some(id) = self.run_for(inner, &value.thread_id, &value.turn.id) else {
            return;
        };
        if inner.runs.get(&id).is_none_or(|run| run.terminal) {
            return;
        }
        let interrupted = value.turn.status == TURN_INTERRUPTED;
        self.close_interactions(inner, &id, interrupted);
        self.close_pending(inner, &id, interrupted);
        let run = &inner.runs[&id];
        let usage = run.usage.clone();
        let session_id = inner.state.session_id.clone();
        match value.turn.status.as_str() {
            TURN_COMPLETED => {
                let result = match &run.schema {
                    Some(schema) => match structured(schema, run.final_text.as_deref()) {
                        Ok(result) => Some(result),
                        Err(defect) => {
                            self.failed(inner, &id, "structured_output_failed", defect, "", usage);
                            return;
                        }
                    },
                    None => None,
                };
                let payload = RunCompletedPayload {
                    session_id,
                    run_id: id.clone(),
                    final_response: Message {
                        id: Some(run.message_id.clone()),
                        role: MessageRole::ASSISTANT,
                        content: MessageContent::from_text(&run.text),
                        ..Message::default()
                    },
                    stop_reason: "end_turn".into(),
                    result,
                    usage,
                    ..RunCompletedPayload::default()
                };
                self.emit(inner, &id, EnvelopeType::RUN_COMPLETED, &payload, true);
            }
            TURN_INTERRUPTED => {
                let payload = RunCancelledPayload {
                    session_id,
                    run_id: id.clone(),
                    reason: "native turn interrupted".into(),
                    usage,
                    ..RunCancelledPayload::default()
                };
                self.emit(inner, &id, EnvelopeType::RUN_CANCELLED, &payload, true);
            }
            TURN_FAILED => {
                let message = value
                    .turn
                    .error
                    .as_ref()
                    .map(|error| error.message.clone())
                    .filter(|message| !message.is_empty())
                    .unwrap_or_else(|| "native turn failed".into());
                self.failed(inner, &id, "native_turn_failed", &message, "", usage);
            }
            _ => self.fail_run(
                inner,
                &id,
                "invalid_native_terminal",
                "turn/completed did not contain a terminal status",
                "",
            ),
        }
    }

    fn fail_active(&self, inner: &mut Inner, code: &str, message: &str, settled_by: &str) {
        if let Some(id) = inner.active.clone() {
            self.fail_run(inner, &id, code, message, settled_by);
        }
    }

    pub(crate) fn fail_run(
        &self,
        inner: &mut Inner,
        id: &RunId,
        code: &str,
        message: &str,
        settled_by: &str,
    ) {
        self.close_interactions(inner, id, false);
        self.close_pending(inner, id, false);
        self.failed(inner, id, code, message, settled_by, None);
    }

    fn failed(
        &self,
        inner: &mut Inner,
        id: &RunId,
        code: &str,
        message: &str,
        settled_by: &str,
        usage: Option<Usage>,
    ) {
        let payload = RunFailedPayload {
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            error: ProtocolError {
                code: code.into(),
                message: if message.is_empty() { code } else { message }.into(),
                ..ProtocolError::default()
            },
            settled_by: settled_by.into(),
            usage,
            ..RunFailedPayload::default()
        };
        self.emit(inner, id, EnvelopeType::RUN_FAILED, &payload, true);
    }

    pub(crate) fn emit_action(
        &self,
        inner: &mut Inner,
        id: &RunId,
        kind: EnvelopeType,
        payload: &ActionCallPayload,
    ) {
        self.emit_call(inner, id, kind, payload, &payload.tool_call_id);
    }

    pub(crate) fn emit_call(
        &self,
        inner: &mut Inner,
        id: &RunId,
        kind: EnvelopeType,
        payload: &impl Serialize,
        tool_call: &ToolCallId,
    ) {
        self.stamp(inner, id, kind, payload, false, Some(tool_call.clone()));
    }

    pub(crate) fn emit(
        &self,
        inner: &mut Inner,
        id: &RunId,
        kind: EnvelopeType,
        payload: &impl Serialize,
        terminal: bool,
    ) {
        self.stamp(inner, id, kind, payload, terminal, None);
    }

    fn stamp(
        &self,
        inner: &mut Inner,
        id: &RunId,
        kind: EnvelopeType,
        payload: &impl Serialize,
        terminal: bool,
        tool_call: Option<ToolCallId>,
    ) {
        let Some(run) = inner.runs.get_mut(id).filter(|run| !run.terminal) else {
            return;
        };
        let sequence = run.next;
        run.next += 1;
        let Ok(mut envelope) = Envelope::new(kind, self.runtime.new_id("event"), payload) else {
            return;
        };
        let now = self.runtime.now_ms();
        envelope.session_id = Some(inner.state.session_id.clone());
        envelope.run_id = Some(id.clone());
        envelope.tool_call_id = tool_call;
        envelope.sequence = Some(sequence);
        envelope.timestamp_ms = Some(now);
        envelope.capability_revision = Some(crate::adapter::pin().capability_revision.clone());
        if terminal {
            run.terminal = true;
            run.settled = Some(if envelope.envelope_type == EnvelopeType::RUN_COMPLETED {
                RunStatus::COMPLETED
            } else if envelope.envelope_type == EnvelopeType::RUN_CANCELLED {
                RunStatus::CANCELLED
            } else {
                RunStatus::FAILED
            });
            inner.state.status = SessionStatus::IDLE;
            inner.state.active_run_id = None;
            inner.state.transcript_cursor = sequence.to_string();
            inner.state.updated_at_ms = now;
            if inner.active.as_ref() == Some(id) {
                inner.active = None;
            }
        }
        self.journal.append(envelope, terminal);
    }
}
