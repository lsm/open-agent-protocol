use oap_protocol::*;
use serde::Deserialize;

use super::{os, payload, s, State};
use crate::codes;

#[derive(Clone, Default)]
pub(super) struct SteerTrack {
    request: String,
    messages: Option<Vec<String>>,
    settled: bool,
}

impl SteerTrack {
    pub(super) fn recovered(pending: &PendingSteer) -> Self {
        Self {
            request: s(&pending.request_id),
            ..Self::default()
        }
    }
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct Admitted {
    message_ids: Option<Vec<String>>,
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct Settlement {
    submission_id: String,
    request_id: String,
    message_ids: Vec<String>,
}

impl State {
    fn requested_submission(&self, reply: &str) -> Option<MessageSubmitRequest> {
        let request = self.requests.get(reply)?;
        let t = &request.envelope_type;
        Some(if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST {
            payload(&request.envelope)
        } else if *t == EnvelopeType::SESSION_OPEN_REQUEST {
            payload::<SessionOpenRequest>(&request.envelope)
                .message
                .map(|message| message.submit(SessionId::from(request.session.as_str())))
                .unwrap_or_default()
        } else {
            MessageSubmitRequest::default()
        })
    }

    fn steer_target(&self, request: &MessageSubmitRequest) -> (String, &'static str) {
        let target = os(&request.target_run_id);
        if !target.is_empty() {
            let Some(run) = self.runs.get(&target) else {
                return (target, "unknown_target");
            };
            let reason = if run.session != request.session_id.as_str() {
                "cross_session"
            } else if run.terminal {
                "terminal"
            } else if run.status == RunStatus::CANCELLING {
                "not_steerable"
            } else if run.compaction_run && !run.compaction_continue {
                "no_model_turn"
            } else if !run.started {
                "queued"
            } else {
                ""
            };
            return (target, reason);
        }
        let order = self.order.get(request.session_id.as_str());
        for id in order.into_iter().flatten() {
            let Some(run) = self.runs.get(id).filter(|run| run.started && !run.terminal) else {
                continue;
            };
            let reason = if run.status == RunStatus::CANCELLING {
                "not_steerable"
            } else if run.compaction_run && !run.compaction_continue {
                "no_model_turn"
            } else {
                ""
            };
            return (id.clone(), reason);
        }
        (String::new(), "no_active_run")
    }

    pub(super) fn steer_admission(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &MessageSubmitResponse,
    ) {
        self.settle_submit_admission(at, e, p);
        let reply = os(&e.in_reply_to);
        if self
            .pending_controls
            .get(&reply)
            .is_some_and(|pending| pending.expectation.is_some())
        {
            return;
        }
        let Some(request) = self.requested_submission(&reply) else {
            return;
        };
        if request.delivery != RequestedDeliveryMode::STEER
            || p.effective_delivery != EffectiveDeliveryMode::STEER
        {
            let message = "steering requires explicit steer delivery";
            self.add(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                "/payload/admission",
                message,
            );
            return;
        }
        let (target, reason) = self.steer_target(&request);
        if !reason.is_empty() {
            let message = "steer target cannot take guidance";
            let expected = "a started nonterminal steerable run";
            let pointer = "/payload/run_id";
            self.add_expected(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                pointer,
                message,
                expected,
                reason,
                &[],
            );
            return;
        }
        let named = os(&p.run_id);
        if named != target {
            let message = "steer admission must name its target";
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/run_id",
                message,
                &target,
                &named,
                &[],
            );
            return;
        }
        let Some(run) = self.runs.get(&target) else {
            return;
        };
        let (cursor, status) = (run.next - 1, run.status.clone());
        let duplicate = run.steers.contains_key(p.submission_id.as_str());
        if p.target_sequence != Some(cursor) {
            let actual = p
                .target_sequence
                .map_or("absent".to_owned(), |at| at.to_string());
            let message = "steer admission must name the target cursor at response";
            let pointer = "/payload/target_sequence";
            self.add_expected(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                pointer,
                message,
                &cursor.to_string(),
                &actual,
                &[],
            );
        }
        if p.status != status {
            let message = "steer admission must report target status";
            self.add_expected(
                codes::ILLEGAL_RUN_TRANSITION,
                at,
                e,
                "/payload/status",
                message,
                status.as_str(),
                p.status.as_str(),
                &[],
            );
        }
        if duplicate {
            let message = "steer submission identity was already admitted";
            self.add(
                codes::DUPLICATE_STEER,
                at,
                e,
                "/payload/submission_id",
                message,
            );
            return;
        }
        let track = SteerTrack {
            request: reply,
            messages: payload::<Admitted>(e).message_ids,
            settled: false,
        };
        if let Some(run) = self.runs.get_mut(&target) {
            run.steers
                .insert(p.submission_id.as_str().to_owned(), track);
        }
    }

    pub(super) fn steer_settlement(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        self.feature(at, e, "delivery.steer");
        let settlement: Settlement = payload(e);
        let applied = e.envelope_type == EnvelopeType::RUN_STEER_APPLIED;
        let track = self
            .runs
            .get_mut(run_id)
            .and_then(|run| run.steers.get_mut(&settlement.submission_id));
        let Some(track) = track else {
            let message = "settlement names no admitted steer";
            self.add(
                codes::UNMATCHED_STEER,
                at,
                e,
                "/payload/submission_id",
                message,
            );
            return;
        };
        if track.settled {
            let message = "steer settled more than once";
            self.add(
                codes::DUPLICATE_STEER,
                at,
                e,
                "/payload/submission_id",
                message,
            );
            return;
        }
        track.settled = true;
        let other_request = track.request != settlement.request_id;
        let other_messages = applied
            && track
                .messages
                .as_ref()
                .is_some_and(|messages| *messages != settlement.message_ids);
        if other_request {
            let message = "settlement does not name the admitting request";
            self.add(
                codes::UNMATCHED_STEER,
                at,
                e,
                "/payload/request_id",
                message,
            );
        }
        if other_messages {
            let message = "settlement differs from admitted guidance";
            self.add(
                codes::UNMATCHED_STEER,
                at,
                e,
                "/payload/message_ids",
                message,
            );
        }
    }
}
