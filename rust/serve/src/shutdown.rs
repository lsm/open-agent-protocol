use oap_protocol::{RunStatus, SessionStatus, ShutdownMode, ShutdownRun};
use serde_json::value::RawValue;
use serde_json::Value;

use crate::Hub;

#[derive(Clone, Copy, Debug, PartialEq, Eq, thiserror::Error)]
#[error("request is an object whose only member is mode, graceful or force")]
pub struct ShutdownRefused;

pub fn asked_shutdown(raw: Option<&RawValue>) -> Result<Option<ShutdownMode>, ShutdownRefused> {
    let Some(raw) = raw.filter(|raw| raw.get().trim() != "null") else {
        return Ok(None);
    };
    let asked: serde_json::Map<String, Value> =
        serde_json::from_str(raw.get()).map_err(|_| ShutdownRefused)?;
    if asked.keys().any(|key| key != "mode") {
        return Err(ShutdownRefused);
    }
    match asked.get("mode") {
        None | Some(Value::Null) => Ok(None),
        Some(Value::String(mode)) => named(mode),
        Some(_) => Err(ShutdownRefused),
    }
}

fn named(mode: &str) -> Result<Option<ShutdownMode>, ShutdownRefused> {
    [ShutdownMode::GRACEFUL, ShutdownMode::FORCE]
        .into_iter()
        .find(|known| known.as_str() == mode)
        .map(Some)
        .ok_or(ShutdownRefused)
}

impl Hub {
    pub async fn nonterminal_runs(&self) -> Vec<ShutdownRun> {
        let mut runs = Vec::new();
        for status in self.session_statuses().await {
            for run in &status.active_runs {
                runs.push(ShutdownRun {
                    session_id: status.session_id.clone(),
                    run_id: run.run_id.clone(),
                    status: run.status.clone(),
                    pending_interactions: run.pending_interactions.clone(),
                });
            }
            let Some(active) = status
                .active_run_id
                .filter(|_| status.active_runs.is_empty())
            else {
                continue;
            };
            let state = if status.status == SessionStatus::QUEUED {
                RunStatus::QUEUED
            } else if status.status == SessionStatus::WAITING_FOR_INPUT {
                RunStatus::WAITING_FOR_INPUT
            } else {
                RunStatus::RUNNING
            };
            runs.push(ShutdownRun {
                session_id: status.session_id,
                run_id: active,
                status: state,
                ..ShutdownRun::default()
            });
        }
        runs
    }

    pub async fn cancel_queued(&self, run: &ShutdownRun) -> bool {
        match self.session(&run.session_id) {
            Ok(entry) => entry.cancel(run.run_id.clone()).await.is_ok(),
            Err(_) => false,
        }
    }
}
