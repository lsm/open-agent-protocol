use std::collections::HashSet;

use oap_protocol::*;

use super::interactions::Interaction;
use super::runs::Run;
use super::steer::SteerTrack;
use super::{os, payload, s, State};
use crate::codes;

const JSON_SAFE: u64 = (1 << 53) - 1;

fn cursor(text: &str) -> Option<u64> {
    if text.is_empty() || !text.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    text.parse().ok().filter(|cursor| *cursor <= JSON_SAFE)
}

pub(super) struct Recovery {
    session: String,
    opened: usize,
    run: String,
    gap: bool,
    cursor: Option<u64>,
    first_replay_seen: bool,
    state_checked: bool,
}

impl State {
    fn resume_sequence(&self, session: &str, run: &str, as_of: Option<u64>) -> u64 {
        if let Some(as_of) = as_of {
            return as_of + 1;
        }
        match self.recoveries.get(session) {
            Some(rec) if !rec.gap && (rec.run.is_empty() || rec.run == run) => {
                rec.cursor.map_or(1, |cursor| cursor + 1)
            }
            _ => 1,
        }
    }

    fn introduce_recovered_run(&mut self, at: (usize, usize), id: String, run: Run) {
        let run = Run {
            admitted: true,
            admitted_at: at.0,
            recovered: true,
            last: at,
            ..run
        };
        self.runs.insert(id.clone(), run);
        self.enter_order(&id);
    }

    pub(super) fn replay_cursor(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        let session = self
            .runs
            .get(run_id)
            .map(|run| run.session.clone())
            .unwrap_or_default();
        let Some(rec) = self.recoveries.get_mut(&session) else {
            return;
        };
        if rec.gap || rec.first_replay_seen || rec.run != run_id {
            return;
        }
        rec.first_replay_seen = true;
        let Some(cursor) = rec.cursor.filter(|cursor| e.sequence != Some(cursor + 1)) else {
            return;
        };
        let actual = e
            .sequence
            .map_or_else(|| "missing".to_owned(), |n| n.to_string());
        let expected = (cursor + 1).to_string();
        self.add_expected(
            codes::SEQUENCE_GAP,
            at,
            e,
            "/sequence",
            "retained replay must begin immediately after the declared cursor",
            &expected,
            &actual,
            &[],
        );
    }

    fn reopen_response(&mut self, at: (usize, usize), e: &Envelope, p: &SessionState) {
        let Some(request) = self
            .requests
            .get(&os(&e.in_reply_to))
            .filter(|request| request.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST)
            .map(|request| payload::<SessionOpenRequest>(&request.envelope))
            .filter(|request| request.reopen)
        else {
            return;
        };
        let recovered = p
            .recovery
            .as_ref()
            .is_some_and(|recovery| recovery.recovered);
        if !recovered {
            self.add(
                codes::SESSION_STATE_MISMATCH,
                at,
                e,
                "/payload/recovery/recovered",
                "a reopened session's state document declares that it was recovered",
            );
        }
        let active = os(&p.active_run_id);
        let restored_queue = recovered
            && active.is_empty()
            && p.active_runs
                .iter()
                .all(|entry| entry.status == RunStatus::QUEUED);
        if request.message.is_none()
            && (!active.is_empty() || !p.active_runs.is_empty())
            && !restored_queue
        {
            self.add(
                codes::SESSION_STATE_MISMATCH,
                at,
                e,
                "/payload/active_runs",
                "a reopen loads a session with no run under way unless it restores queued runs",
            );
        }
    }

    pub(super) fn open_response(&mut self, at: (usize, usize), e: &Envelope) {
        self.open_model(e);
        let p: SessionState = payload(e);
        self.redelivery_response(at, e, &p);
        self.reopen_response(at, e, &p);
        let session = s(&p.session_id);
        let pointer = "/payload/recovery/resume_cursor";
        if let Some(recovery) = p.recovery.as_ref() {
            if recovery.reason == "replay_gap" && recovery.resume_cursor.is_empty() {
                self.add(
                    codes::UNDECLARED_REPLAY_GAP,
                    at,
                    e,
                    pointer,
                    "replay gap lacks an explicit retained boundary cursor",
                );
            }
            let text = recovery.resume_cursor.as_str();
            let bounded = cursor(text);
            if !text.is_empty() && bounded.is_none() {
                self.add_expected(
                    codes::UNDECLARED_REPLAY_GAP,
                    at,
                    e,
                    pointer,
                    "recovery cursor must be an unsigned decimal sequence no greater than 2^53 - 1",
                    "an unsigned decimal integer no greater than 9007199254740991",
                    text,
                    &[],
                );
            }
        }
        if let Some(recovery) = p.recovery.as_ref().filter(|recovery| recovery.recovered) {
            let bounded = cursor(recovery.resume_cursor.as_str());
            let rec = Recovery {
                session: session.clone(),
                opened: at.0,
                run: os(&recovery.previous_run_id),
                gap: recovery.reason == "replay_gap",
                cursor: bounded,
                first_replay_seen: false,
                state_checked: false,
            };
            self.recoveries.insert(session.clone(), rec);
            let mut listed = HashSet::new();
            for entry in &p.active_runs {
                let id = s(&entry.run_id);
                listed.insert(id.clone());
                if self.runs.contains_key(&id) {
                    continue;
                }
                let holds_place = entry.as_of_sequence.is_none() || entry.queue_position.is_some();
                let queued = entry.status == RunStatus::QUEUED
                    || (entry.status == RunStatus::CANCELLING && holds_place);
                let next = self.resume_sequence(&session, &id, entry.as_of_sequence);
                let run = Run {
                    session: session.clone(),
                    started: !queued,
                    admitted_queued: queued,
                    next,
                    status: entry.status.clone(),
                    interactions: entry
                        .pending_interactions
                        .iter()
                        .map(|id| (s(id), Interaction::opaque()))
                        .collect(),
                    steers: entry
                        .pending_steers
                        .iter()
                        .map(|pending| {
                            (
                                pending.submission_id.as_str().to_owned(),
                                SteerTrack::recovered(pending, at),
                            )
                        })
                        .collect(),
                    ..Run::default()
                };
                self.introduce_recovered_run(at, id, run);
            }
            let active = os(&p.active_run_id);
            if !active.is_empty() && !listed.contains(&active) && !self.runs.contains_key(&active) {
                let next = self.resume_sequence(&session, &active, None);
                let run = Run {
                    session: session.clone(),
                    started: true,
                    next,
                    status: RunStatus::RUNNING,
                    prior_unknown: true,
                    ..Run::default()
                };
                self.introduce_recovered_run(at, active, run);
            }
        }
        self.compound_open(at, e, &p);
        self.check_snapshot_listing(at, e, &p);
        self.apply_state_document(&session, &p);
    }

    fn compound_open(&mut self, at: (usize, usize), e: &Envelope, p: &SessionState) {
        let reply = os(&e.in_reply_to);
        let Some(message) = self
            .requests
            .get(&reply)
            .filter(|request| request.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST)
            .and_then(|request| payload::<SessionOpenRequest>(&request.envelope).message)
        else {
            return;
        };
        if let Some(request) = self.requests.get_mut(&reply) {
            if request.session.is_empty() {
                request.session = s(&p.session_id);
            }
        }
        let fresh: Vec<&ActiveRun> = p
            .active_runs
            .iter()
            .filter(|entry| !self.runs.contains_key(entry.run_id.as_str()))
            .collect();
        let [entry] = fresh.as_slice() else { return };
        let queued = entry.status == RunStatus::QUEUED;
        let admitted = MessageSubmitResponse {
            session_id: p.session_id.clone(),
            accepted: true,
            submission_id: SubmissionId::from(reply.as_str()),
            requested_delivery: message.delivery,
            effective_delivery: if queued {
                EffectiveDeliveryMode::QUEUE
            } else {
                EffectiveDeliveryMode::START
            },
            admission: if queued {
                Admission::QUEUED
            } else {
                Admission::STARTED
            },
            run_id: Some(entry.run_id.clone()),
            status: entry.status.clone(),
            model_id: message
                .model_id
                .clone()
                .unwrap_or_else(|| p.current_model_id.clone()),
            ..MessageSubmitResponse::default()
        };
        self.admit_submission(at, e, admitted);
    }

    pub(super) fn state_document(&mut self, at: (usize, usize), e: &Envelope) {
        self.state_model(at, e);
        let p: SessionState = payload(e);
        let session = s(&p.session_id);
        let active = os(&p.active_run_id);
        let (introduce, undeclared) = match self.recoveries.get_mut(&session) {
            Some(rec) if !rec.state_checked => {
                rec.state_checked = true;
                (
                    !rec.gap && !active.is_empty(),
                    rec.gap && p.recovery.is_none(),
                )
            }
            _ => (false, false),
        };
        if undeclared {
            self.add(
                codes::UNDECLARED_REPLAY_GAP,
                at,
                e,
                "/payload/recovery",
                "authoritative state after a replay gap must retain recovery metadata",
            );
        }
        if introduce && !self.runs.contains_key(&active) {
            let next = self.resume_sequence(&session, &active, None);
            let run = Run {
                session: session.clone(),
                started: true,
                next,
                status: RunStatus::RUNNING,
                prior_unknown: true,
                ..Run::default()
            };
            self.introduce_recovered_run(at, active, run);
        }
        self.check_snapshot_listing(at, e, &p);
        self.apply_state_document(&session, &p);
    }

    fn apply_state_document(&mut self, session: &str, p: &SessionState) {
        let active = os(&p.active_run_id);
        if let Some(previous) = self.active.get(session) {
            if *previous != active && self.runs.get(previous).is_some_and(|run| !run.terminal) {
                return;
            }
        }
        if active.is_empty() {
            self.active.remove(session);
        } else {
            self.active.insert(session.to_owned(), active);
        }
    }

    pub(super) fn close_recoveries(&mut self) {
        let mut found = Vec::new();
        for rec in self
            .recoveries
            .values()
            .filter(|rec| rec.gap && !rec.state_checked)
        {
            let stub = Envelope {
                envelope_type: EnvelopeType::SESSION_STATE_RESPONSE,
                session_id: Some(SessionId::from(rec.session.as_str())),
                ..Envelope::default()
            };
            found.push(self.diagnostic(
                codes::UNDECLARED_REPLAY_GAP,
                (rec.opened, 0),
                &stub,
                "",
                "replay gap lacks authoritative session state",
            ));
        }
        self.diagnostics.extend(found);
    }

    pub(super) fn compact_response(&mut self, at: (usize, usize), e: &Envelope) {
        let p: SessionCompactResponse = payload(e);
        let reply = os(&e.in_reply_to);
        let Some(request) = self
            .requests
            .get(&reply)
            .filter(|r| r.envelope_type == EnvelopeType::SESSION_COMPACT_REQUEST)
        else {
            return;
        };
        let request: SessionCompactRequest = payload(&request.envelope);
        let run_id = os(&p.run_id);
        self.check_scope(at, e, p.session_id.as_str(), &run_id);
        if p.session_id != request.session_id {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/session_id",
                "compaction response must answer its request's session",
                request.session_id.as_str(),
                p.session_id.as_str(),
                &[&reply],
            );
        }
        let requested = if request.delivery.is_empty() {
            RequestedDeliveryMode::AUTO
        } else {
            request.delivery
        };
        if p.requested_delivery != requested {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/requested_delivery",
                "compaction response must repeat the requested delivery",
                requested.as_str(),
                p.requested_delivery.as_str(),
                &[&reply],
            );
        }
        let refused = |message: &str| {
            (
                EnvelopeType::ERROR_RESPONSE.as_str().to_owned(),
                message.to_owned(),
            )
        };
        let refusal = if requested != RequestedDeliveryMode::AUTO
            && requested != RequestedDeliveryMode::QUEUE
        {
            Some(refused("a compaction takes auto or queue delivery, so a steer or btw compaction is refused"))
        } else if !p.accepted {
            Some(refused(
                "a refused compaction is a correlated error.response, not a compaction response",
            ))
        } else {
            None
        };
        if let Some((expected, message)) = refusal {
            self.add_expected(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                "/payload/accepted",
                &message,
                &expected,
                e.envelope_type.as_str(),
                &[&reply],
            );
            return;
        }
        let queued = p.admission == Admission::QUEUED;
        let shape = if run_id.is_empty() {
            Some((
                "/payload/run_id",
                "an accepted compaction must reserve a run identity",
                true,
            ))
        } else if p.admission == Admission::STARTED {
            (p.effective_delivery != EffectiveDeliveryMode::START || p.status != RunStatus::RUNNING)
                .then_some((
                    "/payload/admission",
                    "a started compaction resolves to effective start with a running run",
                    false,
                ))
        } else if queued {
            (p.effective_delivery != EffectiveDeliveryMode::QUEUE || p.status != RunStatus::QUEUED)
                .then_some((
                    "/payload/admission",
                    "a queued compaction resolves to effective queue with a queued run",
                    false,
                ))
        } else {
            Some((
                "/payload/admission",
                "a compaction admits a started or queued run, never a steered or side one",
                true,
            ))
        };
        if let Some((pointer, message, stop)) = shape {
            self.add(codes::ILLEGAL_RUN_TRANSITION, at, e, pointer, message);
            if stop {
                return;
            }
        }
        if self.runs.contains_key(&run_id) {
            self.add(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                "/payload/run_id",
                "run was admitted more than once",
            );
            return;
        }
        let submitted = MessageSubmitResponse {
            session_id: p.session_id.clone(),
            run_id: p.run_id.clone(),
            requested_delivery: p.requested_delivery.clone(),
            effective_delivery: p.effective_delivery.clone(),
            delivery_resolution: p.delivery_resolution.clone(),
            admission: p.admission.clone(),
            status: p.status.clone(),
            ..MessageSubmitResponse::default()
        };
        if !self.queue_overlap(at, e, &submitted) {
            self.queue_admission(at, e, &submitted);
        }
        let session = s(&p.session_id);
        let run = Run {
            session: session.clone(),
            next: 1,
            admitted: true,
            admitted_at: at.0,
            status: RunStatus::QUEUED,
            last: at,
            admitted_queued: queued,
            compaction_run: true,
            compaction_continue: request.continue_after,
            ..Run::default()
        };
        self.runs.insert(run_id.clone(), run);
        self.enter_order(&run_id);
        if !queued
            && self
                .active
                .get(&session)
                .is_none_or(|active| self.runs.get(active).is_none_or(|run| run.terminal))
        {
            self.active.insert(session, run_id);
        }
    }
}
