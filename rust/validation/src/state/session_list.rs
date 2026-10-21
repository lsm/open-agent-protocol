use std::collections::HashSet;

use oap_protocol::*;

use super::{os, payload, State};
use crate::codes;

impl State {
    pub(super) fn session_list_response(&mut self, at: (usize, usize), e: &Envelope) {
        let p: SessionListResponse = payload(e);
        let requested = self
            .requests
            .get(&os(&e.in_reply_to))
            .filter(|request| request.envelope_type == EnvelopeType::SESSION_LIST_REQUEST)
            .map(|request| payload::<SessionListRequest>(&request.envelope));
        if let Some(requested) = requested {
            let limit = if requested.limit == 0 {
                SESSION_LIST_DEFAULT_LIMIT
            } else {
                requested.limit
            };
            if p.sessions.len() as i64 > limit {
                self.add_expected(
                    codes::SESSION_LIST_OVER_LIMIT,
                    at,
                    e,
                    "/payload/sessions",
                    "a session list page holds more entries than the request's limit allows",
                    &limit.to_string(),
                    &p.sessions.len().to_string(),
                    &[&os(&e.in_reply_to)],
                );
            }
        }
        let mut seen = HashSet::new();
        for (index, entry) in p.sessions.iter().enumerate() {
            let pointer = format!("/payload/sessions/{index}");
            if !seen.insert(entry.session_id.as_str()) {
                self.add_expected(
                    codes::DUPLICATE_SESSION_ENTRY,
                    at,
                    e,
                    &format!("{pointer}/session_id"),
                    "a session list page lists one session twice",
                    "one entry per session",
                    entry.session_id.as_str(),
                    &[],
                );
                continue;
            }
            if index == 0 {
                continue;
            }
            let previous = &p.sessions[index - 1];
            if previous.updated_at_ms < entry.updated_at_ms
                || (previous.updated_at_ms == entry.updated_at_ms
                    && previous.session_id > entry.session_id)
            {
                self.add_expected(
                    codes::SESSION_LIST_ORDER,
                    at,
                    e,
                    &pointer,
                    "a session list is newest first by updated_at_ms, ties broken by session_id",
                    &format!("after {}", previous.session_id),
                    entry.session_id.as_str(),
                    &[],
                );
            }
        }
    }
}
