use std::sync::{Arc, Mutex, MutexGuard};

use crossbeam_channel::{select, Receiver, Sender};
use oap_adapter::journal::{Journal, Reservation};
use oap_adapter::{Error, EventStream, InteractionResolution, Recovery, ResumeRequest};
use oap_protocol::{
    Admission, EffectiveDeliveryMode, MessageSubmitRequest, MessageSubmitResponse,
    RequestedDeliveryMode, RunCancelResponse, RunId, RunStatus, SessionState, SessionStatus,
    SubmissionId,
};

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
}

struct Run {
    stream: Arc<Mutex<Reservation>>,
    start: Sender<Result<RunId, Error>>,
}

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
                stream: stream.clone(),
                start,
            });
            (client, stream, started)
        };
        let receipt = self.prompt(client, blocks).inspect_err(|error| {
            self.shared
                .abort_own(&mut self.shared.lock(), &stream, error.clone());
        })?;
        if receipt.is_empty() {
            let mut inner = self.shared.lock();
            inner.unusable = true;
            let error = native_protocol("prompt response omitted messageId");
            self.shared.abort_own(&mut inner, &stream, error.clone());
            return Err(error);
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
                admission: Admission::STARTED,
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
        let process = {
            let mut inner = self.shared.lock();
            if inner.process.is_none() {
                return Ok(());
            }
            if inner.run.is_some() {
                return Err(Error::RunActive);
            }
            let Some(process) = inner.process.take() else {
                return Ok(());
            };
            inner.state.status = SessionStatus::CLOSED;
            process
        };
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
        let _ = self.close();
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
            self.fail(&mut inner, &reason);
        }
    }

    fn reduce(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(_) => {
                let mut inner = self.lock();
                inner.unusable = true;
                self.fail(&mut inner, "reverse request");
            }
            Inbound::Notification(notification) => self.apply_notification(&notification),
        }
    }

    fn apply_notification(&self, notification: &NotificationMessage) {
        let params = notification.params.as_ref().map_or("", |raw| raw.get());
        let decoded = decode_notification(&notification.method, params.as_bytes()).ok();
        let mut inner = self.lock();
        if inner.run.is_some() {
            if let Some(detail) = decoded.as_ref().and_then(|value| self.foreign(value)) {
                self.fail(&mut inner, detail);
            }
            return;
        }
        if let (false, Some(Notification::SessionStatus(status))) = (inner.unusable, &decoded) {
            if status.session_id == self.native_id {
                return;
            }
        }
        inner.unusable = true;
    }

    fn foreign(&self, notification: &Notification) -> Option<&'static str> {
        let native = &self.native_id;
        match notification {
            Notification::SessionEvent(event) if &event.session_id != native => {
                Some("session.event for foreign session")
            }
            Notification::SessionStatus(status) if &status.session_id != native => {
                Some("session.status for foreign session")
            }
            Notification::SubagentStarted(child)
                if &child.parent_session_id != native || &child.child_session_id == native =>
            {
                Some("foreign or recursive subagent")
            }
            Notification::SubagentFinished(child) if &child.parent_session_id != native => {
                Some("foreign child finish")
            }
            _ => None,
        }
    }

    fn fail(&self, inner: &mut Inner, detail: &str) {
        self.abort(inner, native_protocol(detail));
    }

    fn abort(&self, inner: &mut Inner, error: Error) {
        if let Some(run) = inner.run.take() {
            run.end(error);
        }
    }

    fn abort_own(&self, inner: &mut Inner, stream: &Arc<Mutex<Reservation>>, error: Error) {
        if let Some(run) = inner.run.take_if(|run| Arc::ptr_eq(&run.stream, stream)) {
            run.end(error);
        }
    }
}

impl Run {
    fn end(self, error: Error) {
        hold(&self.stream).release();
        let _ = self.start.send(Err(error));
    }
}

#[cfg(test)]
#[path = "../tests/support/session_journal.rs"]
mod tests;
