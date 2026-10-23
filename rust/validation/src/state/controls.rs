use std::cmp::Ordering;
use std::collections::{BTreeMap, HashMap, HashSet};
use std::rc::Rc;

use oap_protocol::*;
use serde_json::Value;

use super::queue::Window;
use super::{os, payload, s, State};
use crate::{codes, OutputSchema};

const RUNG_CAPABILITY: u8 = 1;
const RUNG_DEGRADATION: u8 = 2;
const RUNG_UNSATISFIABLE: u8 = 3;

const UNSUPPORTED_FEATURE: &str = "unsupported_feature";
const CAPABILITY_DEGRADED: &str = "capability_degraded";
const MODEL_NOT_FOUND: &str = "model_not_found";
const REFUSED_BY_POLICY: &str = "refused_by_policy";
const CONTROL_UNADVERTISED: &str = "unadvertised";
const CONTROL_UNSATISFIABLE: &str = "unsatisfiable";

#[derive(Clone, Debug)]
pub(super) struct Expectation {
    rung: u8,
    limit: bool,
    pub(super) key: String,
    pointer: String,
    code: &'static str,
    reason: &'static str,
    detail_name: &'static str,
    detail_value: String,
    pub(super) diagnostic: &'static str,
    pub(super) message: String,
}

impl Expectation {
    fn tool_list(degraded: bool) -> Self {
        let mut expectation = Self::delivery(FEATURE_TOOLS_LIST, degraded);
        expectation.pointer = "/payload".to_owned();
        expectation.message = if degraded {
            "a degraded catalog was requested without the caller's opt-in"
        } else {
            "a catalog was requested from an endpoint that has not affirmatively advertised one"
        }
        .to_owned();
        expectation
    }

    fn setting(key: &str, pointer: &str, degraded: bool) -> Self {
        let mut expectation = Self::delivery(key, degraded);
        expectation.pointer = pointer.to_owned();
        expectation.message = if degraded {
            "an open supplies a degraded session setting without the caller's opt-in"
        } else {
            "an open supplies a session setting not advertised for session_open"
        }
        .to_owned();
        expectation
    }

    pub(super) fn delivery(key: &str, degraded: bool) -> Self {
        let (rung, code, reason, diagnostic, message) = if degraded {
            (
                RUNG_DEGRADATION,
                CAPABILITY_DEGRADED,
                "",
                codes::DEGRADED_WITHOUT_OPTIN,
                "submission elects a degraded delivery without the caller's opt-in",
            )
        } else {
            (
                RUNG_CAPABILITY,
                UNSUPPORTED_FEATURE,
                CONTROL_UNADVERTISED,
                codes::UNAVAILABLE_CAPABILITY,
                "submission elects a delivery the endpoint has not affirmatively advertised",
            )
        };
        Self {
            rung,
            limit: false,
            key: key.to_owned(),
            pointer: "/payload/delivery".to_owned(),
            code,
            reason,
            detail_name: "feature",
            detail_value: key.to_owned(),
            diagnostic,
            message: message.to_owned(),
        }
    }

    fn order(&self, other: &Self) -> Ordering {
        self.limit
            .cmp(&other.limit)
            .then_with(|| self.rung.cmp(&other.rung))
            .then_with(|| self.key.cmp(&other.key))
            .then_with(|| pointer_order(&self.pointer, &other.pointer))
    }

    fn describe(&self) -> String {
        let mut description = self.code.to_owned();
        if !self.reason.is_empty() {
            description = format!("{description}/{}", self.reason);
        }
        if self.code == UNSUPPORTED_FEATURE && self.detail_name != "feature" {
            description = format!("{description} feature={}", self.key);
        }
        if !self.detail_name.is_empty() {
            description = format!("{description} {}={}", self.detail_name, self.detail_value);
        }
        description
    }

    fn conformed_by(&self, err: &ProtocolError) -> bool {
        if err.code != self.code {
            return false;
        }
        if self.code == UNSUPPORTED_FEATURE && detail(err, "feature") != Some(self.key.as_str()) {
            return false;
        }
        if !self.reason.is_empty() && detail(err, "reason") != Some(self.reason) {
            return false;
        }
        self.detail_name.is_empty()
            || detail(err, self.detail_name) == Some(self.detail_value.as_str())
    }
}

fn pointer_order(a: &str, b: &str) -> Ordering {
    let left: Vec<&str> = a.split('/').filter(|segment| !segment.is_empty()).collect();
    let right: Vec<&str> = b.split('/').filter(|segment| !segment.is_empty()).collect();
    for (l, r) in left.iter().zip(&right) {
        if l == r {
            continue;
        }
        return match (index(l), index(r)) {
            (Some(l), Some(r)) => l.cmp(&r),
            _ => l.cmp(r),
        };
    }
    left.len().cmp(&right.len())
}

fn index(segment: &str) -> Option<u64> {
    if segment.is_empty() || !segment.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    Some(segment.bytes().fold(0u64, |n, b| {
        n.wrapping_mul(10).wrapping_add(u64::from(b - b'0'))
    }))
}

fn detail<'a>(err: &'a ProtocolError, name: &str) -> Option<&'a str> {
    err.details.get(name).and_then(Value::as_str)
}

fn describe_refusal(err: &ProtocolError) -> String {
    let mut description = err.code.clone();
    if let Some(reason) = detail(err, "reason") {
        description = format!("{description}/{reason}");
    }
    for name in ["feature", "model_id", "tool", "field", "source"] {
        if let Some(value) = detail(err, name) {
            description = format!("{description} {name}={value}");
        }
    }
    description
}

pub(super) fn affirmative(level: &SupportLevel) -> bool {
    *level == SupportLevel::NATIVE
        || *level == SupportLevel::EMULATED
        || *level == SupportLevel::DEGRADED
}

fn control_pointer(key: &str) -> &'static str {
    match key {
        FEATURE_MODEL_SELECTION => "/payload/model_id",
        FEATURE_INSTRUCTIONS => "/payload/instructions",
        FEATURE_TOOL_SELECTION => "/payload/tool_choice",
        FEATURE_STRUCTURED_OUTPUT => "/payload/output_schema",
        _ => "/payload",
    }
}

#[derive(Clone, Default)]
pub(super) struct Controls {
    present: bool,
    model_present: bool,
    model: String,
    choice: Option<ToolChoice>,
    catalog: Vec<String>,
    catalog_known: bool,
    schema: Option<Rc<OutputSchema>>,
    fixed_result: Option<String>,
    calls: BTreeMap<String, (String, bool)>,
}

pub(super) struct PendingOpenSettings {
    level: ReasoningLevel,
    policy: Option<CompactionPolicy>,
    expectations: Vec<Expectation>,
}

pub(super) struct PendingSubscribe {
    expectation: Option<Expectation>,
}

fn refuses_subscribe(err: &ProtocolError) -> bool {
    if err.code != UNSUPPORTED_FEATURE && err.code != CAPABILITY_DEGRADED {
        return false;
    }
    detail(err, "feature").is_none_or(|named| named == FEATURE_OPEN_SUBSCRIBE)
}

#[derive(Default)]
pub(super) struct PendingSubmit {
    pub(super) expectation: Option<Expectation>,
    pub(super) queue: Option<Window>,
    pub(super) session: String,
    satisfiable: HashSet<String>,
    controls: Controls,
}

pub(super) fn duplicate_tool_name(catalog: &[String]) -> Option<&str> {
    let mut seen = HashSet::new();
    catalog
        .iter()
        .find(|name| !seen.insert(name.as_str()))
        .map(String::as_str)
}

pub(super) fn collect_catalog(p: &CapabilitiesResponse) -> Vec<String> {
    let mut names: Vec<String> = p.tools.iter().map(|tool| tool.name.clone()).collect();
    for layer in p.layers.values() {
        names.extend(layer.tools.iter().map(|tool| tool.name.clone()));
    }
    names
}

fn publishes_catalog(e: &Envelope) -> bool {
    let Some(raw) = e.payload.as_ref() else {
        return false;
    };
    let Ok(members) = serde_json::from_str::<HashMap<String, Value>>(raw.get()) else {
        return false;
    };
    if members.contains_key("tools") {
        return true;
    }
    members
        .get("layers")
        .and_then(Value::as_object)
        .is_some_and(|layers| layers.values().any(|layer| layer.get("tools").is_some()))
}

const OPEN_LEVEL_REFUSALS: [&str; 5] = [
    "session_exists",
    "unknown_adapter",
    "session_closed",
    "stale_capabilities",
    "unknown_session",
];

#[derive(Clone, Default)]
pub(super) struct PendingToolList {
    expectation: Option<Expectation>,
    honour: bool,
}

impl PendingToolList {
    pub(super) fn gated(&self) -> bool {
        self.expectation.is_some()
    }
}

impl State {
    fn tools_list_gate(&mut self, at: (usize, usize), e: &Envelope) {
        let p: ToolsListRequest = payload(e);
        let mut pending = PendingToolList::default();
        if let Some(level) = self.control_descriptor(at, e, FEATURE_TOOLS_LIST) {
            if !affirmative(&level) {
                pending.expectation = Some(Expectation::tool_list(false));
            } else if level == SupportLevel::DEGRADED && !p.allows_degraded(FEATURE_TOOLS_LIST) {
                pending.expectation = Some(Expectation::tool_list(true));
            } else {
                pending.honour = true;
            }
        }
        self.pending_tool_lists.insert(s(&e.id), pending);
    }

    fn tools_list_admission(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let expectation = self
            .pending_tool_lists
            .get(&reply)
            .and_then(|pending| pending.expectation.clone());
        if let Some(expectation) = expectation {
            self.add_expected(
                expectation.diagnostic,
                at,
                e,
                "/payload",
                &expectation.message,
                &format!("a typed refusal naming {}", expectation.key),
                "a served catalog",
                &[&reply],
            );
        }
    }

    fn tools_list_refusal(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(pending) = self.pending_tool_lists.get(&reply).cloned() else {
            return;
        };
        let err = payload::<ErrorResponse>(e).error;
        if let Some(expectation) = pending.expectation {
            if !expectation.conformed_by(&err) {
                self.add_expected(
                    expectation.diagnostic,
                    at,
                    e,
                    "/payload/error",
                    "refusal does not tell the caller what to change",
                    &expectation.describe(),
                    &describe_refusal(&err),
                    &[&reply],
                );
            }
        } else if pending.honour {
            self.add_expected(
                codes::UNHONOURED_CAPABILITY, at, e, "/payload/error",
                "a catalog request within every disclosed constraint was refused by an endpoint advertising the catalog",
                "a served catalog", &describe_refusal(&err), &[&reply],
            );
        }
    }
}

impl Controls {
    pub(super) fn selects_model(&self) -> bool {
        self.model_present
    }
}

impl State {
    pub(super) fn controls_dispatch(&mut self, at: (usize, usize), e: &Envelope) {
        let t = &e.envelope_type;
        if *t == EnvelopeType::CAPABILITIES_RESPONSE {
            self.record_catalog(at, e);
            self.check_descriptor_catalog(at, e);
            self.record_limits(at, e);
            self.check_provide_pattern(at, e);
        } else if *t == EnvelopeType::CAPABILITIES_UPDATED {
            self.declared_sources.clear();
            self.descriptor_owners.clear();
        } else if *t == EnvelopeType::ACTION_TOOLS_LIST_REQUEST {
            self.tools_list_gate(at, e);
        } else if *t == EnvelopeType::ACTION_TOOLS_LIST_RESPONSE {
            self.tools_list_admission(at, e);
            self.check_tools_list_shape(at, e);
            let p: ToolsListResponse = payload(e);
            if let Some(session) = p.session_id {
                self.catalog_owners.insert(
                    s(&session),
                    (
                        e.capability_revision.clone().unwrap_or_default(),
                        p.tools
                            .iter()
                            .map(|tool| (tool.name.clone(), s(&tool.execution_owner)))
                            .collect(),
                    ),
                );
            }
        } else if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST {
            let p: MessageSubmitRequest = payload(e);
            self.submit_controls(at, e, &p, &[]);
        } else if *t == EnvelopeType::SESSION_OPEN_REQUEST {
            let p: SessionOpenRequest = payload(e);
            self.subscribe_gate(at, e, &p);
            self.provide_gate(at, e, &p);
            self.open_settings_gate(at, e, &p);
            if let Some(message) = &p.message {
                let submission = message.submit(p.session_id.clone().unwrap_or_default());
                let provided: Vec<String> = p.tools.iter().map(|tool| tool.name.clone()).collect();
                self.submit_controls(at, e, &submission, &provided);
            }
        } else if *t == EnvelopeType::SESSION_COMPACT_REQUEST {
            let p: SessionCompactRequest = payload(e);
            let session = s(&p.session_id);
            let pending = PendingSubmit {
                queue: Some(self.open_queue_window(&session, &p.delivery, false)),
                session: session.clone(),
                ..PendingSubmit::default()
            };
            self.pending_controls.insert(s(&e.id), pending);
            self.open_submit_window(&session, &s(&e.id));
        } else if *t == EnvelopeType::SESSION_OPEN_RESPONSE {
            self.settle_subscribe_admission(at, e);
            self.settle_provide_admission(at, e);
            self.open_settings_response(at, e);
            self.record_provided_tools(e);
        } else if *t == EnvelopeType::ERROR_RESPONSE {
            self.tools_list_refusal(at, e);
            self.settle_control_refusal(at, e);
            self.settle_provide_refusal(at, e);
            self.steer_refusal(at, e);
            self.open_settings_refusal(at, e);
            self.settle_subscribe_refusal(at, e);
        }
        if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE
            || *t == EnvelopeType::SESSION_COMPACT_RESPONSE
            || *t == EnvelopeType::ERROR_RESPONSE
            || *t == EnvelopeType::SESSION_OPEN_RESPONSE
        {
            self.close_submit_window(e);
        }
    }

    fn open_settings_gate(&mut self, at: (usize, usize), e: &Envelope, p: &SessionOpenRequest) {
        if p.reasoning_level.is_empty() && p.compaction_policy.is_none() {
            return;
        }
        let mut pending = PendingOpenSettings {
            level: p.reasoning_level.clone(),
            policy: p.compaction_policy.clone(),
            expectations: Vec::new(),
        };
        for (key, pointer, present) in [
            (
                FEATURE_SESSION_REASONING,
                "/payload/reasoning_level",
                !p.reasoning_level.is_empty(),
            ),
            (
                FEATURE_COMPACTION_POLICY,
                "/payload/compaction_policy",
                p.compaction_policy.is_some(),
            ),
        ] {
            if !present {
                continue;
            }
            let Some(level) = self.control_descriptor(at, e, key) else {
                break;
            };
            let disclosed = self
                .feature_supports
                .get(key)
                .is_some_and(|support| support.discloses_mode(MODE_SESSION_OPEN));
            if !affirmative(&level) || !disclosed {
                pending
                    .expectations
                    .push(Expectation::setting(key, pointer, false));
            } else if level == SupportLevel::DEGRADED && !p.allows_degraded(key) {
                pending
                    .expectations
                    .push(Expectation::setting(key, pointer, true));
            }
        }
        self.pending_open_settings.insert(s(&e.id), pending);
    }

    fn open_settings_response(&mut self, at: (usize, usize), e: &Envelope) {
        let Some(pending) = self.pending_open_settings.remove(&os(&e.in_reply_to)) else {
            return;
        };
        if !pending.expectations.is_empty() {
            for expectation in pending.expectations {
                self.add_expected(
                    expectation.diagnostic,
                    at,
                    e,
                    &expectation.pointer,
                    &expectation.message,
                    &format!("a typed refusal naming {}", expectation.key),
                    "an admitted open",
                    &[&os(&e.in_reply_to)],
                );
            }
            return;
        }
        let p: SessionState = payload(e);
        if !pending.level.is_empty()
            && !p.reasoning_level.is_empty()
            && p.reasoning_level != pending.level
        {
            self.add_expected(
                codes::SESSION_STATE_MISMATCH,
                at,
                e,
                "/payload/reasoning_level",
                "an admitted open runs at the reasoning level it was asked for, or refuses it",
                pending.level.as_str(),
                p.reasoning_level.as_str(),
                &[p.session_id.as_str()],
            );
        }
        if let (Some(asked), Some(reported)) = (&pending.policy, &p.compaction_policy) {
            if asked != reported {
                self.add_expected(
                    codes::SESSION_STATE_MISMATCH, at, e, "/payload/compaction_policy",
                    "an admitted open runs under the compaction policy it was asked for, or refuses it",
                    &serde_json::to_string(asked).unwrap(), &serde_json::to_string(reported).unwrap(),
                    &[p.session_id.as_str()],
                );
            }
        }
    }

    fn open_settings_refusal(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(expectations) = self
            .pending_open_settings
            .get(&reply)
            .map(|p| p.expectations.clone())
        else {
            return;
        };
        let err = payload::<ErrorResponse>(e).error;
        let (discharged, speaker) = self.attribute(&reply, &err);
        if !discharged {
            for expectation in expectations {
                self.refusal_misses(at, e, &expectation, &speaker, &err);
            }
        }
    }

    pub(super) fn record_catalog(&mut self, at: (usize, usize), e: &Envelope) {
        let p: CapabilitiesResponse = payload(e);
        self.feature_supports.clear();
        let mut keys: HashSet<&String> = p.features.keys().collect();
        keys.extend(p.layers.values().flat_map(|layer| layer.features.keys()));
        for key in keys {
            if let Some(support) = p.effective_support(key) {
                self.feature_supports.insert(key.clone(), support.clone());
            }
        }
        self.declared_sources = p
            .sources
            .iter()
            .chain(p.layers.values().flat_map(|layer| &layer.sources))
            .map(|source| source.id.clone())
            .collect();
        self.descriptor_owners = p
            .tools
            .iter()
            .chain(p.layers.values().flat_map(|layer| &layer.tools))
            .map(|tool| (tool.name.clone(), tool.execution_owner.as_str().to_owned()))
            .collect();
        self.catalog = collect_catalog(&p);
        self.catalog_known = publishes_catalog(e);
        self.catalog_ambiguous = duplicate_tool_name(&self.catalog).is_some();
        self.check_selection_modes(at, e, &p);
    }

    fn check_selection_modes(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &CapabilitiesResponse,
    ) {
        let disclosed = |scope: &str| scope == SCOPE_RUN || scope == SCOPE_SESSION;
        let expected = format!("{SCOPE_RUN} or {SCOPE_SESSION}");
        if let Some(support) = p.effective_support(FEATURE_TOOL_SELECTION) {
            if affirmative(&support.level)
                && !support.scope.is_empty()
                && !disclosed(&support.scope)
            {
                self.add_expected(
                    codes::UNDISCLOSED_SELECTION_SCOPE,
                    at,
                    e,
                    "/payload/features/run.tool_selection/scope",
                    "run.tool_selection discloses a scope that is not one the protocol defines",
                    &expected,
                    &support.scope,
                    &[],
                );
            }
        }
        let Some(support) = p.effective_support(FEATURE_MODEL_SELECTION) else {
            return;
        };
        if affirmative(&support.level) && !disclosed(&support.scope) {
            self.add_expected(
                codes::UNDISCLOSED_SELECTION_SCOPE,
                at,
                e,
                "/payload/features/run.model_selection/scope",
                "run.model_selection is advertised without disclosing how long a selection lives",
                &expected,
                &support.scope,
                &[],
            );
        }
    }

    pub(super) fn record_provided_tools(&mut self, e: &Envelope) {
        let Some(request) = self.requests.get(&os(&e.in_reply_to)) else {
            return;
        };
        if request.envelope_type != EnvelopeType::SESSION_OPEN_REQUEST {
            return;
        }
        let tools = payload::<SessionOpenRequest>(&request.envelope).tools;
        let session = s(&payload::<SessionState>(e).session_id);
        let order = self.provided.entry(session).or_default();
        for tool in tools {
            if !order.iter().any(|existing| existing.name == tool.name) {
                order.push(tool);
            }
        }
    }

    fn tool_catalog(&self, session: &str, provided: &[String]) -> Option<Vec<String>> {
        if !self.catalog_known {
            return None;
        }
        let mut catalog = self.catalog.clone();
        catalog.extend(
            self.provided
                .get(session)
                .into_iter()
                .flatten()
                .map(|tool| tool.name.clone()),
        );
        for name in provided {
            if !catalog.contains(name) {
                catalog.push(name.clone());
            }
        }
        Some(catalog)
    }

    pub(super) fn control_descriptor(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        key: &str,
    ) -> Option<SupportLevel> {
        if self.current_capability.is_empty() || self.capabilities_stale {
            self.add(
                codes::UNAVAILABLE_CAPABILITY,
                at,
                e,
                "/type",
                "optional feature requires a current capability descriptor",
            );
            return None;
        }
        let revision = e.capability_revision.clone().unwrap_or_default();
        if revision != self.current_capability {
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
            return None;
        }
        Some(
            self.features
                .get(key)
                .cloned()
                .unwrap_or(SupportLevel::UNAVAILABLE),
        )
    }

    pub(super) fn submit_controls(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &MessageSubmitRequest,
        provided: &[String],
    ) {
        let mut pending = PendingSubmit::default();
        let mut expectations = Vec::new();
        let controls = [
            (FEATURE_MODEL_SELECTION, p.model_id.is_some()),
            (FEATURE_INSTRUCTIONS, p.instructions.is_some()),
            (FEATURE_TOOL_SELECTION, p.tool_choice.is_some()),
            (FEATURE_STRUCTURED_OUTPUT, p.output_schema.is_some()),
        ];
        if controls.iter().any(|(_, present)| *present) {
            pending.controls.present = true;
            for (key, present) in controls {
                if !present {
                    continue;
                }
                let Some(level) = self.control_descriptor(at, e, key) else {
                    continue;
                };
                let expect = |rung, code, reason, diagnostic, message: &str| Expectation {
                    rung,
                    limit: false,
                    key: key.to_owned(),
                    pointer: control_pointer(key).to_owned(),
                    code,
                    reason,
                    detail_name: "feature",
                    detail_value: key.to_owned(),
                    diagnostic,
                    message: message.to_owned(),
                };
                if !affirmative(&level) {
                    expectations.push(expect(RUNG_CAPABILITY, UNSUPPORTED_FEATURE, CONTROL_UNADVERTISED, codes::UNAVAILABLE_CAPABILITY, "submission carries a control the endpoint has not affirmatively advertised"));
                } else if level == SupportLevel::DEGRADED && !p.allows_degraded(key) {
                    expectations.push(expect(
                        RUNG_DEGRADATION,
                        CAPABILITY_DEGRADED,
                        "",
                        codes::DEGRADED_WITHOUT_OPTIN,
                        "submission carries a degraded control without the caller's opt-in",
                    ));
                } else if p.delivery == RequestedDeliveryMode::STEER {
                    expectations.push(expect(
                        RUNG_UNSATISFIABLE,
                        UNSUPPORTED_FEATURE,
                        CONTROL_UNSATISFIABLE,
                        codes::UNSATISFIABLE_CONTROL,
                        "steer cannot change admitted run controls",
                    ));
                } else {
                    if key == FEATURE_TOOL_SELECTION {
                        self.duplicate_tool_names(at, e, p.session_id.as_str());
                    }
                    match self.unsatisfiable(key, p, &mut pending, provided) {
                        Err(defect) => expectations.push(*defect),
                        Ok(true) => {
                            pending.satisfiable.insert(key.to_owned());
                        }
                        Ok(false) => {}
                    }
                }
            }
        }
        let (window, delivery) = self.delivery_expectations(at, e, p);
        expectations.extend(delivery);
        pending.queue = Some(window);
        pending.session = s(&p.session_id);
        expectations.sort_by(Expectation::order);
        pending.expectation = expectations.into_iter().next();
        self.pending_controls.insert(s(&e.id), pending);
        self.open_submit_window(p.session_id.as_str(), &s(&e.id));
    }

    fn unsatisfiable(
        &self,
        key: &str,
        p: &MessageSubmitRequest,
        pending: &mut PendingSubmit,
        provided: &[String],
    ) -> Result<bool, Box<Expectation>> {
        let defect = |pointer: &str, name: &'static str, value: String, message: String| {
            Box::new(Expectation {
                rung: RUNG_UNSATISFIABLE,
                limit: false,
                key: key.to_owned(),
                pointer: pointer.to_owned(),
                code: UNSUPPORTED_FEATURE,
                reason: CONTROL_UNSATISFIABLE,
                detail_name: if value.is_empty() { "" } else { name },
                detail_value: value,
                diagnostic: codes::UNSATISFIABLE_CONTROL,
                message,
            })
        };
        let controls = &mut pending.controls;
        match key {
            FEATURE_MODEL_SELECTION => {
                controls.model_present = true;
                controls.model = p.model_id.clone().unwrap_or_default();
                if controls.model.is_empty() {
                    return Err(Box::new(Expectation {
                        rung: RUNG_UNSATISFIABLE,
                        limit: false,
                        key: key.to_owned(),
                        pointer: "/payload/model_id".to_owned(),
                        code: MODEL_NOT_FOUND,
                        reason: "",
                        detail_name: "model_id",
                        detail_value: String::new(),
                        diagnostic: codes::UNSATISFIABLE_CONTROL,
                        message: "submission carries an empty model_id, which no catalog can list"
                            .to_owned(),
                    }));
                }
                Ok(true)
            }
            FEATURE_INSTRUCTIONS => Ok(true),
            FEATURE_TOOL_SELECTION => {
                let policy = match p.tool_choice_policy() {
                    Err(err) => {
                        return Err(defect(
                            "/payload/tool_choice",
                            "",
                            String::new(),
                            format!("tool_choice is not the typed policy: {err}"),
                        ))
                    }
                    Ok(None) => {
                        return Err(defect(
                            "/payload/tool_choice",
                            "",
                            String::new(),
                            "tool_choice carries no typed policy".to_owned(),
                        ))
                    }
                    Ok(Some(policy)) => policy,
                };
                let catalog = self.tool_catalog(p.session_id.as_str(), provided);
                let known = catalog
                    .as_deref()
                    .is_some_and(|catalog| duplicate_tool_name(catalog).is_none());
                let catalog = catalog.unwrap_or_default();
                let found = policy.unsatisfiable(&catalog, known);
                controls.catalog = catalog;
                controls.catalog_known = known;
                controls.choice = Some(policy);
                match found {
                    Some(found) => Err(defect(
                        &found.pointer,
                        "tool",
                        found.tool,
                        found.reason.to_owned(),
                    )),
                    None => Ok(true),
                }
            }
            FEATURE_STRUCTURED_OUTPUT => {
                let raw = p
                    .output_schema
                    .as_ref()
                    .map(|raw| raw.get())
                    .unwrap_or_default();
                let compiled = OutputSchema::compile(raw).map_err(|err| {
                    defect(
                        "/payload/output_schema",
                        "field",
                        "output_schema".to_owned(),
                        err,
                    )
                })?;
                let fixed = self
                    .feature_supports
                    .get(key)
                    .and_then(|support| support.constraints.get(CONSTRAINT_FIXED_RESULT))
                    .map(|fixed| fixed.get().to_owned())
                    .filter(|fixed| {
                        serde_json::from_str::<serde_json::Map<String, Value>>(fixed).is_ok()
                    });
                if let Some(fixed) = &fixed {
                    if compiled.validate(fixed).is_err() {
                        return Err(defect(
                            "/payload/output_schema",
                            "field",
                            "output_schema".to_owned(),
                            "the disclosed fixed result cannot satisfy the requested schema"
                                .to_owned(),
                        ));
                    }
                }
                controls.schema = Some(Rc::new(compiled));
                controls.fixed_result = fixed;
                Ok(true)
            }
            _ => Ok(false),
        }
    }

    fn duplicate_tool_names(&mut self, at: (usize, usize), e: &Envelope, session: &str) {
        if self.catalog_ambiguous {
            return;
        }
        let Some(catalog) = self.tool_catalog(session, &[]) else {
            return;
        };
        if let Some(name) = duplicate_tool_name(&catalog).map(str::to_owned) {
            self.add_expected(
                codes::DUPLICATE_TOOL_NAME,
                at,
                e,
                "/payload/tool_choice",
                "the catalog this tool_choice is judged against lists two tools with one name",
                "one tool per name",
                &name,
                &[],
            );
        }
    }

    pub(super) fn settle_submit_admission(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        p: &MessageSubmitResponse,
    ) -> Controls {
        let reply = os(&e.in_reply_to);
        let Some(pending) = self.pending_controls.get(&reply) else {
            return Controls::default();
        };
        if let Some(expectation) = pending.expectation.clone() {
            let expected = format!("a typed refusal naming {}", expectation.key);
            self.add_expected(
                expectation.diagnostic,
                at,
                e,
                "/payload/admission",
                &expectation.message,
                &expected,
                p.admission.as_str(),
                &[&reply],
            );
            return Controls::default();
        }
        let controls = pending.controls.clone();
        if controls.model_present && p.model_id != controls.model {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/model_id",
                "admission reports a model other than the one it admitted",
                &controls.model,
                &p.model_id,
                &[&reply],
            );
        }
        controls
    }

    fn attribute(&self, reply: &str, err: &ProtocolError) -> (bool, Option<Expectation>) {
        let opening = self
            .requests
            .get(reply)
            .is_some_and(|request| request.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST);
        if opening && OPEN_LEVEL_REFUSALS.contains(&err.code.as_str()) {
            return (true, None);
        }
        let retained = [
            self.pending_controls
                .get(reply)
                .and_then(|pending| pending.expectation.as_ref()),
            self.pending_subscribes
                .get(reply)
                .and_then(|pending| pending.expectation.as_ref()),
            self.pending_provides
                .get(reply)
                .and_then(|pending| pending.expectation.as_ref()),
        ];
        let retained: Vec<&Expectation> = retained
            .into_iter()
            .flatten()
            .chain(
                self.pending_open_settings
                    .get(reply)
                    .into_iter()
                    .flat_map(|pending| &pending.expectations),
            )
            .collect();
        if retained
            .iter()
            .any(|expectation| expectation.conformed_by(err))
        {
            return (true, None);
        }
        let speaker = retained.into_iter().min_by(|a, b| a.order(b)).cloned();
        (false, speaker)
    }

    fn refusal_misses(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        expectation: &Expectation,
        speaker: &Option<Expectation>,
        err: &ProtocolError,
    ) {
        let owns = speaker
            .as_ref()
            .is_none_or(|speaker| speaker.order(expectation) == Ordering::Equal);
        if owns {
            let reply = os(&e.in_reply_to);
            self.add_expected(
                expectation.diagnostic,
                at,
                e,
                "/payload/error",
                "refusal does not tell the caller what to change",
                &expectation.describe(),
                &describe_refusal(err),
                &[&reply],
            );
        }
    }

    pub(super) fn settle_control_refusal(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(pending) = self.pending_controls.get(&reply) else {
            return;
        };
        let (expectation, satisfiable, window) = (
            pending.expectation.clone(),
            pending.satisfiable.clone(),
            pending.queue.clone(),
        );
        let err = payload::<ErrorResponse>(e).error;
        let (discharged, speaker) = self.attribute(&reply, &err);
        if discharged {
            return;
        }
        if let Some(expectation) = expectation {
            self.refusal_misses(at, e, &expectation, &speaker, &err);
            return;
        }
        let feature = detail(&err, "feature").unwrap_or_default();
        if err.code == UNSUPPORTED_FEATURE && satisfiable.contains(feature) {
            self.add_expected(
                codes::UNSATISFIABLE_CONTROL,
                at,
                e,
                "/payload/error",
                "refusal names a control the endpoint advertises and this request satisfies",
                "admission or a defect the refusal names",
                &describe_refusal(&err),
                &[&reply],
            );
            return;
        }
        if let Some(window) = window {
            self.settle_queue_refusal(at, e, &window, &err, &describe_refusal(&err));
        }
    }

    fn subscribe_gate(&mut self, at: (usize, usize), e: &Envelope, p: &SessionOpenRequest) {
        if !p.subscribe {
            return;
        }
        let Some(level) = self.control_descriptor(at, e, FEATURE_OPEN_SUBSCRIBE) else {
            return;
        };
        let expect = |rung, code, reason, diagnostic, message: &str| Expectation {
            rung,
            limit: false,
            key: FEATURE_OPEN_SUBSCRIBE.to_owned(),
            pointer: "/payload/subscribe".to_owned(),
            code,
            reason,
            detail_name: "feature",
            detail_value: FEATURE_OPEN_SUBSCRIBE.to_owned(),
            diagnostic,
            message: message.to_owned(),
        };
        let expectation = if !affirmative(&level) {
            Some(expect(RUNG_CAPABILITY, UNSUPPORTED_FEATURE, CONTROL_UNADVERTISED, codes::UNAVAILABLE_CAPABILITY, "an open elects subscribe against an endpoint that has not affirmatively advertised it"))
        } else if level == SupportLevel::DEGRADED && !p.allows_degraded(FEATURE_OPEN_SUBSCRIBE) {
            Some(expect(
                RUNG_DEGRADATION,
                CAPABILITY_DEGRADED,
                "",
                codes::DEGRADED_WITHOUT_OPTIN,
                "open elects a degraded subscribe without the caller's opt-in",
            ))
        } else {
            None
        };
        self.pending_subscribes
            .insert(s(&e.id), PendingSubscribe { expectation });
    }

    fn settle_subscribe_refusal(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(expectation) = self
            .pending_subscribes
            .get(&reply)
            .map(|pending| pending.expectation.clone())
        else {
            return;
        };
        let err = payload::<ErrorResponse>(e).error;
        let (discharged, speaker) = self.attribute(&reply, &err);
        self.pending_subscribes.remove(&reply);
        if discharged {
            return;
        }
        match expectation {
            Some(expectation) => self.refusal_misses(at, e, &expectation, &speaker, &err),
            None if refuses_subscribe(&err) => {
                self.add_expected(
                    codes::UNHONOURED_CAPABILITY,
                    at,
                    e,
                    "/payload/error",
                    "an open electing subscribe was refused for a key the endpoint advertises",
                    "a session opened with its subscription",
                    &describe_refusal(&err),
                    &[&reply],
                );
            }
            None => {}
        }
    }

    fn settle_subscribe_admission(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(pending) = self.pending_subscribes.remove(&reply) else {
            return;
        };
        if let Some(expectation) = pending.expectation {
            let expected = format!("a typed refusal naming {}", expectation.key);
            self.add_expected(
                expectation.diagnostic,
                at,
                e,
                "/payload",
                &expectation.message,
                &expected,
                "an admitted open",
                &[&reply],
            );
        }
    }

    pub(super) fn record_call_choice(&mut self, e: &Envelope, run_id: &str) {
        let p: ActionCallPayload = payload(e);
        let Some(run) = self.runs.get_mut(run_id) else {
            return;
        };
        if p.name.is_empty() || p.tool_call_id.as_str().is_empty() {
            return;
        }
        let controls = &mut run.controls;
        let excluded = match (&controls.choice, controls.present) {
            (Some(choice), true) => {
                !choice.permits(&p.name, &controls.catalog, controls.catalog_known)
            }
            _ => false,
        };
        controls
            .calls
            .insert(s(&p.tool_call_id), (p.name, excluded));
    }

    pub(super) fn check_call_settlement(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        let p: ActionCallPayload = payload(e);
        let call = s(&p.tool_call_id);
        let Some((_, excluded)) = self
            .runs
            .get_mut(run_id)
            .and_then(|run| run.controls.calls.remove(&call))
        else {
            return;
        };
        let code = p
            .error
            .as_ref()
            .map(|err| err.code.clone())
            .unwrap_or_default();
        let refused = code == REFUSED_BY_POLICY;
        if excluded && !refused {
            let outcome = if code.is_empty() {
                e.envelope_type.as_str().to_owned()
            } else {
                format!("{} with error.code {code}", e.envelope_type.as_str())
            };
            self.add_expected(codes::UNAPPLIED_CONTROL, at, e, "/type", "a call to a tool the admitted tool_choice excludes settled without a policy refusal", &format!("action.call.failed with error.code {REFUSED_BY_POLICY}"), &outcome, &[&call, run_id]);
        } else if !excluded && refused {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/error/code",
                "a call to a tool the admitted tool_choice permits claimed a policy refusal",
                &format!("any code but {REFUSED_BY_POLICY}"),
                REFUSED_BY_POLICY,
                &[&call, run_id],
            );
        }
    }

    pub(super) fn settle_run_controls(&mut self, at: (usize, usize), e: &Envelope, run_id: &str) {
        let Some(run) = self.runs.get(run_id) else {
            return;
        };
        let controls = run.controls.clone();
        if e.envelope_type == EnvelopeType::RUN_COMPLETED && controls.present {
            self.check_completed_controls(at, e, run_id, &controls);
        }
        for (id, (name, excluded)) in &controls.calls {
            if *excluded {
                self.add_expected(codes::UNAPPLIED_CONTROL, at, e, "/type", "run terminated with a call to an excluded tool never settled as a policy refusal", &format!("action.call.failed with error.code {REFUSED_BY_POLICY}"), &format!("unsettled {name}"), &[id, run_id]);
            }
        }
    }

    fn check_completed_controls(
        &mut self,
        at: (usize, usize),
        e: &Envelope,
        run_id: &str,
        controls: &Controls,
    ) {
        let p: RunCompletedPayload = payload(e);
        if !p.model_id.is_empty() && controls.model_present && p.model_id != controls.model {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/model_id",
                "completion attributes the run to a model other than the admitted one",
                &controls.model,
                &p.model_id,
                &[run_id],
            );
        }
        let Some(schema) = &controls.schema else {
            return;
        };
        let Some(result) = p.result.as_ref().map(|raw| raw.get().to_owned()) else {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/result",
                "completion under an admitted output_schema carries no result",
                "a result conforming to the admitted schema",
                "absent",
                &[run_id],
            );
            return;
        };
        if schema.validate(&result).is_err() {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/result",
                "completion result does not conform to the admitted output_schema",
                "a conforming result",
                &result,
                &[run_id],
            );
        } else if let Some(fixed) = controls
            .fixed_result
            .as_ref()
            .filter(|fixed| !crate::json::same_json(fixed, &result))
        {
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                "/payload/result",
                "completion does not carry the result the endpoint declared it would",
                fixed,
                &result,
                &[run_id],
            );
        }
    }
}

pub(super) struct PendingProvide {
    expectation: Option<Expectation>,
    honour: bool,
}

impl State {
    fn check_provide_pattern(&mut self, at: (usize, usize), e: &Envelope) {
        let p: CapabilitiesResponse = payload(e);
        if let Some(pattern) = p
            .effective_support(FEATURE_TOOLS_PROVIDE)
            .and_then(FeatureSupport::name_pattern)
        {
            if super::name_pattern::compile(&pattern).is_err() {
                self.add_expected(
                    codes::UNDISCLOSED_PROVIDE_LIMIT,
                    at,
                    e,
                    "/payload/features/action.tools.provide/limits/name_pattern",
                    "action.tools.provide discloses a name_pattern that is not RE2",
                    "an RE2 pattern",
                    &pattern,
                    &[],
                );
            }
        }
    }

    fn provide_gate(&mut self, at: (usize, usize), e: &Envelope, p: &SessionOpenRequest) {
        if p.tools.is_empty() {
            return;
        }
        let Some(level) = self.control_descriptor(at, e, FEATURE_TOOLS_PROVIDE) else {
            return;
        };
        let defect =
            |rung, code, reason, pointer: String, detail_name, detail_value: String, diagnostic| {
                Expectation {
                    rung,
                    limit: false,
                    key: FEATURE_TOOLS_PROVIDE.to_owned(),
                    pointer,
                    code,
                    reason,
                    detail_name,
                    detail_value,
                    diagnostic,
                    message: "an open supplies tools that require a typed refusal".to_owned(),
                }
            };
        let unsatisfiable = |pointer, name, value, diagnostic| {
            defect(
                RUNG_UNSATISFIABLE,
                UNSUPPORTED_FEATURE,
                CONTROL_UNSATISFIABLE,
                pointer,
                name,
                value,
                diagnostic,
            )
        };
        let mut defects = Vec::new();
        if !affirmative(&level) {
            defects.push(defect(
                RUNG_CAPABILITY,
                UNSUPPORTED_FEATURE,
                CONTROL_UNADVERTISED,
                "/payload/tools".to_owned(),
                "feature",
                FEATURE_TOOLS_PROVIDE.to_owned(),
                codes::UNAVAILABLE_CAPABILITY,
            ));
        } else if level == SupportLevel::DEGRADED && !p.allows_degraded(FEATURE_TOOLS_PROVIDE) {
            defects.push(defect(
                RUNG_DEGRADATION,
                CAPABILITY_DEGRADED,
                "",
                "/payload/tools".to_owned(),
                "feature",
                FEATURE_TOOLS_PROVIDE.to_owned(),
                codes::DEGRADED_WITHOUT_OPTIN,
            ));
        } else {
            let attached: HashSet<&str> = p
                .tool_sources
                .iter()
                .map(|source| source.id.as_str())
                .collect();
            let lists = self
                .features
                .get(FEATURE_TOOLS_LIST)
                .is_some_and(affirmative);
            let mut seen = HashSet::new();
            for (index, tool) in p.tools.iter().enumerate() {
                let pointer = format!("/payload/tools/{index}");
                if !self.control_participant.is_empty()
                    && tool.execution_owner.as_str() != self.control_participant
                {
                    defects.push(unsatisfiable(
                        format!("{pointer}/execution_owner"),
                        "tool",
                        tool.name.clone(),
                        codes::WRONG_TOOL_OWNER,
                    ));
                }
                if !seen.insert(&tool.name)
                    || (self.catalog_known && self.catalog.contains(&tool.name))
                {
                    defects.push(unsatisfiable(
                        format!("{pointer}/name"),
                        "tool",
                        tool.name.clone(),
                        codes::DUPLICATE_TOOL_NAME,
                    ));
                }
                if tool.source.is_empty() {
                    if lists {
                        defects.push(unsatisfiable(
                            format!("{pointer}/source"),
                            "tool",
                            tool.name.clone(),
                            codes::UNMATCHED_TOOL_SOURCE,
                        ));
                    }
                } else if !self.declared_sources.contains(&tool.source)
                    && !attached.contains(tool.source.as_str())
                {
                    defects.push(unsatisfiable(
                        format!("{pointer}/source"),
                        "source",
                        tool.source.clone(),
                        codes::UNMATCHED_TOOL_SOURCE,
                    ));
                }
            }
        }
        defects.sort_by(Expectation::order);
        let mut expectation = defects.into_iter().next();
        if expectation.is_none() {
            let mut violations = Vec::new();
            if let Some(support) = self.feature_supports.get(FEATURE_TOOLS_PROVIDE) {
                if let Some(max) = support
                    .max_tools()
                    .and_then(|max| usize::try_from(max).ok())
                {
                    if let Some(tool) = p.tools.get(max) {
                        violations.push(unsatisfiable(
                            format!("/payload/tools/{max}"),
                            "tool",
                            tool.name.clone(),
                            codes::UNAVAILABLE_CAPABILITY,
                        ));
                    }
                }
                let pattern = match support
                    .name_pattern()
                    .map(|p| super::name_pattern::compile(&p))
                {
                    Some(Ok(Some(regex))) => Some(regex),
                    Some(Ok(None)) => {
                        self.pending_provides.insert(
                            s(&e.id),
                            PendingProvide {
                                expectation: None,
                                honour: false,
                            },
                        );
                        return;
                    }
                    _ => None,
                };
                if let Some(pattern) = pattern {
                    for (index, tool) in p.tools.iter().enumerate() {
                        let Some(matches) = pattern.matches(&tool.name) else {
                            self.pending_provides.insert(
                                s(&e.id),
                                PendingProvide {
                                    expectation: None,
                                    honour: false,
                                },
                            );
                            return;
                        };
                        if !matches {
                            violations.push(unsatisfiable(
                                format!("/payload/tools/{index}/name"),
                                "tool",
                                tool.name.clone(),
                                codes::UNAVAILABLE_CAPABILITY,
                            ));
                        }
                    }
                }
                if let Some(dialect) = support.schema_dialect() {
                    for (index, tool) in p.tools.iter().enumerate() {
                        let schema = tool
                            .input_schema
                            .as_ref()
                            .and_then(|v| serde_json::from_str::<Value>(v.get()).ok());
                        if schema
                            .as_ref()
                            .and_then(|v| v.get("$schema"))
                            .and_then(Value::as_str)
                            .is_some_and(|declared| !declared.is_empty() && declared != dialect)
                        {
                            violations.push(unsatisfiable(
                                format!("/payload/tools/{index}/input_schema"),
                                "tool",
                                tool.name.clone(),
                                codes::UNAVAILABLE_CAPABILITY,
                            ));
                        }
                    }
                }
            }
            violations.sort_by(Expectation::order);
            expectation = violations.into_iter().next().map(|mut e| {
                e.limit = true;
                e
            });
        }
        let honour = expectation.is_none();
        self.pending_provides.insert(
            s(&e.id),
            PendingProvide {
                expectation,
                honour,
            },
        );
    }

    fn settle_provide_admission(&mut self, at: (usize, usize), e: &Envelope) {
        let Some(pending) = self.pending_provides.get(&os(&e.in_reply_to)) else {
            return;
        };
        if let Some(expectation) = pending.expectation.clone().filter(|e| !e.limit) {
            self.add(
                expectation.diagnostic,
                at,
                e,
                "/payload",
                &expectation.message,
            );
        }
    }

    fn settle_provide_refusal(&mut self, at: (usize, usize), e: &Envelope) {
        let reply = os(&e.in_reply_to);
        let Some(pending) = self.pending_provides.get(&reply) else {
            return;
        };
        let expectation = pending.expectation.clone();
        let honour = pending.honour;
        let err = payload::<ErrorResponse>(e).error;
        let (discharged, speaker) = self.attribute(&reply, &err);
        if discharged {
            return;
        }
        if let Some(expectation) = expectation {
            self.refusal_misses(at, e, &expectation, &speaker, &err);
        } else if honour {
            self.add(
                codes::UNDISCLOSED_PROVIDE_LIMIT,
                at,
                e,
                "/payload/error",
                "a conforming tools array was refused without a disclosed limit",
            );
        }
    }
}

#[cfg(test)]
mod tests {
    use super::pointer_order;
    use crate::json::{exact, same_json};
    use std::cmp::Ordering;

    #[test]
    fn numbers_compare_by_value_whatever_their_spelling() {
        assert_eq!(exact("1"), exact("1.0"));
        assert_eq!(exact("100"), exact("1e2"));
        assert_eq!(exact("0.10"), exact("1E-1"));
        assert_eq!(exact("-0.0"), exact("0"));
        assert_ne!(
            exact("0.10000000000000000000001"),
            exact("0.10000000000000000000002")
        );
        assert!(same_json(
            r#"{"a":[1,2.0],"b":true}"#,
            r#"{"b":true,"a":[1.0,2]}"#
        ));
        assert!(!same_json(r#"{"a":1}"#, r#"{"a":"1"}"#));
        assert!(!same_json(
            r#"{"a":0.10000000000000000000001}"#,
            r#"{"a":0.10000000000000000000002}"#
        ));
        assert!(!same_json(
            r#"{"a":18446744073709551616}"#,
            r#"{"a":18446744073709551617}"#
        ));
    }

    #[test]
    fn pointers_order_numeric_segments_by_value() {
        assert_eq!(
            pointer_order("/payload/a/10", "/payload/a/9"),
            Ordering::Greater
        );
        assert_eq!(pointer_order("/payload/a", "/payload/a/0"), Ordering::Less);
        assert_eq!(pointer_order("/payload/b", "/payload/a"), Ordering::Greater);
    }
}
