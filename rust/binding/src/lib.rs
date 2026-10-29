use std::cmp::Reverse;
use std::collections::HashMap;
use std::sync::Mutex;

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use oap_protocol::CompactionPolicy;
use serde::{Deserialize, Serialize};

mod file;
mod runs;

pub use file::{frame, FileStore};
pub use runs::{open_runs, MemoryRuns, OpenRun, RunAction, RunRecord, Runs, RunsFile};

pub const DEFAULT_LIMIT: usize = 50;
pub const MAX_LIMIT: usize = 100;

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("binding: a record was not written whole: {0}")]
    Torn(String),
    #[error("binding: a cursor this history did not issue")]
    InvalidCursor,
    #[error("binding: limit must be from 1 to {MAX_LIMIT}")]
    InvalidLimit,
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Action {
    Opened,
    Reopened,
    Closed,
    Refused,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Record {
    pub session_id: String,
    pub adapter: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub harness_version: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub native_session_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub home: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub directory: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub reasoning_level: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub compaction_policy: Option<CompactionPolicy>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub tool_source_ids: Vec<String>,
    #[serde(default, skip_serializing_if = "std::ops::Not::not")]
    pub adopted: bool,
}

impl Record {
    pub fn from_open(
        session_id: &str,
        adapter: &str,
        harness_version: &str,
        model: &str,
        home: &str,
        directory: &str,
        tool_source_ids: &[String],
    ) -> Self {
        Self {
            session_id: session_id.into(),
            adapter: adapter.into(),
            harness_version: harness_version.into(),
            home: home.into(),
            directory: directory.into(),
            model: model.into(),
            tool_source_ids: tool_source_ids.to_vec(),
            ..Self::default()
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Entry {
    pub action: Action,
    pub time_ms: i64,
    pub record: Record,
}

impl Entry {
    pub fn opened(record: Record, time_ms: i64) -> Self {
        Self {
            action: Action::Opened,
            time_ms,
            record,
        }
    }

    pub fn reopened(mut record: Record, native_session_id: &str, time_ms: i64) -> Self {
        if !native_session_id.is_empty() {
            record.native_session_id = native_session_id.into();
        }
        Self {
            action: Action::Reopened,
            time_ms,
            record,
        }
    }

    pub fn closed(record: Record, time_ms: i64) -> Self {
        Self {
            action: Action::Closed,
            time_ms,
            record,
        }
    }

    pub fn live(&self) -> bool {
        matches!(self.action, Action::Opened | Action::Reopened)
    }
}

pub fn state(history: &[Entry]) -> Option<&Entry> {
    history
        .iter()
        .rev()
        .find(|entry| entry.action != Action::Refused)
}

pub fn sessions(entries: &[Entry]) -> Vec<Entry> {
    let mut at: HashMap<&str, usize> = HashMap::new();
    let mut states: Vec<Entry> = Vec::new();
    for entry in entries
        .iter()
        .filter(|entry| entry.action != Action::Refused)
    {
        match at.get(entry.record.session_id.as_str()) {
            Some(&slot) => states[slot] = entry.clone(),
            None => {
                at.insert(&entry.record.session_id, states.len());
                states.push(entry.clone());
            }
        }
    }
    states
}

pub trait Store: Send + Sync {
    fn append(&self, entry: Entry) -> Result<(), Error>;
    fn history(&self, session_id: &str) -> Result<Vec<Entry>, Error>;
    fn sessions(&self) -> Result<Vec<Entry>, Error>;

    fn latest(&self, session_id: &str) -> Result<Option<Entry>, Error> {
        Ok(self.history(session_id)?.pop())
    }
}

#[derive(Debug, Default)]
pub struct MemoryStore {
    entries: Mutex<Vec<Entry>>,
}

impl MemoryStore {
    fn held(&self) -> std::sync::MutexGuard<'_, Vec<Entry>> {
        self.entries
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }
}

impl Store for MemoryStore {
    fn append(&self, entry: Entry) -> Result<(), Error> {
        self.held().push(entry);
        Ok(())
    }

    fn history(&self, session_id: &str) -> Result<Vec<Entry>, Error> {
        Ok(self
            .held()
            .iter()
            .filter(|entry| entry.record.session_id == session_id)
            .cloned()
            .collect())
    }

    fn sessions(&self) -> Result<Vec<Entry>, Error> {
        Ok(sessions(&self.held()))
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Page {
    pub entries: Vec<Entry>,
    pub next_cursor: String,
}

pub fn list(sessions: &[Entry], cursor: &str, limit: i64) -> Result<Page, Error> {
    let limit = match limit {
        0 => DEFAULT_LIMIT,
        n if (1..=MAX_LIMIT as i64).contains(&n) => n as usize,
        _ => return Err(Error::InvalidLimit),
    };
    let mut ordered = sessions.to_vec();
    ordered.sort_by(|a, b| order_key(a).cmp(&order_key(b)));
    let start = if cursor.is_empty() {
        0
    } else {
        let (time_ms, session_id) = decode_cursor(cursor)?;
        ordered.partition_point(|entry| order_key(entry) <= (Reverse(time_ms), session_id.as_str()))
    };
    let end = (start + limit).min(ordered.len());
    let next_cursor = if end < ordered.len() {
        encode_cursor(&ordered[end - 1])
    } else {
        String::new()
    };
    Ok(Page {
        entries: ordered.drain(start..end).collect(),
        next_cursor,
    })
}

fn order_key(entry: &Entry) -> (Reverse<i64>, &str) {
    (Reverse(entry.time_ms), entry.record.session_id.as_str())
}

fn encode_cursor(entry: &Entry) -> String {
    URL_SAFE_NO_PAD.encode(format!("{}:{}", entry.time_ms, entry.record.session_id))
}

fn decode_cursor(cursor: &str) -> Result<(i64, String), Error> {
    let raw = URL_SAFE_NO_PAD
        .decode(cursor)
        .map_err(|_| Error::InvalidCursor)?;
    let text = String::from_utf8(raw).map_err(|_| Error::InvalidCursor)?;
    let (time, session_id) = text.split_once(':').ok_or(Error::InvalidCursor)?;
    if session_id.is_empty() || time.starts_with('+') {
        return Err(Error::InvalidCursor);
    }
    let time_ms = time.parse().map_err(|_| Error::InvalidCursor)?;
    Ok((time_ms, session_id.to_owned()))
}
