use oxap_protocol::*;

use super::super::{os, packed, payload, s, State};
use super::{affirmative, describe_refusal, Expectation, PendingQuery, RUNG_DEGRADATION};
use crate::codes;

impl State {
    pub(super) fn query_feature(&mut self, at: (usize, usize), e: &Envelope) {
        let t = &e.envelope_type;
        let (key, opted_in) = if *t == EnvelopeType::MODELS_REQUEST {
            (
                FEATURE_MODELS_LIST,
                payload::<ModelsRequest>(e).allows_degraded(FEATURE_MODELS_LIST),
            )
        } else if *t == EnvelopeType::SESSION_LIST_REQUEST {
            (
                FEATURE_SESSION_LIST,
                payload::<SessionListRequest>(e).allows_degraded(FEATURE_SESSION_LIST),
            )
        } else if *t == EnvelopeType::TRANSCRIPT_LOAD_REQUEST {
            (
                FEATURE_TRANSCRIPT_LOAD,
                payload::<TranscriptLoadRequest>(e).allows_degraded(FEATURE_TRANSCRIPT_LOAD),
            )
        } else {
            self.query_outcome(at, e);
            return;
        };
        let mut pending = PendingQuery::default();
        if let Some(level) = self.control_descriptor(at, e, key) {
            let degraded = level == SupportLevel::DEGRADED;
            if !affirmative(&level) || (degraded && !opted_in) {
                let mut expectation = Expectation::delivery(key, degraded);
                expectation.pointer = "/payload".to_owned();
                expectation.message =
                    format!("{key} query requires its advertised support and degraded opt-in");
                pending.expectation = Some(expectation);
            } else {
                pending.honour = key == FEATURE_MODELS_LIST;
            }
        }
        self.pending_queries.insert(s(&e.id), pending);
    }

    fn query_outcome(&mut self, at: (usize, usize), e: &Envelope) {
        if !matches!(
            e.envelope_type.as_str(),
            "models.response"
                | "session.list.response"
                | "transcript.load.response"
                | "error.response"
        ) {
            return;
        }
        let reply = os(&e.in_reply_to);
        if e.envelope_type != EnvelopeType::ERROR_RESPONSE
            && self.requests.get(&reply).is_none_or(|request| {
                packed::expected(self.packs.as_deref(), &request.envelope_type) != e.envelope_type
            })
        {
            return;
        }
        let Some(pending) = self.pending_queries.remove(&reply) else {
            return;
        };
        if e.envelope_type == EnvelopeType::ERROR_RESPONSE {
            let error = payload::<ErrorResponse>(e).error;
            if let Some(expectation) = pending.expectation {
                if !expectation.conformed_by(&error) {
                    self.add_expected(
                        expectation.diagnostic,
                        at,
                        e,
                        "/payload/error",
                        "refusal does not tell the caller what to change",
                        &expectation.describe(),
                        &describe_refusal(&error),
                        &[&reply],
                    );
                }
            } else if pending.honour {
                self.add_expected(
                    codes::UNHONOURED_CAPABILITY,
                    at,
                    e,
                    "/payload/error",
                    "catalog query refused on an endpoint that advertises models.list",
                    "a served catalog",
                    &describe_refusal(&error),
                    &[&reply],
                );
            }
        } else if let Some(expectation) = pending
            .expectation
            .filter(|expectation| expectation.rung == RUNG_DEGRADATION)
        {
            self.add_expected(
                expectation.diagnostic,
                at,
                e,
                &expectation.pointer,
                &expectation.message,
                &format!("a typed refusal naming {}", expectation.key),
                "a served query",
                &[&reply],
            );
        }
    }
}
