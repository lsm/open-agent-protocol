use thiserror::Error;

pub type Result<T> = std::result::Result<T, Error>;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
#[non_exhaustive]
pub enum StreamErrorKind {
    ProviderError,
    TransportError,
    Aborted,
    Unknown,
}

impl StreamErrorKind {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::ProviderError => "provider_error",
            Self::TransportError => "transport_error",
            Self::Aborted => "aborted",
            Self::Unknown => "unknown",
        }
    }
}

impl std::fmt::Display for StreamErrorKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
#[non_exhaustive]
pub enum AuthErrorKind {
    ProviderError,
    Cancelled,
    TransportError,
    Unknown,
}

impl AuthErrorKind {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::ProviderError => "provider_error",
            Self::Cancelled => "cancelled",
            Self::TransportError => "transport_error",
            Self::Unknown => "unknown",
        }
    }
}

impl std::fmt::Display for AuthErrorKind {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

#[derive(Debug, Error)]
#[non_exhaustive]
pub enum Error {
    #[error("{message}")]
    Stream {
        kind: StreamErrorKind,
        code: Option<String>,
        provider_id: Option<String>,
        message: String,
    },

    #[error("{message}")]
    AuthRequired {
        provider_id: String,
        message: String,
    },

    #[error("{message}")]
    Protocol {
        code: Option<String>,
        message: String,
    },

    #[error("{message}")]
    Auth {
        kind: AuthErrorKind,
        code: Option<String>,
        message: String,
    },

    #[error("{message}")]
    Transport { message: String },

    #[error("{message}")]
    InvalidRequest { message: String },
}

impl Error {
    pub(crate) fn stream(kind: StreamErrorKind, message: impl Into<String>) -> Self {
        Self::Stream {
            kind,
            code: None,
            provider_id: None,
            message: message.into(),
        }
    }

    pub(crate) fn transport_stream(message: impl Into<String>) -> Self {
        Self::stream(StreamErrorKind::TransportError, message)
    }

    pub(crate) fn provider_stream(
        message: impl Into<String>,
        code: Option<String>,
        provider_id: Option<String>,
    ) -> Self {
        Self::Stream {
            kind: StreamErrorKind::ProviderError,
            code,
            provider_id,
            message: message.into(),
        }
    }

    pub(crate) fn auth_required(
        provider_id: impl Into<String>,
        message: impl Into<String>,
    ) -> Self {
        Self::AuthRequired {
            provider_id: provider_id.into(),
            message: message.into(),
        }
    }

    pub(crate) fn protocol(message: impl Into<String>, code: Option<&str>) -> Self {
        Self::Protocol {
            code: code.map(str::to_owned),
            message: message.into(),
        }
    }

    pub(crate) fn auth(
        kind: AuthErrorKind,
        message: impl Into<String>,
        code: Option<String>,
    ) -> Self {
        Self::Auth {
            kind,
            code,
            message: message.into(),
        }
    }

    pub(crate) fn transport(message: impl Into<String>) -> Self {
        Self::Transport {
            message: message.into(),
        }
    }

    pub(crate) fn invalid_request(message: impl Into<String>) -> Self {
        Self::InvalidRequest {
            message: message.into(),
        }
    }

    pub fn code(&self) -> Option<&str> {
        match self {
            Self::Stream { code, .. } | Self::Protocol { code, .. } | Self::Auth { code, .. } => {
                code.as_deref()
            }
            Self::AuthRequired { .. } => Some("auth_required"),
            Self::Transport { .. } | Self::InvalidRequest { .. } => None,
        }
    }

    pub fn provider_id(&self) -> Option<&str> {
        match self {
            Self::Stream { provider_id, .. } => provider_id.as_deref(),
            Self::AuthRequired { provider_id, .. } => Some(provider_id),
            _ => None,
        }
    }

    pub fn is_auth_required(&self) -> bool {
        matches!(self, Self::AuthRequired { .. }) || self.code() == Some("auth_required")
    }

    pub fn is_cancelled(&self) -> bool {
        matches!(
            self,
            Self::Stream {
                kind: StreamErrorKind::Aborted,
                ..
            } | Self::Auth {
                kind: AuthErrorKind::Cancelled,
                ..
            }
        )
    }

    pub(crate) fn message(&self) -> &str {
        match self {
            Self::Stream { message, .. }
            | Self::AuthRequired { message, .. }
            | Self::Protocol { message, .. }
            | Self::Auth { message, .. }
            | Self::Transport { message }
            | Self::InvalidRequest { message } => message,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn transport_errors_carry_no_code() {
        let err = Error::transport_stream("child exited");
        assert_eq!(err.code(), None);
        assert!(!err.is_auth_required());
    }

    #[test]
    fn cancellation_is_reported_for_both_domains() {
        assert!(Error::stream(StreamErrorKind::Aborted, "cancelled").is_cancelled());
        assert!(Error::auth(AuthErrorKind::Cancelled, "cancelled", None).is_cancelled());
        assert!(!Error::transport("boom").is_cancelled());
    }

    #[test]
    fn display_is_the_message() {
        let err = Error::protocol("model not found", Some("invalid_request"));
        assert_eq!(err.to_string(), "model not found");
        assert_eq!(err.code(), Some("invalid_request"));
    }
}
