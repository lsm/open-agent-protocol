use std::collections::{HashMap, HashSet};

use oap_protocol::*;

use super::{os, payload, s, State};
use crate::codes;

#[derive(Default)]
pub(super) struct ToolTrack {
    status: String,
    owner: String,
    source: String,
    pub(super) interaction: String,
}

#[derive(Default)]
pub(super) struct Interaction {
    pub(super) kind: &'static str,
    pub(super) requested_by: String,
    pub(super) responded_by: String,
    allow_cancel: bool,
    choices: HashSet<String>,
    questions: Vec<InputQuestion>,
    pub(super) tool_call_id: String,
    pub(super) resolved: bool,
    pub(super) opened_at: u64,
    pub(super) resolved_at: Option<u64>,
    pub(super) opaque: bool,
    pub(super) call: super::control_tools::Call,
}

impl Interaction {
    pub(super) fn unresolved(&self) -> bool {
        !self.resolved
    }

    pub(super) fn opaque() -> Self {
        Self {
            opaque: true,
            ..Self::default()
        }
    }
}

fn tool_terminal(status: &str) -> bool {
    matches!(status, "completed" | "failed" | "cancelled")
}

fn tool_step(t: &EnvelopeType) -> Option<&'static str> {
    Some(match t.as_str() {
        "action.call.requested" => "requested",
        "action.call.started" => "started",
        "action.call.progress" => "progress",
        "action.call.completed" => "completed",
        "action.call.failed" => "failed",
        "action.call.cancelled" => "cancelled",
        _ => return None,
    })
}

fn interaction_fields(e: &Envelope, kind: &str) -> (String, String, String) {
    let t = &e.envelope_type;
    if kind == "permission" {
        if *t == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
            let p: PermissionResolveRequest = payload(e);
            return (s(&p.interaction_id), s(&p.requested_by), s(&p.responded_by));
        }
        let p: PermissionResolvedPayload = payload(e);
        return (s(&p.interaction_id), s(&p.requested_by), s(&p.responded_by));
    }
    if *t == EnvelopeType::USER_INPUT_RESOLVE_REQUEST {
        let p: UserInputResolveRequest = payload(e);
        return (s(&p.interaction_id), s(&p.requested_by), s(&p.responded_by));
    }
    if *t == EnvelopeType::USER_INPUT_CANCEL_REQUEST {
        let p: UserInputCancelRequest = payload(e);
        return (s(&p.interaction_id), s(&p.requested_by), s(&p.responded_by));
    }
    let p: UserInputResolvedPayload = payload(e);
    (s(&p.interaction_id), s(&p.requested_by), s(&p.responded_by))
}

type Found = Vec<(&'static str, &'static str, String, String, String, String)>;

fn found(
    list: &mut Found,
    code: &'static str,
    pointer: &'static str,
    message: &str,
    expected: &str,
    actual: &str,
    related: &str,
) {
    list.push((
        code,
        pointer,
        message.to_owned(),
        expected.to_owned(),
        actual.to_owned(),
        related.to_owned(),
    ));
}

fn answer_findings(
    list: &mut Found,
    id: &str,
    questions: &[InputQuestion],
    answers: &[InputAnswer],
) {
    let offered: HashMap<&str, &InputQuestion> =
        questions.iter().map(|q| (q.id.as_str(), q)).collect();
    let mut answered = HashSet::new();
    let pointer = "/payload/answers";
    for answer in answers {
        let Some(question) = offered.get(answer.question_id.as_str()) else {
            found(
                list,
                codes::UNMATCHED_INTERACTION,
                pointer,
                "answer names a question the prompt did not offer",
                "offered question",
                &answer.question_id,
                id,
            );
            continue;
        };
        if !answered.insert(answer.question_id.as_str()) {
            found(
                list,
                codes::UNMATCHED_INTERACTION,
                pointer,
                "question was answered more than once",
                "one answer per question",
                &answer.question_id,
                id,
            );
            continue;
        }
        let options: HashSet<&str> = question.options.iter().map(|o| o.id.as_str()).collect();
        match question.kind.as_str() {
            "text"
                if !answer.selected_option_ids.is_empty()
                    || question.required && answer.text.is_empty() =>
            {
                found(
                    list,
                    codes::UNMATCHED_INTERACTION,
                    pointer,
                    "answer does not satisfy its text question",
                    "the text arm; non-empty when required",
                    "",
                    id,
                )
            }
            "single_choice" | "multi_choice" => {
                if answer.selected_option_ids.is_empty() {
                    found(
                        list,
                        codes::UNMATCHED_INTERACTION,
                        pointer,
                        "choice answer selects no option",
                        "offered option",
                        "",
                        id,
                    );
                }
                if question.kind == InputQuestionKind::SINGLE_CHOICE
                    && answer.selected_option_ids.len() > 1
                {
                    let count = answer.selected_option_ids.len().to_string();
                    found(
                        list,
                        codes::UNMATCHED_INTERACTION,
                        pointer,
                        "single-choice answer selects more than one option",
                        "one option",
                        &count,
                        id,
                    );
                }
                for option in answer
                    .selected_option_ids
                    .iter()
                    .filter(|o| !options.contains(o.as_str()))
                {
                    found(
                        list,
                        codes::UNMATCHED_INTERACTION,
                        pointer,
                        "answer selects an option the question did not offer",
                        "offered option",
                        option,
                        id,
                    );
                }
            }
            _ => {}
        }
    }
    for question in questions
        .iter()
        .filter(|q| q.required && !answered.contains(q.id.as_str()))
    {
        found(
            list,
            codes::UNMATCHED_INTERACTION,
            pointer,
            "required question was not answered",
            &question.id,
            "missing",
            id,
        );
    }
}

impl State {
    fn emit(&mut self, at: (usize, usize), e: &Envelope, list: Found) {
        for (code, pointer, message, expected, actual, related) in list {
            let related: Vec<&str> = if related.is_empty() {
                Vec::new()
            } else {
                vec![related.as_str()]
            };
            self.add_expected(code, at, e, pointer, &message, &expected, &actual, &related);
        }
    }

    pub(super) fn participants_from(&mut self, at: (usize, usize), e: &Envelope) {
        let t = &e.envelope_type;
        if *t == EnvelopeType::PROTOCOL_INITIALIZE_REQUEST {
            let p: InitializeRequest = payload(e);
            self.initialized = true;
            if let Some(participant) = p.participant {
                self.control_participant = s(&participant.id);
                self.participants.insert(s(&participant.id));
            }
        } else if *t == EnvelopeType::PROTOCOL_INITIALIZE_RESPONSE {
            let p: InitializeResponse = payload(e);
            self.initialized = true;
            self.participants.insert(p.endpoint.id.as_str().to_owned());
            self.agent_participant = p.endpoint.id.as_str().to_owned();
            let reply = os(&e.in_reply_to);
            let Some(request) = self
                .requests
                .get(&reply)
                .map(|r| payload::<InitializeRequest>(&r.envelope))
            else {
                return;
            };
            if let Some(participant) = request.participant {
                self.participants.insert(s(&participant.id));
            }
            let mut list = Found::new();
            if !request.protocol_versions.is_empty()
                && !request.protocol_versions.contains(&p.protocol_version)
            {
                found(
                    &mut list,
                    codes::SCOPE_MISMATCH,
                    "/payload/protocol_version",
                    "initialize response selects a protocol version the request did not offer",
                    &request.protocol_versions.join(","),
                    &p.protocol_version,
                    &reply,
                );
            }
            if !request.profiles.is_empty() && !request.profiles.contains(&p.profile) {
                found(
                    &mut list,
                    codes::SCOPE_MISMATCH,
                    "/payload/profile",
                    "initialize response selects a profile the request did not offer",
                    &request.profiles.join(","),
                    &p.profile,
                    &reply,
                );
            }
            self.emit(at, e, list);
        } else if *t == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
            self.resolution_request(at, e, "permission");
        } else if *t == EnvelopeType::USER_INPUT_RESOLVE_REQUEST
            || *t == EnvelopeType::USER_INPUT_CANCEL_REQUEST
        {
            self.resolution_request(at, e, "input");
        } else if *t == EnvelopeType::ACTION_CALL_RESOLVE_REQUEST {
            self.control_resolve_request(at, e);
        } else if *t == EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE {
            self.control_resolve_response(at, e);
        }
    }

    fn participant(&self, list: &mut Found, id: &str, pointer: &'static str) {
        if self.initialized && !self.participants.contains(id) {
            found(
                list,
                codes::UNKNOWN_PARTICIPANT,
                pointer,
                "participant was not declared during initialization",
                "initialized participant",
                id,
                "",
            );
        }
    }

    pub(super) fn live_run_event(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        let t = &e.envelope_type;
        let gated = if tool_step(t).is_some() {
            Some("tools")
        } else if *t == EnvelopeType::ACTION_PERMISSION_REQUESTED
            || *t == EnvelopeType::ACTION_PERMISSION_RESOLVED
        {
            Some("permissions")
        } else if *t == EnvelopeType::USER_INPUT_REQUESTED
            || *t == EnvelopeType::USER_INPUT_RESOLVED
        {
            Some("user_input")
        } else if *t == EnvelopeType::RUN_COMPACTION_STARTED
            || *t == EnvelopeType::RUN_COMPACTION_ENDED
        {
            Some(FEATURE_RUN_COMPACTION)
        } else {
            None
        };
        if let Some(name) = gated {
            self.feature(at, e, name);
        }
        if let Some(step) = tool_step(t) {
            self.tool(at, e, run_id, step);
            self.control_call_event(at, e, run_id, step);
            match step {
                "requested" => self.record_call_choice(e, run_id),
                "completed" | "failed" | "cancelled" => self.check_call_settlement(at, e, run_id),
                _ => {}
            }
        } else if *t == EnvelopeType::ACTION_PERMISSION_REQUESTED {
            self.interaction_requested(at, e, run_id, "permission");
        } else if *t == EnvelopeType::USER_INPUT_REQUESTED {
            self.interaction_requested(at, e, run_id, "input");
        } else if *t == EnvelopeType::ACTION_PERMISSION_RESOLVED {
            self.interaction_resolved(at, e, run_id, "permission");
        } else if *t == EnvelopeType::USER_INPUT_RESOLVED {
            self.interaction_resolved(at, e, run_id, "input");
        }
    }

    pub(super) fn pending_at_terminal(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        let Some(run) = self.runs.get(run_id) else {
            return;
        };
        let tools: Vec<(String, String)> = run
            .tools
            .iter()
            .filter(|(_, track)| !tool_terminal(&track.status))
            .map(|(id, track)| (id.clone(), track.status.clone()))
            .collect();
        let interactions: Vec<String> = run
            .interactions
            .iter()
            .filter(|(_, x)| !x.resolved)
            .map(|(id, _)| id.clone())
            .collect();
        for (id, status) in tools {
            self.add_expected(
                codes::PENDING_TOOL_AT_TERMINAL,
                at,
                e,
                "/type",
                "run terminated with a pending tool call",
                "terminal tool",
                &status,
                &[&id],
            );
        }
        for id in interactions {
            let mut d = self.diagnostic(
                codes::PENDING_INTERACTION_AT_TERMINAL,
                at,
                e,
                "/type",
                "run terminated with a pending interaction",
            );
            d.related_ids = vec![id];
            self.diagnostics.push(d);
        }
    }

    fn tool(&mut self, at: (usize, usize), e: &Envelope, run_id: &str, next: &'static str) {
        let p: ActionCallPayload = payload(e);
        self.check_scope(at, e, p.session_id.as_str(), p.run_id.as_str());
        let mut list = Found::new();
        let (call, envelope_call) = (s(&p.tool_call_id), os(&e.tool_call_id));
        if envelope_call != call {
            found(
                &mut list,
                codes::SCOPE_MISMATCH,
                "/payload/tool_call_id",
                "envelope and payload tool_call_id differ",
                &envelope_call,
                &call,
                "",
            );
        }
        let Some(run) = self.runs.get_mut(run_id) else {
            return;
        };
        let current = run.tools.get(&call).map(|track| track.status.clone());
        let known = current.is_some();
        let current = current.unwrap_or_default();
        let valid = match next {
            "requested" => !known,
            "started" => known && current == "requested",
            "cancelled" => known && !tool_terminal(&current),
            _ => known && (current == "started" || current == "progress"),
        };
        let mut plain = Vec::new();
        if !valid && !(!known && run.recovered) {
            let code = if !known && next != "requested" {
                codes::UNMATCHED_TOOL
            } else {
                codes::ILLEGAL_TOOL_TRANSITION
            };
            plain.push(code);
        }
        let owner = p.execution_owner.as_str().to_owned();
        let track = run.tools.entry(call.clone()).or_insert_with(|| ToolTrack {
            status: String::new(),
            owner: owner.clone(),
            source: p.source.clone(),
            interaction: String::new(),
        });
        if known {
            if !track.owner.is_empty() && owner != track.owner {
                found(
                    &mut list,
                    codes::SCOPE_MISMATCH,
                    "/payload/execution_owner",
                    "tool execution owner changed mid-lifecycle",
                    &track.owner,
                    &owner,
                    "",
                );
            }
            if !p.source.is_empty() && p.source != track.source {
                let expected = if track.source.is_empty() {
                    "no source".to_owned()
                } else {
                    track.source.clone()
                };
                found(
                    &mut list,
                    codes::UNMATCHED_TOOL_SOURCE,
                    "/payload/source",
                    "tool source changed mid-lifecycle",
                    &expected,
                    &p.source,
                    &call,
                );
            }
        }
        track.status = next.to_owned();
        for code in plain {
            self.add(
                code,
                at,
                e,
                "/tool_call_id",
                "illegal tool-call lifecycle transition",
            );
        }
        self.emit(at, e, list);
    }

    fn interaction_requested(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        run_id: &str,
        kind: &'static str,
    ) {
        let mut x = Interaction {
            kind,
            opened_at: e.sequence.unwrap_or_default(),
            ..Interaction::default()
        };
        let (id, session, run) = if kind == "permission" {
            let p: PermissionRequestedPayload = payload(e);
            x.choices = p
                .choices
                .iter()
                .filter(|c| !c.id.is_empty())
                .map(|c| c.id.clone())
                .collect();
            x.tool_call_id = os(&p.tool_call_id);
            (x.requested_by, x.responded_by) = (s(&p.requested_by), s(&p.responded_by));
            (s(&p.interaction_id), s(&p.session_id), s(&p.run_id))
        } else {
            let p: UserInputRequestedPayload = payload(e);
            x.allow_cancel = p.allow_cancel;
            x.questions = p.questions;
            x.tool_call_id = os(&p.tool_call_id);
            (x.requested_by, x.responded_by) = (s(&p.requested_by), s(&p.responded_by));
            (s(&p.interaction_id), s(&p.session_id), s(&p.run_id))
        };
        self.check_scope(at, e, &session, &run);
        let mut list = Found::new();
        let envelope_call = os(&e.tool_call_id);
        if envelope_call != x.tool_call_id {
            found(
                &mut list,
                codes::SCOPE_MISMATCH,
                "/payload/tool_call_id",
                "envelope and payload tool_call_id differ",
                &envelope_call,
                &x.tool_call_id,
                "",
            );
        }
        self.participant(&mut list, &x.requested_by, "/payload/requested_by");
        self.participant(&mut list, &x.responded_by, "/payload/responded_by");
        let duplicate = self
            .runs
            .get_mut(run_id)
            .is_some_and(|run| run.interactions.insert(id, x).is_some());
        if duplicate {
            self.add(
                codes::DUPLICATE_INTERACTION,
                at,
                e,
                "/payload",
                "interaction id was requested more than once",
            );
        }
        self.emit(at, e, list);
    }

    fn resolution_request(&mut self, at: (usize, usize), e: &Envelope, kind: &'static str) {
        let (id, requested, responded) = interaction_fields(e, kind);
        let run_id = os(&e.run_id);
        let Some(run) = self.runs.get_mut(&run_id) else {
            self.add(
                codes::UNMATCHED_INTERACTION,
                at,
                e,
                "/payload",
                "resolution has no pending interaction",
            );
            return;
        };
        if !run.interactions.contains_key(&id) {
            if run.prior_unknown {
                run.interactions.insert(id, Interaction::opaque());
            } else {
                self.add(
                    codes::UNMATCHED_INTERACTION,
                    at,
                    e,
                    "/payload",
                    "resolution has no pending interaction",
                );
            }
            return;
        }
        let Some(x) = run.interactions.get(&id) else {
            return;
        };
        if x.opaque {
            return;
        }
        let mut list = Found::new();
        let mut plain = Vec::new();
        if x.kind != kind {
            plain.push("resolution has the wrong interaction kind");
        }
        if e.envelope_type == EnvelopeType::USER_INPUT_CANCEL_REQUEST && !x.allow_cancel {
            plain.push("interaction was not opened for cancellation");
        }
        if e.envelope_type == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
            let p: PermissionResolveRequest = payload(e);
            if !p.choice_id.is_empty() && !x.choices.contains(&p.choice_id) {
                found(
                    &mut list,
                    codes::UNMATCHED_INTERACTION,
                    "/payload/choice_id",
                    "resolution selects a choice the permission did not offer",
                    "offered choice",
                    &p.choice_id,
                    &id,
                );
            }
        }
        if e.envelope_type == EnvelopeType::USER_INPUT_RESOLVE_REQUEST {
            let p: UserInputResolveRequest = payload(e);
            answer_findings(&mut list, &id, &x.questions, &p.answers);
        }
        if responded != x.responded_by || (!requested.is_empty() && requested != x.requested_by) {
            found(
                &mut list,
                codes::WRONG_INTERACTION_RESPONDER,
                "/payload/responded_by",
                "only the declared responder may resolve an interaction",
                &x.responded_by,
                &responded,
                &id,
            );
        }
        for message in plain {
            self.add(codes::UNMATCHED_INTERACTION, at, e, "/payload", message);
        }
        self.emit(at, e, list);
    }

    fn interaction_resolved(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        run_id: &str,
        kind: &'static str,
    ) {
        let (id, requested, responded) = interaction_fields(e, kind);
        let Some(run) = self.runs.get_mut(run_id) else {
            return;
        };
        if !run.interactions.contains_key(&id) {
            if !run.prior_unknown {
                self.add(
                    codes::UNMATCHED_INTERACTION,
                    at,
                    e,
                    "/payload",
                    "resolution event has no pending interaction",
                );
                return;
            }
            run.interactions.insert(id.clone(), Interaction::opaque());
        }
        let Some(x) = run.interactions.get_mut(&id) else {
            return;
        };
        let duplicate = x.resolved;
        x.resolved = true;
        x.resolved_at = x.resolved_at.or(e.sequence);
        let mut list = Found::new();
        if !x.opaque {
            if responded != x.responded_by || requested != x.requested_by {
                found(
                    &mut list,
                    codes::WRONG_INTERACTION_RESPONDER,
                    "/payload/responded_by",
                    "resolution ownership differs from request",
                    &x.responded_by,
                    &responded,
                    &id,
                );
            }
            if x.kind != kind {
                found(
                    &mut list,
                    codes::UNMATCHED_INTERACTION,
                    "/payload",
                    "resolution event has the wrong interaction kind",
                    x.kind,
                    kind,
                    &id,
                );
            }
            let envelope_call = os(&e.tool_call_id);
            if !envelope_call.is_empty() && envelope_call != x.tool_call_id {
                found(
                    &mut list,
                    codes::SCOPE_MISMATCH,
                    "/tool_call_id",
                    "resolution envelope reassigns the request's tool binding",
                    &x.tool_call_id,
                    &envelope_call,
                    "",
                );
            }
            if kind == "input" {
                let p: UserInputResolvedPayload = payload(e);
                if p.status == UserInputResolutionStatus::SUBMITTED {
                    answer_findings(&mut list, &id, &x.questions, &p.answers);
                }
            } else {
                let p: PermissionResolvedPayload = payload(e);
                if p.outcome == InteractionOutcome::RESOLVED
                    && !p.choice_id.is_empty()
                    && !x.choices.contains(&p.choice_id)
                {
                    found(
                        &mut list,
                        codes::UNMATCHED_INTERACTION,
                        "/payload/choice_id",
                        "resolution event selects a choice the permission did not offer",
                        "offered choice",
                        &p.choice_id,
                        &id,
                    );
                }
                let payload_call = os(&p.tool_call_id);
                if !payload_call.is_empty() && payload_call != x.tool_call_id {
                    found(
                        &mut list,
                        codes::SCOPE_MISMATCH,
                        "/payload/tool_call_id",
                        "resolution payload reassigns the request's tool binding",
                        &x.tool_call_id,
                        &payload_call,
                        "",
                    );
                }
            }
        }
        if duplicate {
            self.add(
                codes::DUPLICATE_INTERACTION,
                at,
                e,
                "/payload",
                "interaction was resolved more than once",
            );
        }
        self.emit(at, e, list);
    }
}

impl State {
    pub(super) fn record_features(&mut self, e: &Envelope) {
        let t = &e.envelope_type;
        if *t == EnvelopeType::CAPABILITIES_RESPONSE {
            let p: CapabilitiesResponse = payload(e);
            let mut keys: HashSet<&String> = p.features.keys().collect();
            keys.extend(p.layers.values().flat_map(|layer| layer.features.keys()));
            self.features = keys
                .into_iter()
                .filter_map(|key| {
                    p.effective_support(key)
                        .map(|support| (key.clone(), support.level.clone()))
                })
                .collect();
        }
    }

    pub(super) fn feature(&mut self, at: (usize, usize), e: &Envelope, name: &str) {
        let keys = [
            name.to_owned(),
            format!("session.message.{name}"),
            format!("agent_control.{name}"),
            format!("action.{name}"),
        ];
        if !self.settling && (self.current_capability.is_empty() || self.capabilities_stale) {
            self.add(
                codes::UNAVAILABLE_CAPABILITY,
                at,
                e,
                "/type",
                "optional feature requires a current capability descriptor",
            );
            return;
        }
        let revision = e.capability_revision.clone().unwrap_or_default();
        if !self.settling && revision != self.current_capability {
            let current = self.current_capability.clone();
            self.add_expected(
                codes::STALE_CAPABILITY_REVISION,
                at,
                e,
                "/capability_revision",
                "optional feature must cite the active capability descriptor",
                &current,
                &revision,
                &[],
            );
            return;
        }
        let Some(level) = keys.iter().find_map(|key| self.features.get(key)).cloned() else {
            self.add(
                codes::UNAVAILABLE_CAPABILITY,
                at,
                e,
                "/type",
                "optional feature was not affirmatively advertised",
            );
            return;
        };
        match level.as_str() {
            "native" | "emulated" | "degraded" => {}
            "unavailable" => self.add(codes::UNAVAILABLE_CAPABILITY, at, e, "/type", "event uses capability declared unavailable"),
            other => self.add_expected(codes::UNAVAILABLE_CAPABILITY, at, e, "/type", "event uses capability whose support level is not one this revision recognises; an unknown level is unavailable", "native, emulated, or degraded", other, &[]),
        }
    }

    pub(super) fn feature_for(&mut self, at: (usize, usize), e: &Envelope) {
        if super::runs::RUN_EVENTS.contains(&e.envelope_type) {
            return;
        }
        let t = e.envelope_type.as_str();
        let name = if t.starts_with("action.call.") && !t.starts_with("action.call.resolve") {
            "tools"
        } else if t.starts_with("action.permission.") {
            "permissions"
        } else if t.starts_with("user.input.") {
            "user_input"
        } else if *t == *EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST.as_str() {
            let delivery = payload::<MessageSubmitRequest>(e).delivery;
            if ["auto", "queue", "steer"].contains(&delivery.as_str()) {
                return;
            }
            let key = format!("delivery.{}", delivery.as_str());
            self.feature(at, e, &key);
            return;
        } else if *t == *EnvelopeType::SESSION_COMPACT_REQUEST.as_str() {
            FEATURE_SESSION_COMPACT
        } else if *t == *EnvelopeType::MODELS_RESPONSE.as_str() {
            FEATURE_MODELS_LIST
        } else if *t == *EnvelopeType::SESSION_LIST_RESPONSE.as_str() {
            FEATURE_SESSION_LIST
        } else if *t == *EnvelopeType::TRANSCRIPT_LOAD_RESPONSE.as_str() {
            FEATURE_TRANSCRIPT_LOAD
        } else {
            return;
        };
        self.feature(at, e, name);
    }
}
