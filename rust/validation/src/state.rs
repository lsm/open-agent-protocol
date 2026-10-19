use std::collections::HashMap;

use oap_protocol::*;
use serde::de::DeserializeOwned;

use crate::{codes, Diagnostic, Phase};

pub const IMPLEMENTED: [&str; 5] = [
    codes::DUPLICATE_ENVELOPE_ID,
    codes::UNMATCHED_RESPONSE,
    codes::DUPLICATE_RESPONSE,
    codes::MISSING_RESPONSE,
    codes::STALE_CAPABILITY_REVISION,
];

struct Request {
    envelope_type: EnvelopeType,
    index: usize,
    line: usize,
    envelope: Envelope,
    capability_revision: String,
    responded: bool,
}

pub(crate) struct State {
    fixture: String,
    pub diagnostics: Vec<Diagnostic>,
    ids: HashMap<String, usize>,
    requests: HashMap<String, Request>,
    current_capability: String,
    capabilities_stale: bool,
}

fn payload<T: DeserializeOwned + Default>(e: &Envelope) -> T {
    e.decode_payload().unwrap_or_default()
}

fn is_request(t: &EnvelopeType) -> bool {
    t.as_str().ends_with(".request")
}

fn is_response(t: &EnvelopeType) -> bool {
    t.as_str().ends_with(".response")
}

fn expected_response(t: &EnvelopeType) -> EnvelopeType {
    if *t == EnvelopeType::USER_INPUT_CANCEL_REQUEST {
        return EnvelopeType::USER_INPUT_CANCEL_RESPONSE;
    }
    EnvelopeType::from(format!(
        "{}.response",
        t.as_str().trim_end_matches(".request")
    ))
}

const REVISION_EXEMPT: [EnvelopeType; 4] = [
    EnvelopeType::PROTOCOL_INITIALIZE_REQUEST,
    EnvelopeType::CAPABILITIES_REQUEST,
    EnvelopeType::CAPABILITIES_UPDATED,
    EnvelopeType::ERROR_RESPONSE,
];

impl State {
    pub fn new(fixture: &str) -> Self {
        Self {
            fixture: fixture.to_owned(),
            diagnostics: Vec::new(),
            ids: HashMap::new(),
            requests: HashMap::new(),
            current_capability: String::new(),
            capabilities_stale: false,
        }
    }

    fn diagnostic(
        &self,
        code: &str,
        at: (usize, usize),
        e: &Envelope,
        pointer: &str,
        message: &str,
    ) -> Diagnostic {
        let mut d = Diagnostic::new(&self.fixture, Phase::Semantic, code, message).at(at.0, at.1);
        d.envelope_id = e.id.as_str().to_owned();
        d.envelope_type = e.envelope_type.as_str().to_owned();
        d.pointer = pointer.to_owned();
        d
    }

    fn add(&mut self, code: &str, at: (usize, usize), e: &Envelope, pointer: &str, message: &str) {
        let d = self.diagnostic(code, at, e, pointer, message);
        self.diagnostics.push(d);
    }

    #[allow(clippy::too_many_arguments)]
    fn add_expected(
        &mut self,
        code: &str,
        at: (usize, usize),
        e: &Envelope,
        pointer: &str,
        message: &str,
        expected: &str,
        actual: &str,
        related: &[&str],
    ) {
        let mut d = self.diagnostic(code, at, e, pointer, message);
        d.expected = expected.to_owned();
        d.actual = actual.to_owned();
        d.related_ids = related.iter().map(|id| id.to_string()).collect();
        self.diagnostics.push(d);
    }

    pub fn apply(&mut self, i: usize, line: usize, e: &Envelope) {
        let at = (i, line);
        let id = e.id.as_str().to_owned();
        if let Some(first) = self.ids.get(&id) {
            let related = format!("index:{first}");
            self.add_expected(
                codes::DUPLICATE_ENVELOPE_ID,
                at,
                e,
                "/id",
                "envelope id must be unique",
                "unique id",
                &id,
                &[&related],
            );
            return;
        }
        self.ids.insert(id.clone(), i);
        let revision = e.capability_revision.clone().unwrap_or_default();
        if is_request(&e.envelope_type) {
            let request = Request {
                envelope_type: e.envelope_type.clone(),
                index: i,
                line,
                envelope: e.clone(),
                capability_revision: revision.clone(),
                responded: false,
            };
            self.requests.insert(id, request);
        }
        let duplicate_response = is_response(&e.envelope_type) && !self.response(at, e);
        if !revision.is_empty()
            && !self.current_capability.is_empty()
            && revision != self.current_capability
            && !REVISION_EXEMPT.contains(&e.envelope_type)
        {
            let current = self.current_capability.clone();
            self.add_expected(
                codes::STALE_CAPABILITY_REVISION,
                at,
                e,
                "/capability_revision",
                "operation uses a stale capability revision",
                &current,
                &revision,
                &[],
            );
        }
        if duplicate_response {
            return;
        }
        let t = &e.envelope_type;
        if *t == EnvelopeType::CAPABILITIES_RESPONSE {
            self.current_capability = revision;
            self.capabilities_stale = false;
        } else if *t == EnvelopeType::CAPABILITIES_UPDATED {
            let p: CapabilitiesUpdated = payload(e);
            if !self.current_capability.is_empty() && p.previous_revision != self.current_capability
            {
                let current = self.current_capability.clone();
                self.add_expected(
                    codes::STALE_CAPABILITY_REVISION,
                    at,
                    e,
                    "/payload/previous_revision",
                    "capability update does not continue the active revision",
                    &current,
                    &p.previous_revision,
                    &[],
                );
            }
            if revision == p.previous_revision {
                self.add(
                    codes::STALE_CAPABILITY_REVISION,
                    at,
                    e,
                    "/capability_revision",
                    "capability update must introduce a new revision",
                );
            }
            self.current_capability = revision;
            self.capabilities_stale = true;
        } else if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST && self.capabilities_stale {
            self.add(
                codes::STALE_CAPABILITY_REVISION,
                at,
                e,
                "/capability_revision",
                "submission occurred before refreshed capabilities",
            );
        }
    }

    fn response(&mut self, at: (usize, usize), e: &Envelope) -> bool {
        let reply = e
            .in_reply_to
            .as_ref()
            .map(|id| id.as_str().to_owned())
            .unwrap_or_default();
        let Some(request) = self.requests.get_mut(&reply) else {
            self.add(
                codes::UNMATCHED_RESPONSE,
                at,
                e,
                "/in_reply_to",
                "response does not match an earlier request",
            );
            return true;
        };
        let expected = expected_response(&request.envelope_type);
        if e.envelope_type != expected && e.envelope_type != EnvelopeType::ERROR_RESPONSE {
            self.add_expected(
                codes::UNMATCHED_RESPONSE,
                at,
                e,
                "/type",
                "response type does not match request",
                expected.as_str(),
                e.envelope_type.as_str(),
                &[&reply],
            );
            return true;
        }
        if request.responded {
            self.add(
                codes::DUPLICATE_RESPONSE,
                at,
                e,
                "/in_reply_to",
                "request already has a response",
            );
            return false;
        }
        request.responded = true;
        let checked = request.envelope_type != EnvelopeType::PROTOCOL_INITIALIZE_REQUEST
            && request.envelope_type != EnvelopeType::CAPABILITIES_REQUEST;
        let requested = request.capability_revision.clone();
        let revision = e.capability_revision.clone().unwrap_or_default();
        if e.envelope_type != EnvelopeType::ERROR_RESPONSE
            && checked
            && !requested.is_empty()
            && revision != requested
        {
            self.add_expected(
                codes::STALE_CAPABILITY_REVISION,
                at,
                e,
                "/capability_revision",
                "successful response must repeat the request capability revision",
                &requested,
                &revision,
                &[&reply],
            );
        }
        true
    }

    pub fn close(&mut self) {
        let mut found = Vec::new();
        for (id, request) in &self.requests {
            if !request.responded {
                let at = (request.index, request.line);
                let mut d = self.diagnostic(
                    codes::MISSING_RESPONSE,
                    at,
                    &request.envelope,
                    "",
                    "request has no correlated response",
                );
                d.related_ids = vec![id.clone()];
                found.push(d);
            }
        }
        self.diagnostics.extend(found);
    }
}
