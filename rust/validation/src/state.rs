mod admissions;
mod interactions;
mod runs;

use std::collections::{HashMap, HashSet};

use oap_protocol::*;
use serde::de::DeserializeOwned;

use crate::{codes, Diagnostic, Phase};

struct Request {
    envelope_type: EnvelopeType,
    index: usize,
    line: usize,
    envelope: Envelope,
    capability_revision: String,
    session: String,
    run: String,
    interaction: String,
    responded: bool,
}

pub(crate) struct State {
    fixture: String,
    pub diagnostics: Vec<Diagnostic>,
    ids: HashMap<String, usize>,
    requests: HashMap<String, Request>,
    current_capability: String,
    capabilities_stale: bool,
    runs: HashMap<String, runs::Run>,
    active: HashMap<String, String>,
    late_cancels: HashSet<String>,
    recoveries: HashMap<String, admissions::Recovery>,
    initialized: bool,
    participants: HashSet<String>,
    features: HashMap<String, SupportLevel>,
}

fn payload<T: DeserializeOwned + Default>(e: &Envelope) -> T {
    e.decode_payload().unwrap_or_default()
}

trait Id {
    fn text(&self) -> &str;
}

macro_rules! id {
    ($($id:ty),+) => {$(
        impl Id for $id {
            fn text(&self) -> &str {
                self.as_str()
            }
        }
    )+};
}

id!(
    SessionId,
    RunId,
    EnvelopeId,
    InteractionId,
    MessageId,
    ToolCallId,
    ParticipantId
);

fn s<T: Id>(id: &T) -> String {
    id.text().to_owned()
}

fn os<T: Id>(id: &Option<T>) -> String {
    id.as_ref().map(s).unwrap_or_default()
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

fn request_scope(e: &Envelope) -> (String, String) {
    let t = &e.envelope_type;
    if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST {
        (
            s(&payload::<MessageSubmitRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::SESSION_COMPACT_REQUEST {
        (
            s(&payload::<SessionCompactRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::SESSION_STATE_REQUEST {
        (
            s(&payload::<SessionStateRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::TRANSCRIPT_LOAD_REQUEST {
        (
            s(&payload::<TranscriptLoadRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::SESSION_MODEL_SWITCH_REQUEST {
        (
            s(&payload::<SessionModelSwitchRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::SESSION_SETTINGS_UPDATE_REQUEST {
        (
            s(&payload::<SessionSettingsUpdateRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::SESSION_PROVIDER_ATTACH_REQUEST {
        (
            s(&payload::<SessionProviderAttachRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::MODELS_REQUEST {
        (s(&payload::<ModelsRequest>(e).session_id), String::new())
    } else if *t == EnvelopeType::SESSION_OPEN_REQUEST {
        (
            os(&payload::<SessionOpenRequest>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::RUN_CANCEL_REQUEST {
        let p: RunCancelRequest = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
        let p: PermissionResolveRequest = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::ACTION_CALL_RESOLVE_REQUEST {
        let p: ActionCallResolveRequest = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::USER_INPUT_RESOLVE_REQUEST {
        let p: UserInputResolveRequest = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::USER_INPUT_CANCEL_REQUEST {
        let p: UserInputCancelRequest = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::ACTION_TOOLS_LIST_REQUEST {
        let session = os(&payload::<ToolsListRequest>(e).session_id);
        (
            if session.is_empty() {
                os(&e.session_id)
            } else {
                session
            },
            String::new(),
        )
    } else {
        (String::new(), String::new())
    }
}

fn response_scope(e: &Envelope) -> (String, String) {
    let t = &e.envelope_type;
    if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE {
        let p: MessageSubmitResponse = payload(e);
        (s(&p.session_id), os(&p.run_id))
    } else if *t == EnvelopeType::SESSION_COMPACT_RESPONSE {
        let p: SessionCompactResponse = payload(e);
        (s(&p.session_id), os(&p.run_id))
    } else if *t == EnvelopeType::SESSION_STATE_RESPONSE
        || *t == EnvelopeType::SESSION_STATE_UPDATED
        || *t == EnvelopeType::SESSION_OPEN_RESPONSE
    {
        (s(&payload::<SessionState>(e).session_id), String::new())
    } else if *t == EnvelopeType::SESSION_MODEL_SWITCH_RESPONSE {
        (
            s(&payload::<SessionModelSwitchResponse>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::TRANSCRIPT_LOAD_RESPONSE {
        (
            s(&payload::<TranscriptLoadResponse>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::SESSION_SETTINGS_UPDATE_RESPONSE {
        (
            s(&payload::<SessionSettingsUpdateResponse>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::SESSION_PROVIDER_ATTACH_RESPONSE {
        (
            s(&payload::<SessionProviderAttachResponse>(e).session_id),
            String::new(),
        )
    } else if *t == EnvelopeType::MODELS_RESPONSE {
        (s(&payload::<ModelsResponse>(e).session_id), String::new())
    } else if *t == EnvelopeType::RUN_CANCEL_RESPONSE {
        let p: RunCancelResponse = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::ACTION_PERMISSION_RESOLVE_RESPONSE {
        let p: PermissionResolveResponse = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE {
        let p: ActionCallResolveResponse = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::USER_INPUT_RESOLVE_RESPONSE
        || *t == EnvelopeType::USER_INPUT_CANCEL_RESPONSE
    {
        let p: UserInputResolveResponse = payload(e);
        (s(&p.session_id), s(&p.run_id))
    } else if *t == EnvelopeType::ACTION_TOOLS_LIST_RESPONSE {
        (
            os(&payload::<ToolsListResponse>(e).session_id),
            String::new(),
        )
    } else {
        (String::new(), String::new())
    }
}

fn envelope_interaction(e: &Envelope) -> String {
    let t = &e.envelope_type;
    if *t == EnvelopeType::ACTION_PERMISSION_RESOLVE_RESPONSE {
        s(&payload::<PermissionResolveResponse>(e).interaction_id)
    } else if *t == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
        s(&payload::<PermissionResolveRequest>(e).interaction_id)
    } else if *t == EnvelopeType::ACTION_CALL_RESOLVE_REQUEST {
        s(&payload::<ActionCallResolveRequest>(e).interaction_id)
    } else if *t == EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE {
        s(&payload::<ActionCallResolveResponse>(e).interaction_id)
    } else if *t == EnvelopeType::USER_INPUT_RESOLVE_REQUEST {
        s(&payload::<UserInputResolveRequest>(e).interaction_id)
    } else if *t == EnvelopeType::USER_INPUT_CANCEL_REQUEST {
        s(&payload::<UserInputCancelRequest>(e).interaction_id)
    } else if *t == EnvelopeType::USER_INPUT_RESOLVE_RESPONSE
        || *t == EnvelopeType::USER_INPUT_CANCEL_RESPONSE
    {
        s(&payload::<UserInputResolveResponse>(e).interaction_id)
    } else {
        String::new()
    }
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
            runs: HashMap::new(),
            active: HashMap::new(),
            late_cancels: HashSet::new(),
            recoveries: HashMap::new(),
            initialized: false,
            participants: HashSet::new(),
            features: HashMap::new(),
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
            let (session, run) = request_scope(e);
            self.check_scope(at, e, &session, &run);
            let request = Request {
                envelope_type: e.envelope_type.clone(),
                index: i,
                line,
                envelope: e.clone(),
                capability_revision: revision.clone(),
                session,
                run,
                interaction: envelope_interaction(e),
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
        self.payload_scope(at, e);
        self.record_features(e);
        self.feature_for(at, e);
        self.run_dispatch(at, e);
        self.participants_from(at, e);
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
        let (req_session, req_run, req_interaction) = (
            request.session.clone(),
            request.run.clone(),
            request.interaction.clone(),
        );
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
        let error = e.envelope_type == EnvelopeType::ERROR_RESPONSE;
        let (session, run, prefix) = if error {
            (os(&e.session_id), os(&e.run_id), "")
        } else {
            let (session, run) = response_scope(e);
            (session, run, "/payload")
        };
        let kind = if error { "error response" } else { "response" };
        if !req_session.is_empty() && (!error || !session.is_empty()) && session != req_session {
            let message = format!("{kind} session does not match the request scope");
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                &format!("{prefix}/session_id"),
                &message,
                &req_session,
                &session,
                &[&reply],
            );
        }
        if !req_run.is_empty() && (!error || !run.is_empty()) && run != req_run {
            let message = format!("{kind} run does not match the request scope");
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                &format!("{prefix}/run_id"),
                &message,
                &req_run,
                &run,
                &[&reply],
            );
        }
        let got = envelope_interaction(e);
        if !error && !req_interaction.is_empty() && !got.is_empty() && got != req_interaction {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/interaction_id",
                "response interaction does not match the request scope",
                &req_interaction,
                &got,
                &[&reply],
            );
        }
        true
    }

    fn check_scope(&mut self, at: (usize, usize), e: &Envelope, session: &str, run: &str) {
        let (es, er) = (os(&e.session_id), os(&e.run_id));
        if !es.is_empty() && !session.is_empty() && es != session {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/session_id",
                "envelope and payload session_id differ",
                &es,
                session,
                &[],
            );
        }
        if !er.is_empty() && !run.is_empty() && er != run {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/run_id",
                "envelope and payload run_id differ",
                &er,
                run,
                &[],
            );
        }
    }

    fn payload_scope(&mut self, at: (usize, usize), e: &Envelope) {
        let t = &e.envelope_type;
        let (session, run) = if *t == EnvelopeType::SESSION_STATE_RESPONSE
            || *t == EnvelopeType::SESSION_STATE_UPDATED
        {
            let p: SessionState = payload(e);
            (s(&p.session_id), os(&p.active_run_id))
        } else if *t == EnvelopeType::SESSION_OPEN_RESPONSE || *t == EnvelopeType::MODELS_RESPONSE {
            (response_scope(e).0, String::new())
        } else if *t == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST {
            (request_scope(e).0, String::new())
        } else if [
            EnvelopeType::RUN_CANCEL_RESPONSE,
            EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE,
            EnvelopeType::ACTION_PERMISSION_RESOLVE_RESPONSE,
            EnvelopeType::USER_INPUT_RESOLVE_RESPONSE,
            EnvelopeType::USER_INPUT_CANCEL_RESPONSE,
        ]
        .contains(t)
        {
            response_scope(e)
        } else if [
            EnvelopeType::RUN_CANCEL_REQUEST,
            EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
            EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST,
            EnvelopeType::USER_INPUT_RESOLVE_REQUEST,
            EnvelopeType::USER_INPUT_CANCEL_REQUEST,
        ]
        .contains(t)
        {
            request_scope(e)
        } else {
            return;
        };
        self.check_scope(at, e, &session, &run);
        let call = if *t == EnvelopeType::ACTION_CALL_RESOLVE_REQUEST {
            Some(s(&payload::<ActionCallResolveRequest>(e).tool_call_id))
        } else if *t == EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE {
            Some(s(&payload::<ActionCallResolveResponse>(e).tool_call_id))
        } else {
            None
        };
        let envelope_call = os(&e.tool_call_id);
        if let Some(call) = call.filter(|call| !envelope_call.is_empty() && *call != envelope_call)
        {
            self.add_expected(
                codes::SCOPE_MISMATCH,
                at,
                e,
                "/payload/tool_call_id",
                "envelope and payload tool_call_id differ",
                &envelope_call,
                &call,
                &[],
            );
        }
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
        self.close_runs();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn envelope(value: serde_json::Value) -> Envelope {
        Envelope::parse(value.to_string().as_bytes()).unwrap()
    }

    #[test]
    fn a_response_answering_another_interaction_than_its_request_is_a_scope_mismatch() {
        let request = envelope(serde_json::json!({
            "type": "user.input.resolve.request", "id": "q1", "session_id": "s", "run_id": "r",
            "payload": {"interaction_id": "i1", "requested_by": "agent", "responded_by": "user", "session_id": "s", "run_id": "r", "answers": []}
        }));
        let answer = |interaction: &str| {
            envelope(serde_json::json!({
                "type": "user.input.resolve.response", "id": format!("a-{interaction}"), "in_reply_to": "q1",
                "payload": {"interaction_id": interaction, "session_id": "s", "run_id": "r", "accepted": true}
            }))
        };
        for (interaction, mismatched) in [("i1", false), ("i2", true)] {
            let mut state = State::new("t");
            state.apply(0, 0, &request);
            state.apply(1, 0, &answer(interaction));
            let raised: Vec<&str> = state
                .diagnostics
                .iter()
                .filter(|d| d.code == codes::SCOPE_MISMATCH)
                .map(|d| d.pointer.as_str())
                .collect();
            assert_eq!(
                raised == ["/payload/interaction_id"],
                mismatched,
                "{interaction}: {raised:?}"
            );
        }
    }

    #[test]
    fn a_call_resolution_whose_envelope_names_another_session_run_or_call_is_a_scope_mismatch_both_ways(
    ) {
        let payload = serde_json::json!({
            "interaction_id": "i", "session_id": "s", "run_id": "r", "tool_call_id": "t",
            "requested_by": "agent", "responded_by": "control", "started": {}
        });
        let request = |session: &str, run: &str, call: &str| {
            envelope(serde_json::json!({
                "type": "action.call.resolve.request", "id": "c1", "session_id": session, "run_id": run,
                "tool_call_id": call, "payload": payload
            }))
        };
        let response = |call: &str| {
            envelope(serde_json::json!({
                "type": "action.call.resolve.response", "id": "c2", "in_reply_to": "c1", "tool_call_id": call,
                "payload": {"interaction_id": "i", "session_id": "s", "run_id": "r", "tool_call_id": "t", "accepted": true}
            }))
        };
        let pointers = |request: Envelope, response: Envelope| {
            let mut state = State::new("t");
            state.apply(0, 0, &request);
            state.apply(1, 0, &response);
            let mut found: Vec<String> = state
                .diagnostics
                .iter()
                .map(|d| d.pointer.clone())
                .collect();
            found.sort();
            found
        };
        assert!(pointers(request("s", "r", "t"), response("t")).is_empty());
        assert_eq!(
            pointers(request("x", "r", "t"), response("t")),
            ["/payload/session_id", "/payload/session_id"]
        );
        assert_eq!(
            pointers(request("s", "x", "t"), response("t")),
            ["/payload/run_id", "/payload/run_id"]
        );
        assert_eq!(
            pointers(request("s", "r", "x"), response("t")),
            ["/payload/tool_call_id"]
        );
        assert_eq!(
            pointers(request("s", "r", "t"), response("x")),
            ["/payload/tool_call_id"]
        );
    }
}
