use std::borrow::Cow;
use std::fmt;

use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
#[serde(transparent)]
pub struct EnvelopeType(Cow<'static, str>);

impl EnvelopeType {
    pub const fn from_static(name: &'static str) -> Self {
        Self(Cow::Borrowed(name))
    }

    pub fn as_str(&self) -> &str {
        &self.0
    }

    pub const PROTOCOL_INITIALIZE_REQUEST: Self = Self::from_static("protocol.initialize.request");
    pub const PROTOCOL_INITIALIZE_RESPONSE: Self =
        Self::from_static("protocol.initialize.response");
    pub const CAPABILITIES_REQUEST: Self = Self::from_static("capabilities.request");
    pub const CAPABILITIES_RESPONSE: Self = Self::from_static("capabilities.response");
    pub const CAPABILITIES_UPDATED: Self = Self::from_static("capabilities.updated");
    pub const ENDPOINT_SHUTDOWN_REQUEST: Self = Self::from_static("endpoint.shutdown.request");
    pub const ENDPOINT_SHUTDOWN_RESPONSE: Self = Self::from_static("endpoint.shutdown.response");
    pub const ENDPOINT_SHUTDOWN_STARTED: Self = Self::from_static("endpoint.shutdown.started");
    pub const MODELS_REQUEST: Self = Self::from_static("models.request");
    pub const MODELS_RESPONSE: Self = Self::from_static("models.response");
    pub const AUTH_PROVIDERS_REQUEST: Self = Self::from_static("auth.providers.request");
    pub const AUTH_PROVIDERS_RESPONSE: Self = Self::from_static("auth.providers.response");
    pub const AUTH_LOGIN_START_REQUEST: Self = Self::from_static("auth.login.start.request");
    pub const AUTH_LOGIN_START_RESPONSE: Self = Self::from_static("auth.login.start.response");
    pub const AUTH_LOGIN_EVENT: Self = Self::from_static("auth.login.event");
    pub const AUTH_LOGIN_CANCEL_REQUEST: Self = Self::from_static("auth.login.cancel.request");
    pub const AUTH_LOGIN_CANCEL_RESPONSE: Self = Self::from_static("auth.login.cancel.response");
    pub const AUTH_LOGIN_COMPLETED: Self = Self::from_static("auth.login.completed");
    pub const SESSION_OPEN_REQUEST: Self = Self::from_static("session.open.request");
    pub const SESSION_OPEN_RESPONSE: Self = Self::from_static("session.open.response");
    pub const SESSION_STATE_REQUEST: Self = Self::from_static("session.state.request");
    pub const SESSION_STATE_RESPONSE: Self = Self::from_static("session.state.response");
    pub const SESSION_STATE_UPDATED: Self = Self::from_static("session.state.updated");
    pub const SESSION_LIST_REQUEST: Self = Self::from_static("session.list.request");
    pub const SESSION_LIST_RESPONSE: Self = Self::from_static("session.list.response");
    pub const TRANSCRIPT_LOAD_REQUEST: Self = Self::from_static("transcript.load.request");
    pub const TRANSCRIPT_LOAD_RESPONSE: Self = Self::from_static("transcript.load.response");
    pub const SESSION_MODEL_SWITCH_REQUEST: Self =
        Self::from_static("session.model.switch.request");
    pub const SESSION_MODEL_SWITCH_RESPONSE: Self =
        Self::from_static("session.model.switch.response");
    pub const SESSION_SETTINGS_UPDATE_REQUEST: Self =
        Self::from_static("session.settings.update.request");
    pub const SESSION_SETTINGS_UPDATE_RESPONSE: Self =
        Self::from_static("session.settings.update.response");
    pub const SESSION_PROVIDER_ATTACH_REQUEST: Self =
        Self::from_static("session.provider.attach.request");
    pub const SESSION_PROVIDER_ATTACH_RESPONSE: Self =
        Self::from_static("session.provider.attach.response");
    pub const SESSION_MESSAGE_SUBMIT_REQUEST: Self =
        Self::from_static("session.message.submit.request");
    pub const SESSION_MESSAGE_SUBMIT_RESPONSE: Self =
        Self::from_static("session.message.submit.response");
    pub const SESSION_COMPACT_REQUEST: Self = Self::from_static("session.compact.request");
    pub const SESSION_COMPACT_RESPONSE: Self = Self::from_static("session.compact.response");
    pub const RUN_CANCEL_REQUEST: Self = Self::from_static("run.cancel.request");
    pub const RUN_CANCEL_RESPONSE: Self = Self::from_static("run.cancel.response");
    pub const RUN_STEER_APPLIED: Self = Self::from_static("run.steer.applied");
    pub const RUN_STEER_DROPPED: Self = Self::from_static("run.steer.dropped");
    pub const RUN_COMPACTION_STARTED: Self = Self::from_static("run.compaction.started");
    pub const RUN_COMPACTION_ENDED: Self = Self::from_static("run.compaction.ended");
    pub const RUN_STARTED: Self = Self::from_static("run.started");
    pub const RUN_STATUS_UPDATED: Self = Self::from_static("run.status.updated");
    pub const CONTENT_DELTA: Self = Self::from_static("content.delta");
    pub const CONTENT_COMPLETED: Self = Self::from_static("content.completed");
    pub const RUN_COMPLETED: Self = Self::from_static("run.completed");
    pub const RUN_FAILED: Self = Self::from_static("run.failed");
    pub const RUN_CANCELLED: Self = Self::from_static("run.cancelled");
    pub const ACTION_TOOLS_LIST_REQUEST: Self = Self::from_static("action.tools.list.request");
    pub const ACTION_TOOLS_LIST_RESPONSE: Self = Self::from_static("action.tools.list.response");
    pub const ACTION_CALL_REQUESTED: Self = Self::from_static("action.call.requested");
    pub const ACTION_CALL_STARTED: Self = Self::from_static("action.call.started");
    pub const ACTION_CALL_PROGRESS: Self = Self::from_static("action.call.progress");
    pub const ACTION_CALL_COMPLETED: Self = Self::from_static("action.call.completed");
    pub const ACTION_CALL_FAILED: Self = Self::from_static("action.call.failed");
    pub const ACTION_CALL_CANCELLED: Self = Self::from_static("action.call.cancelled");
    pub const ACTION_CALL_RESOLVE_REQUEST: Self = Self::from_static("action.call.resolve.request");
    pub const ACTION_CALL_RESOLVE_RESPONSE: Self =
        Self::from_static("action.call.resolve.response");
    pub const ACTION_PERMISSION_REQUESTED: Self = Self::from_static("action.permission.requested");
    pub const ACTION_PERMISSION_RESOLVE_REQUEST: Self =
        Self::from_static("action.permission.resolve.request");
    pub const ACTION_PERMISSION_RESOLVE_RESPONSE: Self =
        Self::from_static("action.permission.resolve.response");
    pub const ACTION_PERMISSION_RESOLVED: Self = Self::from_static("action.permission.resolved");
    pub const USER_INPUT_REQUESTED: Self = Self::from_static("user.input.requested");
    pub const USER_INPUT_RESOLVE_REQUEST: Self = Self::from_static("user.input.resolve.request");
    pub const USER_INPUT_RESOLVE_RESPONSE: Self = Self::from_static("user.input.resolve.response");
    pub const USER_INPUT_RESOLVED: Self = Self::from_static("user.input.resolved");
    pub const USER_INPUT_CANCEL_REQUEST: Self = Self::from_static("user.input.cancel.request");
    pub const USER_INPUT_CANCEL_RESPONSE: Self = Self::from_static("user.input.cancel.response");
    pub const ERROR_RESPONSE: Self = Self::from_static("error.response");
}

impl From<String> for EnvelopeType {
    fn from(name: String) -> Self {
        Self(Cow::Owned(name))
    }
}

impl From<&str> for EnvelopeType {
    fn from(name: &str) -> Self {
        Self(Cow::Owned(name.to_owned()))
    }
}

impl PartialEq<str> for EnvelopeType {
    fn eq(&self, other: &str) -> bool {
        self.as_str() == other
    }
}

impl PartialEq<&str> for EnvelopeType {
    fn eq(&self, other: &&str) -> bool {
        self.as_str() == *other
    }
}

impl fmt::Display for EnvelopeType {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}
