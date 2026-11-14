use oxap_protocol::*;

use super::{os, payload, State};
use crate::codes;

#[derive(Default)]
pub(super) struct Update {
    pub(super) request: SessionSettingsUpdateRequest,
    pub(super) response: Option<((usize, usize), Envelope)>,
    pub(super) seen: bool,
}

impl State {
    pub(super) fn live_settings_response(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(requested) = self
            .live_settings
            .get(&reply)
            .map(|update| update.request.clone())
        else {
            return;
        };
        let p: SessionSettingsUpdateResponse = payload(e);
        if requested.session_id != p.session_id {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/session_id",
                "settings response changed the requested session",
                requested.session_id.as_str(),
                p.session_id.as_str(),
                &[&reply],
            );
            return;
        }
        if let Some(pending) = self.pending_open_settings.remove(&reply) {
            if !pending.expectations.is_empty() {
                for expectation in pending.expectations {
                    self.add_expected(
                        expectation.diagnostic,
                        at,
                        e,
                        &expectation.pointer,
                        &expectation.message,
                        &format!("a typed refusal naming {}", expectation.key),
                        "an admitted update",
                        &[&reply],
                    );
                }
                return;
            }
        }
        if !requested.reasoning_level.is_empty() && requested.reasoning_level != p.reasoning_level {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/reasoning_level",
                "an admitted update must apply the requested reasoning level",
                requested.reasoning_level.as_str(),
                if p.reasoning_level.is_empty() {
                    "absent"
                } else {
                    p.reasoning_level.as_str()
                },
                &[&reply],
            );
        }
        if requested.compaction_policy.is_some()
            && requested.compaction_policy != p.compaction_policy
        {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/compaction_policy",
                "an admitted update must apply the requested compaction policy",
                &describe_policy(requested.compaction_policy.as_ref()),
                &describe_policy(p.compaction_policy.as_ref()),
                &[&reply],
            );
        }
        self.live_settings.get_mut(&reply).unwrap().response = Some((at, e.clone()));
    }

    pub(super) fn observe_live_settings(&mut self, e: &Envelope) {
        let p: SessionState = payload(e);
        for update in self
            .live_settings
            .values_mut()
            .filter(|update| update.request.session_id == p.session_id)
        {
            let asked = &update.request;
            update.seen |= (asked.reasoning_level.is_empty()
                || p.reasoning_level.is_empty()
                || asked.reasoning_level == p.reasoning_level)
                && (asked.compaction_policy.is_none()
                    || p.compaction_policy.is_none()
                    || asked.compaction_policy == p.compaction_policy);
        }
    }

    pub(super) fn settings_before_run(&mut self, at: (usize, usize), e: &Envelope, session: &str) {
        let pending: Vec<_> = self
            .live_settings
            .iter()
            .filter(|(_, update)| {
                update.request.session_id.as_str() == session
                    && update.response.is_some()
                    && !update.seen
            })
            .map(|(id, _)| id.clone())
            .collect();
        for id in pending {
            self.live_settings.remove(&id);
            self.add_expected(
                codes::SESSION_STATE_MISMATCH,
                at,
                e,
                "/type",
                "an accepted settings update must be reported before the next run starts",
                "session.state.updated before the next run starts",
                e.envelope_type.as_str(),
                &[&id],
            );
        }
    }

    pub(super) fn close_live_settings(&mut self) {
        for (id, update) in std::mem::take(&mut self.live_settings) {
            if let Some((at, e)) = update.response.filter(|_| !update.seen) {
                self.add_expected(
                    codes::SESSION_STATE_MISMATCH,
                    at,
                    &e,
                    "/type",
                    "accepted settings were never reflected in session.state.updated",
                    "a matching session.state.updated",
                    "none",
                    &[&id],
                );
            }
        }
    }
}

fn describe_policy(policy: Option<&CompactionPolicy>) -> String {
    match policy {
        None => "absent".to_owned(),
        Some(policy) => match policy.kind.as_str() {
            "share" => format!("share {}%", policy.share_percent),
            "tokens" => format!("tokens {}", policy.tokens),
            kind => kind.to_owned(),
        },
    }
}
