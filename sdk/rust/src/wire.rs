//! Envelope construction and frame parsing.
//!
//! Every line on the transport is one JSON envelope. Outbound envelopes are
//! built through [`Envelope`]; inbound lines are parsed into [`Frame`], which
//! keeps the routing fields typed and leaves the per-type payload as JSON for
//! the namespace that owns it to deserialize into a pinned shape.

use serde::Deserialize;
use serde_json::Value;

use crate::error::{Error, Result};

pub(crate) const OAP_PROTOCOL: &str = "open-agent-protocol";
pub(crate) const OAP_VERSION: &str = "0.1";
pub(crate) const AGENT_PROFILE: &str = "open-agent-protocol.agent-control-core";
pub(crate) const SDK_PARTICIPANT: &str = "rust-sdk";
pub(crate) const PROVIDER_PROFILE: &str = "open-agent-protocol.model-provider-core";

/// An inbound frame.
#[derive(Debug, Clone)]
pub struct Frame {
    /// The envelope `type`.
    pub kind: String,
    /// The agent route key, when present.
    pub session_id: Option<String>,
    /// OAP agent run route key.
    pub run_id: Option<String>,
    /// OAP provider event route key.
    pub inference_id: Option<String>,
    /// OAP profile discriminator.
    pub profile: Option<String>,
    /// This envelope's own identity.
    pub id: Option<String>,
    /// The `id` of the request this frame replies to, when it is a reply.
    pub in_reply_to: Option<String>,
    /// The per-direction, per-route allocation counter.
    pub sequence: Option<u64>,
    /// The whole frame as received.
    pub raw: Value,
}

#[derive(Deserialize)]
struct RawFrame {
    #[serde(rename = "type")]
    kind: Option<String>,
    session_id: Option<String>,
    run_id: Option<String>,
    inference_id: Option<String>,
    profile: Option<String>,
    id: Option<String>,
    in_reply_to: Option<String>,
    sequence: Option<u64>,
}

impl Frame {
    /// Parses one NDJSON line.
    pub(crate) fn parse(line: &str) -> Result<Self> {
        let raw: Value =
            serde_json::from_str(line).map_err(|_| Error::transport("invalid JSON frame"))?;
        if !raw.is_object() {
            return Err(Error::transport("frame is not a JSON object"));
        }
        let fields: RawFrame = serde_json::from_value(raw.clone())
            .map_err(|_| Error::transport("malformed envelope fields"))?;
        let Some(kind) = fields.kind else {
            return Err(Error::transport("frame is missing 'type'"));
        };
        Ok(Self {
            kind,
            session_id: fields.session_id,
            run_id: fields.run_id,
            inference_id: fields.inference_id,
            profile: fields.profile,
            id: fields.id,
            in_reply_to: fields.in_reply_to,
            sequence: fields.sequence,
            raw,
        })
    }

    /// The `payload` object, falling back to the frame itself when the envelope
    /// carries its fields at the top level. Mirrors the TypeScript SDK's
    /// `readPayloadOrFrame`.
    pub fn payload(&self) -> &Value {
        match self.raw.get("payload") {
            Some(payload) if payload.is_object() => payload,
            _ => &self.raw,
        }
    }

    /// Reads a string field from the payload.
    pub(crate) fn payload_str(&self, key: &str) -> Option<&str> {
        self.payload().get(key).and_then(Value::as_str)
    }

    /// Reads a string field from the payload, ignoring empty strings.
    pub(crate) fn payload_non_empty(&self, key: &str) -> Option<String> {
        self.payload_str(key)
            .filter(|value| !value.is_empty())
            .map(str::to_owned)
    }
}

/// Reads a string field, ignoring non-strings and empty strings.
pub(crate) fn opt_string(value: &Value, key: &str) -> Option<String> {
    value
        .get(key)
        .and_then(Value::as_str)
        .filter(|text| !text.is_empty())
        .map(str::to_owned)
}

/// Reads the first present string field from `keys`.
pub(crate) fn opt_string_any(value: &Value, keys: &[&str]) -> Option<String> {
    keys.iter().find_map(|key| opt_string(value, key))
}

/// Reads a string field, defaulting to the empty string.
pub(crate) fn str_or_empty(value: &Value, key: &str) -> String {
    value
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

#[cfg(test)]
mod tests {

    use super::*;

    #[test]
    fn parses_routing_fields() {
        let frame = Frame::parse(
            r#"{"type":"auth.login.event","session_id":"S","id":"M","sequence":3,"in_reply_to":"R","payload":{"flow_id":"R"}}"#,
        )
        .expect("frame parses");
        assert_eq!(frame.kind, "auth.login.event");
        assert_eq!(frame.session_id.as_deref(), Some("S"));
        assert_eq!(frame.id.as_deref(), Some("M"));
        assert_eq!(frame.in_reply_to.as_deref(), Some("R"));
        assert_eq!(frame.sequence, Some(3));
        assert_eq!(frame.payload_str("flow_id"), Some("R"));
    }

    #[test]
    fn payload_falls_back_to_the_frame_when_absent() {
        let frame = Frame::parse(r#"{"type":"x","protocol_version":"0.1"}"#).expect("parses");
        assert_eq!(frame.payload_str("protocol_version"), Some("0.1"));
    }

    #[test]
    fn payload_falls_back_when_payload_is_not_an_object() {
        let frame = Frame::parse(r#"{"type":"x","payload":42,"delta":"hi"}"#).expect("parses");
        assert_eq!(frame.payload_str("delta"), Some("hi"));
    }

    #[test]
    fn rejects_non_json_and_shapeless_lines() {
        assert!(Frame::parse("not json").is_err());
        assert!(Frame::parse("[1,2,3]").is_err());
        assert!(Frame::parse(r#"{"session_id":"S"}"#).is_err());
    }

    #[test]
    fn malformed_frames_do_not_expose_raw_contents() {
        let bad_json = Frame::parse(r#"{"answer":"secret-code","#).unwrap_err();
        assert_eq!(bad_json.message(), "invalid JSON frame");
        let bad_field =
            Frame::parse(r#"{"type":"ack","session_id":{"answer":"secret-code"}}"#).unwrap_err();
        assert_eq!(bad_field.message(), "malformed envelope fields");
    }

    #[test]
    fn rejects_wrongly_typed_routing_fields() {
        assert!(Frame::parse(r#"{"type":"ack","sequence":"three"}"#).is_err());
        assert!(Frame::parse(r#"{"type":"ack","session_id":7}"#).is_err());
    }
}
