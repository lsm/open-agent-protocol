use std::collections::BTreeMap;
use std::fmt;

use oap_adapter::gojson::{reject_duplicate_keys, replace_lone_surrogates};
use serde_json::value::RawValue;

pub type Object = BTreeMap<String, Box<RawValue>>;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct WireError;

impl fmt::Display for WireError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("invalid Copilot JSON-RPC frame")
    }
}

impl std::error::Error for WireError {}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Kind {
    #[default]
    Request,
    Notification,
    Response,
    Failure,
}

#[derive(Clone, Debug, Default)]
pub struct Message {
    pub kind: Kind,
    pub id: Option<Box<RawValue>>,
    pub method: String,
    pub params: Option<Box<RawValue>>,
    pub result: Option<Box<RawValue>>,
    pub code: i64,
    pub error: String,
}

fn decode_object(raw: &str) -> Option<Object> {
    serde_json::from_str::<Option<Object>>(&replace_lone_surrogates(raw))
        .ok()
        .flatten()
}

fn member(object: &Object, name: &str) -> Option<Box<RawValue>> {
    object.get(name).cloned()
}

pub fn text(object: &Object, name: &str) -> String {
    object
        .get(name)
        .and_then(|raw| serde_json::from_str::<String>(raw.get()).ok())
        .unwrap_or_default()
}

fn valid_id(raw: &RawValue) -> bool {
    let text = raw.get();
    if text.starts_with('"') {
        return serde_json::from_str::<String>(text).is_ok();
    }
    text != "null" && serde_json::from_str::<i64>(text).is_ok()
}

pub fn parse(frame: &[u8]) -> Result<Message, WireError> {
    reject_duplicate_keys(frame).map_err(|_| WireError)?;
    let frame = std::str::from_utf8(frame).map_err(|_| WireError)?;
    let object = decode_object(frame).ok_or(WireError)?;
    if text(&object, "jsonrpc") != "2.0" {
        return Err(WireError);
    }
    let mut message = Message {
        id: member(&object, "id"),
        params: member(&object, "params"),
        result: member(&object, "result"),
        ..Message::default()
    };
    if message.id.as_deref().is_some_and(|id| !valid_id(id)) {
        return Err(WireError);
    }
    let has_result = object.contains_key("result");
    let failure = object.get("error");
    if let Some(method) = object.get("method") {
        message.method = serde_json::from_str::<Option<String>>(method.get())
            .map_err(|_| WireError)?
            .unwrap_or_default();
        if message.method.is_empty() || has_result || failure.is_some() {
            return Err(WireError);
        }
        message.kind = if message.id.is_some() {
            Kind::Request
        } else {
            Kind::Notification
        };
        return Ok(message);
    }
    if message.id.is_none() || has_result == failure.is_some() {
        return Err(WireError);
    }
    message.kind = Kind::Response;
    if let Some(failure) = failure {
        let body = decode_object(failure.get()).ok_or(WireError)?;
        let code = body.get("code").ok_or(WireError)?;
        if code.get() == "null" {
            return Err(WireError);
        }
        message.code = serde_json::from_str(code.get()).map_err(|_| WireError)?;
        message.error = text(&body, "message");
        if message.error.is_empty() {
            return Err(WireError);
        }
        message.kind = Kind::Failure;
    }
    Ok(message)
}

#[derive(Clone, Debug, Default)]
pub struct Event {
    pub session_id: String,
    pub kind: String,
    pub ephemeral: bool,
    pub data: Object,
}

pub fn decode_event(message: &Message) -> Result<Option<Event>, WireError> {
    if message.kind != Kind::Notification || message.method != "session.event" {
        return Ok(None);
    }
    let params = message
        .params
        .as_deref()
        .and_then(|raw| decode_object(raw.get()))
        .ok_or(WireError)?;
    let event = params
        .get("event")
        .and_then(|raw| decode_object(raw.get()))
        .ok_or(WireError)?;
    let session_id = text(&params, "sessionId");
    let kind = text(&event, "type");
    if session_id.is_empty() || kind.is_empty() {
        return Err(WireError);
    }
    let data = event
        .get("data")
        .and_then(|raw| decode_object(raw.get()))
        .ok_or(WireError)?;
    let ephemeral = event
        .get("ephemeral")
        .and_then(|raw| serde_json::from_str::<bool>(raw.get()).ok())
        .unwrap_or_default();
    Ok(Some(Event {
        session_id,
        kind,
        ephemeral,
        data,
    }))
}
