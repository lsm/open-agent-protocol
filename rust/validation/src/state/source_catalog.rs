use oap_protocol::*;

use super::{payload, State};
use crate::{codes, json::same_json};

impl State {
    pub(super) fn record_attached_sources(
        &mut self,
        session: &str,
        asked: &SessionOpenRequest,
        published: &SessionState,
    ) {
        let attached = self.attached_sources.entry(session.to_owned()).or_default();
        for attachment in &asked.tool_sources {
            let descriptor = attachment.descriptor();
            if let Some(existing) = attached
                .iter_mut()
                .find(|source| source.id == descriptor.id)
            {
                *existing = descriptor;
            } else {
                attached.push(descriptor);
            }
        }
        for descriptor in attached {
            if let Some(source) = published
                .sources
                .iter()
                .find(|source| super::source_state::compatible(descriptor, source))
            {
                *descriptor = source.clone();
            }
        }
    }

    pub(super) fn source_catalog_rules(&mut self, at: (usize, usize), e: &Envelope) {
        let mut found = Vec::new();
        if e.envelope_type == EnvelopeType::CAPABILITIES_RESPONSE {
            for attached in self.attached_sources.values() {
                for source in attached {
                    if self.declared_sources.contains(&source.id) {
                        found.push((
                            codes::DUPLICATE_TOOL_SOURCE,
                            "/payload/sources".to_owned(),
                            "a refreshed descriptor collides with an attached source",
                        ));
                    }
                }
            }
            for (session, provided) in &self.provided {
                for tool in provided {
                    if self.catalog.contains(&tool.name) {
                        found.push((
                            codes::DUPLICATE_TOOL_NAME,
                            "/payload/tools".to_owned(),
                            "a refreshed native tool collides with a provided tool",
                        ));
                    }
                    if !tool.source.is_empty()
                        && !self.declared_sources.contains(&tool.source)
                        && !self.attached_sources.get(session).is_some_and(|sources| {
                            sources.iter().any(|source| source.id == tool.source)
                        })
                    {
                        found.push((
                            codes::UNMATCHED_TOOL_SOURCE,
                            "/payload/sources".to_owned(),
                            "a refreshed descriptor removes a provided tool's source",
                        ));
                    }
                }
            }
        } else {
            let p: ToolsListResponse = payload(e);
            if let Some(session) = &p.session_id {
                for source in self
                    .attached_sources
                    .get(session.as_str())
                    .into_iter()
                    .flatten()
                {
                    if p.sources.iter().find(|listed| listed.id == source.id) != Some(source) {
                        found.push((codes::CATALOG_MISMATCH, "/payload/sources".to_owned(), "a session catalog must retain the attached source's published description"));
                    }
                }
                for supplied in self.provided.get(session.as_str()).into_iter().flatten() {
                    let agrees = p
                        .tools
                        .iter()
                        .find(|listed| listed.name == supplied.name)
                        .is_some_and(|listed| {
                            listed.description == supplied.description
                                && listed.execution_owner == supplied.execution_owner
                                && listed.source == supplied.source
                                && same_json(
                                    supplied
                                        .input_schema
                                        .as_ref()
                                        .map_or("null", |raw| raw.get()),
                                    listed.input_schema.as_ref().map_or("null", |raw| raw.get()),
                                )
                        });
                    if !agrees {
                        found.push((
                            codes::CATALOG_MISMATCH,
                            "/payload/tools".to_owned(),
                            "a session catalog must retain the provided tool's definition",
                        ));
                    }
                }
            }
        }
        for (code, pointer, message) in found {
            self.add(code, at, e, &pointer, message);
        }
    }
}
