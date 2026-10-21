use std::collections::HashSet;

use oap_protocol::*;

use super::{os, payload, State};
use crate::codes;

type Finding = (String, &'static str);

fn terminal(status: &RunStatus) -> bool {
    matches!(status.as_str(), "completed" | "failed" | "cancelled")
}

fn started_entry<'a>(
    entry: &'a ActiveRun,
    pointer: &str,
    reservation: bool,
    started: &mut Option<&'a ActiveRun>,
    found: &mut Vec<Finding>,
) {
    if reservation {
        found.push((
            format!("{pointer}/status"),
            "a run cannot execute behind an earlier reservation",
        ));
    }
    if started.is_some() {
        found.push((
            format!("{pointer}/status"),
            "a listing cannot describe two runs executing at once",
        ));
    } else {
        *started = Some(entry);
    }
}

impl State {
    fn entry_leads_admission(&self, session: &str, entry: &ActiveRun) -> bool {
        !entry.admitted_submit_requests.is_empty()
            && entry.admitted_submit_requests.iter().all(|id| {
                self.requests.get(id.as_str()).is_some_and(|request| {
                    !request.responded
                        && request.session == session
                        && (request.envelope_type == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST
                            || (request.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST
                                && payload::<SessionOpenRequest>(&request.envelope)
                                    .message
                                    .is_some()))
                })
            })
    }

    pub(super) fn check_snapshot_listing(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &SessionState,
    ) {
        let mut found = Vec::new();
        let window = self
            .requests
            .get(&os(&e.in_reply_to))
            .map_or(at.0, |r| r.index);
        let mut settled = HashSet::new();
        let mut anchored = HashSet::new();
        if let Some(capture) = &p.as_of {
            anchored.extend(
                capture
                    .admitted_submit_requests
                    .iter()
                    .map(|id| id.as_str()),
            );
            for (index, entry) in capture.settled.iter().enumerate() {
                if !settled.insert(entry.run_id.as_str()) {
                    found.push((
                        format!("/payload/as_of/settled/{index}/run_id"),
                        "a snapshot cannot claim one run settled twice",
                    ));
                }
            }
        }
        let required: Vec<_> = self
            .order
            .get(p.session_id.as_str())
            .into_iter()
            .flatten()
            .filter_map(|id| self.runs.get(id).map(|run| (id.as_str(), run)))
            .filter(|(id, run)| {
                !run.terminal
                    && !settled.contains(id)
                    && (p.as_of.is_none()
                        || run.admitted_at <= window
                        || anchored.contains(run.submit_request.as_str()))
            })
            .collect();
        let has_listing = e
            .payload
            .as_ref()
            .and_then(|raw| serde_json::from_str::<serde_json::Value>(raw.get()).ok())
            .is_some_and(|value| value.get("active_runs").is_some());
        if !has_listing {
            let reason = if required.len() > 1 {
                Some("an overlapping session must list its nonterminal runs")
            } else if required
                .iter()
                .any(|(_, run)| run.admitted_queued && !run.started)
            {
                Some("a session holding a reservation must carry active_runs")
            } else if self.queue_offered()
                && required
                    .iter()
                    .any(|(_, run)| run.interactions.values().any(|x| x.unresolved()))
            {
                Some("an endpoint advertising the queue must list a run blocked on an interaction")
            } else {
                None
            };
            if let Some(reason) = reason {
                found.push(("/payload/active_runs".to_owned(), reason));
            }
        } else {
            let mut listed = HashSet::new();
            let mut started = None;
            let mut reservation_seen = false;
            let mut unresolved = false;
            let mut last_order = None;
            let mut position = 0;
            let mut leads = false;
            for (index, entry) in p.active_runs.iter().enumerate() {
                let pointer = format!("/payload/active_runs/{index}");
                if !listed.insert(entry.run_id.as_str()) {
                    found.push((
                        format!("{pointer}/run_id"),
                        "active_runs lists one run twice",
                    ));
                    continue;
                }
                if settled.contains(entry.run_id.as_str()) {
                    found.push((
                        format!("{pointer}/run_id"),
                        "a snapshot cannot list a run it claims to have settled",
                    ));
                    continue;
                }
                let run = self
                    .runs
                    .get(entry.run_id.as_str())
                    .filter(|run| run.session == p.session_id.as_str());
                let Some(run) = run else {
                    if self.entry_leads_admission(p.session_id.as_str(), entry) {
                        leads = true;
                        if terminal(&entry.status) {
                            found.push((
                                format!("{pointer}/status"),
                                "active_runs cannot carry a terminal status",
                            ));
                        } else if matches!(entry.status.as_str(), "queued" | "cancelling") {
                            unresolved = true;
                        } else {
                            started_entry(
                                entry,
                                &pointer,
                                reservation_seen,
                                &mut started,
                                &mut found,
                            );
                            if entry.queue_position.is_some() {
                                found.push((
                                    format!("{pointer}/queue_position"),
                                    "an executing run holds no queue position",
                                ));
                            }
                        }
                    } else {
                        found.push((format!("{pointer}/run_id"), "active_runs must name a run admitted on its session or anchor a pending admission"));
                    }
                    continue;
                };
                if run.terminal_at.is_some_and(|terminal| terminal < window) {
                    found.push((
                        format!("{pointer}/run_id"),
                        "a listing cannot retain a run that settled before the read was requested",
                    ));
                    continue;
                }
                if entry
                    .as_of_sequence
                    .zip(run.terminal_sequence)
                    .is_some_and(|(capture, terminal)| capture >= terminal)
                {
                    found.push((
                        format!("{pointer}/as_of_sequence"),
                        "a listing cannot retain a run at or past its terminal position",
                    ));
                }
                if last_order.is_some_and(|previous| run.order <= previous) || leads {
                    found.push((
                        format!("{pointer}/run_id"),
                        "active_runs must follow admission order",
                    ));
                }
                last_order = Some(run.order);
                let cancelling_reservation = entry.status == RunStatus::CANCELLING
                    && if run.started {
                        entry
                            .as_of_sequence
                            .zip(run.start_sequence)
                            .is_some_and(|(capture, start)| capture < start)
                    } else {
                        entry.as_of_sequence.is_none() || entry.queue_position.is_some()
                    };
                let reservation = run.admitted_queued
                    && (entry.status == RunStatus::QUEUED || cancelling_reservation);
                if terminal(&entry.status) {
                    found.push((
                        format!("{pointer}/status"),
                        "active_runs cannot carry a terminal status",
                    ));
                } else if reservation {
                    position += 1;
                    if entry.queue_position != Some(position) {
                        found.push((format!("{pointer}/queue_position"), "a reservation must carry its 1-based position among the listed reservations"));
                    }
                    reservation_seen = true;
                } else {
                    if entry.queue_position.is_some() {
                        found.push((
                            format!("{pointer}/queue_position"),
                            "an executing run holds no queue position",
                        ));
                    }
                    started_entry(entry, &pointer, reservation_seen, &mut started, &mut found);
                }
                if entry.status == RunStatus::QUEUED
                    && run.started
                    && !entry
                        .as_of_sequence
                        .zip(run.start_sequence)
                        .is_some_and(|(capture, start)| capture < start)
                {
                    found.push((
                        format!("{pointer}/status"),
                        "a run cannot be listed queued at or after its observed start",
                    ));
                }
            }
            for (id, _) in required {
                if !listed.contains(id) {
                    found.push((
                        "/payload/active_runs".to_owned(),
                        "a snapshot must list each nonterminal run its capture had to reflect",
                    ));
                }
            }
            if let Some(entry) = started {
                if os(&p.active_run_id) != entry.run_id.as_str() {
                    found.push((
                        "/payload/active_run_id".to_owned(),
                        "active_run_id must name the executing run its listing describes",
                    ));
                }
                let waiting = entry.status == RunStatus::WAITING_FOR_INPUT
                    || !entry.pending_interactions.is_empty();
                let message = match p.status.as_str() {
                    "waiting_for_input" if !waiting => {
                        Some("the session reports a wait its executing entry does not carry")
                    }
                    "running" if waiting => {
                        Some("the session denies the wait its executing entry reports")
                    }
                    "waiting_for_input" | "running" => None,
                    _ => Some(
                        "a listing with an executing run must report running or waiting_for_input",
                    ),
                };
                if let Some(message) = message {
                    found.push(("/payload/status".to_owned(), message));
                }
            } else if !unresolved {
                if !p.active_runs.is_empty() && !os(&p.active_run_id).is_empty() {
                    found.push((
                        "/payload/active_run_id".to_owned(),
                        "a listing holding only reservations names no active run",
                    ));
                }
                if reservation_seen && p.status != SessionStatus::QUEUED {
                    found.push((
                        "/payload/status".to_owned(),
                        "a session whose listing holds only reservations must report queued",
                    ));
                }
            }
        }
        for (pointer, message) in found {
            self.add(codes::SESSION_STATE_MISMATCH, at, e, &pointer, message);
        }
    }
}
