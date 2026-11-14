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
    Init(Box<InitFrame>),
    Status(Box<StatusFrame>),
    SessionState(Box<SessionStateFrame>),
    TaskStarted(Box<TaskStartedFrame>),
    TaskProgress(Box<TaskProgressFrame>),
    TaskNotification(Box<TaskNotificationFrame>),
    TaskUpdated(Box<TaskUpdatedFrame>),
    SystemNotice(Box<SystemNotice>),
    Unknown { kind: String, subtype: String },
}

#[derive(Default, serde::Deserialize)]
struct OutputFileMember {
    #[serde(default, deserialize_with = "oxap_adapter::gojson::raw")]
    output_file: Raw,
}

fn any_empty(members: &[&str]) -> bool {
    members.iter().any(|member| member.is_empty())
}

fn decode_system(subtype: &str, raw: &[u8]) -> Result<Observation, Invalid> {
    Ok(match subtype {
        SYSTEM_INIT => {
            let frame: InitFrame = unmarshal(raw)?;
            if frame.session_id.is_empty() || frame.model.is_empty() || frame.tools.is_none() {
                return Err(invalid("init frame requires session_id, model, and tools"));
            }
            Observation::Init(Box::new(frame))
        }
        SYSTEM_STATUS => Observation::Status(Box::new(unmarshal(raw)?)),
        SYSTEM_SESSION_STATE => {
            let frame: SessionStateFrame = unmarshal(raw)?;
            if frame.state.is_empty() {
                return Err(invalid("session_state_changed requires state"));
            }
            Observation::SessionState(Box::new(frame))
        }
        SYSTEM_TASK_STARTED => {
            let frame: TaskStartedFrame = unmarshal(raw)?;
            if any_empty(&[
                &frame.task_id,
                &frame.description,
                &frame.uuid,
                &frame.session_id,
            ]) {
                return Err(invalid(
                    "task_started requires task_id, description, uuid, and session_id",
                ));
            }
            Observation::TaskStarted(Box::new(frame))
        }
        SYSTEM_TASK_PROGRESS => {
            let frame: TaskProgressFrame = unmarshal(raw)?;
            if any_empty(&[
                &frame.task_id,
                &frame.description,
                &frame.uuid,
                &frame.session_id,
            ]) {
                return Err(invalid(
                    "task_progress requires task_id, description, uuid, and session_id",
                ));
            }
            Observation::TaskProgress(Box::new(frame))
        }
        SYSTEM_TASK_NOTIFICATION => {
            let frame: TaskNotificationFrame = unmarshal(raw)?;
            let members: OutputFileMember = unmarshal(raw)?;
            if members.output_file.is_none()
                || any_empty(&[
                    &frame.task_id,
                    &frame.status,
                    &frame.summary,
                    &frame.uuid,
                    &frame.session_id,
                ])
            {
                return Err(invalid(
                    "task_notification requires task_id, status, output_file, summary, uuid, and session_id",
                ));
            }
            if !terminal_task_status(&frame.status) {
                return Err(invalid(format!(
                    "task_notification status {} is not completed, failed, or stopped",
                    oxap_adapter::gojson::go_quote(&frame.status)
                )));
            }
            Observation::TaskNotification(Box::new(frame))
        }
        SYSTEM_TASK_UPDATED => {
            let frame: TaskUpdatedFrame = unmarshal(raw)?;
            if frame.task_id.is_empty() {
                return Err(invalid("task_updated requires task_id"));
            }
            Observation::TaskUpdated(Box::new(frame))
        }
        _ => {
            let mut notice: SystemNotice = unmarshal(raw)?;
            notice.raw = raw.to_vec();
            Observation::SystemNotice(Box::new(notice))
        }
    })
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
        TYPE_SYSTEM => decode_system(subtype, raw)?,
        _ => Observation::Unknown {
            kind: kind.into(),
            subtype: subtype.into(),
        },
    })
}

#[derive(Clone, Debug)]
pub enum ControlRequest {
    CanUseTool(Box<CanUseToolRequest>),
    HookCallback(Box<HookCallbackRequest>),
    McpMessage(Box<McpMessageRequest>),
    Unknown { subtype: String },
}

#[derive(Default, serde::Deserialize)]
#[serde(bound(deserialize = "T: DeserializeOwned + Default"))]
struct Envelope<T> {
    #[serde(default, deserialize_with = "super::source::structure")]
    request: T,
}

fn request<T: DeserializeOwned + Default>(raw: &[u8]) -> Result<T, Invalid> {
    super::source::decode::<Envelope<T>>(&String::from_utf8_lossy(raw))
        .map(|envelope| envelope.request)
        .map_err(|error| invalid(error.to_string()))
}

pub fn decode_control_request(subtype: &str, raw: &[u8]) -> Result<ControlRequest, Invalid> {
    Ok(match subtype {
        CONTROL_CAN_USE_TOOL => {
            let ask: CanUseToolRequest = request(raw)?;
            if ask.tool_name.is_empty() || ask.tool_use_id.is_empty() || ask.input.is_none() {
                return Err(invalid(
                    "can_use_tool requires tool_name, input, and tool_use_id",
                ));
            }
            ControlRequest::CanUseTool(Box::new(ask))
        }
        CONTROL_HOOK_CALLBACK => {
            let hook: HookCallbackRequest = request(raw)?;
            if hook.callback_id.is_empty() {
                return Err(invalid("hook_callback requires callback_id"));
            }
            ControlRequest::HookCallback(Box::new(hook))
        }
        CONTROL_MCP_MESSAGE => ControlRequest::McpMessage(Box::new(request(raw)?)),
        _ => ControlRequest::Unknown {
            subtype: subtype.into(),
        },
    })
}
