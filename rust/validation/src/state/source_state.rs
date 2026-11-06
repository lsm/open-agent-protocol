use oap_protocol::*;
use std::collections::{BTreeMap, HashMap, HashSet};

use super::controls::affirmative;
use super::{os, payload, State};
use crate::codes;

pub(super) struct Catalog {
    pub(super) revision: String,
    pub(super) owners: HashMap<String, String>,
    pub(super) tools: HashMap<String, String>,
    pub(super) sources: HashSet<String>,
}

pub(super) fn compatible(
    expected: &ToolSourceDescriptor,
    published: &ToolSourceDescriptor,
) -> bool {
    expected.id == published.id
        && expected.kind == published.kind
        && [
            (&expected.display_name, &published.display_name),
            (&expected.protocol, &published.protocol),
            (&expected.endpoint, &published.endpoint),
        ]
        .iter()
        .all(|(expected, actual)| expected.is_empty() || expected == actual)
}

impl State {
    pub(super) fn call_source(&mut self, at: (usize, usize), e: &Envelope) {
        let p: ActionCallPayload = payload(e);
        let catalog = self
            .tool_catalogs
            .get(p.session_id.as_str())
            .filter(|catalog| catalog.revision == self.current_capability);
        let mapping = catalog.map_or(&self.descriptor_attribution, |catalog| &catalog.tools);
        let listed = mapping.get(&p.name);
        let finding = if p.source.is_empty() {
            (self
                .features
                .get(FEATURE_TOOLS_LIST)
                .is_some_and(affirmative)
                && listed.is_some_and(|source| !source.is_empty()))
            .then_some(codes::UNATTRIBUTED_CALL)
        } else if let Some(listed) = listed {
            (listed != &p.source).then_some(codes::UNMATCHED_TOOL_SOURCE)
        } else {
            let declared = catalog.is_some_and(|catalog| catalog.sources.contains(&p.source))
                || self.declared_sources.contains(&p.source)
                || self
                    .attached_sources
                    .get(p.session_id.as_str())
                    .is_some_and(|sources| sources.iter().any(|source| source.id == p.source));
            (!declared).then_some(codes::UNMATCHED_TOOL_SOURCE)
        };
        if let Some(code) = finding {
            self.add(code, at, e, "/payload/source", "a call must name the source its published catalog attributes, or a declared source");
        }
    }

    pub(super) fn source_snapshot(&mut self, at: (usize, usize), e: &Envelope, adopt: bool) {
        if adopt && self.invalid_source_admission(&os(&e.in_reply_to)) {
            return;
        }
        let p: SessionState = payload(e);
        let Some(attached) = self
            .attached_sources
            .get(p.session_id.as_str())
            .filter(|sources| !sources.is_empty())
        else {
            return;
        };
        let mut expected = BTreeMap::new();
        for source in &self.declared_descriptors {
            expected.entry(&source.id).or_insert(source);
        }
        for source in attached {
            expected.insert(&source.id, source);
        }
        let mut reported = BTreeMap::new();
        let mut duplicate = false;
        for source in &p.sources {
            if reported.contains_key(&source.id) {
                duplicate = true;
            } else {
                reported.insert(&source.id, source);
            }
        }
        let mut found = Vec::new();
        if duplicate {
            found.push((
                codes::DUPLICATE_TOOL_SOURCE,
                "a session snapshot declares a source id twice",
            ));
        }
        for (id, source) in &expected {
            if !reported.get(id).is_some_and(|got| {
                if adopt {
                    compatible(source, got)
                } else {
                    *source == *got
                }
            }) {
                found.push((
                    codes::SESSION_STATE_MISMATCH,
                    "a session snapshot omits or redescribes an attached or declared source",
                ));
            }
        }
        for id in reported.keys() {
            if !expected.contains_key(id) {
                found.push((
                    codes::SESSION_STATE_MISMATCH,
                    "a session snapshot names a source never attached or declared",
                ));
            }
        }
        for (code, message) in found {
            self.add(code, at, e, "/payload/sources", message);
        }
    }
}
