use std::fmt;

use crate::gojson::{go_marshal, go_quote, reject_duplicate_keys, replace_lone_surrogates};
use serde::Deserialize;
use serde_json::value::RawValue;
use std::collections::HashMap;

use super::{Dialect, Error, Fault};

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub enum RequestId {
    String(String),
    Integer(i64),
}

impl fmt::Display for RequestId {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::String(text) => f.write_str(&go_quote(text)),
            Self::Integer(value) => write!(f, "{value}"),
        }
    }
}

impl RequestId {
    pub fn parse(raw: &str) -> Result<Self, Fault> {
        let text = raw.trim();
        if text.starts_with('"') {
            return serde_json::from_str(text)
                .map(Self::String)
                .map_err(|error| Fault::InvalidId(error.to_string()));
        }
        let integer = text.strip_prefix('-').unwrap_or(text);
        if integer.is_empty() || !integer.bytes().all(|byte| byte.is_ascii_digit()) {
            return Err(Fault::InvalidId(String::new()));
        }
        text.parse()
            .map(Self::Integer)
            .map_err(|_| Fault::InvalidId(go_quote(text)))
    }

    fn encode(&self) -> String {
        match self {
            Self::String(text) => go_marshal(&serde_json::to_string(text).unwrap_or_default()),
            Self::Integer(value) => value.to_string(),
        }
    }
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Kind {
    Request,
    Notification,
    Response,
    Error,
}

#[derive(Clone, Debug, Default, Deserialize)]
pub struct ErrorObject {
    #[serde(default, deserialize_with = "zero")]
    pub code: i64,
    #[serde(default, deserialize_with = "zero")]
    pub message: String,
    #[serde(default, deserialize_with = "raw")]
    pub data: Option<Box<RawValue>>,
}

fn zero<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

fn raw<'de, D>(deserializer: D) -> Result<Option<Box<RawValue>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    Box::<RawValue>::deserialize(deserializer).map(Some)
}

impl ErrorObject {
    fn encode(&self) -> String {
        let mut out = format!(
            "{{\"code\":{},\"message\":{}",
            self.code,
            serde_json::to_string(&self.message).unwrap_or_default()
        );
        if let Some(data) = &self.data {
            out.push_str(",\"data\":");
            out.push_str(data.get());
        }
        out.push('}');
        go_marshal(&out)
    }
}

#[derive(Clone, Debug)]
pub struct Message {
    pub kind: Kind,
    pub id: Option<RequestId>,
    pub method: String,
    pub params: Option<Box<RawValue>>,
    pub trace: Option<Box<RawValue>>,
    pub result: Option<Box<RawValue>>,
    pub error: Option<ErrorObject>,
}

impl Message {
    fn bare(kind: Kind) -> Self {
        Self {
            kind,
            id: None,
            method: String::new(),
            params: None,
            trace: None,
            result: None,
            error: None,
        }
    }

    pub fn request(id: RequestId, method: &str, params: Option<Box<RawValue>>) -> Self {
        Self {
            id: Some(id),
            method: method.into(),
            params,
            ..Self::bare(Kind::Request)
        }
    }

    pub fn notification(method: &str, params: Option<Box<RawValue>>) -> Self {
        Self {
            method: method.into(),
            params,
            ..Self::bare(Kind::Notification)
        }
    }

    pub fn response(id: RequestId, result: Box<RawValue>) -> Self {
        Self {
            id: Some(id),
            result: Some(result),
            ..Self::bare(Kind::Response)
        }
    }

    pub fn error_response(id: RequestId, error: ErrorObject) -> Self {
        Self {
            id: Some(id),
            error: Some(error),
            ..Self::bare(Kind::Error)
        }
    }

    pub fn parse(dialect: &Dialect, data: &[u8]) -> Result<Self, Error> {
        Self::parse_fault(dialect, data).map_err(|fault| dialect.error(fault))
    }

    fn parse_fault(dialect: &Dialect, data: &[u8]) -> Result<Self, Fault> {
        let invalid = |detail: &str| Fault::InvalidMessage(detail.to_owned());
        let text = std::str::from_utf8(data).map_err(|_| invalid("frame is not UTF-8"))?;
        if dialect.strict_frames && !(data.first() == Some(&b'{') && data.last() == Some(&b'}')) {
            return Err(invalid("frame must be exactly one JSON object"));
        }
        reject_duplicate_keys(data).map_err(|error| invalid(&error.to_string()))?;
        let text = replace_lone_surrogates(text);
        let members: Option<HashMap<String, Box<RawValue>>> =
            serde_json::from_str(&text).map_err(|error| invalid(&error.to_string()))?;
        let mut members = members.ok_or_else(|| invalid("expected one object"))?;
        if !dialect.versioned && members.is_empty() {
            return Err(invalid("empty object"));
        }
        if dialect.strict_frames {
            if let Some(name) = members
                .keys()
                .find(|name| !MEMBERS.contains(&name.as_str()))
            {
                return Err(invalid(&format!("unknown member {}", go_quote(name))));
            }
        }
        if !dialect.versioned {
            if members.contains_key("jsonrpc") {
                return Err(invalid(&format!(
                    "jsonrpc member is not part of the {} dialect",
                    dialect
                        .name
                        .split(' ')
                        .next()
                        .map(capitalized)
                        .unwrap_or_default()
                )));
            }
        } else if members.get("jsonrpc").is_none_or(|version| {
            serde_json::from_str::<String>(version.get())
                .ok()
                .as_deref()
                != Some("2.0")
        }) {
            return Err(invalid("jsonrpc must equal \"2.0\""));
        }
        let id = members.remove("id");
        let method = members.remove("method");
        let params = members.remove("params");
        let result = members.remove("result");
        let error = members.remove("error");
        let trace = members.remove("trace");
        if result.is_some() && error.is_some() {
            return Err(invalid("result and error are mutually exclusive"));
        }
        if method.is_some() && (result.is_some() || error.is_some()) {
            return Err(invalid("method cannot accompany a response"));
        }
        if dialect.versioned && method.is_none() && params.is_some() {
            return Err(invalid("params requires method"));
        }
        let kind = match (&id, &method) {
            (Some(_), Some(_)) => Kind::Request,
            (None, Some(_)) => Kind::Notification,
            (Some(_), None) if result.is_some() => Kind::Response,
            (Some(_), None) if error.is_some() => Kind::Error,
            _ => return Err(invalid("unrecognized object shape")),
        };
        let mut message = Self::bare(kind);
        if let Some(id) = id {
            message.id = Some(RequestId::parse(id.get())?);
        }
        if let Some(method) = method {
            message.method = serde_json::from_str::<Option<String>>(method.get())
                .ok()
                .flatten()
                .filter(|method| !method.is_empty())
                .ok_or_else(|| invalid("method must be a non-empty string"))?;
            if params
                .as_deref()
                .is_some_and(|params| !dialect.params_shape(params.get()))
            {
                return Err(invalid(dialect.params_rule()));
            }
            message.params = params;
            if !dialect.versioned {
                message.trace = trace;
            }
        }
        message.result = result;
        if let Some(error) = error {
            let fields: Option<HashMap<String, Box<RawValue>>> = serde_json::from_str(error.get())
                .map_err(|error| {
                    if dialect.strict_frames {
                        invalid(&format!("malformed error object: {error}"))
                    } else {
                        invalid("malformed error object")
                    }
                })?;
            let fields = fields.ok_or_else(|| invalid("malformed error object"))?;
            if dialect.strict_frames {
                if let Some(name) = fields
                    .keys()
                    .find(|name| !["code", "message", "data"].contains(&name.as_str()))
                {
                    return Err(invalid(&format!("unknown error member {}", go_quote(name))));
                }
            }
            if !fields.contains_key("code") {
                return Err(invalid("error code is required"));
            }
            if dialect.versioned && !fields.contains_key("message") {
                return Err(invalid("error message is required"));
            }
            let object: ErrorObject =
                serde_json::from_str(error.get()).map_err(|_| invalid("malformed error object"))?;
            if (dialect.strict_frames || !dialect.versioned) && object.message.is_empty() {
                return Err(invalid("malformed error object"));
            }
            message.error = Some(object);
        }
        Ok(message)
    }

    pub fn encode(&self, dialect: &Dialect) -> Result<String, Error> {
        self.encode_fault(dialect)
            .map_err(|fault| dialect.error(fault))
    }

    fn encode_fault(&self, dialect: &Dialect) -> Result<String, Fault> {
        let refuse = || Fault::InvalidMessage(String::new());
        let empty = RequestId::String(String::new());
        let id = || {
            self.id
                .as_ref()
                .filter(|id| !(dialect.nonempty_string_ids && *id == &empty))
                .map(RequestId::encode)
                .ok_or_else(refuse)
        };
        let params_ok = self
            .params
            .as_deref()
            .is_none_or(|params| dialect.params_shape(params.get()));
        let mut members: Vec<(&str, String)> = if dialect.versioned {
            vec![("jsonrpc", "\"2.0\"".into())]
        } else {
            Vec::new()
        };
        match self.kind {
            Kind::Request | Kind::Notification => {
                if self.method.is_empty() || !params_ok {
                    return Err(refuse());
                }
                if self.kind == Kind::Request {
                    members.push(("id", id()?));
                }
                members.push((
                    "method",
                    go_marshal(&serde_json::to_string(&self.method).unwrap_or_default()),
                ));
                if let Some(params) = &self.params {
                    members.push(("params", go_marshal(params.get())));
                }
                if let (Kind::Request, Some(trace)) = (self.kind, &self.trace) {
                    members.push(("trace", go_marshal(trace.get())));
                }
            }
            Kind::Response => {
                let result = self.result.as_deref().ok_or_else(refuse)?;
                members.push(("id", id()?));
                members.push(("result", go_marshal(result.get())));
            }
            Kind::Error => {
                let error = self.error.as_ref().ok_or_else(refuse)?;
                members.push(("id", id()?));
                members.push(("error", error.encode()));
            }
        }
        members.sort_by_key(|(name, _)| *name);
        let body: Vec<String> = members
            .into_iter()
            .map(|(name, value)| format!("\"{name}\":{value}"))
            .collect();
        Ok(format!("{{{}}}", body.join(",")))
    }
}

const MEMBERS: [&str; 6] = ["jsonrpc", "id", "method", "params", "result", "error"];

pub(crate) fn object_or_array(raw: &str) -> bool {
    matches!(raw.trim_start().as_bytes().first(), Some(b'{' | b'['))
}

fn capitalized(word: &str) -> String {
    let mut characters = word.chars();
    characters
        .next()
        .map(|first| first.to_uppercase().chain(characters).collect())
        .unwrap_or_default()
}
