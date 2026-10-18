use crate::EnvelopeType;

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("decode envelope: {0}")]
    Envelope(#[source] serde_json::Error),
    #[error("decode envelope member {member}: {source}")]
    Member {
        member: &'static str,
        #[source]
        source: serde_json::Error,
    },
    #[error("decode payload: missing payload")]
    MissingPayload,
    #[error("decode {envelope_type} payload: {source}")]
    Payload {
        envelope_type: EnvelopeType,
        #[source]
        source: serde_json::Error,
    },
    #[error("marshal payload: {0}")]
    MarshalPayload(#[source] serde_json::Error),
    #[error("decode envelope array: {0}")]
    Array(#[source] serde_json::Error),
    #[error("decode JSONL line {line}: {source}")]
    Line {
        line: usize,
        #[source]
        source: Box<Error>,
    },
    #[error("JSON format requires exactly one envelope, got {0}")]
    NotOneEnvelope(usize),
    #[error("encode envelopes: {0}")]
    Encode(#[source] serde_json::Error),
    #[error("read or write envelopes: {0}")]
    Io(#[from] std::io::Error),
}
