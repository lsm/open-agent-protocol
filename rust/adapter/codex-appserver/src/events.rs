use crossbeam_channel::select;
use oap_adapter::gojson::{decode, Decode};
use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, ProtocolError,
    RunCancelledPayload, RunCompletedPayload, RunFailedPayload, RunId, RunStartedPayload,
    RunStatus, SessionStatus,
};
use serde::Serialize;

use crate::native::{
    TurnNotification, METHOD_TURN_COMPLETED, METHOD_TURN_STARTED, TURN_COMPLETED, TURN_FAILED,
    TURN_INTERRUPTED,
};
use crate::rpc::{Inbound, NotificationMessage};
use crate::session::{hold, Inner, Shared};

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
                    Err(_) => return,
                },
                recv(done) -> _ => return,
                recv(stop) -> _ => return,
            }
        }
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
            METHOD_TURN_COMPLETED => match read::<TurnNotification>(notification) {
                Some(value) => self.completed(&mut inner, &value),
                None => invalid(&mut inner, "invalid turn/completed payload"),
            },
            _ => {}
        }
    }

    fn run_for(&self, inner: &Inner, thread: &str, turn: &str) -> Option<RunId> {
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

    fn completed(&self, inner: &mut Inner, value: &TurnNotification) {
        let Some(id) = self.run_for(inner, &value.thread_id, &value.turn.id) else {
            return;
        };
        let Some(run) = inner.runs.get(&id).filter(|run| !run.terminal) else {
            return;
        };
        let session_id = inner.state.session_id.clone();
        match value.turn.status.as_str() {
            TURN_COMPLETED => {
                let payload = RunCompletedPayload {
                    session_id,
                    run_id: id.clone(),
                    final_response: Message {
                        id: Some(run.message_id.clone()),
                        role: MessageRole::ASSISTANT,
                        content: MessageContent::from_text(""),
                        ..Message::default()
                    },
                    stop_reason: "end_turn".into(),
                    ..RunCompletedPayload::default()
                };
                self.emit(inner, &id, EnvelopeType::RUN_COMPLETED, &payload, true);
            }
            TURN_INTERRUPTED => {
                let payload = RunCancelledPayload {
                    session_id,
                    run_id: id.clone(),
                    reason: "native turn interrupted".into(),
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
                self.fail_run(inner, &id, "native_turn_failed", &message, "");
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

    fn fail_run(&self, inner: &mut Inner, id: &RunId, code: &str, message: &str, settled_by: &str) {
        let payload = RunFailedPayload {
            session_id: inner.state.session_id.clone(),
            run_id: id.clone(),
            error: ProtocolError {
                code: code.into(),
                message: if message.is_empty() { code } else { message }.into(),
                ..ProtocolError::default()
            },
            settled_by: settled_by.into(),
            ..RunFailedPayload::default()
        };
        self.emit(inner, id, EnvelopeType::RUN_FAILED, &payload, true);
    }

    fn emit(
        &self,
        inner: &mut Inner,
        id: &RunId,
        kind: EnvelopeType,
        payload: &impl Serialize,
        terminal: bool,
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
        envelope.sequence = Some(sequence);
        envelope.timestamp_ms = Some(now);
        envelope.capability_revision = Some(crate::adapter::pin().capability_revision.clone());
        if terminal {
            run.terminal = true;
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
