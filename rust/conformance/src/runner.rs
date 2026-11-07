use oap_protocol::{
    CapabilitiesRequest, CapabilityDescriptor, Envelope, EnvelopeId, EnvelopeType, ErrorResponse,
    InitializeRequest, InitializeResponse, Participant, SessionId, SessionOpenRequest,
    SessionState, SupportLevel, FEATURE_SESSION_MODEL_SWITCH, PROFILE, VERSION,
};
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};

use crate::Client;

mod models;
mod probes;
mod replay;
mod runs;

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Check {
    pub name: String,
    pub passed: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub detail: String,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub skipped: bool,
}

impl Check {
    pub fn passed(name: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            passed: true,
            ..Self::default()
        }
    }

    pub fn failed(name: impl Into<String>, detail: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            detail: detail.into(),
            ..Self::default()
        }
    }

    pub fn skipped(name: impl Into<String>, detail: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            detail: detail.into(),
            passed: true,
            skipped: true,
        }
    }
}

pub struct Runner<'a> {
    client: &'a mut Client,
    session: SessionId,
    revision: String,
    ids: u64,
    checks: Vec<Check>,
}

impl<'a> Runner<'a> {
    pub fn new(client: &'a mut Client, session: impl Into<SessionId>) -> Self {
        let session = session.into();
        Self {
            client,
            session: if session.as_str().is_empty() {
                "conformance".into()
            } else {
                session
            },
            revision: String::new(),
            ids: 0,
            checks: Vec::new(),
        }
    }

    pub fn checks(&self) -> &[Check] {
        &self.checks
    }

    pub fn into_checks(self) -> Vec<Check> {
        self.checks
    }

    pub(crate) fn drive(&mut self, preferred_model: &str) {
        self.initialize();
        let Some(descriptor) = self.capabilities() else {
            return;
        };
        let Some(state) = self.open() else {
            return;
        };
        self.list_sessions(&descriptor);
        let selected = self.select_model(&descriptor, &state.current_model_id, preferred_model);
        let switched = self.switch_model(&selected, &state.current_model_id);
        let Some(run) = self.submit_run(&selected, switched) else {
            return;
        };
        self.consume_run(&run);
        self.replay_run(&run);
        self.check_state();
        self.refuse_stale_revision();
        self.answer_cancel(&descriptor, Some(&run));
        self.refuse_addressable_envelope();
        self.refuse_unknown_reopen(&descriptor);
    }

    pub fn initialize(&mut self) {
        const NAME: &str = "protocol.initialize.request is answered";
        let payload = InitializeRequest {
            protocol_versions: vec![VERSION.into()],
            profiles: vec![PROFILE.into()],
            participant: Some(Participant {
                id: "conformance".into(),
                name: "OAP conformance runner".into(),
                ..Participant::default()
            }),
        };
        match self.request(EnvelopeType::PROTOCOL_INITIALIZE_REQUEST, &payload) {
            Err(error) => self.checks.push(Check::failed(NAME, error)),
            Ok(answer) => {
                if self
                    .decode::<InitializeResponse>(&answer, "protocol.initialize.response decodes")
                    .is_some()
                {
                    self.checks.push(Check::passed(NAME));
                }
            }
        }
    }

    pub fn capabilities(&mut self) -> Option<CapabilityDescriptor> {
        let answer = self.request(EnvelopeType::CAPABILITIES_REQUEST, &CapabilitiesRequest {});
        let answer = self.record("capabilities.request is answered", answer)?;
        let descriptor: CapabilityDescriptor =
            self.decode(&answer, "capabilities.response decodes as a descriptor")?;
        self.revision = answer.capability_revision.unwrap_or_default();
        const REVISION: &str = "capabilities.response carries a capability revision";
        self.checks.push(if self.revision.is_empty() {
            Check::failed(REVISION, "the response set no capability_revision, so nothing can be bound to this descriptor")
        } else {
            Check::passed(REVISION)
        });
        const SWITCH: &str = "core session.model.switch is advertised";
        let supported = supports(&descriptor, FEATURE_SESSION_MODEL_SWITCH);
        self.checks.push(if supported {
            Check::passed(SWITCH)
        } else {
            Check::failed(
                SWITCH,
                "the endpoint did not advertise the core model switch capability",
            )
        });
        Some(descriptor)
    }

    pub fn open(&mut self) -> Option<SessionState> {
        let payload = SessionOpenRequest {
            session_id: Some(self.session.clone()),
            ..SessionOpenRequest::default()
        };
        let answer = self.request(EnvelopeType::SESSION_OPEN_REQUEST, &payload);
        let answer = self.record("session.open.request is answered", answer)?;
        let state: SessionState =
            self.decode(&answer, "session.open.response decodes as a session state")?;
        const NAME: &str = "the open names the session it was asked for";
        self.checks.push(if state.session_id == self.session {
            Check::passed(NAME)
        } else {
            Check::failed(
                NAME,
                format!(
                    "opened {:?}, asked for {:?}",
                    state.session_id.as_str(),
                    self.session.as_str()
                ),
            )
        });
        Some(state)
    }

    fn request(
        &mut self,
        kind: EnvelopeType,
        payload: &impl Serialize,
    ) -> Result<Envelope, String> {
        let envelope = self.envelope(kind, payload)?;
        self.request_envelope(envelope)
    }

    fn request_envelope(&mut self, envelope: Envelope) -> Result<Envelope, String> {
        let kind = envelope.envelope_type.clone();
        let answer = self.exchange_envelope(envelope)?;
        if answer.envelope_type == EnvelopeType::ERROR_RESPONSE {
            let failure: ErrorResponse = decode_payload(&answer)?;
            return Err(format!(
                "{kind} was refused {}: {}",
                failure.error.code, failure.error.message
            ));
        }
        Ok(answer)
    }

    fn next(&mut self, kind: &str) -> EnvelopeId {
        self.ids += 1;
        format!("conformance-{kind}-{}", self.ids).into()
    }

    fn envelope(
        &mut self,
        kind: EnvelopeType,
        payload: &impl Serialize,
    ) -> Result<Envelope, String> {
        let mut envelope = Envelope::new(kind, self.next("request"), payload)
            .map_err(|error| error.to_string())?;
        envelope.session_id = Some(self.session.clone());
        envelope.capability_revision = (!self.revision.is_empty()).then(|| self.revision.clone());
        Ok(envelope)
    }

    fn exchange(
        &mut self,
        kind: EnvelopeType,
        payload: &impl Serialize,
    ) -> Result<Envelope, String> {
        let envelope = self.envelope(kind, payload)?;
        self.exchange_envelope(envelope)
    }

    fn exchange_envelope(&mut self, envelope: Envelope) -> Result<Envelope, String> {
        let id = envelope.id.clone();
        self.client
            .send(envelope)
            .map_err(|error| error.to_string())?;
        self.client.response(&id).map_err(|error| error.to_string())
    }

    fn record(&mut self, name: &str, result: Result<Envelope, String>) -> Option<Envelope> {
        let answer = self.result(name, result)?;
        self.checks.push(Check::passed(name));
        Some(answer)
    }

    fn decode<T: DeserializeOwned + Default>(
        &mut self,
        answer: &Envelope,
        name: &str,
    ) -> Option<T> {
        self.result(name, decode_payload(answer))
    }

    fn result<T>(&mut self, name: &str, result: Result<T, String>) -> Option<T> {
        match result {
            Ok(payload) => Some(payload),
            Err(error) => {
                self.checks.push(Check::failed(name, error));
                None
            }
        }
    }
}

fn supports(descriptor: &CapabilityDescriptor, feature: &str) -> bool {
    descriptor.features.get(feature).is_some_and(|support| {
        !support.level.as_str().is_empty() && support.level != SupportLevel::UNAVAILABLE
    })
}

fn decode_payload<T: DeserializeOwned + Default>(answer: &Envelope) -> Result<T, String> {
    let payload = answer
        .payload
        .as_ref()
        .ok_or_else(|| oap_protocol::Error::MissingPayload.to_string())?;
    oap_adapter::gojson::structure(&mut serde_json::Deserializer::from_str(payload.get())).map_err(
        |error| {
            format!(
                "{} payload could not be decoded as an object ({:?})",
                answer.envelope_type,
                error.classify()
            )
        },
    )
}
