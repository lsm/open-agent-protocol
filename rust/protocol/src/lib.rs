mod codec;
mod envelope;
mod envelope_type;
mod error;
mod ids;
mod number;

pub use codec::{decode, decode_array, decode_jsonl, encode, Format};
pub use envelope::Envelope;
pub use envelope_type::EnvelopeType;
pub use error::Error;
pub use ids::{
    EndpointId, EnvelopeId, InteractionId, MessageId, ParticipantId, RunId, SessionId,
    SubmissionId, ToolCallId,
};

pub const PROTOCOL: &str = "open-agent-protocol";
pub const VERSION: &str = "0.1";
pub const PROFILE: &str = "open-agent-protocol.agent-control-core";
