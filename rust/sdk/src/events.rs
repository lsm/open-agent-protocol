use crate::types::Usage;

#[derive(Debug, Clone, PartialEq)]
#[non_exhaustive]
pub enum ProviderEvent {
    MessageStart {
        provider_id: Option<String>,
        api: Option<String>,
        model_id: Option<String>,
    },
    TextDelta {
        delta: String,
    },
    ThinkingDelta {
        delta: String,
    },
    ToolCall {
        tool_call_id: String,
        name: String,
        arguments_json: String,
    },
    MessageEnd {
        usage: Option<Usage>,
        stop_reason: Option<String>,
        error_message: Option<String>,
    },
    Error {
        message: String,
        code: Option<String>,
        provider_id: Option<String>,
    },
}

impl ProviderEvent {
    pub fn is_terminal(&self) -> bool {
        matches!(self, Self::MessageEnd { .. } | Self::Error { .. })
    }

    pub fn text(&self) -> Option<&str> {
        match self {
            Self::TextDelta { delta } => Some(delta),
            _ => None,
        }
    }
}

#[derive(Debug, Clone, PartialEq)]
#[non_exhaustive]
pub enum AgentEvent {
    AgentStart {
        session_id: Option<String>,
    },
    TurnStart,
    TurnEnd {
        stop_reason: Option<String>,
        error_message: Option<String>,
    },
    ToolExecutionStart {
        tool_call_id: String,
        tool_name: String,
    },
    ToolExecutionEnd {
        tool_call_id: String,
        is_error: Option<bool>,
    },
    AgentEnd {
        usage: Option<Usage>,
        stop_reason: Option<String>,
        error_message: Option<String>,
        provider_id: Option<String>,
        api: Option<String>,
    },
    Provider(ProviderEvent),
}

impl AgentEvent {
    pub fn is_terminal(&self) -> bool {
        match self {
            Self::AgentEnd { .. } => true,
            Self::Provider(event) => matches!(event, ProviderEvent::Error { .. }),
            _ => false,
        }
    }

    pub fn text(&self) -> Option<&str> {
        match self {
            Self::Provider(event) => event.text(),
            _ => None,
        }
    }
}
