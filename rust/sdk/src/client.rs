use std::collections::BTreeMap;
use std::path::PathBuf;
use std::sync::Arc;
use std::time::Duration;

use crate::agent::AgentApi;
use crate::auth::{AuthApi, AuthHandlers};
use crate::binary::BinaryResolver;
use crate::error::Result;
use crate::models::ModelsApi;
use crate::provider::ProviderApi;
use crate::transport::{Transport, TransportOptions};
use crate::types::AuthRetryPolicy;

const DEFAULT_HANDSHAKE_TIMEOUT: Duration = Duration::from_millis(1_500);
const DEFAULT_RESPONSE_TIMEOUT: Duration = Duration::from_secs(30);
const DEFAULT_FRAME_TIMEOUT: Duration = Duration::from_secs(30);

#[derive(Debug, Clone)]
pub struct ClientBuilder {
    command: Option<PathBuf>,
    resolver: BinaryResolver,
    args: Vec<String>,
    cwd: Option<PathBuf>,
    env: BTreeMap<String, String>,
    env_clear: bool,
    expected_protocol_version: String,
    handshake_timeout: Duration,
    response_timeout: Duration,
    frame_timeout: Duration,
    auth_retry_policy: Option<AuthRetryPolicy>,
    auth_handlers: AuthHandlers,
}

impl Default for ClientBuilder {
    fn default() -> Self {
        Self {
            command: None,
            resolver: BinaryResolver::default(),
            args: vec![
                "serve".to_owned(),
                "agent,provider".to_owned(),
                "--stdio".to_owned(),
            ],
            cwd: None,
            env: BTreeMap::new(),
            env_clear: false,
            expected_protocol_version: "0.1".to_owned(),
            handshake_timeout: DEFAULT_HANDSHAKE_TIMEOUT,
            response_timeout: DEFAULT_RESPONSE_TIMEOUT,
            frame_timeout: DEFAULT_FRAME_TIMEOUT,
            auth_retry_policy: None,
            auth_handlers: AuthHandlers::new(),
        }
    }
}

impl ClientBuilder {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn command(mut self, path: impl Into<PathBuf>) -> Self {
        self.command = Some(path.into());
        self
    }

    pub fn binary_path(mut self, path: impl Into<PathBuf>) -> Self {
        self.resolver.binary_path = Some(path.into());
        self
    }

    pub fn binary_url(
        mut self,
        url: impl Into<String>,
        checksum_sha256: impl Into<String>,
    ) -> Self {
        self.resolver.binary_url = Some(url.into());
        self.resolver.checksum_sha256 = Some(checksum_sha256.into());
        self
    }

    pub fn cache_dir(mut self, path: impl Into<PathBuf>) -> Self {
        self.resolver.cache_dir = Some(path.into());
        self
    }

    pub fn resolver(mut self, resolver: BinaryResolver) -> Self {
        self.resolver = resolver;
        self
    }

    pub fn args<I, S>(mut self, args: I) -> Self
    where
        I: IntoIterator<Item = S>,
        S: Into<String>,
    {
        self.args = args.into_iter().map(Into::into).collect();
        self
    }

    pub fn current_dir(mut self, path: impl Into<PathBuf>) -> Self {
        self.cwd = Some(path.into());
        self
    }

    pub fn env(mut self, key: impl Into<String>, value: impl Into<String>) -> Self {
        self.env.insert(key.into(), value.into());
        self
    }

    pub fn env_clear(mut self) -> Self {
        self.env_clear = true;
        self
    }

    pub fn expected_protocol_version(mut self, version: impl Into<String>) -> Self {
        self.expected_protocol_version = version.into();
        self
    }

    pub fn handshake_timeout(mut self, timeout: Duration) -> Self {
        self.handshake_timeout = timeout;
        self
    }

    pub fn response_timeout(mut self, timeout: Duration) -> Self {
        self.response_timeout = timeout;
        self
    }

    pub fn frame_timeout(mut self, timeout: Duration) -> Self {
        self.frame_timeout = timeout;
        self
    }

    pub fn auth_retry_policy(mut self, policy: AuthRetryPolicy) -> Self {
        self.auth_retry_policy = Some(policy);
        self
    }

    pub fn auth_handlers(mut self, handlers: AuthHandlers) -> Self {
        self.auth_handlers = handlers;
        self
    }

    pub async fn connect(self) -> Result<Client> {
        let command = match &self.command {
            Some(command) => command.clone(),
            None => self.resolver.resolve().await?,
        };
        let transport = Transport::connect(TransportOptions {
            command,
            args: self.args.clone(),
            cwd: self.cwd.clone(),
            env: self
                .env
                .iter()
                .map(|(k, v)| (k.clone(), v.clone()))
                .collect(),
            env_clear: self.env_clear,
            expected_protocol_version: self.expected_protocol_version.clone(),
            handshake_timeout: self.handshake_timeout,
        })
        .await?;

        let transport = Arc::new(transport);
        let auth = AuthApi::new(
            Arc::clone(&transport),
            self.auth_handlers.clone(),
            self.frame_timeout,
        );
        let models = ModelsApi::new(Arc::clone(&transport), self.response_timeout);

        Ok(Client {
            provider: ProviderApi {
                transport: Arc::clone(&transport),
                auth: auth.clone(),
                auth_retry_policy: self.auth_retry_policy,
                response_timeout: self.response_timeout,
            },
            agent: AgentApi {
                transport: Arc::clone(&transport),
                auth: auth.clone(),
                auth_retry_policy: self.auth_retry_policy,
                response_timeout: self.response_timeout,
                models: models.clone(),
            },
            auth,
            models,
            transport,
        })
    }
}

#[derive(Debug, Clone)]
pub struct Client {
    transport: Arc<Transport>,
    auth: AuthApi,
    models: ModelsApi,
    provider: ProviderApi,
    agent: AgentApi,
}

impl std::fmt::Debug for Transport {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Transport")
            .field("closed", &self.is_closed())
            .finish()
    }
}

impl Client {
    pub async fn connect() -> Result<Self> {
        ClientBuilder::new().connect().await
    }

    pub fn builder() -> ClientBuilder {
        ClientBuilder::new()
    }

    pub fn auth(&self) -> AuthApi {
        self.auth.clone()
    }

    pub fn models(&self) -> ModelsApi {
        self.models.clone()
    }

    pub fn provider(&self) -> ProviderApi {
        self.provider.clone()
    }

    pub fn agent(&self) -> AgentApi {
        self.agent.clone()
    }

    pub fn is_closed(&self) -> bool {
        self.transport.is_closed()
    }

    pub async fn close(&self) {
        self.transport.close().await;
    }
}
