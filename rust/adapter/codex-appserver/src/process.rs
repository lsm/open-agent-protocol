use std::collections::BTreeMap;
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use crossbeam_channel::Receiver;
use oap_adapter::process::{wait_error, Child, ChildConfig};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::client::{Client, ClientFactory};
use crate::rpc::{error, Error, Fault, Inbound, DIALECT};
use oap_adapter::jsonrpc::{ClientOptions, RpcClient};

const DEFAULT_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);
const ABORT_STDERR_GRACE: Duration = Duration::from_millis(250);

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize)]
pub struct ClientInfo {
    pub name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub title: Option<String>,
    pub version: String,
}

#[derive(Clone, Debug, Default, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeCapabilities {
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub experimental_api: bool,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub request_attestation: bool,
    #[serde(
        rename = "mcpServerOpenaiFormElicitation",
        skip_serializing_if = "std::ops::Not::not"
    )]
    pub mcp_server_openai_form_elicitation: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub opt_out_notification_methods: Vec<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: BTreeMap<String, Box<RawValue>>,
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeResponse {
    #[serde(default)]
    pub user_agent: String,
    #[serde(default)]
    pub codex_home: String,
    #[serde(default)]
    pub platform_family: String,
    #[serde(default)]
    pub platform_os: String,
}

#[derive(Clone, Debug, Default)]
pub struct ProcessConfig {
    pub path: String,
    pub args: Vec<String>,
    pub dir: String,
    pub env: Option<Vec<String>>,
    pub frame_limit: usize,
    pub queue_capacity: usize,
    pub stderr_limit: usize,
    pub shutdown_timeout: Option<Duration>,
    pub client_info: ClientInfo,
    pub capabilities: Option<InitializeCapabilities>,
}

pub struct Process {
    pub client: RpcClient,
    pub initialize: InitializeResponse,
    child: Child,
    timeout: Duration,
    closed: Mutex<Option<Result<(), String>>>,
}

fn handshake(detail: impl std::fmt::Display) -> Error {
    error(Fault::Transport(format!(
        "codex app-server rpc: initialization handshake failed: {detail}"
    )))
}

impl Process {
    pub fn start(config: &ProcessConfig, deadline: Option<Instant>) -> Result<Self, Error> {
        if config.path.is_empty()
            || config.client_info.name.is_empty()
            || config.client_info.version.is_empty()
        {
            return Err(handshake("executable path and client info are required"));
        }
        let mut args = config.args.clone();
        args.extend(["app-server", "--listen", "stdio://"].map(String::from));
        let (child, stdout) = Child::spawn(&ChildConfig {
            path: config.path.clone(),
            args,
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
                first_request_id: -1,
                strict_response_ids: true,
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
            initialize: InitializeResponse::default(),
            child,
            timeout,
            closed: Mutex::new(None),
        };
        #[derive(Serialize)]
        #[serde(rename_all = "camelCase")]
        struct Params<'a> {
            client_info: &'a ClientInfo,
            #[serde(skip_serializing_if = "Option::is_none")]
            capabilities: Option<&'a InitializeCapabilities>,
        }
        let params = serde_json::value::to_raw_value(&Params {
            client_info: &config.client_info,
            capabilities: config.capabilities.as_ref(),
        })
        .map_err(|failure| error(Fault::Encode(failure.to_string())))?;
        let initialized = process
            .client
            .call("initialize", Some(params), deadline)
            .and_then(|raw| {
                if raw.get() == "null" {
                    return Ok(InitializeResponse::default());
                }
                serde_json::from_str::<InitializeResponse>(raw.get()).map_err(|failure| {
                    error(Fault::Decode {
                        method: "initialize".into(),
                        detail: failure.to_string(),
                    })
                })
            });
        let initialize = match initialized {
            Ok(initialize) => initialize,
            Err(failure) => {
                process.abort();
                return Err(handshake(format!(
                    "{failure}; stderr: {}",
                    process.stderr()
                )));
            }
        };
        if let Err(failure) = process.client.notify("initialized", None, deadline) {
            process.abort();
            return Err(handshake(format!(
                "notify initialized: {failure}; stderr: {}",
                process.stderr()
            )));
        }
        Ok(Self {
            initialize,
            ..process
        })
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
        self.child.release_stdin();
        let outcome = if self.child.await_exit(self.timeout) {
            match wait_error(self.child.exit_status()) {
                Some(failure) => Err(failure),
                None => Ok(()),
            }
        } else {
            self.child.kill();
            self.client.close();
            let _ = self.child.exited().recv();
            Err("codex app-server rpc: shutdown timed out".into())
        };
        self.client.close();
        *closed = Some(outcome.clone());
        outcome
    }
}

pub struct ProcessClient(pub Process);

impl Client for ProcessClient {
    fn call(
        &self,
        method: &str,
        params: Box<RawValue>,
        deadline: Option<Instant>,
    ) -> Result<Box<RawValue>, Error> {
        self.0.client.call(method, Some(params), deadline)
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.0.client.inbound()
    }

    fn done(&self) -> Receiver<()> {
        self.0.client.done()
    }

    fn err(&self) -> Option<String> {
        self.0.client.err()
    }

    fn close(&self) -> Result<(), Error> {
        self.0
            .close()
            .map_err(|reason| error(Fault::Transport(reason)))
    }
}

pub struct ProcessFactory(pub ProcessConfig);

impl ClientFactory for ProcessFactory {
    fn start(&self, deadline: Option<Instant>) -> Result<Arc<dyn Client>, Error> {
        Ok(Arc::new(ProcessClient(Process::start(&self.0, deadline)?)))
    }
}
