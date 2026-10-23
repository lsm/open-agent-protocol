use std::collections::HashSet;

use oap_protocol::*;

use super::{payload, State};
use crate::codes;

impl State {
    pub(super) fn check_descriptor_catalog(&mut self, at: (usize, usize), e: &Envelope) {
        let p: CapabilitiesResponse = payload(e);
        let mut declared = HashSet::new();
        let mut duplicate_source = None;
        for source in p
            .sources
            .iter()
            .chain(p.layers.values().flat_map(|layer| &layer.sources))
        {
            if !declared.insert(source.id.as_str()) && duplicate_source.is_none() {
                duplicate_source = Some(source.id.as_str());
            }
        }
        if let Some(id) = duplicate_source {
            self.add_expected(
                codes::DUPLICATE_TOOL_SOURCE,
                at,
                e,
                "/payload/sources",
                "the descriptor declares two tool sources with one id",
                "one source per id",
                id,
                &[],
            );
        }
        let tools: Vec<_> = p
            .tools
            .iter()
            .chain(p.layers.values().flat_map(|layer| &layer.tools))
            .collect();
        let mut names = HashSet::new();
        let mut duplicate_name = None;
        for tool in &tools {
            if !names.insert(tool.name.as_str()) && duplicate_name.is_none() {
                duplicate_name = Some(tool.name.as_str());
            }
        }
        if let Some(name) = duplicate_name {
            self.add_expected(
                codes::DUPLICATE_TOOL_NAME,
                at,
                e,
                "/payload/tools",
                "the descriptor's effective catalog lists two tools with one name",
                "one tool per name",
                name,
                &[],
            );
        }
        for tool in tools {
            if !tool.source.is_empty() && !declared.contains(tool.source.as_str()) {
                self.add_expected(
                    codes::UNMATCHED_TOOL_SOURCE,
                    at,
                    e,
                    "/payload/tools",
                    "a descriptor tool names a source the descriptor does not declare",
                    "a declared source",
                    &tool.source,
                    &[&tool.name],
                );
            }
        }
    }
}
