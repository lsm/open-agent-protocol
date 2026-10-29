use std::collections::BTreeMap;
use std::fmt;
use std::io::{BufRead, ErrorKind};

use crossbeam_channel::Sender;
use serde_json::value::RawValue;

use crate::native::{
    decode_strict, validate_event, Command, Event, ExtensionUiRequest, ExtensionUiResponse,
    Response,
};

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Error {
    InvalidFrame(String),
    Remote {
        id: String,
        command: String,
        message: String,
    },
    Closed,
    Other(String),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidFrame(detail) => write!(f, "pi rpc: invalid JSONL frame: {detail}"),
            Self::Remote {
                id,
                command,
                message,
            } => write!(f, "pi rpc error for {id} ({command}): {message}"),
            Self::Closed => f.write_str("pi rpc: client closed"),
            Self::Other(text) => f.write_str(text),
        }
    }
}

impl std::error::Error for Error {}

#[derive(Clone, Debug)]
pub enum Frame {
    Response(Box<Response>),
    Event(Event),
    ExtensionRequest(Box<ExtensionUiRequest>),
}

#[derive(Debug)]
pub enum Inbound {
    Event(Event),
    ExtensionRequest(Box<ExtensionUiRequest>),
    Barrier(Sender<()>),
}

pub fn decode_frame(data: &str) -> Result<Frame, Error> {
    let object: BTreeMap<String, Box<RawValue>> =
        decode_strict(data).map_err(|_| Error::InvalidFrame("missing type".into()))?;
    let kind = object
        .get("type")
        .and_then(|raw| serde_json::from_str::<String>(raw.get()).ok())
        .filter(|kind| !kind.is_empty())
        .ok_or_else(|| Error::InvalidFrame("missing type".into()))?;
    match kind.as_str() {
        "response" => {
            let response: Response = decode_strict(data)
                .map_err(|error| Error::InvalidFrame(format!("response: {error}")))?;
            response
                .validate()
                .map_err(|invalid| Error::InvalidFrame(invalid.to_string()))?;
            Ok(Frame::Response(Box::new(response)))
        }
        "extension_ui_request" => {
            let request: ExtensionUiRequest = decode_strict(data)
                .map_err(|error| Error::InvalidFrame(format!("extension request: {error}")))?;
            request
                .validate()
                .map_err(|invalid| Error::InvalidFrame(invalid.to_string()))?;
            Ok(Frame::ExtensionRequest(Box::new(request)))
        }
        _ => {
            validate_event(data, &kind)
                .map_err(|invalid| Error::InvalidFrame(format!("event: {invalid}")))?;
            Ok(Frame::Event(Event {
                kind,
                raw: data.to_owned(),
            }))
        }
    }
}

pub const DEFAULT_FRAME_LIMIT: usize = 8 << 20;
const FRAME_TOO_LARGE: &str = "pi rpc: frame exceeds configured limit";

pub enum Outbound {
    Command(Box<Command>),
    Response(ExtensionUiResponse),
}

fn limit_or_default(limit: usize) -> usize {
    if limit == 0 {
        DEFAULT_FRAME_LIMIT
    } else {
        limit
    }
}

pub fn read_frame(reader: &mut impl BufRead, limit: usize) -> Result<String, Error> {
    let limit = limit_or_default(limit);
    let invalid = |detail: &str| Error::InvalidFrame(detail.to_owned());
    let mut frame = Vec::with_capacity(limit.min(4096));
    loop {
        let available = match reader.fill_buf() {
            Ok(available) => available,
            Err(error) if error.kind() == ErrorKind::Interrupted => continue,
            Err(error) => return Err(Error::Other(error.to_string())),
        };
        if available.is_empty() {
            if frame.is_empty() {
                return Err(Error::Other("EOF".into()));
            }
            return Err(invalid("unterminated frame"));
        }
        let newline = available.iter().position(|byte| *byte == b'\n');
        let take = newline.map_or(available.len(), |at| at + 1);
        if frame.len() + take > limit + 1 {
            return Err(Error::Other(FRAME_TOO_LARGE.into()));
        }
        frame.extend_from_slice(&available[..take]);
        reader.consume(take);
        if newline.is_none() {
            continue;
        }
        frame.pop();
        if frame.contains(&b'\r') {
            return Err(invalid("carriage return is not valid framing"));
        }
        if frame.is_empty() {
            return Err(invalid("empty frame"));
        }
        return String::from_utf8(frame).map_err(|_| invalid("frame is not UTF-8"));
    }
}

pub fn encode(value: &Outbound, limit: usize) -> Result<Vec<u8>, Error> {
    let limit = limit_or_default(limit);
    let encoded = match value {
        Outbound::Command(command) => {
            command
                .validate()
                .map_err(|invalid| Error::Other(invalid.to_string()))?;
            serde_json::to_string(command)
        }
        Outbound::Response(response) => {
            response
                .validate()
                .map_err(|invalid| Error::Other(invalid.to_string()))?;
            serde_json::to_string(response)
        }
    };
    let mut data = oap_adapter::gojson::go_marshal(
        &encoded.map_err(|failure| Error::Other(failure.to_string()))?,
    )
    .into_bytes();
    if data.len() > limit {
        return Err(Error::Other(FRAME_TOO_LARGE.into()));
    }
    data.push(b'\n');
    Ok(data)
}
