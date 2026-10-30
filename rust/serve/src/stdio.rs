use std::fmt;
use std::io::{BufRead, Write};

use serde::de::{self, MapAccess, Visitor};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use serde_json::Value;

use crate::live::block_on;
use crate::Hub;

pub const DEFAULT_FRAME_LIMIT: usize = (16 << 20) + (2 << 20);

pub const MIN_FRAME_LIMIT: usize = 256;

const MESSAGE_LIMIT: usize = 300;

const PARAMS: [&str; 8] = [
    "adapter",
    "session_id",
    "run_id",
    "after",
    "request",
    "cursor",
    "limit",
    "allow_degraded_features",
];

#[derive(Clone, Debug)]
pub struct StdioOptions {
    pub frame_limit: usize,
}

impl Default for StdioOptions {
    fn default() -> Self {
        Self {
            frame_limit: DEFAULT_FRAME_LIMIT,
        }
    }
}

#[derive(Debug, thiserror::Error)]
pub enum StdioError {
    #[error(
        "servestdio: frame limit {0} is below the 256-byte minimum a correlated refusal needs"
    )]
    FrameLimit(usize),
    #[error("line {line} is not a valid request: {detail}")]
    MalformedLine { line: usize, detail: String },
    #[error("servestdio: encoded line exceeds the frame limit")]
    LineTooLarge,
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

#[derive(Debug, Default)]
struct Request {
    id: i64,
    op: String,
    present: Vec<String>,
}

#[derive(Serialize)]
struct WireError {
    code: &'static str,
    message: String,
}

#[derive(Serialize)]
struct Response<'a> {
    id: i64,
    ok: bool,
    result: &'a RawValue,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<WireError>,
}

pub fn serve(
    hub: &Hub,
    mut input: impl BufRead,
    mut output: impl Write,
    options: &StdioOptions,
) -> Result<(), StdioError> {
    if options.frame_limit < MIN_FRAME_LIMIT {
        return Err(StdioError::FrameLimit(options.frame_limit));
    }
    let mut number = 0;
    loop {
        number += 1;
        let malformed = |detail: String| StdioError::MalformedLine {
            line: number,
            detail,
        };
        let Some(frame) = read_frame(&mut input, options.frame_limit).map_err(|err| match err {
            FrameError::Defect(detail) => malformed(detail),
            FrameError::Io(err) => StdioError::Io(err),
        })?
        else {
            return Ok(());
        };
        let request = decode_request(&frame).map_err(malformed)?;
        let answer = dispatch(hub, &request);
        let Some(line) = fitted(request.id, answer, options.frame_limit)? else {
            continue;
        };
        output.write_all(&line)?;
        output.write_all(b"\n")?;
        output.flush()?;
    }
}

enum FrameError {
    Defect(String),
    Io(std::io::Error),
}

fn read_frame(input: &mut impl BufRead, limit: usize) -> Result<Option<Vec<u8>>, FrameError> {
    let mut frame = Vec::new();
    loop {
        let available = input.fill_buf().map_err(FrameError::Io)?;
        if available.is_empty() {
            if frame.is_empty() {
                return Ok(None);
            }
            return Err(FrameError::Defect("unterminated final line".into()));
        }
        let (taken, ended) = match available.iter().position(|byte| *byte == b'\n') {
            Some(at) => (at + 1, true),
            None => (available.len(), false),
        };
        if frame.len() + taken > limit + 1 {
            return Err(FrameError::Defect(format!(
                "line exceeds the {limit}-byte frame limit"
            )));
        }
        frame.extend_from_slice(&available[..taken]);
        input.consume(taken);
        if !ended {
            continue;
        }
        frame.pop();
        if frame.contains(&b'\r') {
            return Err(FrameError::Defect(
                "carriage return is not valid framing".into(),
            ));
        }
        if frame.is_empty() {
            return Err(FrameError::Defect("empty line".into()));
        }
        if std::str::from_utf8(&frame).is_err() {
            return Err(FrameError::Defect("line is not UTF-8".into()));
        }
        return Ok(Some(frame));
    }
}

struct Members(Vec<(String, Box<RawValue>)>);

impl<'de> Deserialize<'de> for Members {
    fn deserialize<D: de::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct Collect;

        impl<'de> Visitor<'de> for Collect {
            type Value = Members;

            fn expecting(&self, formatter: &mut fmt::Formatter) -> fmt::Result {
                formatter.write_str("request is not a JSON object")
            }

            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Members, A::Error> {
                let mut members = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    members.push((key, map.next_value::<Box<RawValue>>()?));
                }
                Ok(Members(members))
            }
        }

        deserializer.deserialize_map(Collect)
    }
}

fn invalid(detail: impl fmt::Display) -> String {
    trim(&format!("invalid JSON request: {detail}"))
}

fn decode_request(frame: &[u8]) -> Result<Request, String> {
    let mut deserializer = serde_json::Deserializer::from_slice(frame);
    let Members(members) = Members::deserialize(&mut deserializer).map_err(invalid)?;
    deserializer
        .end()
        .map_err(|_| invalid("trailing data after the request object"))?;
    let mut request = Request::default();
    let mut id = None;
    for (key, value) in members {
        if request.present.contains(&key) {
            return Err(invalid(format!("repeated key {key:?}")));
        }
        let typed = |ok: bool| {
            if ok {
                Ok(())
            } else {
                Err(invalid(format!("field {key:?} has the wrong type")))
            }
        };
        let parsed: Value = serde_json::from_str(value.get()).map_err(invalid)?;
        match key.as_str() {
            "id" => {
                id = parsed.as_i64();
                typed(id.is_some())?;
            }
            "op" => {
                typed(parsed.is_string())?;
                request.op = parsed.as_str().unwrap_or_default().to_owned();
            }
            "adapter" | "session_id" | "run_id" | "cursor" => {
                typed(parsed.is_string() || parsed.is_null())?;
            }
            "limit" => typed(parsed.is_i64() || parsed.is_null())?,
            "allow_degraded_features" => typed(match &parsed {
                Value::Array(items) => items.iter().all(Value::is_string),
                Value::Null => true,
                _ => false,
            })?,
            "after" | "request" => {}
            _ => {
                return Err(invalid(format!(
                    "field {key:?} must use its exact protocol spelling"
                )))
            }
        }
        request.present.push(key);
    }
    request.id = id.ok_or_else(|| invalid("id is required"))?;
    if request.op.is_empty() {
        return Err(invalid("op is required"));
    }
    Ok(request)
}

fn trim(message: &str) -> String {
    match message.char_indices().nth(MESSAGE_LIMIT) {
        Some((at, _)) => format!("{}…", &message[..at]),
        None => message.to_owned(),
    }
}

type Answer = Result<Value, WireError>;

impl Request {
    fn only(&self, allowed: &[&str]) -> Result<(), WireError> {
        let extra: Vec<&str> = PARAMS
            .into_iter()
            .filter(|param| !allowed.contains(param) && self.present.iter().any(|key| key == param))
            .collect();
        if extra.is_empty() {
            return Ok(());
        }
        Err(WireError {
            code: "invalid_request",
            message: format!(
                "op {:?} accepts no {} parameter",
                trim(&self.op),
                extra.join(", ")
            ),
        })
    }
}

fn dispatch(hub: &Hub, request: &Request) -> Answer {
    match request.op.as_str() {
        "adapters" => {
            request.only(&[])?;
            Ok(adapters(hub))
        }
        _ => Err(WireError {
            code: "unknown_op",
            message: format!("no op {:?}", trim(&request.op)),
        }),
    }
}

#[derive(Serialize)]
struct AdapterInfo {
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    capability_revision: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    capabilities: Option<oap_protocol::CapabilityDescriptor>,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
}

fn adapters(hub: &Hub) -> Value {
    let listed: Vec<AdapterInfo> = block_on(hub.adapters())
        .into_iter()
        .map(|(name, probed)| match probed {
            Ok(descriptor) => AdapterInfo {
                name,
                capability_revision: descriptor.capability_revision,
                capabilities: Some(descriptor.capabilities),
                error: String::new(),
            },
            Err(err) => AdapterInfo {
                name,
                capability_revision: String::new(),
                capabilities: None,
                error: trim(&err.to_string()),
            },
        })
        .collect();
    serde_json::json!({ "adapters": listed })
}

fn fitted(id: i64, answer: Answer, limit: usize) -> Result<Option<Vec<u8>>, StdioError> {
    let reduced = match &answer {
        Err(error) => Some(WireError {
            code: error.code,
            message: trim(&error.message),
        }),
        Ok(_) => None,
    };
    let fallback = WireError {
        code: "response_too_large",
        message: "the encoded response exceeds the frame limit".into(),
    };
    for candidate in [Some(answer), reduced.map(Err), Some(Err(fallback))]
        .into_iter()
        .flatten()
    {
        let line = response_line(id, candidate)?;
        if line.len() <= limit {
            return Ok(Some(line));
        }
    }
    Ok(None)
}

fn response_line(id: i64, answer: Answer) -> Result<Vec<u8>, StdioError> {
    let (result, error) = match answer {
        Ok(value) => (value, None),
        Err(error) => (Value::Null, Some(error)),
    };
    let result = serde_json::value::to_raw_value(&result).map_err(|_| StdioError::LineTooLarge)?;
    serde_json::to_vec(&Response {
        id,
        ok: error.is_none(),
        result: &result,
        error,
    })
    .map_err(|_| StdioError::LineTooLarge)
}
