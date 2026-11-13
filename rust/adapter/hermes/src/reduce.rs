use crossbeam_channel::select;
use oap_adapter::gojson::{go_quote, remarshal_any};
use oap_protocol::{
    ActionCallPayload, ContentDeltaPayload, ContentPart, ContentPartType, Envelope, EnvelopeId,
    EnvelopeType, InputOption, InputQuestion, InputQuestionKind, InteractionId, Message,
    MessageContent, MessageRole, ProtocolError, RunCompletedPayload, RunFailedPayload,
    RunStartedPayload, RunStatus, RunStatusUpdatedPayload, SessionStatus, ToolCallId, Usage,
    UserInputRequestedPayload, UserInputResolutionStatus, UserInputResolvedPayload,
};
use serde::Serialize;
use serde_json::value::RawValue;

use std::sync::Arc;

use crate::adapter::ENDPOINT_ID;
use crate::native::{
    decode_notification, decode_server_request, decode_strict, is_run_scoped, request_session_id,
    DeltaPayload, Event, MessageCompletePayload, RequestCancelPayload, ServerRequest,
    ToolCompletePayload, ToolStartPayload, EVENT_MESSAGE_COMPLETE, EVENT_MESSAGE_DELTA,
    EVENT_MESSAGE_START, EVENT_REASONING_DELTA, EVENT_REQUEST_CANCEL, EVENT_TOOL_COMPLETE,
    EVENT_TOOL_START, REQUEST_APPROVAL, REQUEST_CLARIFY, REQUEST_SECRET, REQUEST_SUDO,
};
use crate::rpc::{Inbound, IncomingRequest, RequestId};
use crate::session::{native_protocol, Inner, Interaction, Observation, Run, Shared, Tool};

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

    fn open_interaction(&self, inner: &mut Inner, request: &Arc<IncomingRequest>) {
        let method = request.method.as_str();
        let params = request.params.as_deref().map_or("", |raw| raw.get());
        let Ok(Some(decoded)) = decode_server_request(method, params.as_bytes()) else {
            let message = format!("invalid {method} gate");
            return self.fail_run(inner, "hermes_invalid_event", &message, "");
        };
        let id = InteractionId::from(self.runtime.new_id("interaction"));
        let (title, description, questions) = gate(decoded);
        if !questions.iter().all(answerable) {
            let message = "gate with a question nobody can answer";
            return self.fail_run(inner, "hermes_invalid_event", message, "");
        }
        let Some(run_id) = inner.run.as_ref().map(|run| run.id.clone()) else {
            return;
        };
        let payload = UserInputRequestedPayload {
            interaction_id: id.clone(),
            requested_by: ENDPOINT_ID.into(),
            responded_by: self.participant.clone(),
            session_id: inner.state.session_id.clone(),
            run_id: run_id.clone(),
            title: title.into(),
            description,
            questions: questions.clone(),
            allow_cancel: true,
            ..UserInputRequestedPayload::default()
        };
        let kind = EnvelopeType::USER_INPUT_REQUESTED;
        let Some(requested) = self.emit_input(inner, kind, &payload, &id, None) else {
            return;
        };
        if let Some(run) = inner.run.as_mut() {
            run.interactions.push(Interaction {
                id: id.clone(),
                kind: method.into(),
                request_id: match &request.id {
                    RequestId::String(id) => id.clone(),
                    _ => String::new(),
                },
                request: request.clone(),
                questions,
                requested,
                resolved: false,
                settling: false,
                withdrawn: false,
            });
        }
        let waiting = RunStatusUpdatedPayload {
            session_id: inner.state.session_id.clone(),
            run_id,
            status: RunStatus::WAITING_FOR_INPUT,
            pending_user_input_id: Some(id),
            updated_at_ms: self.runtime.now_ms(),
        };
        self.emit(inner, EnvelopeType::RUN_STATUS_UPDATED, &waiting, false);
    }

    fn expire_interaction(&self, inner: &mut Inner, payload: &RequestCancelPayload) {
        let Some(interaction) = inner.run.as_mut().and_then(|run| {
            run.interactions
                .iter_mut()
                .find(|interaction| !interaction.resolved && interaction.request_id == payload.id)
        }) else {
            return;
        };
        if interaction.settling {
            interaction.withdrawn = true;
            return;
        }
        interaction.resolved = true;
        let (id, requested) = (interaction.id.clone(), interaction.requested.clone());
        self.cancel_interaction(inner, &id, requested);
        let Some(run_id) = inner.run.as_ref().map(|run| run.id.clone()) else {
            return;
        };
        let running = RunStatusUpdatedPayload {
            session_id: inner.state.session_id.clone(),
            run_id,
            status: RunStatus::RUNNING,
            updated_at_ms: self.runtime.now_ms(),
            ..RunStatusUpdatedPayload::default()
        };
        self.emit(inner, EnvelopeType::RUN_STATUS_UPDATED, &running, false);
    }

    pub(crate) fn cancel_interaction(
        &self,
        inner: &mut Inner,
        id: &InteractionId,
        requested: EnvelopeId,
    ) {
        let payload = UserInputResolvedPayload {
            interaction_id: id.clone(),
            requested_by: ENDPOINT_ID.into(),
            responded_by: self.participant.clone(),
            session_id: inner.state.session_id.clone(),
            run_id: inner
                .run
                .as_ref()
                .map(|run| run.id.clone())
                .unwrap_or_default(),
            status: UserInputResolutionStatus::CANCELLED,
            ..UserInputResolvedPayload::default()
        };
        let kind = EnvelopeType::USER_INPUT_RESOLVED;
        self.emit_input(inner, kind, &payload, id, Some(requested));
    }

    pub(crate) fn flush_deferred(&self, inner: &mut Inner) {
        let Some(payload) = inner.run.as_mut().and_then(|run| run.deferred.take()) else {
            return;
        };
        self.settle_run(inner, &payload);
    }

    pub(crate) fn emit_input(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &impl Serialize,
        turn: &InteractionId,
        reply: Option<EnvelopeId>,
    ) -> Option<EnvelopeId> {
        self.emit_envelope(inner, kind, payload, false, |envelope| {
            envelope.in_reply_to = reply;
            envelope.turn_id = Some(turn.clone());
        })
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
            EVENT_REQUEST_CANCEL => match decode_strict(event.payload_bytes()) {
                Ok(payload) => self.expire_interaction(inner, &payload),
                Err(_) => {
                    self.fail_run(inner, "hermes_invalid_event", "invalid request cancel", "")
                }
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
        if let Some(run) = inner.run.as_mut().filter(|run| settling(run)) {
            run.deferred = Some(payload.clone());
            return;
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
        let gates: Vec<_> = run
            .interactions
            .iter_mut()
            .filter(|interaction| !interaction.resolved)
            .map(|interaction| {
                interaction.resolved = true;
                (interaction.id.clone(), interaction.requested.clone())
            })
            .collect();
        for (id, requested) in gates {
            self.cancel_interaction(inner, &id, requested);
        }
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

fn gate(request: ServerRequest) -> (&'static str, String, Vec<InputQuestion>) {
    let question =
        |id: &str, prompt: String, kind: InputQuestionKind, options: Vec<InputOption>| {
            InputQuestion {
                id: id.into(),
                prompt,
                kind,
                required: true,
                options,
            }
        };
    match request {
        ServerRequest::Approval(approval) => {
            let description = if approval.command.is_empty() {
                approval.description
            } else {
                approval.command
            };
            let options = options(&approval.choices);
            let kind = InputQuestionKind::SINGLE_CHOICE;
            let choice = question("choice", description.clone(), kind, options);
            ("Command approval", description, vec![choice])
        }
        ServerRequest::Clarify(clarify) => {
            let questions = clarify
                .questions
                .into_iter()
                .map(|asked| {
                    let kind = match (asked.choices.is_empty(), asked.multi_select) {
                        (true, _) => InputQuestionKind::TEXT,
                        (false, true) => InputQuestionKind::MULTI_CHOICE,
                        (false, false) => InputQuestionKind::SINGLE_CHOICE,
                    };
                    question(&asked.qid, asked.question, kind, options(&asked.choices))
                })
                .collect();
            ("Clarification", String::new(), questions)
        }
        ServerRequest::Sudo(sudo) => {
            let prompt = "Enter the sudo password".into();
            let password = question("password", prompt, InputQuestionKind::TEXT, Vec::new());
            ("Password required", sudo.command, vec![password])
        }
        ServerRequest::Secret(secret) => {
            let value = question("value", secret.prompt, InputQuestionKind::TEXT, Vec::new());
            ("Secret required", secret.env_var, vec![value])
        }
    }
}

fn options(choices: &[String]) -> Vec<InputOption> {
    choices
        .iter()
        .map(|choice| InputOption {
            id: choice.clone(),
            label: choice.clone(),
            ..InputOption::default()
        })
        .collect()
}

fn answerable(question: &InputQuestion) -> bool {
    question.options.iter().all(|option| !option.id.is_empty())
}

fn settling(run: &Run) -> bool {
    run.interactions
        .iter()
        .any(|interaction| interaction.settling)
}
