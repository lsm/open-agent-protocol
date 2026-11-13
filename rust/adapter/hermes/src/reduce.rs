use crossbeam_channel::select;
use oap_adapter::gojson::{go_quote, remarshal_any};
use oap_protocol::{
    ActionCallPayload, ContentDeltaPayload, ContentPart, ContentPartType, Envelope, EnvelopeId,
    EnvelopeType, Message, MessageContent, MessageRole, ProtocolError, RunCompletedPayload,
    RunFailedPayload, RunStartedPayload, RunStatus, SessionStatus, ToolCallId, Usage,
};
use serde::Serialize;
use serde_json::value::RawValue;

use std::sync::Arc;

use crate::adapter::ENDPOINT_ID;
use crate::native::{
    decode_notification, decode_strict, is_run_scoped, request_session_id, DeltaPayload, Event,
    MessageCompletePayload, ToolCompletePayload, ToolStartPayload, EVENT_MESSAGE_COMPLETE,
    EVENT_MESSAGE_DELTA, EVENT_MESSAGE_START, EVENT_REASONING_DELTA, EVENT_TOOL_COMPLETE,
    EVENT_TOOL_START, REQUEST_APPROVAL, REQUEST_CLARIFY, REQUEST_SECRET, REQUEST_SUDO,
};
use crate::rpc::{Inbound, IncomingRequest, RequestId};
use crate::session::{native_protocol, Inner, Observation, Shared, Tool};

const TERMINAL_WON: &str = "hermes adapter: terminal already selected";

impl Shared {
    pub(crate) fn dispatch(&self) {
        let (inbound, done, stop) = (self.client.inbound(), self.client.done(), &self.stopped);
        let mut hung_up = false;
        loop {
            if hung_up {
                select! {
                    recv(inbound) -> message => match message {
                        Ok(message) => self.reduce(message),
                        Err(_) => return self.transport_failed(),
                    },
                    recv(stop) -> _ => return,
                }
            } else {
                select! {
                    recv(inbound) -> message => match message {
                        Ok(message) => self.reduce(message),
                        Err(_) => select! {
                            recv(done) -> _ => return self.transport_failed(),
                            recv(stop) -> _ => return,
                        },
                    },
                    recv(done) -> _ => hung_up = true,
                    recv(stop) -> _ => return,
                }
            }
        }
    }

    fn reduce(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(request) => self.apply_request(&mut self.lock(), request),
            Inbound::Notification(notification) => {
                let params = notification.params.as_deref().map_or("", |raw| raw.get());
                let mut inner = self.lock();
                match decode_notification(&notification.method, params.as_bytes()) {
                    Ok(event) => self.apply_event(&mut inner, event),
                    Err(_) => self.foreign_activity(&mut inner, "non-event notification"),
                }
            }
        }
    }

    fn apply_event(&self, inner: &mut Inner, event: Event) {
        if event.session_id.is_empty() {
            return;
        }
        if event.session_id != self.native_id {
            let what = format!("event for foreign session {}", go_quote(&event.session_id));
            return self.foreign_activity(inner, &what);
        }
        if event.seq != inner.last_seq + 1 {
            let what = format!("non-contiguous seq {} after {}", event.seq, inner.last_seq);
            return self.foreign_activity(inner, &what);
        }
        inner.last_seq = event.seq;
        if inner.unusable {
            return;
        }
        let Some(run) = inner.run.as_mut() else {
            if is_run_scoped(&event.kind) {
                let what = format!(
                    "session event {} without a reserved run",
                    go_quote(&event.kind)
                );
                self.foreign_activity(inner, &what);
            }
            return;
        };
        if run.terminal {
            return;
        }
        if !run.started && event.kind != EVENT_MESSAGE_START {
            run.buffered.push(Observation::Event(event));
            return;
        }
        self.apply_run_event(inner, &event);
    }

    fn apply_request(&self, inner: &mut Inner, request: Arc<IncomingRequest>) {
        let method = request.method.as_str();
        if !matches!(request.id, RequestId::String(_)) {
            let what = format!("{method} request with a non-string id");
            return self.foreign_activity(inner, &what);
        }
        if !matches!(
            method,
            REQUEST_APPROVAL | REQUEST_CLARIFY | REQUEST_SUDO | REQUEST_SECRET
        ) {
            let _ = request.respond_error(-32601, "method not found");
            return;
        }
        let params = request.params.as_deref().map_or("", |raw| raw.get());
        let session = request_session_id(params.as_bytes());
        if session != self.native_id {
            let what = format!(
                "{method} request for foreign session {}",
                go_quote(&session)
            );
            return self.foreign_activity(inner, &what);
        }
        if inner.unusable {
            return;
        }
        match inner.run.as_mut().filter(|run| !run.terminal) {
            None => {
                let what = format!("{method} request without an owned run");
                self.foreign_activity(inner, &what);
            }
            Some(run) if !run.started => run.buffered.push(Observation::Request(request)),
            Some(_) => self.open_interaction(inner, &request),
        }
    }

    fn open_interaction(&self, _inner: &mut Inner, request: &IncomingRequest) {
        let _ = request.respond_error(-32601, "method not found");
    }

    fn apply_run_event(&self, inner: &mut Inner, event: &Event) {
        match event.kind.as_str() {
            EVENT_MESSAGE_START => {
                let Some(run) = inner.run.as_mut() else {
                    return;
                };
                let (already, accepted) = (run.open_seen, run.accepted);
                run.open_seen = true;
                if already {
                    self.fail_run(inner, "hermes_invalid_grammar", "turn opened twice", "");
                } else if accepted {
                    self.start_run(inner);
                }
            }
            EVENT_MESSAGE_COMPLETE => match decode_strict(event.payload_bytes()) {
                Ok(payload) => self.settle_run(inner, &payload),
                Err(_) => self.fail_run(inner, "hermes_invalid_event", "invalid settlement", ""),
            },
            EVENT_MESSAGE_DELTA => self.emit_delta(inner, event, ContentPartType::TEXT),
            EVENT_REASONING_DELTA => self.emit_delta(inner, event, ContentPartType::REASONING),
            EVENT_TOOL_START => match decode_strict(event.payload_bytes()) {
                Ok(payload) => self.start_tool(inner, &payload),
                Err(_) => self.fail_run(inner, "hermes_invalid_event", "invalid tool start", ""),
            },
            EVENT_TOOL_COMPLETE => match decode_strict(event.payload_bytes()) {
                Ok(payload) => self.end_tool(inner, &payload),
                Err(_) => self.fail_run(inner, "hermes_invalid_event", "invalid tool complete", ""),
            },
            _ => {}
        }
    }

    fn emit_delta(&self, inner: &mut Inner, event: &Event, kind: ContentPartType) {
        let Some(run) = inner.run.as_ref().filter(|run| run.started) else {
            return;
        };
        let Ok(delta) = decode_strict::<DeltaPayload>(event.payload_bytes()) else {
            return self.fail_run(inner, "hermes_invalid_event", "invalid delta", "");
        };
        if delta.text.is_empty() {
            return;
        }
        let mut part = ContentPart {
            part_type: kind.clone(),
            ..ContentPart::default()
        };
        if kind == ContentPartType::TEXT {
            part.text = delta.text;
        } else {
            part.reasoning = delta.text;
        }
        let payload = ContentDeltaPayload {
            session_id: inner.state.session_id.clone(),
            run_id: run.id.clone(),
            message_id: Some(run.message_id.clone()),
            part,
        };
        self.emit(inner, EnvelopeType::CONTENT_DELTA, &payload, false);
    }

    fn settle_run(&self, inner: &mut Inner, payload: &MessageCompletePayload) {
        let Some(run) = inner.run.as_ref() else {
            return;
        };
        if !run.started {
            let failure = native_protocol("settlement before the turn opened");
            return self.abort_pre_start(inner, failure);
        }
        if payload.status.is_empty() {
            let message = "child-mirror settlement on the parent stream";
            return self.fail_run(inner, "hermes_invalid_settlement", message, "");
        }
        self.settle_children(inner);
        let Some(run) = inner.run.as_ref() else {
            return;
        };
        let (session_id, run_id) = (inner.state.session_id.clone(), run.id.clone());
        if payload.status == "complete" {
            let mut usage = None;
            if let Some(reported) = &payload.usage {
                let count = |count: Option<i64>| count.map(u64::try_from).transpose();
                let (Ok(input), Ok(output), Ok(total)) = (
                    count(reported.input),
                    count(reported.output),
                    count(reported.total),
                ) else {
                    return self.fail_run(inner, "hermes_invalid_event", "invalid settlement", "");
                };
                usage = Some(Usage {
                    input_tokens: input,
                    output_tokens: output,
                    total_tokens: total,
                    ..Usage::default()
                });
            }
            let completed = RunCompletedPayload {
                session_id,
                run_id,
                final_response: Message {
                    id: Some(run.message_id.clone()),
                    role: MessageRole::ASSISTANT,
                    content: MessageContent::from_text(&payload.text),
                    ..Message::default()
                },
                stop_reason: "completed".into(),
                usage,
                ..RunCompletedPayload::default()
            };
            self.emit(inner, EnvelopeType::RUN_COMPLETED, &completed, true);
            return;
        }
        let surface = payload
            .error_surface
            .as_ref()
            .map_or("", |surface| surface.code.as_str());
        let code = format!(
            "hermes_{}",
            if surface.is_empty() {
                &payload.status
            } else {
                surface
            }
        );
        let message = [payload.error.as_str(), payload.text.as_str()]
            .into_iter()
            .find(|message| !message.is_empty())
            .map_or_else(|| format!("turn {}", payload.status), str::to_owned);
        self.fail_run(inner, &code, &message, "");
    }

    pub(crate) fn start_run(&self, inner: &mut Inner) {
        let Some(run) = inner
            .run
            .as_mut()
            .filter(|run| !run.started && !run.terminal)
        else {
            return;
        };
        run.started = true;
        run.id = self.runtime.new_id("run").into();
        let (id, replay, start) = (
            run.id.clone(),
            std::mem::take(&mut run.buffered),
            run.start.take(),
        );
        inner.state.status = SessionStatus::RUNNING;
        inner.state.active_run_id = Some(id.clone());
        let payload = RunStartedPayload {
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            status: RunStatus::RUNNING,
            model_id: inner.state.current_model_id.clone(),
            started_at_ms: self.runtime.now_ms(),
            ..RunStartedPayload::default()
        };
        let emitted = self.emit(inner, EnvelopeType::RUN_STARTED, &payload, false);
        if let Some(start) = start {
            let _ = start.send(emitted.then_some(id).ok_or_else(|| TERMINAL_WON.into()));
        }
        for observed in replay {
            if inner.run.as_ref().is_none_or(|run| run.terminal) {
                return;
            }
            match observed {
                Observation::Event(event) => self.apply_run_event(inner, &event),
                Observation::Request(request) => self.open_interaction(inner, &request),
            }
        }
    }

    fn start_tool(&self, inner: &mut Inner, payload: &ToolStartPayload) {
        let Some(run) = inner.run.as_ref() else {
            return;
        };
        if run
            .tools
            .iter()
            .any(|tool| tool.native_id == payload.tool_id)
        {
            return self.fail_run(inner, "hermes_tool_lifecycle", "duplicate tool call", "");
        }
        let args = payload.args.as_ref().map_or("null", |args| args.get());
        let Ok(args) = remarshal_any(args).and_then(RawValue::from_string) else {
            return self.fail_run(inner, "hermes_invalid_event", "invalid tool start", "");
        };
        let id = ToolCallId::from(self.runtime.new_id("tool-call"));
        let mut call = self.tool_payload(inner, &id, &payload.name);
        call.arguments_json = Some(args);
        let requested = self.emit_call(inner, EnvelopeType::ACTION_CALL_REQUESTED, &call, None);
        call.arguments_json = None;
        let started = self.emit_call(inner, EnvelopeType::ACTION_CALL_STARTED, &call, requested);
        if let Some(run) = inner.run.as_mut() {
            run.tools.push(Tool {
                native_id: payload.tool_id.clone(),
                id,
                name: payload.name.clone(),
                started: started.unwrap_or_default(),
                terminal: false,
            });
        }
    }

    fn end_tool(&self, inner: &mut Inner, payload: &ToolCompletePayload) {
        let Some(tool) = inner.run.as_mut().and_then(|run| {
            run.tools
                .iter_mut()
                .find(|tool| tool.native_id == payload.tool_id)
                .filter(|tool| !tool.terminal)
        }) else {
            return self.fail_run(
                inner,
                "hermes_tool_lifecycle",
                "unmatched tool completion",
                "",
            );
        };
        tool.terminal = true;
        let (id, name, started) = (tool.id.clone(), tool.name.clone(), tool.started.clone());
        let mut call = self.tool_payload(inner, &id, &name);
        call.result = Some(payload.result.clone().unwrap_or_default());
        self.emit_call(
            inner,
            EnvelopeType::ACTION_CALL_COMPLETED,
            &call,
            Some(started),
        );
    }

    fn settle_children(&self, inner: &mut Inner) {
        let Some(run) = inner.run.as_mut() else {
            return;
        };
        let open: Vec<_> = run
            .tools
            .iter_mut()
            .filter(|tool| !tool.terminal)
            .map(|tool| {
                tool.terminal = true;
                (tool.id.clone(), tool.name.clone(), tool.started.clone())
            })
            .collect();
        for (id, name, started) in open {
            let mut call = self.tool_payload(inner, &id, &name);
            call.error = Some(ProtocolError {
                code: "incomplete_tool".into(),
                message: "turn settled with an unfinished hermes tool".into(),
                ..ProtocolError::default()
            });
            self.emit_call(
                inner,
                EnvelopeType::ACTION_CALL_FAILED,
                &call,
                Some(started),
            );
        }
    }

    fn tool_payload(&self, inner: &Inner, id: &ToolCallId, name: &str) -> ActionCallPayload {
        ActionCallPayload {
            session_id: inner.state.session_id.clone(),
            run_id: inner
                .run
                .as_ref()
                .map(|run| run.id.clone())
                .unwrap_or_default(),
            tool_call_id: id.clone(),
            requested_by: Some(ENDPOINT_ID.into()),
            execution_owner: "hermes".into(),
            name: name.into(),
            ..ActionCallPayload::default()
        }
    }

    fn emit_call(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        call: &ActionCallPayload,
        reply: Option<EnvelopeId>,
    ) -> Option<EnvelopeId> {
        self.emit_envelope(inner, kind, call, false, |envelope| {
            envelope.in_reply_to = reply;
            envelope.tool_call_id = Some(call.tool_call_id.clone());
        })
    }

    pub(crate) fn emit(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &impl Serialize,
        terminal: bool,
    ) -> bool {
        self.emit_envelope(inner, kind, payload, terminal, |_| {})
            .is_some()
    }

    fn emit_envelope(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &impl Serialize,
        terminal: bool,
        stamp: impl FnOnce(&mut Envelope),
    ) -> Option<EnvelopeId> {
        let run = inner.run.as_mut().filter(|run| !run.terminal)?;
        let mut envelope =
            Envelope::new(kind.clone(), self.runtime.new_id("event"), payload).ok()?;
        let now = self.runtime.now_ms();
        let sequence = run.next;
        run.next += 1;
        envelope.sequence = Some(sequence);
        envelope.timestamp_ms = Some(now);
        envelope.session_id = Some(inner.state.session_id.clone());
        envelope.run_id = Some(run.id.clone());
        envelope.capability_revision = Some(crate::adapter::pin().capability_revision.clone());
        stamp(&mut envelope);
        let id = envelope.id.clone();
        self.journal.append(envelope, terminal);
        inner.state.transcript_cursor = sequence.to_string();
        inner.state.updated_at_ms = now;
        if terminal {
            inner.run = None;
            inner.state.status = SessionStatus::IDLE;
            inner.state.active_run_id = None;
        }
        Some(id)
    }

    pub(crate) fn fail_run(&self, inner: &mut Inner, code: &str, message: &str, settled_by: &str) {
        let Some(run) = inner.run.as_ref() else {
            return;
        };
        if !run.started {
            return self.abort_pre_start(inner, native_protocol(message));
        }
        self.settle_children(inner);
        let Some(run) = inner.run.as_ref() else {
            return;
        };
        let payload = RunFailedPayload {
            session_id: inner.state.session_id.clone(),
            run_id: run.id.clone(),
            error: ProtocolError {
                code: code.into(),
                message: message.into(),
                ..ProtocolError::default()
            },
            settled_by: settled_by.into(),
            ..RunFailedPayload::default()
        };
        self.emit(inner, EnvelopeType::RUN_FAILED, &payload, true);
    }

    fn foreign_activity(&self, inner: &mut Inner, what: &str) {
        inner.unusable = true;
        self.fail_run(inner, "hermes_external_activity", what, "");
    }

    fn transport_failed(&self) {
        let mut inner = self.lock();
        if inner.closed {
            return;
        }
        inner.unusable = true;
        inner.transport_dead = true;
        if inner
            .run
            .as_ref()
            .is_some_and(|run| run.started || !run.submitting)
        {
            self.fail_transport(&mut inner);
        }
    }

    pub(crate) fn fail_transport(&self, inner: &mut Inner) {
        let failure = self.client.err().unwrap_or_else(|| "<nil>".into());
        self.fail_run(inner, "hermes_process_exit", &failure, "inferred");
    }
}
