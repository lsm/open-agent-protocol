use std::io::{BufRead, Write};

use oap_adapter::gojson::zero;
use oap_protocol::{Envelope, RunId, SessionId};
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ControlFrame {
    #[serde(deserialize_with = "zero")]
    pub control: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "zero")]
    pub id: String,
    #[serde(skip_serializing_if = "is_default", deserialize_with = "zero")]
    pub session_id: SessionId,
    #[serde(skip_serializing_if = "is_default", deserialize_with = "zero")]
    pub run_id: RunId,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub after: Option<u64>,
    #[serde(skip_serializing_if = "is_default", deserialize_with = "zero")]
    pub requested_after: u64,
    #[serde(skip_serializing_if = "is_default", deserialize_with = "zero")]
    pub oldest_available: u64,
    #[serde(skip_serializing_if = "is_default", deserialize_with = "zero")]
    pub latest_available: u64,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "zero")]
    pub code: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "zero")]
    pub message: String,
}

fn is_default<T: Default + PartialEq>(value: &T) -> bool {
    *value == T::default()
}

#[derive(Clone, Debug)]
pub enum Frame {
    Envelope(Box<Envelope>),
    Control(ControlFrame),
}

#[derive(Debug, thiserror::Error)]
pub enum WireError {
    #[error("conformance: {0}")]
    Io(#[from] std::io::Error),
    #[error("stdout carried a line that is not JSON: {0}")]
    Json(serde_json::Error),
    #[error("stdout carried a malformed control frame: {0}")]
    Control(serde_json::Error),
    #[error("stdout carried a line that is not an OAP envelope: {0}")]
    Envelope(oap_protocol::Error),
    #[error("conformance: could not encode a frame: {0}")]
    Encode(serde_json::Error),
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct Shape {
    protocol: Option<String>,
    control: Option<String>,
}

pub fn read_frame(input: &mut impl BufRead) -> Result<Option<Frame>, WireError> {
    let mut line = String::new();
    loop {
        line.clear();
        if input.read_line(&mut line)? == 0 {
            return Ok(None);
        }
        let text = line.trim();
        if text.is_empty() {
            continue;
        }
        let value: serde_json::Value = serde_json::from_str(text).map_err(WireError::Json)?;
        let shape: Shape = serde_json::from_value::<Option<Shape>>(value.clone())
            .map_err(WireError::Json)?
            .unwrap_or_default();
        if shape.protocol.unwrap_or_default().is_empty()
            && !shape.control.unwrap_or_default().is_empty()
        {
            let control = serde_json::from_value(value).map_err(WireError::Control)?;
            return Ok(Some(Frame::Control(control)));
        }
        let envelope = Envelope::parse(text.as_bytes()).map_err(WireError::Envelope)?;
        return Ok(Some(Frame::Envelope(Box::new(envelope))));
    }
}

pub fn write_frame(output: &mut impl Write, frame: &Frame) -> Result<(), WireError> {
    let bytes = match frame {
        Frame::Envelope(envelope) => serde_json::to_vec(envelope),
        Frame::Control(control) => serde_json::to_vec(control),
    }
    .map_err(WireError::Encode)?;
    output.write_all(&bytes)?;
    output.write_all(b"\n")?;
    output.flush()?;
    Ok(())
}
