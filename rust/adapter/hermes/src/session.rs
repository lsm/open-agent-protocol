use std::sync::{Arc, Mutex, MutexGuard};

use crossbeam_channel::{Receiver, Sender};
use oap_adapter::gojson::go_quote;
use oap_adapter::journal::Journal;
use oap_adapter::runtime::Runtime;
use oap_adapter::{refuse_unadvertised_controls, Error, EventStream};
use oap_protocol::{
    Admission, ContentPartType, EffectiveDeliveryMode, EnvelopeId, MessageId, MessageRole,
    MessageSubmitRequest, MessageSubmitResponse, RecoveryMetadata, RequestedDeliveryMode, RunId,
    RunStatus, SessionState, SessionStatus, SubmissionId, ToolCallId,
};
use serde_json::value::RawValue;

use crate::client::{call, Client, NATIVE_PROTOCOL};
use crate::native::{
    Event, PromptSubmitParams, PromptSubmitResult, SessionInfoPayload, METHOD_PROMPT_SUBMIT,
    SUBMIT_STREAMING,
};
use crate::rpc::IncomingRequest;

pub(crate) const REOPEN_RECOVERY_REASON: &str = "Hermes restored the bound conversation and reports the model it last ran under; OAP runs and cursors remain process-local";

pub(crate) enum Observation {
    Event(Event),
    Request(Arc<IncomingRequest>),
}

pub(crate) struct Tool {
    pub(crate) native_id: String,
    pub(crate) id: ToolCallId,
    pub(crate) name: String,
    pub(crate) started: EnvelopeId,
    pub(crate) terminal: bool,
}

pub(crate) struct Run {
    pub(crate) id: RunId,
    pub(crate) message_id: MessageId,
    pub(crate) next: u64,
    pub(crate) started: bool,
    pub(crate) terminal: bool,
    pub(crate) accepted: bool,
    pub(crate) open_seen: bool,
    pub(crate) submitting: bool,
    pub(crate) buffered: Vec<Observation>,
    pub(crate) tools: Vec<Tool>,
    pub(crate) start: Option<Sender<Result<RunId, String>>>,
}

pub(crate) struct Inner {
    pub(crate) state: SessionState,
    pub(crate) closed: bool,
    pub(crate) unusable: bool,
    pub(crate) transport_dead: bool,
    pub(crate) last_seq: i64,
    pub(crate) run: Option<Run>,
}

pub(crate) struct Shared {
    pub(crate) client: Arc<dyn Client>,
    pub(crate) native_id: String,
    stored_id: String,
    pub(crate) runtime: Runtime,
    pub(crate) journal: Journal,
    prompt: Mutex<()>,
    inner: Mutex<Inner>,
    stop: Mutex<Option<Sender<()>>>,
    pub(crate) stopped: Receiver<()>,
}

pub struct Session {
    shared: Arc<Shared>,
}

pub(crate) fn hold<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

pub(crate) fn native_protocol(detail: &str) -> String {
    format!("{NATIVE_PROTOCOL}: {detail}")
}

fn submit_text(request: &MessageSubmitRequest) -> Result<String, Error> {
    refuse_unadvertised_controls(request, &[])?;
    let delivery = &request.delivery;
    if request.session_id.as_str().is_empty()
        || request.messages.len() != 1
        || !(delivery.is_empty() || *delivery == RequestedDeliveryMode::AUTO)
    {
        return Err(Error::InvalidSubmission);
    }
    let message = &request.messages[0];
    if message.role != MessageRole::USER {
        return Err(Error::InvalidSubmission);
    }
    if let Some(text) = message.content.text() {
        return Ok(text);
    }
    let parts = message.content.parts().ok_or(Error::InvalidSubmission)?;
    let mut texts = Vec::with_capacity(parts.len());
    for part in parts {
        if part.part_type != ContentPartType::TEXT {
            return Err(Error::InvalidSubmission);
        }
        texts.push(part.text);
    }
    Ok(texts.join("\n"))
}

impl Shared {
    pub(crate) fn lock(&self) -> MutexGuard<'_, Inner> {
        hold(&self.inner)
    }

    pub(crate) fn abort_pre_start(&self, inner: &mut Inner, error: String) {
        let Some(run) = inner
            .run
            .as_mut()
            .filter(|run| !run.terminal && !run.started)
        else {
            return;
        };
        run.terminal = true;
        if let Some(start) = run.start.take() {
            let _ = start.send(Err(error));
        }
        inner.run = None;
        inner.state.status = SessionStatus::IDLE;
    }

    fn submit(
        &self,
        request: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        let text = submit_text(&request)?;
        let prompt = hold(&self.prompt);
        let (start, started) = crossbeam_channel::bounded(1);
        let message_id = {
            let mut inner = self.lock();
            if inner.closed || inner.unusable {
                return Err(Error::SessionClosed);
            }
            if request.session_id != inner.state.session_id {
                return Err(Error::RunNotFound);
            }
            if inner.run.is_some() || inner.state.status != SessionStatus::IDLE {
                return Err(Error::RunActive);
            }
            let message_id = MessageId::from(self.runtime.new_id("message"));
            inner.run = Some(Run {
                id: RunId::default(),
                message_id: message_id.clone(),
                next: 1,
                started: false,
                terminal: false,
                accepted: false,
                open_seen: false,
                submitting: true,
                buffered: Vec::new(),
                tools: Vec::new(),
                start: Some(start),
            });
            message_id
        };
        let params = PromptSubmitParams {
            session_id: self.native_id.clone(),
            text,
        };
        let submitted =
            call::<PromptSubmitResult>(&*self.client, METHOD_PROMPT_SUBMIT, &params, None);
        {
            let mut inner = self.lock();
            let refused = match submitted {
                Err(failure) => Some(failure.to_string()),
                Ok(result) if result.status != SUBMIT_STREAMING => Some(native_protocol(&format!(
                    "prompt.submit returned status {}",
                    go_quote(&result.status)
                ))),
                Ok(_) => None,
            };
            let ours = inner
                .run
                .as_ref()
                .is_some_and(|run| run.message_id == message_id);
            if let Some(refused) = refused {
                if ours {
                    self.abort_pre_start(&mut inner, refused.clone());
                }
                return Err(Error::Harness(refused));
            }
            if let Some(run) = inner.run.as_mut().filter(|_| ours) {
                run.submitting = false;
                run.accepted = true;
                let open_seen = run.open_seen;
                if open_seen {
                    self.start_run(&mut inner);
                }
                if inner.transport_dead {
                    self.fail_transport(&mut inner);
                }
            }
        }
        drop(prompt);
        let run_id = match started.recv() {
            Ok(Ok(run_id)) => run_id,
            Ok(Err(failure)) => return Err(Error::Harness(failure)),
            Err(_) => return Err(Error::SessionClosed),
        };
        let model_id = self.lock().state.current_model_id.clone();
        let stream = self.journal.follow(run_id.as_str(), 0);
        Ok((
            MessageSubmitResponse {
                session_id: request.session_id,
                accepted: true,
                submission_id: SubmissionId::from(message_id.as_str()),
                requested_delivery: RequestedDeliveryMode::AUTO,
                effective_delivery: EffectiveDeliveryMode::START,
                delivery_resolution: "session_idle".into(),
                admission: Admission::STARTED,
                run_id: Some(run_id),
                status: RunStatus::RUNNING,
                model_id,
                message_ids: vec![message_id],
                ..MessageSubmitResponse::default()
            },
            stream,
        ))
    }

    fn close(&self) -> Result<(), Error> {
        {
            let mut inner = self.lock();
            if inner.closed {
                return Ok(());
            }
            if inner.run.is_some() {
                return Err(Error::RunActive);
            }
            inner.closed = true;
            inner.state.status = SessionStatus::CLOSED;
        }
        let closed = self.client.close().map_err(Error::Harness);
        hold(&self.stop).take();
        self.journal.close();
        closed
    }
}

impl Session {
    pub(crate) fn new(
        client: Arc<dyn Client>,
        native_id: String,
        runtime: Runtime,
        capacity: usize,
        state: SessionState,
    ) -> Self {
        let (stop, stopped) = crossbeam_channel::bounded(0);
        let shared = Arc::new(Shared {
            stored_id: client.stored_session_id(),
            client,
            native_id,
            runtime,
            journal: Journal::new(capacity),
            prompt: Mutex::new(()),
            inner: Mutex::new(Inner {
                state,
                closed: false,
                unusable: false,
                transport_dead: false,
                last_seq: 0,
                run: None,
            }),
            stop: Mutex::new(Some(stop)),
            stopped,
        });
        let dispatching = shared.clone();
        std::thread::spawn(move || dispatching.dispatch());
        Self { shared }
    }

    pub(crate) fn restore_state(&self, info: Option<&RawValue>) {
        let mut inner = self.shared.lock();
        inner.state.recovery = Some(RecoveryMetadata {
            recovered: true,
            reason: REOPEN_RECOVERY_REASON.into(),
            ..RecoveryMetadata::default()
        });
        let info = info.and_then(|raw| serde_json::from_str::<SessionInfoPayload>(raw.get()).ok());
        if let Some(info) = info.filter(|info| !info.model.is_empty()) {
            inner.state.current_model_id = info.model;
        }
    }

    pub fn native_session_id(&self) -> String {
        self.shared.stored_id.clone()
    }

    pub fn submit(
        &self,
        request: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        self.shared.submit(request)
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.shared.lock();
        if inner.closed || inner.unusable {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn close(&self) -> Result<(), Error> {
        self.shared.close()
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        hold(&self.shared.stop).take();
    }
}
