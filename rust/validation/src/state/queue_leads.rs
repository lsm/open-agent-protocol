use oxap_protocol::*;
use std::collections::HashSet;

use super::snapshot_claims::pending_matches;
use super::{os, State};
use crate::codes;

pub(super) struct Group {
    pub(super) at: (usize, usize),
    pub(super) envelope: Envelope,
    pub(super) state: SessionState,
    pub(super) entries: Vec<(usize, i64)>,
    pub(super) executing: Option<ActiveRun>,
}

impl State {
    pub(super) fn close_queue_leads(&mut self) {
        for group in std::mem::take(&mut self.queue_leads) {
            let mut found = Vec::new();
            let mut reservations = 0;
            let mut unknown = 0;
            let mut previous_order = None;
            let mut executing = group.executing.as_ref();
            for (index, ahead) in group.entries {
                let entry = &group.state.active_runs[index];
                let pointer = format!("/payload/active_runs/{index}");
                let classify = matches!(entry.status.as_str(), "queued" | "cancelling");
                let Some(run) = self.runs.get(entry.run_id.as_str()).filter(|run| {
                    run.session == group.state.session_id.as_str()
                        && entry
                            .admitted_submit_requests
                            .iter()
                            .any(|id| id.as_str() == run.submit_request)
                }) else {
                    unknown += i64::from(classify);
                    continue;
                };
                if previous_order.is_some_and(|order| run.order <= order) {
                    found.push((
                        format!("{pointer}/run_id"),
                        "active_runs must follow admission order",
                    ));
                } else {
                    previous_order = Some(run.order);
                }
                let listed: HashSet<_> = entry
                    .pending_interactions
                    .iter()
                    .map(|id| id.as_str().to_owned())
                    .collect();
                if entry.as_of_sequence.is_none() && !listed.is_empty() {
                    found.push((
                        format!("{pointer}/as_of_sequence"),
                        "pending interactions require a capture position",
                    ));
                } else if entry.as_of_sequence.is_some_and(|seq| seq >= run.next) {
                    found.push((
                        "/payload/active_runs".to_owned(),
                        "the run never reached the claimed capture position",
                    ));
                } else if entry.as_of_sequence.is_some()
                    && !pending_matches(run, entry.as_of_sequence, &listed)
                {
                    found.push((
                        format!("{pointer}/pending_interactions"),
                        "pending interactions must match the capture position",
                    ));
                }
                if !classify {
                    if run.admitted_queued && !run.started {
                        found.push((
                            "/payload/active_runs".to_owned(),
                            "a reservation reported as executing never started",
                        ));
                    }
                    continue;
                }
                let reservation = run.admitted_queued
                    && (entry.status == RunStatus::QUEUED
                        || entry.as_of_sequence.is_none()
                        || entry.queue_position.is_some());
                if !run.admitted_queued && entry.status == RunStatus::QUEUED {
                    found.push((
                        format!("{pointer}/status"),
                        "a started admission cannot be listed queued",
                    ));
                }
                if run.admitted_queued && entry.status == RunStatus::CANCELLING {
                    let held = entry.queue_position.is_some();
                    if let Some(capture) = entry.as_of_sequence {
                        if run
                            .start_sequence
                            .is_some_and(|start| held != (capture < start))
                            || (!held && !run.started)
                        {
                            found.push((
                                "/payload/active_runs".to_owned(),
                                "the capture's queue position must agree with its promotion",
                            ));
                        }
                    }
                }
                if reservation {
                    let low = ahead + reservations + 1;
                    if !entry
                        .queue_position
                        .is_some_and(|position| position >= low && position <= low + unknown)
                    {
                        found.push((
                            format!("{pointer}/queue_position"),
                            "a reservation must occupy its place among the listed reservations",
                        ));
                    }
                    reservations += 1;
                } else {
                    if entry.queue_position.is_some() {
                        found.push((
                            format!("{pointer}/queue_position"),
                            "an executing run holds no queue position",
                        ));
                    }
                    if executing.is_some() {
                        found.push((
                            format!("{pointer}/status"),
                            "a listing cannot describe two runs executing at once",
                        ));
                    } else {
                        executing = Some(entry);
                    }
                }
            }
            if group.executing.is_none() {
                let expected = executing.map_or("", |entry| entry.run_id.as_str());
                if os(&group.state.active_run_id) != expected {
                    found.push((
                        "/payload/active_run_id".to_owned(),
                        "active_run_id must name the executing run",
                    ));
                }
                let valid = match executing {
                    Some(entry) => match group.state.status.as_str() {
                        "waiting_for_input" => {
                            entry.status == RunStatus::WAITING_FOR_INPUT
                                || !entry.pending_interactions.is_empty()
                        }
                        "running" => {
                            entry.status != RunStatus::WAITING_FOR_INPUT
                                && entry.pending_interactions.is_empty()
                        }
                        _ => false,
                    },
                    None => group.state.status == SessionStatus::QUEUED,
                };
                if !valid {
                    found.push((
                        "/payload/status".to_owned(),
                        "session status must reflect the resolved listing",
                    ));
                }
            }
            for (pointer, message) in found {
                self.add(
                    codes::SESSION_STATE_MISMATCH,
                    group.at,
                    &group.envelope,
                    &pointer,
                    message,
                );
            }
        }
    }
}
