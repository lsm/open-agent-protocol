use std::collections::HashSet;
use std::fs::OpenOptions;
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard, PoisonError};

use serde::{Deserialize, Serialize};

use crate::file::{frame, unframe};
use crate::Error;

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum RunAction {
    #[default]
    Admitted,
    Started,
    Settled,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct RunRecord {
    pub action: RunAction,
    pub time_ms: i64,
    pub session_id: String,
    pub run_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub request: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub replaces: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub root: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct OpenRun {
    pub run_id: String,
    pub admitted_ms: i64,
    pub started: bool,
    pub request: String,
    pub root: String,
}

pub trait Runs: Send + Sync {
    fn append_run(&self, record: RunRecord) -> Result<(), Error>;
    fn open_runs(&self, session_id: &str) -> Result<Vec<OpenRun>, Error>;
}

fn stamped(records: &[RunRecord], mut record: RunRecord) -> RunRecord {
    if record.action != RunAction::Admitted || record.replaces.is_empty() || !record.root.is_empty()
    {
        return record;
    }
    if let Some(run) = open_runs(records, &record.session_id)
        .into_iter()
        .find(|run| run.run_id == record.replaces)
    {
        record.root = run.root;
    }
    record
}

fn kept_after(records: &[RunRecord], record: RunRecord) -> Vec<RunRecord> {
    let mut all = records.to_vec();
    all.push(stamped(records, record));
    let mut seen = HashSet::new();
    let sessions: Vec<String> = all
        .iter()
        .filter(|held| seen.insert(held.session_id.clone()))
        .map(|held| held.session_id.clone())
        .collect();
    let mut kept = Vec::new();
    for session_id in &sessions {
        for open in open_runs(&all, session_id) {
            kept.extend(
                all.iter()
                    .filter(|held| {
                        held.action != RunAction::Settled
                            && &held.session_id == session_id
                            && held.run_id == open.run_id
                    })
                    .cloned(),
            );
        }
    }
    kept
}

pub fn open_runs(records: &[RunRecord], session_id: &str) -> Vec<OpenRun> {
    let mut runs: Vec<OpenRun> = Vec::new();
    let mut ended: HashSet<&str> = HashSet::new();
    let slot = |runs: &[OpenRun], run_id: &str| runs.iter().position(|run| run.run_id == run_id);
    for record in records {
        if record.session_id != session_id || ended.contains(record.run_id.as_str()) {
            continue;
        }
        if record.action == RunAction::Settled {
            ended.insert(&record.run_id);
            if let Some(at) = slot(&runs, &record.run_id) {
                runs.remove(at);
            }
            continue;
        }
        let redelivery = record.action == RunAction::Admitted && !record.replaces.is_empty();
        if redelivery {
            ended.insert(&record.replaces);
            if let Some(at) = slot(&runs, &record.replaces) {
                let root = if record.root.is_empty() {
                    runs[at].root.clone()
                } else {
                    record.root.clone()
                };
                runs[at] = OpenRun {
                    run_id: record.run_id.clone(),
                    admitted_ms: record.time_ms,
                    started: false,
                    request: record.request.clone(),
                    root,
                };
                continue;
            }
        }
        let at = slot(&runs, &record.run_id).unwrap_or_else(|| {
            runs.push(OpenRun {
                run_id: record.run_id.clone(),
                admitted_ms: record.time_ms,
                started: false,
                request: String::new(),
                root: if record.root.is_empty() {
                    record.run_id.clone()
                } else {
                    record.root.clone()
                },
            });
            runs.len() - 1
        });
        match record.action {
            RunAction::Admitted => runs[at].request = record.request.clone(),
            RunAction::Started => runs[at].started = true,
            RunAction::Settled => {}
        }
    }
    runs
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(PoisonError::into_inner)
}

#[derive(Debug, Default)]
pub struct MemoryRuns {
    records: Mutex<Vec<RunRecord>>,
}

impl Runs for MemoryRuns {
    fn append_run(&self, record: RunRecord) -> Result<(), Error> {
        let mut held = lock(&self.records);
        *held = kept_after(&held, record);
        Ok(())
    }

    fn open_runs(&self, session_id: &str) -> Result<Vec<OpenRun>, Error> {
        Ok(open_runs(&lock(&self.records), session_id))
    }
}

#[derive(Debug)]
pub struct RunsFile {
    path: PathBuf,
    held: Mutex<()>,
}

impl RunsFile {
    pub fn open(path: &Path) -> Result<Self, Error> {
        if path.as_os_str().is_empty() {
            return Err(Error::Torn("a run store needs a path".into()));
        }
        if let Some(dir) = path.parent().filter(|dir| !dir.as_os_str().is_empty()) {
            std::fs::DirBuilder::new()
                .recursive(true)
                .mode(0o700)
                .create(dir)?;
        }
        Ok(Self {
            path: path.to_owned(),
            held: Mutex::new(()),
        })
    }

    fn records(&self) -> Result<Vec<RunRecord>, Error> {
        let text = match std::fs::read(&self.path) {
            Ok(text) => text,
            Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
            Err(err) => return Err(err.into()),
        };
        let mut records = Vec::new();
        for line in text.split_inclusive(|byte| *byte == b'\n') {
            if !line.ends_with(b"\n") {
                break;
            }
            let Ok(record) = unframe(line).and_then(|payload| {
                serde_json::from_slice(payload).map_err(|err| Error::Torn(err.to_string()))
            }) else {
                break;
            };
            records.push(record);
        }
        Ok(records)
    }
}

impl Runs for RunsFile {
    fn append_run(&self, record: RunRecord) -> Result<(), Error> {
        let _held = lock(&self.held);
        let mut next = Vec::new();
        for kept in kept_after(&self.records()?, record) {
            let payload = serde_json::to_vec(&kept).map_err(|err| Error::Torn(err.to_string()))?;
            next.extend(frame(&payload));
        }
        let mut staging = self.path.clone().into_os_string();
        staging.push(".writing");
        let mut file = OpenOptions::new()
            .create(true)
            .write(true)
            .truncate(true)
            .mode(0o600)
            .open(&staging)?;
        file.write_all(&next)?;
        file.sync_all()?;
        drop(file);
        std::fs::rename(&staging, &self.path)?;
        Ok(())
    }

    fn open_runs(&self, session_id: &str) -> Result<Vec<OpenRun>, Error> {
        let _held = lock(&self.held);
        Ok(open_runs(&self.records()?, session_id))
    }
}
