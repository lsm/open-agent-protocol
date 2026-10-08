//! The events a provider or agent stream yields.
//!
//! The SDK builds them from OAP envelopes: `inference.*` for a provider stream
//! and `run.*`, `content.delta` and `action.call.*` for an agent run. A tool
//! call arrives whole, as one [`ProviderEvent::ToolCall`].

use crate::types::Usage;

/// An event from a provider-level stream.
#[derive(Debug, Clone, PartialEq)]
#[non_exhaustive]
pub enum ProviderEvent {
    /// Generation started. Carries the resolved identity when the runtime knows it.
    MessageStart {
        /// The provider serving the request.
        provider_id: Option<String>,
        /// The API it is reached through.
        api: Option<String>,
        /// The resolved model id.
        model_id: Option<String>,
    },
    /// Newly generated text.
    TextDelta {
        /// The new text. Deltas concatenate; they are not cumulative.
        delta: String,
    },
    /// Newly generated reasoning output.
    ThinkingDelta {
        /// The new reasoning text.
        delta: String,
    },
    /// A fully buffered tool call.
    ToolCall {
        /// Correlates this call with its result.
        tool_call_id: String,
        /// The tool's name.
        name: String,
        /// The arguments, as a JSON document in a string.
        arguments_json: String,
    },
    /// Generation finished. One of the two terminal events (spec §3.5).
    MessageEnd {
        /// Token accounting, when the provider reported any.
        usage: Option<Usage>,
        /// Why generation stopped.
        stop_reason: Option<String>,
        /// Failure detail, when the turn failed but still produced a terminal event.
        error_message: Option<String>,
    },
    /// The stream failed. The other terminal event.
    Error {
        /// Human-readable detail.
        message: String,
        /// The protocol error code, when the runtime supplied one.
        code: Option<String>,
        /// The provider the failure is attributed to.
        provider_id: Option<String>,
    },
}

impl ProviderEvent {
    /// Whether this event ends the stream.
    pub fn is_terminal(&self) -> bool {
        matches!(self, Self::MessageEnd { .. } | Self::Error { .. })
    }

    /// The text this event contributes, if any.
    pub fn text(&self) -> Option<&str> {
        match self {
            Self::TextDelta { delta } => Some(delta),
            _ => None,
        }
    }
}

/// An event from an agent run. Wraps [`ProviderEvent`] with the loop's own
/// lifecycle events.
#[derive(Debug, Clone, PartialEq)]
#[non_exhaustive]
pub enum AgentEvent {
    /// The run started.
    AgentStart {
        /// The run's session id, a correlation key only.
        session_id: Option<String>,
    },
    /// A provider turn started.
    TurnStart,
    /// A provider turn finished.
    TurnEnd {
        /// Why the turn stopped. Turn-scoped, not the run's outcome.
        stop_reason: Option<String>,
        /// Failure detail for a turn that failed at the provider.
        error_message: Option<String>,
    },
    /// The loop is about to run a tool.
    ToolExecutionStart {
        /// The call being executed.
        tool_call_id: String,
        /// The tool's name.
        tool_name: String,
    },
    /// A tool finished.
    ToolExecutionEnd {
        /// The call that finished.
        tool_call_id: String,
        /// Whether it failed.
        is_error: Option<bool>,
    },
    /// The run finished. The terminal event on the success path.
    AgentEnd {
        /// Aggregate token accounting across every turn.
        usage: Option<Usage>,
        /// Why the run stopped; may be agent-level, such as `max_turns`.
        stop_reason: Option<String>,
        /// Failure detail, when a turn failed at the provider.
        error_message: Option<String>,
        /// The provider that served the run.
        provider_id: Option<String>,
        /// The API it was reached through.
        api: Option<String>,
    },
    /// A provider-level event from the turn currently running.
    Provider(ProviderEvent),
}

impl AgentEvent {
    /// Whether this event ends the run.
    pub fn is_terminal(&self) -> bool {
        match self {
            Self::AgentEnd { .. } => true,
            Self::Provider(event) => matches!(event, ProviderEvent::Error { .. }),
            _ => false,
        }
    }

    /// The text this event contributes, if any.
    pub fn text(&self) -> Option<&str> {
        match self {
            Self::Provider(event) => event.text(),
            _ => None,
        }
    }
}
