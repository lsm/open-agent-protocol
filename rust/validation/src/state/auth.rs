use oap_protocol::*;

use super::{os, payload, State};
use crate::codes;

pub(super) struct Flow {
    next: u64,
    terminal: bool,
    at: (usize, usize),
    start: Envelope,
}

impl State {
    pub(super) fn auth_dispatch(&mut self, at: (usize, usize), e: &Envelope) {
        if e.envelope_type == EnvelopeType::AUTH_LOGIN_START_RESPONSE {
            self.auth_start_response(at, e);
            return;
        }
        let id = match e.envelope_type.as_str() {
            "auth.login.event" => payload::<AuthLoginEvent>(e).flow_id.as_str().to_owned(),
            "auth.login.completed" => payload::<AuthLoginCompleted>(e).flow_id.as_str().to_owned(),
            "auth.login.cancel.request" => payload::<AuthLoginCancelRequest>(e)
                .flow_id
                .as_str()
                .to_owned(),
            _ => return,
        };
        let Some(flow) = self.auth_flows.get_mut(&id) else {
            self.add(
                codes::AUTH_FLOW_ORDER,
                at,
                e,
                "/payload/flow_id",
                "auth flow event or command preceded its start response",
            );
            return;
        };
        if flow.terminal {
            self.add(
                codes::EVENT_AFTER_TERMINAL,
                at,
                e,
                "/payload/flow_id",
                "auth flow continued after its terminal",
            );
            return;
        }
        let completed = e.envelope_type == EnvelopeType::AUTH_LOGIN_COMPLETED;
        if e.envelope_type != EnvelopeType::AUTH_LOGIN_EVENT && !completed {
            return;
        }
        let expected = flow.next;
        if let Some(sequence) = e.sequence {
            flow.next = sequence.wrapping_add(1);
        }
        if completed {
            flow.terminal = true;
        }
        if let Some(sequence) = e.sequence {
            if sequence < expected {
                self.add_expected(
                    codes::SEQUENCE_REGRESSION,
                    at,
                    e,
                    "/sequence",
                    "auth flow sequence regressed",
                    &expected.to_string(),
                    &sequence.to_string(),
                    &[],
                );
            } else if sequence > expected {
                self.add_expected(
                    codes::SEQUENCE_GAP,
                    at,
                    e,
                    "/sequence",
                    "auth flow sequence skipped an event",
                    &expected.to_string(),
                    &sequence.to_string(),
                    &[],
                );
            }
        }
    }

    fn auth_start_response(&mut self, at: (usize, usize), e: &Envelope) {
        if !self
            .requests
            .get(&os(&e.in_reply_to))
            .is_some_and(|request| request.envelope_type == EnvelopeType::AUTH_LOGIN_START_REQUEST)
        {
            return;
        }
        let id = payload::<AuthLoginStartResponse>(e)
            .flow_id
            .as_str()
            .to_owned();
        if self.auth_flows.contains_key(&id) {
            self.add(
                codes::AUTH_FLOW_ORDER,
                at,
                e,
                "/payload/flow_id",
                "login start reused an active flow id",
            );
            return;
        }
        self.auth_flows.insert(
            id,
            Flow {
                next: 1,
                terminal: false,
                at,
                start: e.clone(),
            },
        );
    }

    pub(super) fn close_auth_flows(&mut self) {
        let mut found = Vec::new();
        for (id, flow) in &self.auth_flows {
            if !flow.terminal {
                let mut diagnostic = self.diagnostic(
                    codes::MISSING_AUTH_TERMINAL,
                    flow.at,
                    &flow.start,
                    "/payload/flow_id",
                    "accepted auth login flow has no auth.login.completed terminal",
                );
                diagnostic.related_ids.push(id.clone());
                found.push(diagnostic);
            }
        }
        self.diagnostics.extend(found);
    }
}
