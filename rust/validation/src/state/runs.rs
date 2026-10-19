use std::collections::{HashMap, HashSet};

use oap_protocol::*;

use super::controls::Controls;
use super::interactions::{Interaction, ToolTrack};
use super::{os, payload, s, State};
use crate::codes;

#[derive(Default)]
pub(super) struct Run {
    pub(super) session: String,
    pub(super) next: u64,
    pub(super) admitted: bool,
    pub(super) started: bool,
    pub(super) terminal: bool,
    pub(super) terminal_type: EnvelopeType,
    pub(super) cancel_accepted: bool,
    pub(super) recovered: bool,
    pub(super) status: RunStatus,
    pub(super) last: (usize, usize),
    pub(super) completed_messages: HashSet<String>,
    pub(super) prior_unknown: bool,
    pub(super) tools: HashMap<String, ToolTrack>,
    pub(super) interactions: HashMap<String, Interaction>,
    pub(super) controls: Controls,
    pub(super) admitted_model: String,
}

pub(super) const RUN_EVENTS: [EnvelopeType; 21] = [
    EnvelopeType::RUN_COMPACTION_STARTED,
    EnvelopeType::RUN_COMPACTION_ENDED,
    EnvelopeType::RUN_STEER_APPLIED,
    EnvelopeType::RUN_STEER_DROPPED,
    EnvelopeType::RUN_STARTED,
    EnvelopeType::RUN_STATUS_UPDATED,
    EnvelopeType::CONTENT_DELTA,
    EnvelopeType::CONTENT_COMPLETED,
    EnvelopeType::RUN_COMPLETED,
    EnvelopeType::RUN_FAILED,
    EnvelopeType::RUN_CANCELLED,
    EnvelopeType::ACTION_CALL_REQUESTED,
    EnvelopeType::ACTION_CALL_STARTED,
    EnvelopeType::ACTION_CALL_PROGRESS,
    EnvelopeType::ACTION_CALL_COMPLETED,
    EnvelopeType::ACTION_CALL_FAILED,
    EnvelopeType::ACTION_CALL_CANCELLED,
    EnvelopeType::ACTION_PERMISSION_REQUESTED,
    EnvelopeType::ACTION_PERMISSION_RESOLVED,
    EnvelopeType::USER_INPUT_REQUESTED,
    EnvelopeType::USER_INPUT_RESOLVED,
];

fn is_terminal(t: &EnvelopeType) -> bool {
    *t == EnvelopeType::RUN_COMPLETED
        || *t == EnvelopeType::RUN_FAILED
        || *t == EnvelopeType::RUN_CANCELLED
}

fn legal_transition(from: &RunStatus, to: &RunStatus) -> bool {
    match from.as_str() {
        "queued" => *to == RunStatus::RUNNING || *to == RunStatus::CANCELLING,
        "running" | "waiting_for_input" => {
            *to == RunStatus::RUNNING
                || *to == RunStatus::WAITING_FOR_INPUT
                || *to == RunStatus::CANCELLING
        }
        "cancelling" => *to == RunStatus::CANCELLING,
        _ => false,
    }
}

fn legal_targets(from: &RunStatus) -> &'static str {
    match from.as_str() {
        "queued" => "running or cancelling",
        "running" | "waiting_for_input" => "running, waiting_for_input, or cancelling",
        "cancelling" => "cancelling or a terminal event",
        _ => "no transition from terminal status",
    }
}

fn shape(value: &str, started: &str, queued: &str) -> &'static str {
    if value == started {
        "started"
    } else if value == queued {
        "queued"
    } else {
        ""
    }
}

fn admission_shape(p: &MessageSubmitResponse) -> bool {
    let votes = [
        shape(p.admission.as_str(), "started", "queued"),
        shape(p.effective_delivery.as_str(), "start", "queue"),
        shape(p.status.as_str(), "running", "queued"),
    ];
    !votes[0].is_empty() && votes.iter().all(|vote| *vote == votes[0])
}

#[derive(Default, serde::Deserialize)]
#[serde(default)]
struct RunScope {
    session_id: Option<String>,
    run_id: Option<String>,
}

type Finding = (&'static str, &'static str, &'static str, String, String);

impl State {
    pub(super) fn run_dispatch(&mut self, at: (usize, usize), e: &Envelope) {
        let t = &e.envelope_type;
        if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE {
            self.admit_submission(at, e, payload(e));
        } else if *t == EnvelopeType::SESSION_OPEN_RESPONSE {
            self.open_response(at, e);
        } else if *t == EnvelopeType::SESSION_STATE_RESPONSE
            || *t == EnvelopeType::SESSION_STATE_UPDATED
        {
            self.state_document(at, e);
        } else if *t == EnvelopeType::SESSION_COMPACT_RESPONSE {
            self.compact_response(at, e);
        } else if *t == EnvelopeType::RUN_CANCEL_REQUEST {
            let p: RunCancelRequest = payload(e);
            match self.runs.get(p.run_id.as_str()) {
                None => self.add(
                    codes::ILLEGAL_RUN_TRANSITION,
                    at,
                    e,
                    "/payload/run_id",
                    "cannot cancel an unknown run",
                ),
                Some(run) if run.terminal && run.terminal_type != EnvelopeType::RUN_CANCELLED => {
                    self.late_cancels.insert(s(&e.id));
                }
                Some(_) => {}
            }
        } else if *t == EnvelopeType::RUN_CANCEL_RESPONSE {
            let p: RunCancelResponse = payload(e);
            if !p.accepted {
                return;
            }
            let late = self.late_cancels.contains(&os(&e.in_reply_to));
            match self.runs.get_mut(p.run_id.as_str()) {
                None => self.add(codes::ILLEGAL_RUN_TRANSITION, at, e, "/payload/accepted", "cancellation cannot be accepted for an unknown run"),
                Some(_) if late => self.add(codes::ILLEGAL_RUN_TRANSITION, at, e, "/payload/accepted", "cancellation cannot be accepted when the request arrived after the run completed or failed"),
                Some(run) if !run.terminal => {
                    run.cancel_accepted = true;
                    run.status = RunStatus::CANCELLING;
                }
                Some(_) => {}
            }
        } else if RUN_EVENTS.contains(t) {
            self.run_event(at, e);
        }
    }

    pub(super) fn admit_submission(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: MessageSubmitResponse,
    ) {
        let run_id = os(&p.run_id);
        self.check_scope(at, e, p.session_id.as_str(), &run_id);
        let reply = os(&e.in_reply_to);
        let requested = self.requests.get(&reply).and_then(|request| {
            if request.envelope_type == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST {
                Some(payload::<MessageSubmitRequest>(&request.envelope).delivery)
            } else if request.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST {
                Some(
                    payload::<SessionOpenRequest>(&request.envelope)
                        .message
                        .map(|m| m.delivery)
                        .unwrap_or_default(),
                )
            } else {
                None
            }
        });
        if let Some(delivery) = requested
            .as_ref()
            .filter(|delivery| p.requested_delivery != **delivery)
        {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/requested_delivery",
                "submit response must repeat the requested delivery",
                delivery.as_str(),
                p.requested_delivery.as_str(),
                &[&reply],
            );
        }
        let refusal = if !p.accepted {
            self.add_expected(codes::ILLEGAL_RUN_TRANSITION, at, e, "/payload/accepted", "rejected submission must be reported as a correlated error.response, not a submit.response", EnvelopeType::ERROR_RESPONSE.as_str(), e.envelope_type.as_str(), &[&reply]);
            return;
        } else if requested.as_ref() == Some(&RequestedDeliveryMode::STEER)
            && p.admission != Admission::STEERED
        {
            self.settle_submit_admission(at, e, &p);
            Some((
                "/payload/admission",
                "explicit steer cannot admit a new run",
            ))
        } else if p.admission == Admission::STEERED {
            return;
        } else if run_id.is_empty() {
            Some((
                "/payload/run_id",
                "accepted submission must reserve a run identity",
            ))
        } else if !admission_shape(&p) {
            Some(("/payload/admission", "v0.1 admission must resolve auto to one started (status running) or queued (status queued) run (decision 0002)"))
        } else if self.runs.contains_key(&run_id) {
            Some(("/payload/run_id", "run was admitted more than once"))
        } else {
            None
        };
        if let Some((pointer, message)) = refusal {
            self.add(codes::ILLEGAL_RUN_TRANSITION, at, e, pointer, message);
            return;
        }
        let controls = self.settle_submit_admission(at, e, &p);
        let session = s(&p.session_id);
        let run = Run {
            session: session.clone(),
            next: 1,
            admitted: true,
            status: RunStatus::QUEUED,
            last: at,
            controls,
            admitted_model: p.model_id.clone(),
            ..Run::default()
        };
        self.runs.insert(run_id.clone(), run);
        if p.admission != Admission::QUEUED
            && self
                .active
                .get(&session)
                .is_none_or(|active| self.runs.get(active).is_none_or(|run| run.terminal))
        {
            self.active.insert(session, run_id);
        }
    }

    fn run_event(&mut self, at: (usize, usize), e: &Envelope) {
        let scope: RunScope = payload(e);
        self.check_scope(
            at,
            e,
            &scope.session_id.unwrap_or_default(),
            &scope.run_id.unwrap_or_default(),
        );
        let run_id = os(&e.run_id);
        if !self.runs.contains_key(&run_id) {
            self.add(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                "/run_id",
                "run event has no accepted admission",
            );
            self.runs.insert(
                run_id.clone(),
                Run {
                    session: os(&e.session_id),
                    next: 1,
                    ..Run::default()
                },
            );
        }
        self.replay_cursor(at, e, &run_id);
        let live = self.runs.get(&run_id).is_some_and(|run| !run.terminal);
        let (found, plain) = self.advance(at, e, &run_id);
        for (code, pointer, message, expected, actual) in found {
            let related: &[&str] = if code == codes::SCOPE_MISMATCH {
                &[run_id.as_str()]
            } else {
                &[]
            };
            self.add_expected(code, at, e, pointer, message, &expected, &actual, related);
        }
        for (code, pointer, message) in plain {
            self.add(code, at, e, pointer, message);
        }
        if live {
            self.live_run_event(at, e, &run_id);
            if is_terminal(&e.envelope_type) {
                self.settle_run_controls(at, e, &run_id);
                self.pending_at_terminal(at, e, &run_id);
            }
        }
        let Some(run) = self.runs.get(&run_id) else {
            return;
        };
        if run.terminal
            && is_terminal(&e.envelope_type)
            && self.active.get(&run.session) == Some(&run_id)
        {
            let owner = run.session.clone();
            self.active.remove(&owner);
        }
    }

    fn advance(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        run_id: &str,
    ) -> (
        Vec<Finding>,
        Vec<(&'static str, &'static str, &'static str)>,
    ) {
        let t = &e.envelope_type;
        let mut found: Vec<Finding> = Vec::new();
        let mut plain = Vec::new();
        let Some(run) = self.runs.get_mut(run_id) else {
            return (found, plain);
        };
        run.last = at;
        let session = os(&e.session_id);
        if session != run.session {
            found.push((
                codes::SCOPE_MISMATCH,
                "/session_id",
                "run event session differs from run owner",
                run.session.clone(),
                session,
            ));
        }
        if let Some(sequence) = e.sequence {
            if sequence < run.next {
                found.push((
                    codes::SEQUENCE_REGRESSION,
                    "/sequence",
                    "run sequence regressed or repeated",
                    run.next.to_string(),
                    sequence.to_string(),
                ));
            } else if sequence > run.next {
                found.push((
                    codes::SEQUENCE_GAP,
                    "/sequence",
                    "run sequence is not contiguous",
                    run.next.to_string(),
                    sequence.to_string(),
                ));
            }
            run.next = run.next.max(sequence + 1);
        }
        let unaccepted_cancel =
            *t == EnvelopeType::RUN_CANCELLED && !run.cancel_accepted && !run.recovered;
        if run.terminal {
            if unaccepted_cancel {
                plain.push((
                    codes::ILLEGAL_RUN_TRANSITION,
                    "/type",
                    "run.cancelled requires accepted cancellation",
                ));
            }
            if is_terminal(t) {
                plain.push((
                    codes::DUPLICATE_RUN_TERMINAL,
                    "/type",
                    "run emitted more than one terminal event",
                ));
            } else {
                plain.push((
                    codes::EVENT_AFTER_TERMINAL,
                    "/type",
                    "run event occurred after terminality",
                ));
            }
            return (found, plain);
        }
        if *t == EnvelopeType::RUN_STARTED {
            if run.started {
                plain.push((
                    codes::ILLEGAL_RUN_TRANSITION,
                    "/type",
                    "run.started occurred more than once",
                ));
            }
            run.started = true;
            run.status = RunStatus::RUNNING;
            if !run.admitted_model.is_empty() {
                let model = payload::<RunStartedPayload>(e).model_id;
                if model.is_empty() && run.controls.selects_model() {
                    found.push((
                        codes::UNAPPLIED_CONTROL,
                        "/payload/model_id",
                        "run.started omits the model the run was admitted under",
                        run.admitted_model.clone(),
                        "absent".to_owned(),
                    ));
                } else if !model.is_empty() && model != run.admitted_model {
                    found.push((
                        codes::UNAPPLIED_CONTROL,
                        "/payload/model_id",
                        "run.started model disagrees with the admitted model",
                        run.admitted_model.clone(),
                        model,
                    ));
                }
            }
            return (found, plain);
        }
        if !run.started && *t != EnvelopeType::RUN_FAILED && *t != EnvelopeType::RUN_CANCELLED {
            plain.push((
                codes::MISSING_RUN_STARTED,
                "/type",
                "run-scoped event occurred before run.started",
            ));
        }
        if *t == EnvelopeType::RUN_STATUS_UPDATED {
            let p: RunStatusUpdatedPayload = payload(e);
            if legal_transition(&run.status, &p.status) {
                run.status = p.status;
            } else {
                found.push((
                    codes::ILLEGAL_RUN_TRANSITION,
                    "/payload/status",
                    "illegal run status transition",
                    legal_targets(&run.status).to_owned(),
                    p.status.as_str().to_owned(),
                ));
            }
        } else if *t == EnvelopeType::CONTENT_DELTA {
            let p: ContentDeltaPayload = payload(e);
            if p.message_id
                .as_ref()
                .is_some_and(|id| run.completed_messages.contains(id.as_str()))
            {
                plain.push((
                    codes::CONTENT_AFTER_COMPLETION,
                    "/payload/message_id",
                    "content.delta streams into a message its content.completed already closed",
                ));
            }
        } else if *t == EnvelopeType::CONTENT_COMPLETED {
            let p: ContentCompletedPayload = payload(e);
            if !run
                .completed_messages
                .insert(p.message_id.as_str().to_owned())
            {
                plain.push((codes::DUPLICATE_MESSAGE_COMPLETION, "/payload/message_id", "content.completed closes a message a previous content.completed already closed"));
            }
        } else if is_terminal(t) {
            if unaccepted_cancel {
                plain.push((
                    codes::ILLEGAL_RUN_TRANSITION,
                    "/type",
                    "run.cancelled requires accepted cancellation",
                ));
            }
            run.terminal = true;
            run.terminal_type = t.clone();
        }
        (found, plain)
    }

    pub(super) fn close_runs(&mut self) {
        let mut found = Vec::new();
        for (id, run) in &self.runs {
            let stub = Envelope {
                id: EnvelopeId::from(id.as_str()),
                run_id: Some(RunId::from(id.as_str())),
                session_id: Some(SessionId::from(run.session.as_str())),
                ..Envelope::default()
            };
            if run.admitted && !run.started && !run.terminal {
                let started = Envelope {
                    envelope_type: EnvelopeType::RUN_STARTED,
                    ..stub.clone()
                };
                found.push(self.diagnostic(
                    codes::MISSING_RUN_STARTED,
                    run.last,
                    &started,
                    "",
                    "admitted run never emitted run.started",
                ));
            }
            if run.admitted && !run.terminal {
                let (code, message) = if run.cancel_accepted {
                    (
                        codes::CANCEL_NOT_SETTLED,
                        "accepted cancellation did not settle with a terminal event",
                    )
                } else {
                    (
                        codes::MISSING_RUN_TERMINAL,
                        "admitted run has no terminal event",
                    )
                };
                found.push(self.diagnostic(code, run.last, &stub, "", message));
            }
        }
        self.diagnostics.extend(found);
    }
}
