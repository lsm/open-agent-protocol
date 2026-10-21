use super::interactions::Interaction;
use super::{os, payload, s, State};
use crate::codes;
use oap_protocol::*;
use serde_json::Value;
use std::collections::HashSet;

#[derive(Default)]
pub(super) struct Call {
    acked: bool,
    accepted_arm: String,
    accepted_payload: Option<Value>,
    resolve_requests: HashSet<String>,
    settlements: HashSet<String>,
}

pub(super) struct PendingResolve {
    run: String,
    interaction: String,
    arm: String,
    reason: ResolveReason,
    opaque: bool,
}

fn reason_code(reason: &ResolveReason) -> &'static str {
    match reason.as_str() {
        "wrong_responder" => codes::WRONG_INTERACTION_RESPONDER,
        "already_resolved" | "repeated_acknowledgement" => codes::DUPLICATE_INTERACTION,
        _ => codes::UNMATCHED_INTERACTION,
    }
}

fn resolve_reason(p: &ActionCallResolveRequest, x: &Interaction) -> ResolveReason {
    let mut reasons = Vec::new();
    if p.responded_by.as_str() != x.responded_by
        || (!p.requested_by.as_str().is_empty() && p.requested_by.as_str() != x.requested_by)
    {
        reasons.push(ResolveReason::WRONG_RESPONDER);
    }
    let acknowledgement = p.arm() == Some(RESOLVE_ARM_ACKNOWLEDGE);
    if x.resolved {
        reasons.push(ResolveReason::ALREADY_RESOLVED);
    } else if !x.call.accepted_arm.is_empty() {
        reasons.push(if acknowledgement {
            ResolveReason::LATE_ACKNOWLEDGEMENT
        } else {
            ResolveReason::ALREADY_RESOLVED
        });
    }
    if acknowledgement && x.call.acked {
        reasons.push(ResolveReason::REPEATED_ACKNOWLEDGEMENT);
    }
    ResolveReason::highest(&reasons).unwrap_or_default()
}

fn same_payload(expected: &Option<Value>, actual: &Option<Value>) -> bool {
    expected == actual
        || expected
            .as_ref()
            .zip(actual.as_ref())
            .is_some_and(|(expected, actual)| {
                jsonschema::is_valid(&serde_json::json!({"const": expected}), actual)
            })
}

impl State {
    fn control_owned(&self, owner: &str) -> bool {
        !self.control_participant.is_empty() && owner == self.control_participant
    }

    pub(super) fn control_call_event(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        run_id: &str,
        next: &str,
    ) {
        let p: ActionCallPayload = payload(e);
        if next == "requested" {
            self.control_call_owner(at, e, &p);
            self.control_call_requested(at, e, run_id, &p);
            return;
        }
        let controlled = self.control_owned(p.execution_owner.as_str());
        let Some(run) = self.runs.get_mut(run_id) else {
            return;
        };
        let id = run
            .tools
            .get(p.tool_call_id.as_str())
            .map(|t| t.interaction.clone())
            .filter(|id| !id.is_empty())
            .unwrap_or_else(|| os(&p.interaction_id));
        let Some(x) = run.interactions.get_mut(&id) else {
            return;
        };
        let terminal = matches!(next, "completed" | "failed" | "cancelled");
        if x.opaque {
            if controlled && terminal {
                x.resolved = true;
                x.call.settlements.insert(s(&e.id));
            }
            return;
        }
        if x.kind != "tool_call" {
            return;
        }
        let accepted = match next {
            "started" => x.call.acked || !x.call.accepted_arm.is_empty(),
            "completed" => x.call.accepted_arm == RESOLVE_ARM_RESULT,
            "failed" => x.call.accepted_arm == RESOLVE_ARM_ERROR,
            _ => true,
        };
        if terminal {
            x.resolved = true;
            x.call.settlements.insert(s(&e.id));
        }
        if !accepted {
            self.add(
                codes::ILLEGAL_TOOL_TRANSITION,
                at,
                e,
                "/type",
                "a control-owned call event requires an accepted resolution of the matching arm",
            );
            return;
        }
        if !matches!(next, "started" | "completed" | "failed") {
            return;
        }
        let request = os(&p.request_id);
        let known_request = x.call.resolve_requests.contains(&request);
        let expected = x.call.accepted_payload.clone();
        if request.is_empty() || !known_request {
            self.add(codes::UNMATCHED_INTERACTION, at, e, "/payload/request_id", "a resolve-derived event must name a resolve request this trace carries for its interaction");
        }
        let (pointer, actual) = match next {
            "completed" => (
                "/payload/result",
                p.result
                    .as_ref()
                    .and_then(|v| serde_json::from_str::<Value>(v.get()).ok()),
            ),
            "failed" => (
                "/payload/error",
                p.error.as_ref().and_then(|v| serde_json::to_value(v).ok()),
            ),
            _ => return,
        };
        if !same_payload(&expected, &actual) {
            self.add_expected(
                codes::RESOLUTION_PAYLOAD_MISMATCH,
                at,
                e,
                pointer,
                "a control-owned call's terminal differs from its accepted resolution",
                &serde_json::to_string(&expected).unwrap_or_default(),
                &serde_json::to_string(&actual).unwrap_or_default(),
                &[&s(&p.tool_call_id)],
            );
        }
    }

    fn control_call_requested(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        run_id: &str,
        p: &ActionCallPayload,
    ) {
        if !self.control_owned(p.execution_owner.as_str()) {
            return;
        }
        let id = os(&p.interaction_id);
        let responder = os(&p.responded_by);
        if id.is_empty() || responder.is_empty() {
            self.add(
                codes::ILLEGAL_TOOL_TRANSITION,
                at,
                e,
                "/payload/interaction_id",
                "a control-owned call must open an interaction and name its responder",
            );
            return;
        }
        if self.initialized && !self.participants.contains(&responder) {
            self.add(
                codes::UNKNOWN_PARTICIPANT,
                at,
                e,
                "/payload/responded_by",
                "participant was not declared during initialization",
            );
        }
        let Some(run) = self.runs.get_mut(run_id) else {
            return;
        };
        if run.interactions.contains_key(&id) {
            self.add(
                codes::DUPLICATE_INTERACTION,
                at,
                e,
                "/payload/interaction_id",
                "interaction id was requested more than once",
            );
            return;
        }
        let mut x = Interaction::default();
        x.kind = "tool_call";
        x.requested_by = os(&p.requested_by);
        x.responded_by = responder;
        x.tool_call_id = s(&p.tool_call_id);
        run.interactions.insert(id.clone(), x);
        if let Some(track) = run.tools.get_mut(p.tool_call_id.as_str()) {
            track.interaction = id;
        }
    }

    pub(super) fn control_resolve_request(&mut self, at: (usize, usize), e: &Envelope) {
        let p: ActionCallResolveRequest = payload(e);
        self.feature(at, e, "tools");
        let mut pending = PendingResolve {
            run: s(&p.run_id),
            interaction: s(&p.interaction_id),
            arm: p.arm().unwrap_or_default().to_owned(),
            reason: ResolveReason::default(),
            opaque: false,
        };
        let x = self.runs.get_mut(p.run_id.as_str()).and_then(|run| {
            if run.prior_unknown && !run.interactions.contains_key(p.interaction_id.as_str()) {
                run.interactions
                    .insert(s(&p.interaction_id), Interaction::opaque());
            }
            run.interactions.get_mut(p.interaction_id.as_str())
        });
        if let Some(x) = x {
            if x.opaque {
                pending.opaque = true;
            } else if x.kind != "tool_call" {
                pending.opaque = true;
                self.add(
                    codes::UNMATCHED_INTERACTION,
                    at,
                    e,
                    "/payload/interaction_id",
                    "a call resolution answers an interaction of another kind",
                );
            } else {
                x.call.resolve_requests.insert(s(&e.id));
                pending.reason = resolve_reason(&p, x);
                let expected = x.tool_call_id.clone();
                if p.tool_call_id.as_str() != expected {
                    self.add_expected(
                        codes::SCOPE_MISMATCH,
                        at,
                        e,
                        "/payload/tool_call_id",
                        "a call resolution names a call its interaction is not bound to",
                        &expected,
                        p.tool_call_id.as_str(),
                        &[p.interaction_id.as_str()],
                    );
                }
            }
        } else {
            pending.reason = ResolveReason::UNKNOWN_INTERACTION;
        }
        self.pending_resolves.insert(s(&e.id), pending);
    }

    pub(super) fn control_resolve_response(&mut self, at: (usize, usize), e: &Envelope) {
        let p: ActionCallResolveResponse = payload(e);
        self.feature(at, e, "tools");
        let Some(pending) = self.pending_resolves.get(&os(&e.in_reply_to)) else {
            return;
        };
        let reason = pending.reason.clone();
        let request = self
            .requests
            .get(&os(&e.in_reply_to))
            .map(|r| payload::<ActionCallResolveRequest>(&r.envelope));
        let mut x = self
            .runs
            .get_mut(&pending.run)
            .and_then(|r| r.interactions.get_mut(&pending.interaction));
        if let Some(x) = x.as_deref_mut() {
            if x.opaque {
                if p.accepted && pending.arm == RESOLVE_ARM_ACKNOWLEDGE {
                    x.call.acked = true;
                }
                return;
            }
            if pending.opaque {
                return;
            }
        } else if pending.opaque {
            return;
        }
        if p.accepted {
            if let Some(x) = x {
                if pending.arm == RESOLVE_ARM_ACKNOWLEDGE {
                    x.call.acked = true;
                } else if matches!(pending.arm.as_str(), RESOLVE_ARM_RESULT | RESOLVE_ARM_ERROR) {
                    x.call.accepted_arm = pending.arm.clone();
                    x.call.accepted_payload = request.and_then(|request| {
                        if pending.arm == RESOLVE_ARM_RESULT {
                            request
                                .result
                                .as_ref()
                                .and_then(|v| serde_json::from_str(v.get()).ok())
                        } else {
                            request.error.and_then(|v| serde_json::to_value(v).ok())
                        }
                    });
                    x.call.settlements.insert(s(&e.id));
                }
            }
            if !reason.is_empty() {
                self.add_expected(
                    reason_code(&reason),
                    at,
                    e,
                    "/payload/accepted",
                    "a resolution the interaction's state forbids was accepted",
                    reason.as_str(),
                    "accepted",
                    &[],
                );
            }
            return;
        }
        if reason.is_empty() {
            self.add(
                codes::UNMATCHED_INTERACTION,
                at,
                e,
                "/payload/accepted",
                "a valid call resolution was refused",
            );
        } else if p.reason != reason {
            self.add_expected(
                reason_code(&reason),
                at,
                e,
                "/payload/reason",
                "a refusal must name the highest-ranked condition its request satisfies",
                reason.as_str(),
                p.reason.as_str(),
                &[],
            );
        } else if reason == ResolveReason::ALREADY_RESOLVED {
            let settlement = p
                .details
                .as_ref()
                .map(|d| os(&d.settlement_id))
                .unwrap_or_default();
            if x.is_none_or(|x| !x.call.settlements.contains(&settlement)) {
                self.add(codes::UNMATCHED_INTERACTION, at, e, "/payload/details/settlement_id", "an already_resolved refusal must name an observed settlement for its interaction");
            }
        }
    }
}

impl State {
    fn control_call_owner(&mut self, at: (usize, usize), e: &Envelope, p: &ActionCallPayload) {
        let owner = self
            .provided
            .get(p.session_id.as_str())
            .and_then(|tools| tools.iter().find(|t| t.name == p.name))
            .map(|t| s(&t.execution_owner))
            .or_else(|| {
                self.catalog_owners
                    .get(p.session_id.as_str())
                    .filter(|(revision, _)| revision == &self.current_capability)
                    .and_then(|(_, owners)| owners.get(&p.name))
                    .cloned()
            })
            .or_else(|| self.descriptor_owners.get(&p.name).cloned());
        if let Some(owner) = owner.filter(|owner| owner != p.execution_owner.as_str()) {
            self.add_expected(
                codes::WRONG_TOOL_OWNER,
                at,
                e,
                "/payload/execution_owner",
                "a call names an owner other than the catalog in force records",
                &owner,
                p.execution_owner.as_str(),
                &[&p.name],
            );
        }
    }
}
