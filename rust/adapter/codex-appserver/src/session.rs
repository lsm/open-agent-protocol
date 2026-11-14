use std::collections::HashMap;
use std::sync::{Arc, Condvar, Mutex, MutexGuard};

use crossbeam_channel::{Receiver, Sender};

use oap_adapter::journal::Journal;
use oap_adapter::runtime::Runtime;
use oap_adapter::{
    refuse_unadvertised_controls, Error, EventStream, InteractionResolution, Recovery,
    ResumeRequest, UnsupportedControl, CONTROL_UNSATISFIABLE,
};
use oap_protocol::{
    Admission, EffectiveDeliveryMode, InputQuestion, InteractionId, MessageId, MessageRole,
    MessageSubmitRequest, MessageSubmitResponse, ParticipantId, RequestedDeliveryMode, RunId,
    RunStatus, SessionState, SessionStatus, SubmissionId, ToolCallId, FEATURE_MODEL_SELECTION,
    FEATURE_STRUCTURED_OUTPUT,
};
use oap_validation::OutputSchema;
use serde_json::value::RawValue;

use crate::adapter::{call, native_protocol};
use crate::client::Client;
use crate::native::{TurnStartParams, TurnStartResponse, UserInput, METHOD_TURN_START};
use crate::rpc::{Inbound, IncomingRequest};

pub(crate) struct Run {
    pub(crate) model: String,
    pub(crate) message_id: MessageId,
    pub(crate) started: bool,
    pub(crate) terminal: bool,
    pub(crate) next: u64,
    pub(crate) text: String,
    pub(crate) schema: Option<OutputSchema>,
    pub(crate) final_text: Option<String>,
}

#[derive(Clone)]
pub(crate) struct Binding {
    pub(crate) run: RunId,
    pub(crate) tool_call: ToolCallId,
    pub(crate) name: String,
    pub(crate) arguments: Box<RawValue>,
    pub(crate) terminal: bool,
    pub(crate) order: u64,
}

#[derive(Clone)]
pub(crate) struct Interaction {
    pub(crate) run: RunId,
    pub(crate) tool_call: ToolCallId,
    pub(crate) request: Arc<IncomingRequest>,
    pub(crate) resolved: bool,
    pub(crate) choices: Vec<String>,
    pub(crate) questions: Option<Vec<InputQuestion>>,
    pub(crate) order: u64,
}

pub(crate) struct Inner {
    pub(crate) state: SessionState,
    pub(crate) closed: bool,
    pub(crate) active: Option<RunId>,
    pub(crate) runs: HashMap<RunId, Run>,
    pub(crate) turns: HashMap<String, RunId>,
    pub(crate) items: HashMap<String, Binding>,
    pub(crate) interactions: HashMap<InteractionId, Interaction>,
    pub(crate) next_order: u64,
}

pub(crate) struct Shared {
    pub(crate) client: Arc<dyn Client>,
    pub(crate) thread_id: String,
    pub(crate) participant: ParticipantId,
    model: String,
    pub(crate) runtime: Runtime,
    pub(crate) journal: Journal,
    pub(crate) operating: Mutex<Operating>,
    idle: Condvar,
    inner: Mutex<Inner>,
    stop: Mutex<Option<Sender<()>>>,
    pub(crate) stopped: Receiver<()>,
}

#[derive(Default)]
pub(crate) struct Operating {
    pub(crate) starting: bool,
    pub(crate) deferred: Vec<Inbound>,
}

pub struct Session {
    shared: Arc<Shared>,
}

pub(crate) fn hold<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

struct Starting<'a>(&'a Shared);

impl Drop for Starting<'_> {
    fn drop(&mut self) {
        let mut operating = hold(&self.0.operating);
        {
            let mut inner = self.0.lock();
            inner.active = None;
            inner.closed = true;
            inner.state.active_run_id = None;
            inner.state.status = SessionStatus::CLOSED;
        }
        operating.deferred.clear();
        operating.starting = false;
        self.0.idle.notify_all();
    }
}

impl Shared {
    pub(crate) fn new(
        client: Arc<dyn Client>,
        thread_id: String,
        participant: ParticipantId,
        model: String,
        runtime: Runtime,
        capacity: usize,
        state: SessionState,
    ) -> Self {
        let (stop, stopped) = crossbeam_channel::bounded(0);
        Self {
            client,
            thread_id,
            participant,
            model,
            runtime,
            journal: Journal::new(capacity),
            operating: Mutex::default(),
            idle: Condvar::new(),
            inner: Mutex::new(Inner {
                state,
                closed: false,
                active: None,
                runs: HashMap::new(),
                turns: HashMap::new(),
                items: HashMap::new(),
                interactions: HashMap::new(),
                next_order: 0,
            }),
            stop: Mutex::new(Some(stop)),
            stopped,
        }
    }

    pub(crate) fn operate(&self) -> MutexGuard<'_, Operating> {
        let mut operating = hold(&self.operating);
        while operating.starting {
            operating = self
                .idle
                .wait(operating)
                .unwrap_or_else(|poison| poison.into_inner());
        }
        operating
    }

    pub(crate) fn lock(&self) -> MutexGuard<'_, Inner> {
        hold(&self.inner)
    }

    pub fn native_session_id(&self) -> String {
        self.thread_id.clone()
    }

    pub fn submit(
        &self,
        request: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        let mut operating = self.operate();
        refuse_unadvertised_controls(
            &request,
            &[FEATURE_MODEL_SELECTION, FEATURE_STRUCTURED_OUTPUT],
        )?;
        let model = match &request.model_id {
            Some(model) if model.is_empty() => return Err(Error::ModelNotFound(String::new())),
            Some(model) => model.clone(),
            None => self.model.clone(),
        };
        let schema = match &request.output_schema {
            Some(schema) => Some(OutputSchema::compile(schema.get()).map_err(|detail| {
                Error::UnsupportedControl(Box::new(UnsupportedControl {
                    feature: FEATURE_STRUCTURED_OUTPUT.into(),
                    reason: CONTROL_UNSATISFIABLE,
                    field: "output_schema".into(),
                    detail,
                    ..UnsupportedControl::default()
                }))
            })?),
            None => None,
        };
        let session_id = self.lock().state.session_id.clone();
        if request.session_id != session_id
            || request.messages.is_empty()
            || !(request.delivery.is_empty() || request.delivery == RequestedDeliveryMode::AUTO)
            || !request.allow_degraded_features.is_empty()
            || !request.metadata.is_empty()
        {
            return Err(Error::InvalidSubmission);
        }
        let mut input = Vec::with_capacity(request.messages.len());
        let mut message_ids = Vec::with_capacity(request.messages.len());
        for message in &request.messages {
            let text = message
                .content
                .raw()
                .and_then(|raw| serde_json::from_str::<Option<String>>(raw.get()).ok());
            let (true, Some(text)) = (message.role == MessageRole::USER, text) else {
                return Err(Error::UnsupportedInput);
            };
            input.push(UserInput {
                kind: "text".into(),
                text: text.unwrap_or_default(),
            });
            message_ids.push(match &message.id {
                Some(id) if !id.as_str().is_empty() => id.clone(),
                _ => MessageId::from(self.runtime.new_id("message")),
            });
        }
        {
            let inner = self.lock();
            if inner.closed {
                return Err(Error::SessionClosed);
            }
            if inner.active.is_some() {
                return Err(Error::RunActive);
            }
        }
        let params = TurnStartParams {
            thread_id: self.thread_id.clone(),
            input,
            model: model.clone(),
            effort: String::new(),
            output_schema: request.output_schema.clone(),
        };
        let run_id = RunId::from(self.runtime.new_id("run"));
        let message_id = MessageId::from(self.runtime.new_id("message"));
        {
            let mut inner = self.lock();
            inner.active = Some(run_id.clone());
            inner.state.status = SessionStatus::QUEUED;
            inner.state.active_run_id = Some(run_id.clone());
            inner.state.updated_at_ms = self.runtime.now_ms();
        }
        operating.starting = true;
        drop(operating);
        let starting = Starting(self);
        let started = call::<TurnStartResponse>(&*self.client, METHOD_TURN_START, &params);
        let mut operating = hold(&self.operating);
        let outcome = (|| -> Result<(MessageSubmitResponse, EventStream), Error> {
            let mut inner = self.lock();
            let turn = match started {
                Ok(response) if !response.turn.id.is_empty() => response.turn.id,
                outcome => {
                    inner.active = None;
                    inner.state.active_run_id = None;
                    let (error, closes) = match outcome {
                        Err(failure) => (
                            Error::Harness(format!("start Codex turn: {failure}")),
                            self.client.err().is_some(),
                        ),
                        Ok(_) => (native_protocol("turn/start returned no turn id"), true),
                    };
                    if closes {
                        inner.closed = true;
                        inner.state.status = SessionStatus::CLOSED;
                    } else {
                        inner.state.status = SessionStatus::IDLE;
                    }
                    return Err(error);
                }
            };
            inner.turns.insert(turn, run_id.clone());
            inner.runs.insert(
                run_id.clone(),
                Run {
                    model: model.clone(),
                    message_id,
                    started: false,
                    terminal: false,
                    next: 1,
                    text: String::new(),
                    schema,
                    final_text: None,
                },
            );
            drop(inner);
            let stream = self.journal.follow(run_id.as_str(), 0);
            Ok((
                MessageSubmitResponse {
                    session_id,
                    accepted: true,
                    submission_id: SubmissionId::from(self.runtime.new_id("submission")),
                    requested_delivery: RequestedDeliveryMode::AUTO,
                    effective_delivery: EffectiveDeliveryMode::START,
                    delivery_resolution: "session_idle".into(),
                    admission: Admission::STARTED,
                    run_id: Some(run_id),
                    status: RunStatus::RUNNING,
                    model_id: model,
                    message_ids,
                    ..MessageSubmitResponse::default()
                },
                stream,
            ))
        })();
        for message in std::mem::take(&mut operating.deferred) {
            self.handle(message);
        }
        operating.starting = false;
        self.idle.notify_all();
        std::mem::forget(starting);
        outcome
    }

    pub fn resume(&self, request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        let inner = self.lock();
        let run = inner.runs.get(&request.run_id).ok_or(Error::RunNotFound)?;
        let latest = run.next - 1;
        if request.after_sequence > latest {
            return Err(Error::ReplayCursorFuture);
        }
        let (recovery, replay) = self.journal.resume(
            inner.state.clone(),
            request.run_id.as_str(),
            request.after_sequence,
            latest,
        );
        if let Some(gap) = recovery.replay_gap {
            return Err(Error::ReplayGap(gap));
        }
        Ok((recovery, replay))
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.lock();
        if inner.closed {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn close(&self) -> Result<(), Error> {
        let _operating = self.operate();
        {
            let mut inner = self.lock();
            if inner.closed {
                return Ok(());
            }
            if inner.active.is_some() {
                return Err(Error::RunActive);
            }
            inner.closed = true;
            inner.state.status = SessionStatus::CLOSED;
            inner.state.active_run_id = None;
            hold(&self.stop).take();
        }
        self.journal.close();
        self.client.close().map_err(Error::harness)
    }
}

impl Session {
    pub(crate) fn new(shared: Shared) -> Self {
        let shared = Arc::new(shared);
        let dispatching = shared.clone();
        std::thread::spawn(move || dispatching.dispatch());
        Self { shared }
    }

    pub fn native_session_id(&self) -> String {
        self.shared.native_session_id()
    }

    pub fn submit(
        &self,
        request: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        self.shared.submit(request)
    }

    pub fn resume(&self, request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        self.shared.resume(request)
    }

    pub fn resolve(&self, resolution: InteractionResolution) -> Result<(), Error> {
        self.shared.resolve(resolution)
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        self.shared.state()
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
