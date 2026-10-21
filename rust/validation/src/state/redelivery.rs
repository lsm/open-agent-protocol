use std::collections::{HashMap, HashSet};

use oap_protocol::*;

use super::{os, payload, s, State};
use crate::codes;

#[derive(Default)]
pub(super) struct Redelivery {
    roots: HashMap<String, String>,
    heads: HashMap<String, String>,
    closed: HashSet<String>,
    held: Option<Vec<String>>,
}

impl State {
    pub(super) fn redelivery_held(&mut self, e: &Envelope) {
        let held = if e.envelope_type == EnvelopeType::ENDPOINT_SHUTDOWN_RESPONSE {
            let p: EndpointShutdownResponse = payload(e);
            if p.outcome == ShutdownOutcome::CHOOSE {
                return;
            }
            p.held
        } else if e.envelope_type == EnvelopeType::ENDPOINT_SHUTDOWN_STARTED {
            payload::<EndpointShutdownStarted>(e).held
        } else {
            return;
        };
        let mut groups: HashMap<String, Vec<String>> = HashMap::new();
        for entry in held {
            groups
                .entry(s(&entry.session_id))
                .or_default()
                .push(s(&entry.run_id));
        }
        for (session, ids) in groups {
            self.redeliveries.entry(session).or_default().held = Some(ids);
        }
    }

    pub(super) fn redelivery_response(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &SessionState,
    ) {
        let session = s(&p.session_id);
        let records = p
            .recovery
            .as_ref()
            .map(|r| r.redelivered_runs.as_slice())
            .unwrap_or_default();
        let mut history = self.redeliveries.remove(&session).unwrap_or_default();
        if !p.recovery.as_ref().is_some_and(|r| r.recovered) {
            if !records.is_empty() {
                self.add(
                    codes::SESSION_STATE_MISMATCH,
                    at,
                    e,
                    "/payload/recovery",
                    "redelivery requires a recovered session",
                );
            }
            self.redeliveries.insert(session, history);
            return;
        }
        let requested = self
            .requests
            .get(&os(&e.in_reply_to))
            .filter(|r| r.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST)
            .map(|r| payload::<SessionOpenRequest>(&r.envelope))
            .unwrap_or_default();
        let restarting = requested.reopen || !records.is_empty();
        let queued = self
            .order
            .get(&session)
            .into_iter()
            .flatten()
            .any(|id| self.runs.get(id).is_some_and(|r| !r.terminal && !r.started));
        if records.is_empty() && history.held.is_none() && !queued && !restarting {
            self.redeliveries.insert(session, history);
            return;
        }
        let held = history.held.clone().unwrap_or_else(|| {
            self.order
                .get(&session)
                .into_iter()
                .flatten()
                .filter(|id| {
                    self.runs
                        .get(*id)
                        .is_some_and(|run| !run.terminal && !run.started)
                })
                .cloned()
                .collect()
        });
        let mut bad = false;
        if history.held.is_some() || !held.is_empty() {
            if held.len() != records.len() {
                bad = true;
            }
            for (n, id) in held.iter().enumerate() {
                if records
                    .get(n)
                    .is_none_or(|entry| entry.previous_run_id.as_str() != id)
                {
                    bad = true;
                }
            }
        }
        let mut previous = HashSet::new();
        let mut roots = HashSet::new();
        let paired: HashSet<&str> = records
            .iter()
            .filter_map(|r| r.run_id.as_ref().map(|id| id.as_str()))
            .collect();
        let mut message_runs = 0;
        for entry in &p.active_runs {
            if restarting && !paired.contains(entry.run_id.as_str()) {
                if requested.message.is_some() && !self.runs.contains_key(entry.run_id.as_str()) {
                    if history.closed.contains(entry.run_id.as_str())
                        || history.heads.contains_key(entry.run_id.as_str())
                    {
                        bad = true;
                    }
                    message_runs += 1;
                    if message_runs > 1 {
                        bad = true;
                    }
                    history.roots.insert(s(&entry.run_id), s(&entry.run_id));
                    history.heads.insert(s(&entry.run_id), s(&entry.run_id));
                } else if entry.status == RunStatus::QUEUED {
                    bad = true;
                }
            }
        }
        let positions: HashMap<&str, usize> = p
            .active_runs
            .iter()
            .enumerate()
            .filter(|(_, entry)| entry.status == RunStatus::QUEUED)
            .map(|(n, entry)| (entry.run_id.as_str(), n + 1))
            .collect();
        let mut last = 0;
        let mut prior_order = None;
        for entry in records {
            let prev = entry.previous_run_id.as_str();
            let root = entry.root_run_id.as_str();
            let next = os(&entry.run_id);
            if !previous.insert(prev) || !roots.insert(root) || history.closed.contains(prev) {
                bad = true;
            }
            let mut expected = history.roots.get(prev).map(String::as_str);
            if history.heads.get(root).is_some_and(|head| head != prev) {
                bad = true;
            }
            if let Some(run) = self.runs.get(prev) {
                if prior_order.is_some_and(|order| run.order <= order) {
                    bad = true;
                }
                prior_order = Some(run.order);
                if run.session != session || run.started {
                    bad = true;
                }
                if expected.is_none() && !run.recovered {
                    expected = Some(prev);
                }
            }
            if expected.is_some_and(|expected| root != expected) {
                bad = true;
            }
            for active in &p.active_runs {
                if active.run_id.as_str() == prev || active.run_id.as_str() == root {
                    bad = true;
                }
            }
            if !entry.withdrawn {
                let position = positions.get(next.as_str()).copied().unwrap_or_default();
                if self.runs.contains_key(&next)
                    || history.closed.contains(&next)
                    || history.heads.contains_key(&next)
                    || position == 0
                    || position <= last
                {
                    bad = true;
                }
                last = position;
            }
        }
        for entry in records {
            if previous.contains(entry.root_run_id.as_str())
                && entry.root_run_id != entry.previous_run_id
            {
                bad = true;
            }
        }
        if bad {
            self.add(codes::SESSION_STATE_MISMATCH, at, e, "/payload/recovery/redelivered_runs", "redeliveries must pair held carriers in order, preserve their roots and name fresh queued runs");
        }
        for id in held {
            if let Some(run) = self.runs.get_mut(&id).filter(|run| !run.started) {
                run.terminal = true;
            }
        }
        for entry in records {
            history.closed.insert(s(&entry.previous_run_id));
            history
                .heads
                .insert(s(&entry.root_run_id), os(&entry.run_id));
            if !entry.withdrawn {
                history
                    .roots
                    .insert(os(&entry.run_id), s(&entry.root_run_id));
            }
        }
        history.held = None;
        self.redeliveries.insert(session, history);
    }
}
