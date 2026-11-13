use crossbeam_channel::select;
use oap_adapter::gojson::go_quote;
use oap_protocol::{
    Envelope, EnvelopeType, ProtocolError, RunFailedPayload, RunStartedPayload, RunStatus,
    SessionStatus,
};
use serde::Serialize;

use std::sync::Arc;

use crate::native::{
    decode_notification, is_run_scoped, request_session_id, Event, EVENT_MESSAGE_START,
    REQUEST_APPROVAL, REQUEST_CLARIFY, REQUEST_SECRET, REQUEST_SUDO,
};
use crate::rpc::{Inbound, IncomingRequest, RequestId};
use crate::session::{native_protocol, Inner, Observation, Shared};

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
        if event.kind == EVENT_MESSAGE_START {
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

    pub(crate) fn emit(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &impl Serialize,
        terminal: bool,
    ) -> bool {
        let Some(run) = inner.run.as_mut().filter(|run| !run.terminal) else {
            return false;
        };
        let Ok(mut envelope) = Envelope::new(kind.clone(), self.runtime.new_id("event"), payload)
        else {
            return false;
        };
        let now = self.runtime.now_ms();
        let sequence = run.next;
        run.next += 1;
        envelope.sequence = Some(sequence);
        envelope.timestamp_ms = Some(now);
        envelope.session_id = Some(inner.state.session_id.clone());
        envelope.run_id = Some(run.id.clone());
        envelope.capability_revision = Some(crate::adapter::pin().capability_revision.clone());
        self.journal.append(envelope, terminal);
        inner.state.transcript_cursor = sequence.to_string();
        inner.state.updated_at_ms = now;
        if terminal {
            inner.run = None;
            inner.state.status = SessionStatus::IDLE;
            inner.state.active_run_id = None;
        }
        true
    }

    pub(crate) fn fail_run(&self, inner: &mut Inner, code: &str, message: &str, settled_by: &str) {
        let Some(run) = inner.run.as_ref() else {
            return;
        };
        if !run.started {
            return self.abort_pre_start(inner, native_protocol(message));
        }
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
