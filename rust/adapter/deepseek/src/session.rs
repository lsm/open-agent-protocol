use std::sync::{Arc, Mutex, MutexGuard};

use crossbeam_channel::{select, Receiver, Sender};
use oap_adapter::journal::{Journal, Reservation};
use oap_adapter::{Error, EventStream, InteractionResolution, Recovery, ResumeRequest};
use oap_protocol::{
    Admission as SubmitAdmission, EffectiveDeliveryMode, Envelope, EnvelopeType,
    MessageSubmitRequest, MessageSubmitResponse, ProtocolError, RequestedDeliveryMode,
    RunCancelResponse, RunFailedPayload, RunId, RunStartedPayload, RunStatus, SessionState,
    SessionStatus, SubmissionId, SETTLED_BY_INFERRED,
};

use crate::admission::{Admission, Candidate};

use crate::native::{
    decode_notification, Notification, SessionPromptParams, SessionPromptResult,
    METHOD_SESSION_PROMPT,
};
use crate::process::Process;
use crate::prompt::native_prompt;
use crate::rpc::{Inbound, NotificationMessage};
use crate::runtime::Runtime;

pub struct Session {
    shared: Arc<Shared>,
}

struct Shared {
    prompting: Mutex<()>,
    inner: Mutex<Inner>,
    journal: Journal,
    native_id: String,
    runtime: Runtime,
}

struct Inner {
    process: Option<Process>,
    state: SessionState,
    unusable: bool,
    run: Option<Run>,
    last_seq: Option<i64>,
}

struct Run {
    id: Option<RunId>,
    receipt: String,
    started: bool,
    next: u64,
    buffered: Vec<Notification>,
    candidate: Candidate,
    stream: Arc<Mutex<Reservation>>,
    start: Sender<Result<RunId, Error>>,
}

type Failure<'a> = (&'static str, &'a str);

const MISMATCH: &str = "deepseek_session_mismatch";
const EXTERNAL: &str = "deepseek_external_activity";

fn native_protocol(detail: &str) -> Error {
    Error::Harness(format!(
        "deepseek adapter: invalid native protocol observation: {detail}"
    ))
}

fn hold<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

impl Session {
    pub(crate) fn new(
        process: Process,
        state: SessionState,
        journal_capacity: usize,
        runtime: Runtime,
    ) -> Self {
        let inbound = process.client.inbound();
        let done = process.client.done();
        let shared = Arc::new(Shared {
            native_id: state.session_id.as_str().into(),
            inner: Mutex::new(Inner {
                process: Some(process),
                state,
                unusable: false,
                run: None,
                last_seq: None,
            }),
            journal: Journal::new(journal_capacity),
            runtime,
            prompting: Mutex::new(()),
        });
        let reader = shared.clone();
        std::thread::spawn(move || reader.dispatch(&inbound, &done));
        Self { shared }
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.shared.lock();
        if inner.process.is_none() || inner.unusable {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn submit(
        &self,
        request: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        let runtime = &self.shared.runtime;
        let (blocks, message_ids) = native_prompt(&request, &|kind| runtime.new_id(kind))?;
        let prompting = hold(&self.shared.prompting);
        let (client, stream, started) = {
            let mut inner = self.shared.lock();
            let client = match &inner.process {
                Some(process) if !inner.unusable => process.client.clone(),
                _ => return Err(Error::SessionClosed),
            };
            if request.session_id != inner.state.session_id {
                return Err(Error::RunNotFound);
            }
            if inner.run.is_some() || inner.state.status != SessionStatus::IDLE {
                return Err(Error::RunActive);
            }
            let (start, started) = crossbeam_channel::bounded(1);
            let stream = Arc::new(Mutex::new(self.shared.journal.reserve()));
            inner.run = Some(Run {
                id: None,
                receipt: String::new(),
                started: false,
                next: 1,
                buffered: Vec::new(),
                candidate: Candidate::default(),
                stream: stream.clone(),
                start,
            });
            (client, stream, started)
        };
        let receipt = self.prompt(client, blocks).inspect_err(|error| {
            self.shared
                .abort_own(&mut self.shared.lock(), &stream, error.clone());
        })?;
        {
            let mut inner = self.shared.lock();
            if receipt.is_empty() {
                inner.unusable = true;
                let error = native_protocol("prompt response omitted messageId");
                self.shared.abort_own(&mut inner, &stream, error.clone());
                return Err(error);
            }
            if let Some(run) = inner.run.as_mut().filter(|run| run.owns(&stream)) {
                run.receipt.clone_from(&receipt);
                self.shared.admit(&mut inner);
            }
        }
        drop(prompting);
        let run_id = started.recv().unwrap_or(Err(Error::SessionClosed))?;
        let stream = hold(&stream).stream();
        Ok((
            MessageSubmitResponse {
                session_id: request.session_id,
                accepted: true,
                submission_id: SubmissionId::from(receipt),
                requested_delivery: RequestedDeliveryMode::AUTO,
                effective_delivery: EffectiveDeliveryMode::START,
                delivery_resolution: "session_idle".into(),
                admission: SubmitAdmission::STARTED,
                run_id: Some(run_id),
                status: RunStatus::RUNNING,
                model_id: self.shared.lock().state.current_model_id.clone(),
                message_ids,
                ..MessageSubmitResponse::default()
            },
            stream,
        ))
    }

    fn prompt(
        &self,
        client: crate::rpc::RpcClient,
        blocks: Vec<crate::native::ContentBlock>,
    ) -> Result<String, Error> {
        let params = serde_json::value::to_raw_value(&SessionPromptParams {
            session_id: self.shared.native_id.clone(),
            content_blocks: Some(blocks),
        })
        .map_err(Error::harness)?;
        let (report, reported) = crossbeam_channel::bounded(1);
        let (answer, answered) = crossbeam_channel::bounded(1);
        std::thread::spawn(move || {
            let _ = answer.send(client.call_started(METHOD_SESSION_PROMPT, Some(params), report));
        });
        if let Ok(Some(refused)) = reported.recv() {
            return Err(Error::Harness(refused));
        }
        let raw = answered
            .recv()
            .map_err(|_| Error::SessionClosed)?
            .map_err(Error::harness)?;
        if raw.get() == "null" {
            return Ok(String::new());
        }
        serde_json::from_str::<SessionPromptResult>(raw.get())
            .map(|result| result.message_id)
            .map_err(Error::harness)
    }

    pub fn resolve(&self, _: InteractionResolution) -> Result<(), Error> {
        Err(Error::InteractionNotFound)
    }

    pub fn cancel(&self, _: &RunId) -> Result<RunCancelResponse, Error> {
        Err(Error::Harness(
            "deepseek adapter: operation unavailable".into(),
        ))
    }

    pub fn resume(&self, request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        let inner = self.shared.lock();
        if inner.process.is_none() {
            return Err(Error::SessionClosed);
        }
        let journal = &self.shared.journal;
        let run = request.run_id.as_str();
        let latest = journal.ended(run).ok_or(Error::RunNotFound)?;
        if request.after_sequence > latest {
            return Err(Error::ReplayCursorFuture);
        }
        let (recovery, replay) =
            journal.resume(inner.state.clone(), run, request.after_sequence, latest);
        if let Some(gap) = recovery.replay_gap {
            return Err(Error::ReplayGap(gap));
        }
        Ok((recovery, replay))
    }

    pub fn close(&self) -> Result<(), Error> {
        let inner = self.shared.lock();
        if inner.process.is_some() && inner.run.is_some() {
            return Err(Error::RunActive);
        }
        self.shut_down(inner)
    }

    fn shut_down(&self, mut inner: MutexGuard<'_, Inner>) -> Result<(), Error> {
        let Some(process) = inner.process.take() else {
            return Ok(());
        };
        inner.state.status = SessionStatus::CLOSED;
        drop(inner);
        let closed = process.close();
        self.shared.journal.close();
        closed.map_err(Error::Harness)
    }

    pub fn native_session_id(&self) -> String {
        self.shared.native_id.clone()
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        let _ = self.shut_down(self.shared.lock());
    }
}

impl Shared {
    fn lock(&self) -> MutexGuard<'_, Inner> {
        hold(&self.inner)
    }

    fn dispatch(&self, inbound: &Receiver<Inbound>, done: &Receiver<()>) {
        loop {
            select! {
                recv(inbound) -> message => match message {
                    Ok(message) => self.reduce(message),
                    Err(_) => break,
                },
                recv(done) -> _ => break,
            }
        }
        while let Ok(message) = inbound.try_recv() {
            self.reduce(message);
        }
        let mut inner = self.lock();
        if let Some(process) = &inner.process {
            let reason = process.client.err().unwrap_or_else(|| "<nil>".into());
            inner.unusable = true;
            self.fail_settled(
                &mut inner,
                ("deepseek_process_exit", &reason),
                SETTLED_BY_INFERRED,
            );
        }
    }

    fn reduce(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(_) => {
                let mut inner = self.lock();
                inner.unusable = true;
                self.fail(&mut inner, (EXTERNAL, "reverse request"));
            }
            Inbound::Notification(notification) => self.apply_notification(&notification),
        }
    }

    fn apply_notification(&self, notification: &NotificationMessage) {
        let params = notification.params.as_ref().map_or("", |raw| raw.get());
        let decoded = decode_notification(&notification.method, params.as_bytes()).ok();
        let mut inner = self.lock();
        if let Some(run) = inner.run.as_mut() {
            let Some(decoded) = decoded else { return };
            if !run.started {
                run.buffered.push(decoded.clone());
            }
            return self.apply_native(&mut inner, &decoded);
        }
        if let (false, Some(Notification::SessionStatus(status))) = (inner.unusable, &decoded) {
            if status.session_id == self.native_id {
                return;
            }
        }
        inner.unusable = true;
    }

    fn apply_native(&self, inner: &mut Inner, notification: &Notification) {
        if let Some(failure) = self.foreign(inner, notification) {
            return self.fail(inner, failure);
        }
        if let Notification::SessionEvent(event) = notification {
            inner.last_seq = Some(event.event.seq);
            self.admit(inner);
        }
    }

    fn foreign(&self, inner: &Inner, notification: &Notification) -> Option<Failure<'static>> {
        let native = &self.native_id;
        let (code, detail) = match notification {
            Notification::SessionEvent(event) if &event.session_id != native => {
                (MISMATCH, "session.event for foreign session")
            }
            Notification::SessionEvent(event)
                if inner.last_seq.is_some_and(|last| event.event.seq <= last) =>
            {
                (
                    "deepseek_invalid_sequence",
                    "non-monotonic native event sequence",
                )
            }
            Notification::SessionStatus(status) if &status.session_id != native => {
                (MISMATCH, "session.status for foreign session")
            }
            Notification::SubagentStarted(child)
                if &child.parent_session_id != native || &child.child_session_id == native =>
            {
                (EXTERNAL, "foreign or recursive subagent")
            }
            Notification::SubagentFinished(child) if &child.parent_session_id != native => {
                (EXTERNAL, "foreign child finish")
            }
            _ => return None,
        };
        Some((code, detail))
    }

    fn admit(&self, inner: &mut Inner) {
        let Some(run) = inner
            .run
            .as_mut()
            .filter(|run| !run.started && !run.receipt.is_empty())
        else {
            return;
        };
        let events = run
            .buffered
            .iter()
            .filter_map(|notification| match notification {
                Notification::SessionEvent(event) => Some(&event.event),
                _ => None,
            });
        match run.candidate.evaluate(&run.receipt, events) {
            Admission::Pending => {}
            Admission::Refused => {
                self.abort(inner, native_protocol("submission ownership proof failed"));
            }
            Admission::Admitted => {
                let id = RunId::from(self.runtime.new_id("run"));
                run.id = Some(id.clone());
                run.started = true;
                hold(&run.stream).bind(id.as_str());
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
                if let Some(run) = &inner.run {
                    let _ = run.start.send(emitted.map(|()| id));
                }
            }
        }
    }

    fn emit(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &impl serde::Serialize,
        terminal: bool,
    ) -> Result<(), Error> {
        let run = inner.run.as_mut().ok_or(Error::RunNotFound)?;
        let mut envelope =
            Envelope::new(kind, self.runtime.new_id("event"), payload).map_err(Error::harness)?;
        let now = self.runtime.now_ms();
        envelope.sequence = Some(run.next);
        envelope.timestamp_ms = Some(now);
        envelope.session_id = Some(inner.state.session_id.clone());
        envelope.run_id.clone_from(&run.id);
        envelope.capability_revision = Some(crate::adapter::pin().capability_revision.clone());
        inner.state.transcript_cursor = run.next.to_string();
        inner.state.updated_at_ms = now;
        run.next += 1;
        self.journal.append(envelope, terminal);
        if terminal {
            inner.run = None;
            inner.state.status = SessionStatus::IDLE;
            inner.state.active_run_id = None;
        }
        Ok(())
    }

    fn fail(&self, inner: &mut Inner, failure: Failure) {
        self.fail_settled(inner, failure, "");
    }

    fn fail_settled(&self, inner: &mut Inner, (code, detail): Failure, settled_by: &str) {
        let Some(run_id) = inner.run.as_ref().and_then(|run| run.id.clone()) else {
            return self.abort(inner, native_protocol(detail));
        };
        let payload = RunFailedPayload {
            session_id: inner.state.session_id.clone(),
            run_id,
            error: ProtocolError {
                code: code.into(),
                message: detail.into(),
                ..ProtocolError::default()
            },
            settled_by: settled_by.into(),
            ..RunFailedPayload::default()
        };
        let _ = self.emit(inner, EnvelopeType::RUN_FAILED, &payload, true);
    }

    fn abort(&self, inner: &mut Inner, error: Error) {
        if let Some(run) = inner.run.take_if(|run| !run.started) {
            run.end(error);
        }
    }

    fn abort_own(&self, inner: &mut Inner, stream: &Arc<Mutex<Reservation>>, error: Error) {
        if let Some(run) = inner.run.take_if(|run| run.owns(stream)) {
            run.end(error);
        }
    }
}

impl Run {
    fn owns(&self, stream: &Arc<Mutex<Reservation>>) -> bool {
        !self.started && Arc::ptr_eq(&self.stream, stream)
    }

    fn end(self, error: Error) {
        hold(&self.stream).release();
        let _ = self.start.send(Err(error));
    }
}

#[cfg(test)]
#[path = "../tests/support/session_journal.rs"]
mod tests;
