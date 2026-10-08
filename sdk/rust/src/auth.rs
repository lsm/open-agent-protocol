//! Provider authentication: listing auth state and running interactive logins.
//!
//! Token material stays inside the runtime. This SDK never reads
//! `~/.oapx/auth.json`, never shells out to `makai auth ...`, and never hands a
//! credential back to the caller (spec §3.7).

use std::sync::Arc;
use std::time::Duration;

use serde::{Deserialize, Serialize};
use serde_json::json;

use crate::error::{AuthErrorKind, Error, Result};
use crate::ids::new_ulid;
use crate::models::{AuthKind, AuthStatus};
use crate::transport::Transport;
use crate::wire::AGENT_PROFILE;

/// One provider's auth state.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct ProviderAuthInfo {
    /// The provider id to pass to [`AuthApi::login`].
    pub id: String,
    /// A human-readable name.
    pub name: String,
    /// How this provider accepts a credential, in preference order.
    ///
    /// The wire requires at least one entry, so an empty vector means a runtime
    /// older than the field sent none — which is not the same as a provider
    /// that needs no credential, and a new client must not fail the whole
    /// listing against an older runtime.
    #[serde(default)]
    pub auth_kinds: Vec<AuthKind>,
    /// Whether its credentials are usable.
    #[serde(default = "unknown_status")]
    pub auth_status: AuthStatus,
    /// The last failure, when there was one.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub last_error: Option<String>,
}

fn unknown_status() -> AuthStatus {
    AuthStatus::Unknown
}

/// A question the login flow needs answered.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct AuthPrompt {
    /// The flow this prompt belongs to.
    pub flow_id: String,
    /// Identifies the prompt within the flow; echoed back in the response.
    pub prompt_id: String,
    /// The provider being authenticated.
    pub provider_id: String,
    /// What to ask the user.
    pub message: String,
    /// Whether an empty answer is acceptable.
    pub allow_empty: bool,
}

/// Something that happened during a login flow.
#[derive(Debug, Clone, PartialEq, Eq)]
#[non_exhaustive]
pub enum AuthEvent {
    /// A URL the user must open.
    AuthUrl {
        /// The flow this belongs to.
        flow_id: String,
        /// The provider being authenticated.
        provider_id: String,
        /// The URL to open.
        url: String,
        /// What to do once it is open.
        instructions: Option<String>,
    },
    /// The flow needs an answer, which OAP cannot carry; the login then fails.
    Prompt(AuthPrompt),
    /// Progress detail worth showing.
    Progress {
        /// The flow this belongs to.
        flow_id: String,
        /// The provider being authenticated.
        provider_id: String,
        /// The message.
        message: String,
    },
    /// The flow succeeded.
    Success {
        /// The flow this belongs to.
        flow_id: String,
        /// The provider that was authenticated.
        provider_id: String,
    },
    /// The flow reported a failure. A terminal `auth_login_result` still follows.
    Error {
        /// The flow this belongs to.
        flow_id: String,
        /// The provider being authenticated.
        provider_id: String,
        /// The protocol error code, when supplied.
        code: Option<String>,
        /// Human-readable detail.
        message: String,
    },
}

type EventCallback = Arc<dyn Fn(AuthEvent) + Send + Sync>;

/// Callbacks that observe an interactive login.
///
/// OAP carries no prompt answer, so a flow that reaches a prompt fails with
/// `auth_input_unavailable`.
#[derive(Clone, Default)]
pub struct AuthHandlers {
    on_event: Option<EventCallback>,
}

impl std::fmt::Debug for AuthHandlers {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("AuthHandlers")
            .field("on_event", &self.on_event.is_some())
            .finish()
    }
}

impl AuthHandlers {
    /// No handlers.
    pub fn new() -> Self {
        Self::default()
    }

    /// Observes every event in the flow.
    pub fn on_event(mut self, handler: impl Fn(AuthEvent) + Send + Sync + 'static) -> Self {
        self.on_event = Some(Arc::new(handler));
        self
    }
}

/// The auth namespace.
///
/// Cheap to clone; every clone shares one transport.
#[derive(Clone)]
pub struct AuthApi {
    transport: Arc<Transport>,
    handlers: AuthHandlers,
    frame_timeout: Duration,
}

impl std::fmt::Debug for AuthApi {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("AuthApi")
            .field("handlers", &self.handlers)
            .field("frame_timeout", &self.frame_timeout)
            .finish()
    }
}

impl AuthApi {
    pub(crate) fn new(
        transport: Arc<Transport>,
        handlers: AuthHandlers,
        frame_timeout: Duration,
    ) -> Self {
        Self {
            transport,
            handlers,
            frame_timeout,
        }
    }

    /// Lists every provider the runtime knows about, with its auth state.
    pub async fn list_providers(&self) -> Result<Vec<ProviderAuthInfo>> {
        let response = self
            .transport
            .request_oap(
                AGENT_PROFILE,
                "auth.providers.request",
                json!({}),
                None,
                self.frame_timeout,
            )
            .await?;
        if response.kind != "auth.providers.response" {
            return Err(Error::protocol(
                "unexpected OAP auth providers response",
                Some("malformed_response"),
            ));
        }
        let providers = response
            .payload()
            .get("providers")
            .cloned()
            .ok_or_else(|| {
                Error::protocol(
                    "OAP auth providers response has no providers",
                    Some("malformed_response"),
                )
            })?;
        serde_json::from_value(providers).map_err(|err| {
            Error::protocol(
                format!("invalid OAP auth providers response: {err}"),
                Some("malformed_response"),
            )
        })
    }

    /// Runs an interactive login for `provider_id`.
    ///
    /// `handlers` overrides the client-level handlers for this call; passing
    /// `None` uses the client's (spec §3.7's normative handler resolution order).
    ///
    /// Returns once the runtime sends a terminal `auth_login_result`. Dropping
    /// the returned future cancels the flow: the SDK sends `auth_cancel` so the
    /// runtime does not leave an OAuth listener running.
    pub async fn login(&self, provider_id: &str, handlers: Option<&AuthHandlers>) -> Result<()> {
        self.login_oap(provider_id, handlers).await
    }

    async fn login_oap(&self, provider_id: &str, handlers: Option<&AuthHandlers>) -> Result<()> {
        let handlers = handlers.unwrap_or(&self.handlers).clone();
        let started = self
            .transport
            .request_oap(
                AGENT_PROFILE,
                "auth.login.start.request",
                json!({ "provider_id": provider_id }),
                None,
                self.frame_timeout,
            )
            .await?;
        if started.kind != "auth.login.start.response" {
            return Err(Error::protocol(
                "unexpected OAP auth login start response",
                Some("malformed_response"),
            ));
        }
        let flow_id = started.payload_non_empty("flow_id").ok_or_else(|| {
            Error::protocol(
                "OAP auth login start response has no flow_id",
                Some("malformed_response"),
            )
        })?;
        let mut subscription = self.transport.subscribe_stream(&flow_id);
        let mut guard = OapLoginGuard {
            transport: Arc::clone(&self.transport),
            flow_id: flow_id.clone(),
            settled: false,
        };
        let mut sequence = 0u64;
        loop {
            let frame = subscription
                .next_within(self.frame_timeout, "auth.login.event/auth.login.completed")
                .await
                .map_err(to_auth_error)?;
            if frame.profile.as_deref() != Some(AGENT_PROFILE)
                || frame.sequence != Some(sequence + 1)
                || frame.payload_str("flow_id") != Some(flow_id.as_str())
                || frame.payload_str("provider_id") != Some(provider_id)
            {
                return Err(Error::protocol(
                    "OAP auth flow identity or sequence mismatch",
                    Some("malformed_response"),
                ));
            }
            sequence += 1;
            match frame.kind.as_str() {
                "auth.login.event" => {
                    let event = match frame.payload_str("kind") {
                        Some("url") => AuthEvent::AuthUrl {
                            flow_id: flow_id.clone(),
                            provider_id: provider_id.to_owned(),
                            url: frame.payload_non_empty("url").ok_or_else(|| {
                                Error::protocol("auth URL is missing", Some("malformed_response"))
                            })?,
                            instructions: frame.payload_str("instructions").map(str::to_owned),
                        },
                        Some("prompt") => {
                            return Err(Error::auth(
                                AuthErrorKind::ProviderError,
                                "manual login input cannot be sent over OAP",
                                Some("auth_input_unavailable".to_owned()),
                            ));
                        }
                        Some("progress") => AuthEvent::Progress {
                            flow_id: flow_id.clone(),
                            provider_id: provider_id.to_owned(),
                            message: frame.payload_non_empty("message").ok_or_else(|| {
                                Error::protocol(
                                    "auth progress message is missing",
                                    Some("malformed_response"),
                                )
                            })?,
                        },
                        _ => {
                            return Err(Error::protocol(
                                "unknown OAP auth event kind",
                                Some("malformed_response"),
                            ))
                        }
                    };
                    if let Some(callback) = &handlers.on_event {
                        callback(event.clone());
                    }
                }
                "auth.login.completed" => {
                    guard.settled = true;
                    let error = frame.payload().get("error");
                    let code = error
                        .and_then(|value| value.get("code"))
                        .and_then(serde_json::Value::as_str)
                        .map(str::to_owned);
                    let message = error
                        .and_then(|value| value.get("message"))
                        .and_then(serde_json::Value::as_str);
                    return match frame.payload_str("status") {
                        Some("success") => {
                            if let Some(callback) = &handlers.on_event {
                                callback(AuthEvent::Success {
                                    flow_id,
                                    provider_id: provider_id.to_owned(),
                                });
                            }
                            Ok(())
                        }
                        Some("failed") => {
                            let detail = message.unwrap_or("auth login failed").to_owned();
                            if let Some(callback) = &handlers.on_event {
                                callback(AuthEvent::Error {
                                    flow_id,
                                    provider_id: provider_id.to_owned(),
                                    code: code.clone(),
                                    message: detail.clone(),
                                });
                            }
                            Err(Error::auth(AuthErrorKind::ProviderError, detail, code))
                        }
                        Some("cancelled") => {
                            let detail = message.unwrap_or("auth login cancelled").to_owned();
                            if let Some(callback) = &handlers.on_event {
                                callback(AuthEvent::Error {
                                    flow_id,
                                    provider_id: provider_id.to_owned(),
                                    code: code.clone(),
                                    message: detail.clone(),
                                });
                            }
                            Err(Error::auth(AuthErrorKind::Cancelled, detail, code))
                        }
                        _ => Err(Error::protocol(
                            "unknown OAP auth completion status",
                            Some("malformed_response"),
                        )),
                    };
                }
                _ => {
                    return Err(Error::protocol(
                        "unexpected OAP auth flow frame",
                        Some("malformed_response"),
                    ))
                }
            }
        }
    }
}

/// Cancels an OAP flow when its login future is dropped or fails before a terminal.
struct OapLoginGuard {
    transport: Arc<Transport>,
    flow_id: String,
    settled: bool,
}

impl Drop for OapLoginGuard {
    fn drop(&mut self) {
        if self.settled {
            return;
        }
        let _ = self.transport.send_oap(
            AGENT_PROFILE,
            "auth.login.cancel.request",
            &new_ulid(),
            json!({ "flow_id": self.flow_id }),
            None,
        );
    }
}

fn to_auth_error(error: Error) -> Error {
    match error {
        Error::Auth { .. } => error,
        other => Error::auth(
            AuthErrorKind::TransportError,
            other.message().to_owned(),
            other.code().map(str::to_owned),
        ),
    }
}
