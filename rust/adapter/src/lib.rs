mod answer;
mod contract;
mod error;
pub mod gojson;
pub mod journal;
pub mod memory;
mod prompt;
mod quote_table;
mod refuse;
pub mod subscriber;
#[cfg(feature = "testkit")]
pub mod testkit;

use oap_protocol::{
    CompactionPolicy, Participant, ReasoningLevel, SessionId, ToolDefinition, ToolSourceAttachment,
};
use serde_json::Value;

pub use answer::{index_input_answers, validate_input_answer};
pub use contract::*;
pub use error::*;
pub use prompt::{message_text, run_to_terminal, RunError, RunErrorKind, RunOutcome};
pub use refuse::*;

#[derive(Clone, Debug, Default)]
pub struct OpenRequest {
    pub session_id: SessionId,
    pub participant: Participant,
    pub metadata: serde_json::Map<String, Value>,
    pub tool_sources: Vec<ToolSourceAttachment>,
    pub tools: Vec<ToolDefinition>,
    pub allow_degraded_features: Vec<String>,
    pub reopen: bool,
    pub native_session_id: String,
    pub reasoning_level: ReasoningLevel,
    pub compaction_policy: Option<CompactionPolicy>,
    pub adopted: bool,
}

impl OpenRequest {
    pub fn allows_degraded(&self, key: &str) -> bool {
        self.allow_degraded_features
            .iter()
            .any(|allowed| allowed == key)
    }
}
