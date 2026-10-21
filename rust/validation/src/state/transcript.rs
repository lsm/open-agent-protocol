use std::collections::HashSet;

use oap_protocol::*;

use super::{os, payload, State};
use crate::codes;

impl State {
    pub(super) fn transcript_load_response(&mut self, at: (usize, usize), e: &Envelope) {
        let p: TranscriptLoadResponse = payload(e);
        let requested = self
            .requests
            .get(&os(&e.in_reply_to))
            .filter(|request| request.envelope_type == EnvelopeType::TRANSCRIPT_LOAD_REQUEST)
            .map(|request| payload::<TranscriptLoadRequest>(&request.envelope));
        if let Some(requested) = &requested {
            let limit = if requested.limit == 0 {
                TRANSCRIPT_LOAD_DEFAULT_LIMIT
            } else {
                requested.limit
            };
            if p.entries.len() as i64 > limit {
                self.add_expected(
                    codes::TRANSCRIPT_OVER_LIMIT,
                    at,
                    e,
                    "/payload/entries",
                    "a transcript page holds more entries than the request's limit allows",
                    &limit.to_string(),
                    &p.entries.len().to_string(),
                    &[&os(&e.in_reply_to)],
                );
            }
        }
        let mut seen = HashSet::new();
        for (index, entry) in p.entries.iter().enumerate() {
            if !seen.insert(entry.entry_id.as_str()) {
                self.add_expected(
                    codes::DUPLICATE_TRANSCRIPT_ENTRY,
                    at,
                    e,
                    &format!("/payload/entries/{index}/entry_id"),
                    "a transcript page returns one entry twice",
                    "one occurrence per entry",
                    entry.entry_id.as_str(),
                    &[],
                );
            }
        }
        let Some(gap) = p.gap else {
            return;
        };
        let resumed = gap.resumed_at.as_ref().map_or("", |id| id.as_str());
        if requested.is_some_and(|request| request.cursor.is_empty()) {
            self.add_expected(
                codes::TRANSCRIPT_GAP_MISMATCH,
                at,
                e,
                "/payload/gap",
                "a read with no cursor starts at the oldest entry held, so it has no gap",
                "no gap",
                gap.reason.as_str(),
                &[&os(&e.in_reply_to)],
            );
        } else if p.entries.is_empty() && !resumed.is_empty() {
            self.add_expected(
                codes::TRANSCRIPT_GAP_MISMATCH,
                at,
                e,
                "/payload/gap/resumed_at",
                "a gap on an empty page has no resumed_at",
                "no resumed_at",
                resumed,
                &[],
            );
        } else if let Some(first) = p.entries.first() {
            if resumed != first.entry_id.as_str() {
                self.add_expected(
                    codes::TRANSCRIPT_GAP_MISMATCH,
                    at,
                    e,
                    "/payload/gap/resumed_at",
                    "a gap resumes at the first entry it returns",
                    first.entry_id.as_str(),
                    resumed,
                    &[],
                );
            }
        }
    }
}
