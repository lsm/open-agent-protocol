use oxap_protocol::*;

use super::super::{os, payload, s, State};
use super::{affirmative, Expectation};

impl State {
    pub(super) fn reopen_gate(&mut self, at: (usize, usize), e: &Envelope, p: &SessionOpenRequest) {
        if !p.reopen {
            return;
        }
        let Some(level) = self.control_descriptor(at, e, FEATURE_OPEN_REOPEN) else {
            return;
        };
        let degraded = level == SupportLevel::DEGRADED;
        if !affirmative(&level) || (degraded && !p.allows_degraded(FEATURE_OPEN_REOPEN)) {
            let mut expectation = Expectation::delivery(FEATURE_OPEN_REOPEN, degraded);
            expectation.pointer = "/payload/reopen".to_owned();
            expectation.message =
                "a reopen requires its advertised support and degraded opt-in".to_owned();
            self.pending_reopens.insert(s(&e.id), expectation);
        }
    }

    pub(in crate::state) fn reopen_admission(&mut self, at: (usize, usize), e: &Envelope) -> bool {
        let reply = os(&e.in_reply_to);
        let Some(expectation) = self.pending_reopens.get(&reply).cloned() else {
            return false;
        };
        self.add_expected(
            expectation.diagnostic,
            at,
            e,
            "/payload",
            &expectation.message,
            &format!("a typed refusal naming {}", expectation.key),
            "an admitted reopen",
            &[&reply],
        );
        true
    }

    pub(super) fn reopen_refusal(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(expectation) = self.pending_reopens.get(&reply).cloned() else {
            return;
        };
        let error = payload::<ErrorResponse>(e).error;
        let (discharged, speaker) = self.attribute(&reply, &error);
        if !discharged {
            self.refusal_misses(at, e, &expectation, &speaker, &error);
        }
    }
}
