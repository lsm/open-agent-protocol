mod codec;
mod content;
mod envelope;
mod envelope_type;
mod error;
mod feature;
mod ids;
mod interaction;
mod number;
mod serde_util;
mod vocabulary;

pub use codec::{decode, decode_array, decode_jsonl, encode, Format};
pub use content::*;
pub use envelope::Envelope;
pub use envelope_type::EnvelopeType;
pub use error::Error;
pub use feature::*;
pub use ids::{
    CompactionId, EndpointId, EnvelopeId, InteractionId, MessageId, ParticipantId, RunId,
    SessionId, SubmissionId, ToolCallId,
};
pub use interaction::*;

pub const PROTOCOL: &str = "open-agent-protocol";
pub const VERSION: &str = "0.1";
pub const PROFILE: &str = "open-agent-protocol.agent-control-core";
