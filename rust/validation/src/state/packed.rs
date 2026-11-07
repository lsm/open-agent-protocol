use std::collections::BTreeSet;

use oap_protocol::*;
use serde_json::Value;

use super::{os, payload, State};
use crate::codes;
use crate::pack::{entries, text, CompiledPacks};

#[derive(Clone)]
pub(super) struct Gate {
    key: String,
    refusals: Option<BTreeSet<String>>,
    advertised: bool,
}

pub(super) fn declared<'a>(packs: Option<&'a CompiledPacks>, kind: &str) -> Option<&'a Value> {
    packs?
        .resources()
        .descriptors()
        .flat_map(|(_, d)| entries(d, "envelope_types"))
        .find(|entry| text(entry, "type") == kind)
}

pub(super) fn expected(packs: Option<&CompiledPacks>, kind: &EnvelopeType) -> EnvelopeType {
    if declared(packs, kind.as_str()).is_none() {
        return super::expected_response(kind);
    }
    packs
        .into_iter()
        .flat_map(|p| p.resources().descriptors())
        .flat_map(|(_, d)| entries(d, "envelope_types"))
        .find(|entry| entry["role"] == "response" && text(entry, "replies_to") == kind.as_str())
        .map(|entry| EnvelopeType::from(text(entry, "type")))
        .unwrap_or_default()
}

pub(super) fn scope(e: &Envelope) -> (String, String) {
    let value: Value = payload(e);
    let named = |key, fallback| {
        value
            .get(key)
            .and_then(Value::as_str)
            .filter(|name| !name.is_empty())
            .map(str::to_owned)
            .unwrap_or(fallback)
    };
    (
        named("session_id", os(&e.session_id)),
        named("run_id", os(&e.run_id)),
    )
}

impl State {
    pub(super) fn packed_role(&self, kind: &EnvelopeType, role: &str) -> bool {
        declared(self.packs.as_deref(), kind.as_str()).map_or_else(
            || {
                if role == "request" {
                    super::is_request(kind)
                } else {
                    super::is_response(kind)
                }
            },
            |entry| text(entry, "role") == role,
        )
    }

    pub(super) fn packed_run_event(&self, e: &Envelope) -> bool {
        declared(self.packs.as_deref(), e.envelope_type.as_str()).is_some()
            && e.run_id.is_some()
            && e.sequence.is_some()
    }

    fn pack_gates(&self, e: &Envelope, typed: bool) -> Vec<Gate> {
        let mut gates = Vec::new();
        let Some(packs) = self.packs.as_deref() else {
            return gates;
        };
        let value: Value = payload(e);
        for (_, descriptor) in packs.resources().descriptors() {
            for gate in entries(descriptor, "gates") {
                let direct = text(gate, "type") == e.envelope_type.as_str();
                let member = text(gate, "payload_type") == e.envelope_type.as_str()
                    && value.get(text(gate, "member")).is_some();
                let key = text(gate, "capability");
                if key.is_empty() || (!member && !(direct && typed)) {
                    continue;
                }
                let refusals = direct.then(|| {
                    declared(Some(packs), e.envelope_type.as_str())
                        .into_iter()
                        .flat_map(|entry| entries(entry, "refusals"))
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect()
                });
                let advertised = !self.current_capability.is_empty()
                    && !self.capabilities_stale
                    && self.features.get(key).is_some_and(|level| {
                        matches!(level.as_str(), "native" | "emulated" | "degraded")
                    });
                gates.push(Gate {
                    key: key.to_owned(),
                    refusals,
                    advertised,
                });
            }
        }
        gates
    }

    pub(super) fn pack_envelope(&mut self, at: (usize, usize), e: &Envelope) {
        if self.packed_role(&e.envelope_type, "request") {
            let gates = self.pack_gates(e, true);
            if let Some(request) = self.requests.get_mut(e.id.as_str()) {
                request.gates = gates;
            }
            return;
        }
        let event = !self.packed_role(&e.envelope_type, "response");
        for gate in self.pack_gates(e, event) {
            self.feature_keys(at, e, &[gate.key]);
        }
        let gates = self
            .requests
            .get(&os(&e.in_reply_to))
            .map(|request| request.gates.clone())
            .unwrap_or_default();
        if e.envelope_type == EnvelopeType::ERROR_RESPONSE {
            self.pack_refusal(at, e, &gates);
        } else if !event {
            for gate in gates.iter().filter(|gate| !gate.advertised) {
                self.add_expected(codes::UNAVAILABLE_CAPABILITY, at, e, "/type",
                    "response admits an unadvertised capability request",
                    &format!("unsupported_feature with details.feature {:?} and details.reason unadvertised", gate.key),
                    e.envelope_type.as_str(), &[&os(&e.in_reply_to)]);
            }
        }
    }

    fn pack_refusal(&mut self, at: (usize, usize), e: &Envelope, gates: &[Gate]) {
        let error = payload::<ErrorResponse>(e).error;
        let mut unadvertised: Vec<_> = gates
            .iter()
            .filter(|g| !g.advertised)
            .map(|g| g.key.as_str())
            .collect();
        unadvertised.sort_unstable();
        if !unadvertised.is_empty() {
            let feature = error
                .details
                .get("feature")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let reason = error
                .details
                .get("reason")
                .and_then(Value::as_str)
                .unwrap_or_default();
            if error.code == "unsupported_feature"
                && reason == "unadvertised"
                && unadvertised.contains(&feature)
            {
                return;
            }
            let named = if unadvertised.len() == 1 {
                format!("details.feature {:?}", unadvertised[0])
            } else {
                format!("details.feature one of {unadvertised:?}")
            };
            self.add_expected(
                codes::UNAVAILABLE_CAPABILITY,
                at,
                e,
                "/payload/error/code",
                "an unadvertised capability requires its typed refusal",
                &format!(
                    "unsupported_feature with {named} and details.reason {:?}",
                    "unadvertised"
                ),
                &error.code,
                &[&os(&e.in_reply_to)],
            );
            return;
        }
        if let Some(refusals) = gates
            .iter()
            .find_map(|g| g.refusals.as_ref())
            .filter(|r| !r.contains(&error.code))
        {
            let expected = if refusals.is_empty() {
                "no declared refusal".to_owned()
            } else {
                refusals.iter().cloned().collect::<Vec<_>>().join(", ")
            };
            self.add_expected(
                codes::UNHONOURED_CAPABILITY,
                at,
                e,
                "/payload/error/code",
                "advertised packed request refused under an undeclared code",
                &expected,
                &error.code,
                &[&os(&e.in_reply_to)],
            );
        }
    }
}
