use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use oxap_adapter::gojson::{go_marshal, strings};

use super::{
    Invalid, CONTROL_INITIALIZE, CONTROL_INTERRUPT, HOOK_PRE_TOOL_USE, TOOL_SELECTION_HOOK,
    TYPE_USER,
};

fn marshal(value: &impl Serialize) -> Result<String, serde_json::Error> {
    serde_json::to_string(value).map(|json| go_marshal(&json))
}

#[derive(Serialize)]
struct TurnMessage<'a> {
    content: &'a str,
    role: &'static str,
}

#[derive(Serialize)]
struct TurnOrigin {
    kind: &'static str,
}

#[derive(Serialize)]
struct UserTurn<'a> {
    #[serde(skip_serializing_if = "Option::is_none")]
    client_composed: Option<bool>,
    message: TurnMessage<'a>,
    origin: TurnOrigin,
    parent_tool_use_id: Option<()>,
    session_id: &'static str,
    #[serde(rename = "type")]
    kind: &'static str,
    uuid: &'a str,
}

pub fn new_user_turn(uuid: &str, text: &str, composed: bool) -> Result<String, serde_json::Error> {
    let turn = UserTurn {
        client_composed: composed.then_some(true),
        message: TurnMessage {
            content: text,
            role: "user",
        },
        origin: TurnOrigin { kind: "human" },
        parent_tool_use_id: None,
        session_id: "default",
        kind: TYPE_USER,
        uuid,
    };
    marshal(&turn)
}

pub fn validate_turn_uuid(uuid: &str) -> Result<(), Invalid> {
    if uuid.is_empty() || uuid.len() > 128 {
        return Err(Invalid("turn uuid must be 1-128 bytes".into()));
    }
    if !uuid
        .chars()
        .all(|character| character.is_ascii_alphanumeric() || character == '-')
    {
        return Err(Invalid(
            "turn uuid must be alphanumeric with hyphens".into(),
        ));
    }
    Ok(())
}

#[derive(Serialize)]
struct Empty {}

pub fn hook_continue() -> Result<String, serde_json::Error> {
    marshal(&Empty {})
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct PreToolUseDecision<'a> {
    hook_event_name: &'static str,
    permission_decision: &'static str,
    permission_decision_reason: &'a str,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct HookDeny<'a> {
    hook_specific_output: PreToolUseDecision<'a>,
}

pub fn deny_tool(reason: &str) -> Result<String, serde_json::Error> {
    marshal(&HookDeny {
        hook_specific_output: PreToolUseDecision {
            hook_event_name: HOOK_PRE_TOOL_USE,
            permission_decision: "deny",
            permission_decision_reason: reason,
        },
    })
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct HookMatcher {
    matcher: Option<()>,
    hook_callback_ids: [&'static str; 1],
}

#[derive(Serialize)]
struct ToolSelectionHooks {
    #[serde(rename = "PreToolUse")]
    pre_tool_use: [HookMatcher; 1],
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct InitializeRequest<'a> {
    subtype: &'static str,
    hooks: ToolSelectionHooks,
    #[serde(skip_serializing_if = "<[String]>::is_empty")]
    sdk_mcp_servers: &'a [String],
}

pub fn initialize_request(sdk_mcp_servers: &[String]) -> Result<String, serde_json::Error> {
    marshal(&InitializeRequest {
        subtype: CONTROL_INITIALIZE,
        hooks: ToolSelectionHooks {
            pre_tool_use: [HookMatcher {
                matcher: None,
                hook_callback_ids: [TOOL_SELECTION_HOOK],
            }],
        },
        sdk_mcp_servers,
    })
}

#[derive(Serialize)]
struct InterruptRequest {
    subtype: &'static str,
}

pub fn interrupt_request() -> Result<String, serde_json::Error> {
    marshal(&InterruptRequest {
        subtype: CONTROL_INTERRUPT,
    })
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct PermissionAllow<'a> {
    behavior: &'static str,
    updated_input: Option<&'a RawValue>,
}

pub fn permission_allow(updated_input: Option<&RawValue>) -> Result<String, serde_json::Error> {
    marshal(&PermissionAllow {
        behavior: "allow",
        updated_input,
    })
}

#[derive(Serialize)]
struct PermissionDeny<'a> {
    behavior: &'static str,
    message: &'a str,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    interrupt: bool,
}

pub fn permission_deny(message: &str, interrupt: bool) -> Result<String, serde_json::Error> {
    marshal(&PermissionDeny {
        behavior: "deny",
        message,
        interrupt,
    })
}

#[derive(Serialize)]
struct McpResponse<'a> {
    mcp_response: Option<&'a RawValue>,
}

pub fn mcp_response(message: Option<&RawValue>) -> Result<String, serde_json::Error> {
    marshal(&McpResponse {
        mcp_response: message,
    })
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct InterruptResult {
    #[serde(default, deserialize_with = "strings")]
    pub still_queued: Vec<String>,
    #[serde(default, deserialize_with = "strings")]
    pub cancelled: Vec<String>,
}

pub fn decode_interrupt_result(raw: &[u8]) -> Result<InterruptResult, serde_json::Error> {
    super::source::decode_object(&String::from_utf8_lossy(raw))
}
