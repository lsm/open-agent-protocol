use std::sync::Arc;
use std::time::Instant;

use crossbeam_channel::{select, Receiver, Sender};
use oap_adapter::gojson::{decode, go_quote, Decode};
use oap_adapter::Error;
use serde::Serialize;
use serde_json::value::RawValue;

use crate::native::{
    SessionCreateParams, SessionCreateResult, SessionResumeParams, SessionResumeResult,
    METHOD_SESSION_CREATE, METHOD_SESSION_RESUME,
};
use crate::process::{Process, ProcessConfig};
use crate::rpc::{self, error, Fault, Inbound, RpcClient};

pub const NATIVE_PROTOCOL: &str = "hermes adapter: invalid native protocol observation";
pub const PENDING_AUTO_CONTINUE: &str = "Hermes would restart the turn a crash interrupted, which a reopen must not do; open it in Hermes to settle the turn, or wait for Hermes to retire the marker";
const RELAY_CAPACITY: usize = 256;
const SESSION_NOT_FOUND: i64 = 4007;

pub trait Client: Send + Sync {
    fn call(
        &self,
        method: &str,
        params: Box<RawValue>,
        deadline: Option<Instant>,
    ) -> Result<Box<RawValue>, rpc::Error>;

    fn inbound(&self) -> Receiver<Inbound>;

    fn done(&self) -> Receiver<()>;

    fn err(&self) -> Option<String>;

    fn close(&self) -> Result<(), String>;

    fn stored_session_id(&self) -> String {
        String::new()
    }
}

pub trait ClientFactory: Send + Sync {
    fn start(&self, deadline: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error>;

    fn start_with_reasoning(
        &self,
        _effort: &str,
        deadline: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, String), Error> {
        self.start(deadline)
    }

    fn reopen(
        &self,
        _stored: &str,
        _deadline: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, SessionResumeResult), String> {
        Err("this client factory cannot reload a stored session".into())
    }
}

pub fn native_protocol() -> Error {
    Error::Harness(NATIVE_PROTOCOL.into())
}

pub fn call<T: Decode>(
    client: &dyn Client,
    method: &str,
    params: &impl Serialize,
    deadline: Option<Instant>,
) -> Result<T, rpc::Error> {
    let params = serde_json::value::to_raw_value(params)
        .map_err(|failure| error(Fault::Encode(failure.to_string())))?;
    let result = client.call(method, params, deadline)?;
    decode(result.get().as_bytes()).map_err(|failure| {
        error(Fault::Decode {
            method: method.into(),
            detail: failure.to_string(),
        })
    })
}

pub struct ProcessClient {
    process: Process,
    stored: String,
    relay: Receiver<Inbound>,
}

impl ProcessClient {
    pub fn new(process: Process) -> Self {
        let (forward, relay) = crossbeam_channel::bounded(RELAY_CAPACITY);
        let client = &process.client;
        let (inbound, done, read_done) = (client.inbound(), client.done(), client.read_done());
        std::thread::spawn(move || relay_inbound(&inbound, &done, &read_done, &forward));
        Self {
            process,
            stored: String::new(),
            relay,
        }
    }

    pub fn rpc(&self) -> &RpcClient {
        &self.process.client
    }
}

fn relay_inbound(
    inbound: &Receiver<Inbound>,
    done: &Receiver<()>,
    read_done: &Receiver<()>,
    forward: &Sender<Inbound>,
) {
    let send = |message: Inbound| match message {
        Inbound::Barrier(ack) => {
            drop(ack);
            true
        }
        message => forward.send(message).is_ok(),
    };
    loop {
        select! {
            recv(inbound) -> message => if !message.is_ok_and(send) {
                return;
            },
            recv(done) -> _ => break,
        }
    }
    loop {
        select! {
            recv(inbound) -> message => if !message.is_ok_and(send) {
                return;
            },
            recv(read_done) -> _ => break,
        }
    }
    while let Ok(message) = inbound.try_recv() {
        if !send(message) {
            return;
        }
    }
}

impl Client for ProcessClient {
    fn call(
        &self,
        method: &str,
        params: Box<RawValue>,
        deadline: Option<Instant>,
    ) -> Result<Box<RawValue>, rpc::Error> {
        self.process.client.call(method, Some(params), deadline)
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.relay.clone()
    }

    fn done(&self) -> Receiver<()> {
        self.process.exited()
    }

    fn err(&self) -> Option<String> {
        self.process
            .wait_error()
            .or_else(|| self.process.client.err())
    }

    fn close(&self) -> Result<(), String> {
        self.process.close()
    }

    fn stored_session_id(&self) -> String {
        self.stored.clone()
    }
}

#[derive(Clone, Debug, Default)]
pub struct ProcessFactory {
    pub config: ProcessConfig,
    pub model: String,
}

impl ClientFactory for ProcessFactory {
    fn start(&self, deadline: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        self.start_with_reasoning("", deadline)
    }

    fn start_with_reasoning(
        &self,
        effort: &str,
        deadline: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, String), Error> {
        let process = Process::start(&self.config, deadline).map_err(Error::harness)?;
        let mut client = ProcessClient::new(process);
        let params = SessionCreateParams {
            model: self.model.clone(),
            reasoning_effort: effort.into(),
            ..SessionCreateParams::default()
        };
        let created: SessionCreateResult =
            call(&client, METHOD_SESSION_CREATE, &params, deadline).map_err(Error::harness)?;
        if created.session_id.is_empty() || created.stored_session_id.is_empty() {
            return Err(native_protocol());
        }
        client.stored = created.stored_session_id;
        Ok((Arc::new(client), created.session_id))
    }

    fn reopen(
        &self,
        stored: &str,
        deadline: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, SessionResumeResult), String> {
        let process = Process::start(&self.config, deadline).map_err(|error| error.to_string())?;
        let mut client = ProcessClient::new(process);
        let resumed = resume_stored(&client, stored, deadline)?;
        client.stored = stored.into();
        Ok((Arc::new(client), resumed))
    }
}

pub fn resume_stored(
    client: &dyn Client,
    stored: &str,
    deadline: Option<Instant>,
) -> Result<SessionResumeResult, String> {
    let params = SessionResumeParams {
        session_id: stored.into(),
    };
    let resumed: SessionResumeResult = call(client, METHOD_SESSION_RESUME, &params, deadline)
        .map_err(|failure| match failure.remote_code() {
            Some(SESSION_NOT_FOUND) => format!("Hermes has no stored session {}", go_quote(stored)),
            _ => failure.to_string(),
        })?;
    if resumed.session_id.is_empty() {
        return Err("Hermes omitted the reloaded session id".into());
    }
    if resumed
        .auto_continue
        .as_deref()
        .is_some_and(|raw| raw.get() != "null")
    {
        return Err(PENDING_AUTO_CONTINUE.into());
    }
    if resumed.running {
        return Err("Hermes reattached the stored session while it was running".into());
    }
    if !resumed.status.is_empty() && resumed.status != "idle" {
        return Err(format!(
            "Hermes reattached the stored session while it was {}",
            resumed.status
        ));
    }
    Ok(resumed)
}
