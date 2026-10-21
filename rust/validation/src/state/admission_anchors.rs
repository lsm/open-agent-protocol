use std::collections::HashSet;

use oap_protocol::*;

use super::runs::Run;
use super::{os, payload, State};
use crate::codes;

pub(super) struct Claim {
    request: String,
    session: String,
    run: String,
    accounted: HashSet<String>,
    pointer: String,
    at: (usize, usize),
    envelope: Envelope,
}

impl State {
    fn anchor_admission(&self, request: &str) -> Option<(&str, &Run)> {
        self.runs.iter().find_map(|(id, run)| {
            (run.submit_request == request
                || run.steers.values().any(|steer| steer.request == request))
            .then_some((id.as_str(), run))
        })
    }

    fn anchor_failure(&self, claim: &Claim, deferred: bool) -> Option<&'static str> {
        let request = self.requests.get(&claim.request)?;
        match self.anchor_admission(&claim.request) {
            Some((_, run)) if run.session != claim.session => {
                Some("the anchor was admitted on another session")
            }
            Some((id, _)) if !claim.run.is_empty() && claim.run != id => {
                Some("the anchor was admitted to a different run")
            }
            Some((id, _)) if deferred && claim.run.is_empty() && !claim.accounted.contains(id) => {
                Some("the snapshot accounts for the anchor's admitted run nowhere")
            }
            Some(_) => None,
            None if request.responded => {
                Some("the answered request did not establish the claimed admission")
            }
            None if deferred => Some("the claimed admission received no response"),
            None => None,
        }
    }

    fn record_admission_anchor(&mut self, claim: Claim) {
        let valid = self.requests.get(&claim.request).is_some_and(|request| {
            request.session == claim.session
                && (request.envelope_type == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST
                    || (request.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST
                        && payload::<SessionOpenRequest>(&request.envelope)
                            .message
                            .is_some()))
        });
        let failure = if !valid {
            Some("an anchor must name a submit or message-carrying open request on this session")
        } else {
            self.anchor_failure(&claim, false)
        };
        if let Some(message) = failure {
            self.add(
                codes::SESSION_STATE_MISMATCH,
                claim.at,
                &claim.envelope,
                &claim.pointer,
                message,
            );
        } else if self.anchor_admission(&claim.request).is_none() {
            self.admission_anchors.push(claim);
        }
    }

    pub(super) fn check_admission_anchors(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &SessionState,
    ) {
        let mut accounted: HashSet<String> = p
            .active_runs
            .iter()
            .map(|entry| entry.run_id.as_str().to_owned())
            .collect();
        let mut settled = HashSet::new();
        if let Some(capture) = &p.as_of {
            for entry in &capture.settled {
                settled.insert(entry.run_id.as_str());
                accounted.insert(entry.run_id.as_str().to_owned());
            }
        }
        if let Some(active) = &p.active_run_id {
            accounted.insert(active.as_str().to_owned());
        }
        if let Some(capture) = &p.as_of {
            for request in &capture.admitted_submit_requests {
                self.record_admission_anchor(Claim {
                    request: request.as_str().to_owned(),
                    session: p.session_id.as_str().to_owned(),
                    run: String::new(),
                    accounted: accounted.clone(),
                    pointer: "/payload/as_of/admitted_submit_requests".to_owned(),
                    at,
                    envelope: e.clone(),
                });
            }
        }
        let window = self
            .requests
            .get(&os(&e.in_reply_to))
            .map_or(at.0, |request| request.index);
        let mut seen = HashSet::new();
        for (index, entry) in p.active_runs.iter().enumerate() {
            let id = entry.run_id.as_str();
            if !seen.insert(id) || settled.contains(id) {
                continue;
            }
            let run = self
                .runs
                .get(id)
                .filter(|run| run.session == p.session_id.as_str());
            if run.is_some_and(|run| run.terminal_at.is_some_and(|terminal| terminal < window)) {
                continue;
            }
            let leading = run.is_none();
            if leading && !self.entry_leads_admission(p.session_id.as_str(), entry) {
                continue;
            }
            let pointer = if leading {
                format!("/payload/active_runs/{index}/run_id")
            } else {
                format!("/payload/active_runs/{index}/admitted_submit_requests")
            };
            for request in &entry.admitted_submit_requests {
                self.record_admission_anchor(Claim {
                    request: request.as_str().to_owned(),
                    session: p.session_id.as_str().to_owned(),
                    run: id.to_owned(),
                    accounted: HashSet::new(),
                    pointer: pointer.clone(),
                    at,
                    envelope: e.clone(),
                });
            }
        }
    }

    pub(super) fn close_admission_anchors(&mut self) {
        for claim in std::mem::take(&mut self.admission_anchors) {
            if let Some(message) = self.anchor_failure(&claim, true) {
                self.add(
                    codes::SESSION_STATE_MISMATCH,
                    claim.at,
                    &claim.envelope,
                    &claim.pointer,
                    message,
                );
            }
        }
    }
}
