use oxap_protocol::{
    CapabilityDescriptor, EnvelopeType, ErrorResponse, ModelsRequest, ModelsResponse,
    SessionListRequest, SessionListResponse, SessionListState, SessionModelSwitchRequest,
    SessionModelSwitchResponse, SessionState, SessionStateRequest, FEATURE_MODELS_LIST,
    FEATURE_SESSION_LIST,
};

use super::{decode_payload, supports, Check, Runner};

impl Runner<'_> {
    pub fn select_model(
        &mut self,
        descriptor: &CapabilityDescriptor,
        current: &str,
        preferred: &str,
    ) -> String {
        if !preferred.is_empty() {
            return preferred.to_owned();
        }
        if !supports(descriptor, FEATURE_MODELS_LIST) {
            return current.to_owned();
        }
        let payload = ModelsRequest {
            session_id: self.session.clone(),
            ..ModelsRequest::default()
        };
        let result = self
            .request(EnvelopeType::MODELS_REQUEST, &payload)
            .and_then(|answer| decode_payload::<ModelsResponse>(&answer));
        let Ok(catalog) = result else {
            return current.to_owned();
        };
        catalog
            .models
            .iter()
            .find(|model| model.id != current)
            .or_else(|| catalog.models.iter().find(|model| model.default))
            .or_else(|| catalog.models.first())
            .map(|model| model.id.as_str())
            .filter(|id| !id.is_empty())
            .unwrap_or(current)
            .to_owned()
    }

    pub fn list_sessions(&mut self, descriptor: &CapabilityDescriptor) {
        const NAME: &str = "session.list is answered with the open session when advertised, or refused as unadvertised";
        let result = self
            .envelope(
                EnvelopeType::SESSION_LIST_REQUEST,
                &SessionListRequest::default(),
            )
            .and_then(|mut envelope| {
                envelope.session_id = None;
                let id = envelope.id.clone();
                self.client
                    .probe(envelope)
                    .map_err(|error| error.to_string())?;
                self.client.response(&id).map_err(|error| error.to_string())
            });
        let Some(answer) = self.result(NAME, result) else {
            return;
        };
        let passed = if supports(descriptor, FEATURE_SESSION_LIST) {
            if answer.envelope_type != EnvelopeType::SESSION_LIST_RESPONSE {
                false
            } else {
                let Some(page) = self.decode::<SessionListResponse>(&answer, NAME) else {
                    return;
                };
                page.sessions.iter().any(|entry| {
                    entry.session_id == self.session && entry.state == SessionListState::LIVE
                })
            }
        } else if answer.envelope_type == EnvelopeType::ERROR_RESPONSE {
            let Some(failure) = self.decode::<ErrorResponse>(&answer, NAME) else {
                return;
            };
            failure.error.code == "unsupported_feature"
        } else {
            false
        };
        self.checks.push(if passed {
            Check::passed(NAME)
        } else {
            Check::failed(
                NAME,
                format!(
                    "{} did not satisfy the session.list declaration for {:?}",
                    answer.envelope_type,
                    self.session.as_str()
                ),
            )
        });
    }

    pub fn switch_model(&mut self, model: &str, previous: &str) -> bool {
        const NAME: &str = "session.model.switch changes the session default";
        const CANONICAL: &str = "session.model.switch publishes canonical state";
        if model.is_empty() {
            self.checks.push(Check::failed(
                NAME,
                "no model is named by the caller, session state, or model catalog",
            ));
            return false;
        }
        let payload = SessionModelSwitchRequest {
            session_id: self.session.clone(),
            model_id: model.into(),
            ..SessionModelSwitchRequest::default()
        };
        let result = self.request(EnvelopeType::SESSION_MODEL_SWITCH_REQUEST, &payload);
        let Some(answer) = self.result(NAME, result) else {
            return false;
        };
        if answer.envelope_type != EnvelopeType::SESSION_MODEL_SWITCH_RESPONSE {
            self.checks.push(Check::failed(
                NAME,
                format!("answer type is {}", answer.envelope_type),
            ));
            return false;
        }
        let Some(switched) = self.decode::<SessionModelSwitchResponse>(&answer, NAME) else {
            return false;
        };
        if switched.session_id != self.session
            || switched.model_id != model
            || switched.previous_model_id != previous
        {
            self.checks.push(Check::failed(
                NAME,
                "switch response does not name the requested session, model and previous model",
            ));
            return false;
        }
        if model != previous {
            let result = self.client.event().map_err(|error| error.to_string());
            let Some(event) = self.result(CANONICAL, result) else {
                return false;
            };
            if event.envelope_type != EnvelopeType::SESSION_STATE_UPDATED {
                self.checks.push(Check::failed(
                    CANONICAL,
                    format!("event type is {}", event.envelope_type),
                ));
                return false;
            }
            let Some(state) = self.decode::<SessionState>(&event, CANONICAL) else {
                return false;
            };
            if state.session_id != self.session || state.current_model_id != model {
                self.checks.push(Check::failed(
                    CANONICAL,
                    "state update does not reflect the switched session and model",
                ));
                return false;
            }
            self.checks.push(Check::passed(CANONICAL));
        }
        let payload = SessionStateRequest {
            session_id: self.session.clone(),
        };
        let result = self.request(EnvelopeType::SESSION_STATE_REQUEST, &payload);
        let Some(answer) = self.result(NAME, result) else {
            return false;
        };
        let Some(state) = self.decode::<SessionState>(&answer, NAME) else {
            return false;
        };
        if state.current_model_id != model {
            self.checks.push(Check::failed(
                NAME,
                "state after switch does not reflect the requested model",
            ));
            return false;
        }
        self.checks.push(Check::passed(NAME));
        const MISSING: &str = "session.model.switch refuses a missing model with its id";
        let missing = format!("conformance-no-such-model-{}", self.next("missing"));
        let payload = SessionModelSwitchRequest {
            session_id: self.session.clone(),
            model_id: missing.clone(),
            ..SessionModelSwitchRequest::default()
        };
        let result = self.exchange(EnvelopeType::SESSION_MODEL_SWITCH_REQUEST, &payload);
        let Some(refusal) = self.result(MISSING, result) else {
            return true;
        };
        if refusal.envelope_type != EnvelopeType::ERROR_RESPONSE {
            self.checks.push(Check::failed(
                MISSING,
                format!("answer type is {}", refusal.envelope_type),
            ));
            return true;
        }
        let Some(failure) = self.decode::<ErrorResponse>(&refusal, MISSING) else {
            return true;
        };
        self.checks.push(
            if failure.error.code == "model_not_found"
                && failure
                    .error
                    .details
                    .get("model_id")
                    .and_then(serde_json::Value::as_str)
                    == Some(missing.as_str())
            {
                Check::passed(MISSING)
            } else {
                Check::failed(
                    MISSING,
                    "the refusal does not name model_not_found and the requested missing model id",
                )
            },
        );
        true
    }
}
