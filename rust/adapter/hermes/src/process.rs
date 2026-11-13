use std::sync::Mutex;
use std::time::{Duration, Instant};

use crossbeam_channel::{select, Receiver};
use oap_adapter::process::{wait_error, Child, ChildConfig};

use crate::native::{decode_strict, Event, ReadyPayload, EVENT_GATEWAY_READY};
use crate::rpc::{
    check_notification, error, ClientOptions, Error, Fault, Inbound, RpcClient, DIALECT,
};

const DEFAULT_EXIT_TIMEOUT: Duration = Duration::from_secs(10);
const INTERRUPT_GRACE: Duration = Duration::from_secs(2);
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
    pub exit_timeout: Option<Duration>,
}

pub struct Process {
    pub client: RpcClient,
    pub ready: ReadyPayload,
    child: Child,
    timeout: Duration,
    closed: Mutex<Option<Result<(), String>>>,
}

fn handshake(detail: impl std::fmt::Display) -> Error {
    error(Fault::Transport(format!(
        "hermes rpc: gateway ready handshake failed: {detail}"
    )))
}

fn ready_from(message: Inbound) -> Result<ReadyPayload, String> {
    let notification = match message {
        Inbound::Barrier(ack) => {
            drop(ack);
            return Err("response preceded the ready event".into());
        }
        Inbound::Request(_) => return Err("reverse request preceded the ready event".into()),
        Inbound::Notification(notification) => notification,
    };
    let params = notification.params.as_deref().map_or("", |raw| raw.get());
    let event: Event = decode_strict(params.as_bytes()).unwrap_or_default();
    if event.kind != EVENT_GATEWAY_READY {
        let observed = if event.kind.is_empty() {
            &notification.method
        } else {
            &event.kind
        };
        return Err(format!(
            "first observation was {}, not gateway.ready",
            oap_adapter::gojson::go_quote(observed)
        ));
    }
    decode_strict(event.payload_bytes())
        .map_err(|detail| format!("invalid ready payload: {detail}"))
}

impl Process {
    pub fn start(config: &ProcessConfig, deadline: Option<Instant>) -> Result<Self, Error> {
        Self::start_with(config, deadline, &crossbeam_channel::never())
    }

    pub fn start_with(
        config: &ProcessConfig,
        deadline: Option<Instant>,
        cancel: &Receiver<()>,
    ) -> Result<Self, Error> {
        if config.path.is_empty() {
            return Err(handshake("executable path is required"));
        }
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
            .exit_timeout
            .filter(|timeout| !timeout.is_zero())
            .unwrap_or(DEFAULT_EXIT_TIMEOUT);
        child.watch(client.read_done(), timeout);
        let mut process = Self {
            client,
            ready: ReadyPayload::default(),
            child,
            timeout,
            closed: Mutex::new(None),
        };
        let inbound = process.client.inbound();
        let done = process.client.done();
        let expiry = deadline.map_or_else(crossbeam_channel::never, crossbeam_channel::at);
        let outcome = select! {
            recv(inbound) -> message => match message {
                Ok(message) => ready_from(message),
                Err(_) => Err(process.client.err().unwrap_or_else(|| "EOF".into())),
            },
            recv(done) -> _ => Err(process.client.err().unwrap_or_else(|| "EOF".into())),
            recv(expiry) -> _ => Err("context deadline exceeded".into()),
            recv(cancel) -> _ => Err("context canceled".into()),
        };
        match outcome {
            Ok(ready) => {
                process.ready = ready;
                Ok(process)
            }
            Err(detail) => {
                process.abort();
                Err(handshake(format!("{detail}; stderr: {}", process.stderr())))
            }
        }
    }

    pub fn stderr(&self) -> String {
        self.child.stderr()
    }

    pub fn exited(&self) -> Receiver<()> {
        self.child.exited()
    }

    pub fn wait_error(&self) -> Option<String> {
        wait_error(self.child.exit_status())
    }

    fn abort(&self) {
        self.child.kill();
        self.child.drain_stderr(ABORT_STDERR_GRACE);
        self.client.close();
        let _ = self.child.exited().recv();
    }

    pub fn close(&self) -> Result<(), String> {
        let mut closed = self
            .closed
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        if let Some(outcome) = closed.as_ref() {
            return outcome.clone();
        }
        self.child.release_stdin();
        let outcome = if self.child.await_exit(self.timeout) {
            self.wait_error().map_or(Ok(()), Err)
        } else {
            self.child.interrupt();
            if !self.child.await_exit(INTERRUPT_GRACE) {
                self.child.kill();
                self.client.close();
                let _ = self.child.exited().recv();
            }
            Err("hermes rpc: gateway did not exit on stdin EOF".into())
        };
        self.client.close();
        *closed = Some(outcome.clone());
        outcome
    }
}

impl Drop for Process {
    fn drop(&mut self) {
        let _ = self.close();
    }
}
