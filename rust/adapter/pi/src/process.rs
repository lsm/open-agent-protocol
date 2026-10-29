use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use crossbeam_channel::{select, Receiver};
use oap_adapter::process::{wait_error, Child, ChildConfig};
use serde_json::value::RawValue;

use crate::client::Client;
use crate::native::{decode_state, validate_state, Command, ExtensionUiResponse, SessionState};
use crate::rpc::{Error, Inbound};
use crate::rpc_client::{ClientOptions, RpcClient};

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
}

pub struct Process {
    client: RpcClient,
    child: Child,
    timeout: Duration,
    closed: Mutex<Option<Result<(), String>>>,
}

fn handshake(detail: impl std::fmt::Display) -> Error {
    Error::Other(format!("pi rpc: startup handshake failed: {detail}"))
}

impl Process {
    pub fn start(
        config: &ProcessConfig,
        deadline: Option<Instant>,
    ) -> Result<(Arc<Self>, SessionState), Error> {
        if config.path.is_empty() {
            return Err(handshake("executable path is required"));
        }
        let mut args = config.args.clone();
        args.extend(["--mode".to_owned(), "rpc".to_owned()]);
        let (child, stdout) = Child::spawn(&ChildConfig {
            path: config.path.clone(),
            args,
            dir: config.dir.clone(),
            env: config.env.clone(),
            stderr_limit: config.stderr_limit,
        })
        .map_err(|failure| Error::Other(failure.to_string()))?;
        let releaser = child.clone();
        let client = RpcClient::new(
            stdout,
            child.writer(),
            ClientOptions {
                frame_limit: config.frame_limit,
                queue_capacity: config.queue_capacity,
                write_queue_capacity: config.write_queue_capacity,
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
            child,
            timeout,
            closed: Mutex::new(None),
        };
        let (reply, ready) = crossbeam_channel::bounded(1);
        let caller = process.client.clone();
        std::thread::spawn(move || {
            let command = Command {
                kind: "get_state".into(),
                ..Command::default()
            };
            let _ = reply.send(caller.call(&command, true));
        });
        let inbound = process.client.inbound();
        let done = process.client.done();
        let expiry = deadline.map_or_else(crossbeam_channel::never, crossbeam_channel::at);
        let outcome: Result<SessionState, String> = loop {
            select! {
                recv(ready) -> outcome => break match outcome {
                    Ok(Ok(Some(raw))) => decode_state(raw.get())
                        .map_err(|failure| format!("decode get_state response: {failure}")),
                    Ok(Ok(None)) => Ok(SessionState::default()),
                    Ok(Err(failure)) => Err(failure.to_string()),
                    Err(_) => Err(process.client.err().unwrap_or_else(|| "EOF".into())),
                },
                recv(inbound) -> message => match message {
                    Ok(Inbound::Barrier(ack)) => drop(ack),
                    _ => break Err("native observation preceded get_state response".into()),
                },
                recv(done) -> _ => break Err(process.client.err().unwrap_or_else(|| "EOF".into())),
                recv(expiry) -> _ => break Err("context deadline exceeded".into()),
            }
        };
        let validated = outcome.and_then(|state| {
            validate_state(&state)
                .map(|()| state)
                .map_err(|_| "invalid get_state response".into())
        });
        match validated {
            Ok(state) => Ok((Arc::new(process), state)),
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
        self.client
            .close_with(Error::Other("pi rpc: process exited".into()));
        let _ = self.child.exited().recv();
    }

    fn shutdown(&self) -> Result<(), String> {
        let mut closed = self.closed.lock().unwrap_or_else(|p| p.into_inner());
        if let Some(outcome) = closed.as_ref() {
            return outcome.clone();
        }
        self.child.release_stdin();
        let outcome = if self.child.await_exit(self.timeout) {
            match wait_error(self.child.exit_status()) {
                Some(failure) => Err(failure),
                None => Ok(()),
            }
        } else {
            self.child.kill();
            self.client
                .close_with(Error::Other("pi rpc: process exited".into()));
            let _ = self.child.exited().recv();
            Err("pi rpc: shutdown timed out".into())
        };
        self.client.close_with(Error::Closed);
        *closed = Some(outcome.clone());
        outcome
    }
}

impl Client for Process {
    fn call(&self, command: &Command, want: bool) -> Result<Option<Box<RawValue>>, Error> {
        self.client.call(command, want)
    }

    fn respond(&self, response: &ExtensionUiResponse) -> Result<(), Error> {
        self.client.respond(response)
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.client.inbound()
    }

    fn done(&self) -> Receiver<()> {
        self.child.exited()
    }

    fn err(&self) -> Option<String> {
        wait_error(self.child.exit_status()).or_else(|| self.client.err())
    }

    fn close(&self) -> Result<(), Error> {
        self.shutdown().map_err(Error::Other)
    }
}
