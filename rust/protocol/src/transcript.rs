use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::serde_util::{is_false, is_zero_i64};
use crate::vocabulary::vocabulary;
use crate::{CompactionId, Message, RunId, SessionId, TranscriptEntryId};

pub const FEATURE_TRANSCRIPT_LOAD: &str = "transcript.load";
pub const TRANSCRIPT_LOAD_DEFAULT_LIMIT: i64 = 100;
pub const TRANSCRIPT_LOAD_MAX_LIMIT: i64 = 500;
pub const TRANSCRIPT_PAGE_BYTES: usize = 512 << 10;

vocabulary!(TranscriptEntryKind {
    MESSAGE = "message",
    COMPACTION = "compaction",
});

vocabulary!(TranscriptGapReason {
    PRUNED = "pruned",
    REWRITTEN = "rewritten",
});

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct TranscriptLoadRequest {
    pub session_id: SessionId,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub cursor: String,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub limit: i64,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub allow_degraded_features: Vec<String>,
}

impl TranscriptLoadRequest {
    pub fn allows_degraded(&self, key: &str) -> bool {
        self.allow_degraded_features
            .iter()
            .any(|allowed| allowed == key)
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct TranscriptCompactionEntry {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub compaction_id: Option<CompactionId>,
    pub summary: String,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct TranscriptEntry {
    pub entry_id: TranscriptEntryId,
    pub kind: TranscriptEntryKind,
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub at_ms: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub run_id: Option<RunId>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub message: Option<Message>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub compaction: Option<TranscriptCompactionEntry>,
    #[serde(skip_serializing_if = "is_false")]
    pub truncated: bool,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub extensions: BTreeMap<String, Box<RawValue>>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct TranscriptGap {
    pub reason: TranscriptGapReason,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub resumed_at: Option<TranscriptEntryId>,
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct TranscriptLoadResponse {
    pub session_id: SessionId,
    pub entries: Vec<TranscriptEntry>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub next_cursor: String,
    pub complete: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub gap: Option<TranscriptGap>,
}
