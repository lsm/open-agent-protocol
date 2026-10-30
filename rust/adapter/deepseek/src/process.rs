use std::sync::Mutex;
use std::time::{Duration, Instant};

use crossbeam_channel::select;
use oap_adapter::process::{wait_error, Child, ChildConfig};
use serde_json::value::RawValue;

use crate::native::{
    validate_initialize_params, validate_initialize_result, InitializeParams, InitializeResult,
    METHOD_INITIALIZE, METHOD_SHUTDOWN,
};
use crate::rpc::{
    check_notification, error, ClientOptions, Error, Fault, Inbound, RpcClient, DIALECT,
};

const DEFAULT_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);
const ABORT_STDERR_GRACE: Duration = Duration::from_millis(250);

#[derive(Clone, Debug, Default)]
pub struct ProcessConfig {
    pub path: String,
    pub args: Vec<String>,
    pub dir: String,
    pub env: Option<Vec<String>>,
    pub frame_limit: usize,
    pub queue_capacity: usize,
    pub write_queue_capacity: usize,
    pub stderr_limit: usize,
    pub shutdown_timeout: Option<Duration>,
    pub initialize: InitializeParams,
}

pub struct Process {
    pub client: RpcClient,
    pub initialize: InitializeResult,
    child: Child,
    timeout: Duration,
    closed: Mutex<Option<Result<(), String>>>,
}

fn handshake(detail: impl std::fmt::Display) -> Error {
    error(Fault::Transport(format!(
        "deepseek rpc: initialization handshake failed: {detail}"
    )))
}

fn go_kind(text: &str) -> &'static str {
    match text.as_bytes().first() {
        Some(b'[') => "array",
        Some(b'"') => "string",
        Some(b't' | b'f') => "bool",
        _ => "number",
    }
}

fn initialize_result(raw: &RawValue) -> Result<InitializeResult, Error> {
    let text = raw.get();
    let decoded = match text.as_bytes().first() {
        Some(b'n') => Ok(InitializeResult::default()),
        Some(b'{') => serde_json::from_str(text).map_err(|failure| failure.to_string()),
        _ => Err(format!(
            "json: cannot unmarshal {} into Go value of type native.InitializeResult",
            go_kind(text)
        )),
    };
    decoded.map_err(|detail| {
        error(Fault::Decode {
            method: METHOD_INITIALIZE.into(),
            detail,
        })
    })
}

impl Process {
    pub fn start(config: ProcessConfig, deadline: Option<Instant>) -> Result<Self, Error> {
        if config.path.is_empty() {
            return Err(handshake("executable path is required"));
        }
        validate_initialize_params(&config.initialize).map_err(handshake)?;
        let (child, stdout) = Child::spawn(&ChildConfig {
            path: config.path.clone(),
            args: config.args.clone(),
            dir: config.dir.clone(),
            env: config.env.clone(),
            stderr_limit: config.stderr_limit,
        })
        .map_err(|failure| error(Fault::Io(failure)))?;
        let releaser = child.clone();
        let client = RpcClient::new(
            DIALECT,
            stdout,
            child.writer(),
            ClientOptions {
                frame_limit: config.frame_limit,
                queue_capacity: config.queue_capacity,
                write_queue_capacity: config.write_queue_capacity,
                strict_response_ids: true,
                check_notification: Some(check_notification),
                closer: Some(Box::new(move || releaser.release_stdin())),
                ..ClientOptions::default()
            },
        );
        let timeout = config
            .shutdown_timeout
            .filter(|timeout| !timeout.is_zero())
            .unwrap_or(DEFAULT_SHUTDOWN_TIMEOUT);
        child.watch(client.read_done(), timeout);
        let process = Self {
            client,
            initialize: InitializeResult::default(),
            child,
            timeout,
            closed: Mutex::new(None),
        };
        let params = serde_json::value::to_raw_value(&config.initialize)
            .map_err(|failure| error(Fault::Encode(failure.to_string())))?;
        let (reply, ready) = crossbeam_channel::bounded(1);
        let caller = process.client.clone();
        std::thread::spawn(move || {
            let outcome = caller
                .call(METHOD_INITIALIZE, Some(params), deadline)
                .and_then(|raw| initialize_result(&raw));
            let _ = reply.send(outcome);
        });
        let mut inbound = process.client.inbound();
        let mut done = process.client.done();
        let expiry = deadline.map_or_else(crossbeam_channel::never, crossbeam_channel::at);
        let outcome: Result<InitializeResult, String> = loop {
            select! {
                recv(ready) -> outcome => break match outcome {
                    Ok(outcome) => outcome.map_err(|failure| failure.to_string()),
                    Err(_) => Err(process.client.err().unwrap_or_else(|| "EOF".into())),
                },
                recv(inbound) -> message => match message {
                    Ok(Inbound::Barrier(ack)) => {
                        drop(ack);
                        inbound = crossbeam_channel::never();
                        done = crossbeam_channel::never();
                    }
                    _ => break Err("native observation preceded initialize response".into()),
                },
                recv(done) -> _ => break Err(process.client.err().unwrap_or_else(|| "EOF".into())),
                recv(expiry) -> _ => break Err("context deadline exceeded".into()),
            }
        };
        match outcome
            .and_then(|initialized| validate_initialize_result(&initialized).map(|()| initialized))
        {
            Ok(initialize) => Ok(Self {
                initialize,
                ..process
            }),
            Err(detail) => {
                process.abort();
                Err(handshake(format!("{detail}; stderr: {}", process.stderr())))
            }
        }
    }

    pub fn stderr(&self) -> String {
        self.child.stderr()
    }

    fn abort(&self) {
        self.child.kill();
        self.child.drain_stderr(ABORT_STDERR_GRACE);
        self.client.close();
        let _ = self.child.exited().recv();
    }

    pub fn close(&self) -> Result<(), String> {
        let mut closed = self.closed.lock().unwrap_or_else(|p| p.into_inner());
        if let Some(outcome) = closed.as_ref() {
            return outcome.clone();
        }
        let inbound = self.client.inbound();
        let done = self.client.done();
        let drain = std::thread::spawn(move || loop {
            select! {
                recv(inbound) -> message => match message {
                    Ok(Inbound::Barrier(ack)) => drop(ack),
                    Ok(_) => {}
                    Err(_) => return,
                },
                recv(done) -> _ => return,
            }
        });
        let deadline = Instant::now() + self.timeout;
        let answer = self.client.call(METHOD_SHUTDOWN, None, Some(deadline));
        let expired =
            matches!(&answer, Err(failure) if matches!(failure.fault, Fault::DeadlineExceeded));
        if answer.is_ok_and(|raw| matches!(raw.get().as_bytes().first(), Some(b'n' | b'{'))) {
            self.child.release_stdin();
        }
        let left = deadline.saturating_duration_since(Instant::now());
        let outcome = if !expired && self.child.await_exit(left) {
            match wait_error(self.child.exit_status()) {
                Some(failure) => Err(failure),
                None => Ok(()),
            }
        } else {
            self.child.kill();
            self.client.close();
            let _ = self.child.exited().recv();
            Err("deepseek rpc: shutdown timed out".into())
        };
        self.client.close();
        let _ = drain.join();
        *closed = Some(outcome.clone());
        outcome
    }
}
