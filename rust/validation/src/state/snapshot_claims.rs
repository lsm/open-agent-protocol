use std::collections::HashSet;

use oap_protocol::*;

use super::runs::Run;
use super::{os, State};
use crate::codes;

pub(super) enum Kind {
    Pending(HashSet<String>),
    Queued,
    Reservation(bool),
    Settled,
}

pub(super) struct Claim {
    kind: Kind,
    run: String,
    sequence: Option<u64>,
    at: (usize, usize),
    envelope: Envelope,
}

fn pending_matches(run: &Run, sequence: Option<u64>, listed: &HashSet<String>) -> bool {
    let expected: HashSet<_> = run
        .interactions
        .iter()
        .filter(|(_, x)| match sequence {
            Some(sequence) => {
                x.opened_at <= sequence
                    && (!x.resolved || x.resolved_at.is_some_and(|at| at > sequence))
            }
            None => !x.resolved,
        })
        .map(|(id, _)| id.clone())
        .collect();
    let listed: HashSet<_> = listed
        .iter()
        .filter(|id| !run.prior_unknown || run.interactions.contains_key(*id))
        .cloned()
        .collect();
    expected == listed
}

impl State {
    pub(super) fn check_snapshot_claims(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &SessionState,
    ) {
        let mut claims = Vec::new();
        let mut found = Vec::new();
        let mut settled = HashSet::new();
        if let Some(capture) = &p.as_of {
            for entry in &capture.settled {
                if !settled.insert(entry.run_id.as_str()) {
                    continue;
                }
                match self
                    .runs
                    .get(entry.run_id.as_str())
                    .filter(|run| run.session == p.session_id.as_str())
                {
                    None => found.push((
                        "/payload/as_of/settled".to_owned(),
                        "a settled claim must name a run admitted on this session",
                    )),
                    Some(run) if !run.terminal => claims.push((
                        Kind::Settled,
                        entry.run_id.as_str().to_owned(),
                        Some(entry.sequence),
                    )),
                    _ => {}
                }
            }
        }
        let window = self
            .requests
            .get(&os(&e.in_reply_to))
            .map_or(at.0, |r| r.index);
        let mut seen = HashSet::new();
        for (index, entry) in p.active_runs.iter().enumerate() {
            let id = entry.run_id.as_str();
            if !seen.insert(id) || settled.contains(id) {
                continue;
            }
            let Some(run) = self
                .runs
                .get(id)
                .filter(|run| run.session == p.session_id.as_str())
            else {
                continue;
            };
            if run.terminal_at.is_some_and(|terminal| terminal < window) {
                continue;
            }
            let pointer = format!("/payload/active_runs/{index}");
            let listed: HashSet<_> = entry
                .pending_interactions
                .iter()
                .map(|id| id.as_str().to_owned())
                .collect();
            if entry.as_of_sequence.is_none() && !listed.is_empty() {
                found.push((
                    format!("{pointer}/as_of_sequence"),
                    "an entry naming pending interactions must state its capture position",
                ));
            } else if entry
                .as_of_sequence
                .is_some_and(|sequence| sequence >= run.next)
            {
                claims.push((Kind::Pending(listed), id.to_owned(), entry.as_of_sequence));
            } else if !pending_matches(run, entry.as_of_sequence, &listed) {
                found.push((
                    format!("{pointer}/pending_interactions"),
                    "the pending interactions must match the run at the stated capture position",
                ));
            }
            if run.started {
                continue;
            }
            if entry.status == RunStatus::QUEUED {
                if entry.as_of_sequence.is_some() {
                    claims.push((Kind::Queued, id.to_owned(), entry.as_of_sequence));
                }
            } else if !run.admitted_queued {
                continue;
            } else if entry.status == RunStatus::CANCELLING && entry.as_of_sequence.is_some() {
                claims.push((
                    Kind::Reservation(entry.queue_position.is_some()),
                    id.to_owned(),
                    entry.as_of_sequence,
                ));
            } else if entry.status != RunStatus::CANCELLING
                && !matches!(entry.status.as_str(), "completed" | "failed" | "cancelled")
            {
                claims.push((Kind::Reservation(false), id.to_owned(), None));
            }
        }
        self.snapshot_claims
            .extend(claims.into_iter().map(|(kind, run, sequence)| Claim {
                kind,
                run,
                sequence,
                at,
                envelope: e.clone(),
            }));
        for (pointer, message) in found {
            self.add(codes::SESSION_STATE_MISMATCH, at, e, &pointer, message);
        }
    }

    pub(super) fn reconcile_snapshot_claims(&mut self, e: &Envelope, run_id: &str) {
        let mut retained = Vec::new();
        for claim in std::mem::take(&mut self.snapshot_claims) {
            let Some(run) = self.runs.get(&claim.run).filter(|_| claim.run == run_id) else {
                retained.push(claim);
                continue;
            };
            let finding = match &claim.kind {
                Kind::Settled => {
                    let terminal = matches!(
                        e.envelope_type.as_str(),
                        "run.completed" | "run.failed" | "run.cancelled"
                    );
                    Some((
                        "/payload/as_of/settled",
                        (!terminal || e.sequence != claim.sequence)
                            .then_some("the next run event must be the claimed terminal"),
                    ))
                }
                Kind::Pending(listed)
                    if e.sequence
                        .zip(claim.sequence)
                        .is_some_and(|(event, capture)| event >= capture) =>
                {
                    let mismatch = !pending_matches(run, claim.sequence, listed);
                    Some(("/payload/active_runs", mismatch.then_some("the pending interactions must match the run at the stated capture position")))
                }
                Kind::Queued if run.started => {
                    let mismatch = claim
                        .sequence
                        .zip(run.start_sequence)
                        .is_some_and(|(capture, start)| capture >= start);
                    Some((
                        "/payload/active_runs",
                        mismatch.then_some("a queued capture must precede the run's start"),
                    ))
                }
                Kind::Reservation(held) if run.started => {
                    let mismatch = claim
                        .sequence
                        .zip(run.start_sequence)
                        .is_some_and(|(capture, start)| *held != (capture < start));
                    Some((
                        "/payload/active_runs",
                        mismatch.then_some(
                            "the capture's queue position must agree with the run's start",
                        ),
                    ))
                }
                _ => None,
            };
            match finding {
                Some((pointer, Some(message))) => self.add(
                    codes::SESSION_STATE_MISMATCH,
                    claim.at,
                    &claim.envelope,
                    pointer,
                    message,
                ),
                Some((_, None)) => {}
                None => retained.push(claim),
            }
        }
        self.snapshot_claims = retained;
    }

    pub(super) fn close_snapshot_claims(&mut self) {
        for claim in std::mem::take(&mut self.snapshot_claims) {
            let finding = match claim.kind {
                Kind::Settled => Some((
                    "/payload/as_of/settled",
                    "the claimed terminal never arrived",
                )),
                Kind::Pending(_) => Some((
                    "/payload/active_runs",
                    "the run never reached the entry's stated capture position",
                )),
                Kind::Reservation(false) => Some((
                    "/payload/active_runs",
                    "a reservation reported as executing never started",
                )),
                _ => None,
            };
            if let Some((pointer, message)) = finding {
                self.add(
                    codes::SESSION_STATE_MISMATCH,
                    claim.at,
                    &claim.envelope,
                    pointer,
                    message,
                );
            }
        }
    }
}
