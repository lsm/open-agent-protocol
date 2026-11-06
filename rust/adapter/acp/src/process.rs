use std::collections::BTreeMap;
use std::sync::Mutex;
use std::time::{Duration, Instant};

use oap_adapter::gojson::{decode, zero, Decode};
use oap_adapter::jsonrpc::{ClientOptions, Dialect, Error, Fault, RpcClient};
use oap_adapter::process::{wait_error, Child, ChildConfig};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

pub const PROTOCOL_VERSION: i64 = 1;
pub const DIALECT: Dialect = Dialect::lenient("acp");
const DEFAULT_SHUTDOWN_TIMEOUT: Duration = Duration::from_secs(5);
const ABORT_STDERR_GRACE: Duration = Duration::from_millis(250);

pub type Capabilities = BTreeMap<String, Box<RawValue>>;

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
pub struct Implementation {
    #[serde(default, deserialize_with = "zero")]
    pub name: String,
    #[serde(
        default,
        deserialize_with = "zero",
        skip_serializing_if = "String::is_empty"
    )]
    pub title: String,
    #[serde(default, deserialize_with = "zero")]
    pub version: String,
}

impl Decode for Implementation {}

fn implementation<'de, D>(deserializer: D) -> Result<Option<Implementation>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    let raw = Option::<Box<RawValue>>::deserialize(deserializer)?;
    raw.map(|raw| decode(raw.get().as_bytes()).map_err(serde::de::Error::custom))
        .transpose()
}

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct InitializeResponse {
    #[serde(default, deserialize_with = "zero")]
    pub protocol_version: i64,
    #[serde(default)]
    pub agent_capabilities: Option<Capabilities>,
    #[serde(default, deserialize_with = "zero")]
    pub auth_methods: Vec<Box<RawValue>>,
    #[serde(default, deserialize_with = "implementation")]
    pub agent_info: Option<Implementation>,
}

impl Decode for InitializeResponse {}

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
    pub protocol_version: i64,
    pub client_capabilities: Capabilities,
    pub client_info: Option<Implementation>,
}

pub struct Process {
    pub client: RpcClient,
    pub initialize: InitializeResponse,
    child: Child,
    timeout: Duration,
    closed: Mutex<Option<Result<(), String>>>,
}

fn handshake(detail: impl std::fmt::Display) -> Error {
    DIALECT.error(Fault::Transport(format!(
        "acp rpc: initialization handshake failed: {detail}"
    )))
}

impl Process {
    pub fn start(config: &ProcessConfig, deadline: Option<Instant>) -> Result<Self, Error> {
        if config.path.is_empty() {
            return Err(handshake("executable path is required"));
        }
        let version = if config.protocol_version == 0 {
            PROTOCOL_VERSION
        } else {
            config.protocol_version
        };
        if version != PROTOCOL_VERSION {
            return Err(handshake(format!(
                "unsupported requested protocol version {version}"
            )));
        }
        if config
            .client_info
            .as_ref()
            .is_some_and(|info| info.name.is_empty() || info.version.is_empty())
        {
            return Err(handshake("client info name and version are required"));
        }
        let (child, stdout) = Child::spawn(&ChildConfig {
            path: config.path.clone(),
            args: config.args.clone(),
            dir: config.dir.clone(),
            env: config.env.clone(),
            stderr_limit: config.stderr_limit,
        })
        .map_err(|failure| DIALECT.error(Fault::Io(failure)))?;
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
            protocol_version: i64,
            client_capabilities: &'a Capabilities,
            #[serde(skip_serializing_if = "Option::is_none")]
            client_info: Option<&'a Implementation>,
        }
        let initialized = serde_json::value::to_raw_value(&Params {
            protocol_version: version,
            client_capabilities: &config.client_capabilities,
            client_info: config.client_info.as_ref(),
        })
        .map_err(|failure| failure.to_string())
        .and_then(|params| {
            process
                .client
                .call("initialize", Some(params), deadline)
                .map_err(|failure| failure.to_string())
        })
        .and_then(|raw| {
            decode::<InitializeResponse>(raw.get().as_bytes())
                .map_err(|failure| format!("decode initialize response: {failure}"))
        })
        .and_then(|initialized| {
            if initialized.protocol_version != PROTOCOL_VERSION {
                return Err(format!(
                    "agent selected unsupported protocol version {}",
                    initialized.protocol_version
                ));
            }
            if initialized.agent_capabilities.is_none() {
                return Err("agentCapabilities is required".into());
            }
            Ok(initialized)
        });
        match initialized {
            Ok(initialize) => {
                let mut process = process;
                process.initialize = initialize;
                Ok(process)
            }
            Err(detail) => {
                process.child.kill();
                process.child.drain_stderr(ABORT_STDERR_GRACE);
                process.client.close();
                let _ = process.child.exited().recv();
                Err(handshake(format!("{detail}; stderr: {}", process.stderr())))
            }
        }
    }

    pub fn stderr(&self) -> String {
        self.child.stderr()
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
            wait_error(self.child.exit_status()).map_or(Ok(()), Err)
        } else {
            self.child.kill();
            self.client.close();
            let _ = self.child.exited().recv();
            Err("acp rpc: shutdown timed out".into())
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
