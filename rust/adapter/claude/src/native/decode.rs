use std::fmt;

use serde::de::DeserializeOwned;

use super::*;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Invalid(pub String);

impl fmt::Display for Invalid {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "claude native: invalid frame for a known type: {}",
            self.0
        )
    }
}

impl std::error::Error for Invalid {}

fn invalid(detail: impl Into<String>) -> Invalid {
    Invalid(detail.into())
}

fn unmarshal<T: DeserializeOwned>(raw: &[u8]) -> Result<T, Invalid> {
    let text = String::from_utf8_lossy(raw);
    serde_json::from_str(&oxap_adapter::gojson::replace_lone_surrogates(&text))
        .map_err(|error| invalid(error.to_string()))
}

#[derive(Clone, Debug)]
pub enum Observation {
    User(Box<UserFrame>),
    Assistant(Box<AssistantFrame>),
    Result(Box<ResultFrame>),
    StreamEvent(Box<StreamEventFrame>),
    ToolProgress(Box<ToolProgressFrame>),
    CommandLifecycle(Box<CommandLifecycleFrame>),
    ConversationReset(Box<ConversationResetFrame>),
    Unknown { kind: String, subtype: String },
}

pub fn decode_observation(kind: &str, subtype: &str, raw: &[u8]) -> Result<Observation, Invalid> {
    Ok(match kind {
        TYPE_USER => {
            let frame: UserFrame = unmarshal(raw)?;
            if frame.message.role.is_empty() || frame.message.content.is_none() {
                return Err(invalid(
                    "user frame requires message.role and message.content",
                ));
            }
            Observation::User(Box::new(frame))
        }
        TYPE_ASSISTANT => {
            let frame: AssistantFrame = unmarshal(raw)?;
            if frame.message.model.is_empty() || frame.message.content.is_none() {
                return Err(invalid(
                    "assistant frame requires message.model and message.content",
                ));
            }
            Observation::Assistant(Box::new(frame))
        }
        TYPE_RESULT => {
            let frame: ResultFrame = unmarshal(raw)?;
            if frame.subtype.is_empty() {
                return Err(invalid("result frame requires subtype"));
            }
            if frame.session_id.is_empty() {
                return Err(invalid("result frame requires session_id"));
            }
            Observation::Result(Box::new(frame))
        }
        TYPE_STREAM_EVENT => {
            let frame: StreamEventFrame = unmarshal(raw)?;
            if frame.event.is_none() || frame.uuid.is_empty() || frame.session_id.is_empty() {
                return Err(invalid("stream_event requires event, uuid, and session_id"));
            }
            Observation::StreamEvent(Box::new(frame))
        }
        TYPE_TOOL_PROGRESS => {
            let frame: ToolProgressFrame = unmarshal(raw)?;
            if frame.tool_use_id.is_empty()
                || frame.tool_name.is_empty()
                || frame.session_id.is_empty()
            {
                return Err(invalid(
                    "tool_progress requires tool_use_id, tool_name, and session_id",
                ));
            }
            Observation::ToolProgress(Box::new(frame))
        }
        TYPE_COMMAND_LIFECYCLE => {
            let frame: CommandLifecycleFrame = unmarshal(raw)?;
            if frame.command_uuid.is_empty()
                || frame.state.is_empty()
                || frame.session_id.is_empty()
            {
                return Err(invalid(
                    "command_lifecycle requires command_uuid, state, and session_id",
                ));
            }
            Observation::CommandLifecycle(Box::new(frame))
        }
        TYPE_KEEP_ALIVE => Observation::Unknown {
            kind: TYPE_KEEP_ALIVE.into(),
            subtype: String::new(),
        },
        TYPE_CONVERSATION => {
            let frame: ConversationResetFrame = unmarshal(raw)?;
            if frame.new_conversation_id.is_empty()
                || frame.uuid.is_empty()
                || frame.session_id.is_empty()
            {
                return Err(invalid(
                    "conversation_reset requires new_conversation_id, uuid, and session_id",
                ));
            }
            Observation::ConversationReset(Box::new(frame))
        }
        _ => Observation::Unknown {
            kind: kind.into(),
            subtype: subtype.into(),
        },
    })
}
