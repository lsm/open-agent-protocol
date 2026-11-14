use serde::Serialize;

use super::{Invalid, TYPE_USER};

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
    serde_json::to_string(&turn).map(|json| oxap_adapter::gojson::go_marshal(&json))
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
