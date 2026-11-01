use std::io::{BufRead, Write};
use std::sync::atomic::{AtomicU64, Ordering};

use oap_protocol::{Envelope, EnvelopeType};
use serde_json::Value;

use crate::live::block_on;
use crate::stdio::{detailed, wire, WireError};
use crate::{Hub, HubError};

pub const DEFAULT_FRAME_LIMIT: usize = 1 << 20;

const BASE_MEMBERS: [&str; 6] = ["protocol", "version", "profile", "type", "id", "payload"];

#[derive(Clone, Debug, Default)]
pub struct EndpointOptions {
    pub adapter: String,
    pub frame_limit: usize,
}

#[derive(Debug, thiserror::Error)]
pub enum EndpointError {
    #[error("serveendpoint: an adapter name is required")]
    NoAdapter,
    #[error("serveendpoint: no adapter {0:?}")]
    UnknownAdapter(String),
    #[error("serveendpoint: line exceeds the frame limit: {0} bytes")]
    FrameTooLarge(usize),
    #[error("serveendpoint: line is not a JSON envelope: {0}")]
    MalformedLine(String),
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

pub struct Endpoint {
    hub: Hub,
    adapter: String,
    frame_limit: usize,
    ids: AtomicU64,
}

impl Endpoint {
    pub fn new(hub: &Hub, options: EndpointOptions) -> Result<Self, EndpointError> {
        if options.adapter.is_empty() {
            return Err(EndpointError::NoAdapter);
        }
        if hub.registry().lookup(&options.adapter).is_none() {
            return Err(EndpointError::UnknownAdapter(options.adapter));
        }
        Ok(Self {
            hub: hub.clone(),
            adapter: options.adapter,
            frame_limit: match options.frame_limit {
                0 => DEFAULT_FRAME_LIMIT,
                limit => limit,
            },
            ids: AtomicU64::new(0),
        })
    }

    pub fn run(
        &self,
        mut input: impl BufRead,
        mut output: impl Write,
    ) -> Result<(), EndpointError> {
        while let Some(line) = self.read_line(&mut input)? {
            if String::from_utf8_lossy(&line).trim().is_empty() {
                continue;
            }
            if let Some(answer) = self.handle(&line)? {
                if answer.len() + 1 > self.frame_limit {
                    return Err(EndpointError::FrameTooLarge(answer.len()));
                }
                output.write_all(&answer)?;
                output.write_all(b"\n")?;
                output.flush()?;
            }
        }
        Ok(())
    }

    fn read_line(&self, input: &mut impl BufRead) -> Result<Option<Vec<u8>>, EndpointError> {
        let mut line = Vec::new();
        loop {
            let buffer = input.fill_buf()?;
            if buffer.is_empty() {
                return Ok((!line.is_empty()).then_some(line));
            }
            let (taken, ended) = match buffer.iter().position(|byte| *byte == b'\n') {
                Some(at) => (at + 1, true),
                None => (buffer.len(), false),
            };
            line.extend_from_slice(&buffer[..taken]);
            input.consume(taken);
            if ended {
                line.pop();
                if line.last() == Some(&b'\r') {
                    line.pop();
                }
            }
            if line.len() > self.frame_limit {
                return Err(EndpointError::FrameTooLarge(line.len()));
            }
            if ended {
                return Ok(Some(line));
            }
        }
    }

    fn handle(&self, line: &[u8]) -> Result<Option<Vec<u8>>, EndpointError> {
        let malformed = |detail: String| EndpointError::MalformedLine(detail);
        let shape: Value =
            serde_json::from_slice(line).map_err(|err| malformed(err.to_string()))?;
        let text = |key: &str| match &shape[key] {
            Value::Null => Ok(String::new()),
            Value::String(text) => Ok(text.clone()),
            _ => Err(malformed(format!("{key} is not a string"))),
        };
        let (protocol, control) = (text("protocol")?, text("control")?);
        if protocol.is_empty() && !control.is_empty() {
            let refused = serde_json::json!({
                "control": "replay.error",
                "id": text("id").unwrap_or_default(),
                "code": "unsupported_control",
                "message": format!("this endpoint serves no {control:?} control"),
            });
            return Ok(Some(
                serde_json::to_vec(&refused).map_err(std::io::Error::from)?,
            ));
        }
        if protocol.is_empty() {
            return Err(malformed(
                "the line declares neither protocol nor control".into(),
            ));
        }
        let decoded = serde_json::from_slice::<Envelope>(line);
        let named = shape["id"].as_str().unwrap_or_default();
        if named.is_empty() {
            return Err(malformed("an envelope needs an id to be answerable".into()));
        }
        let envelope = match decoded {
            Ok(envelope) => envelope,
            Err(err) => {
                let mut asked = Envelope {
                    id: named.into(),
                    ..Envelope::default()
                };
                asked.session_id = shape["session_id"].as_str().map(Into::into);
                asked.run_id = shape["run_id"].as_str().map(Into::into);
                return self.encode(self.refused(&asked, wire("invalid_request", err)));
            }
        };
        let missing: Vec<&str> = BASE_MEMBERS
            .into_iter()
            .filter(|member| shape[*member].is_null())
            .collect();
        if !missing.is_empty() {
            return self.encode(self.refused(
                &envelope,
                wire(
                    "invalid_request",
                    format!(
                        "the envelope omits required member(s): {}",
                        missing.join(", ")
                    ),
                ),
            ));
        }
        let answer = self
            .serve(&envelope)
            .unwrap_or_else(|refused| self.refused(&envelope, refused));
        self.encode(answer)
    }

    fn encode(&self, envelope: Envelope) -> Result<Option<Vec<u8>>, EndpointError> {
        Ok(Some(
            serde_json::to_vec(&envelope).map_err(std::io::Error::from)?,
        ))
    }

    fn serve(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        self.refuse_stale_revision(envelope)?;
        let kind = &envelope.envelope_type;
        if *kind == EnvelopeType::PROTOCOL_INITIALIZE_REQUEST {
            return self.initialize(envelope);
        }
        if *kind == EnvelopeType::CAPABILITIES_REQUEST {
            return self.capabilities(envelope);
        }
        Err(wire(
            "unsupported_request",
            format!("this endpoint serves no {}", kind.as_str()),
        ))
    }

    fn next_id(&self, kind: &str) -> String {
        format!("oap-{kind}-{}", self.ids.fetch_add(1, Ordering::SeqCst) + 1)
    }

    fn probe(&self) -> Result<oap_adapter::Descriptor, WireError> {
        block_on(self.hub.probe(&self.adapter)).map_err(hub_refusal)
    }

    fn reply(
        &self,
        kind: EnvelopeType,
        asked: &Envelope,
        payload: &impl serde::Serialize,
        revision: &str,
    ) -> Result<Envelope, WireError> {
        let mut answer = Envelope::new(kind, self.next_id("response"), payload)
            .map_err(|err| wire("internal", err))?;
        answer.in_reply_to = Some(asked.id.clone());
        answer.capability_revision = (!revision.is_empty()).then(|| revision.to_owned());
        Ok(answer)
    }

    fn refuse_stale_revision(&self, envelope: &Envelope) -> Result<(), WireError> {
        let Some(cited) = envelope
            .capability_revision
            .as_deref()
            .filter(|cited| !cited.is_empty())
        else {
            return Ok(());
        };
        let kind = &envelope.envelope_type;
        if *kind == EnvelopeType::PROTOCOL_INITIALIZE_REQUEST
            || *kind == EnvelopeType::CAPABILITIES_REQUEST
        {
            return Ok(());
        }
        let current = self.probe()?.capability_revision;
        if current.is_empty() || cited == current {
            return Ok(());
        }
        Err(detailed(
            "stale_capabilities",
            format!("capability revision {cited:?} is not the current {current:?}"),
            serde_json::json!({ "expected_revision": cited, "current_revision": current }),
        ))
    }

    fn initialize(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let descriptor = self.probe()?;
        envelope
            .decode_payload::<oap_protocol::InitializeRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        self.reply(
            EnvelopeType::PROTOCOL_INITIALIZE_RESPONSE,
            envelope,
            &oap_protocol::InitializeResponse {
                protocol_version: oap_protocol::VERSION.into(),
                profile: oap_protocol::PROFILE.into(),
                endpoint: descriptor.capabilities.endpoint,
            },
            &descriptor.capability_revision,
        )
    }

    fn capabilities(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let descriptor = self.probe()?;
        let mut capabilities = descriptor.capabilities;
        if self.hub.bindings().is_some() {
            capabilities.features.insert(
                oap_protocol::FEATURE_SESSION_LIST.into(),
                oap_protocol::FeatureSupport {
                    level: oap_protocol::SupportLevel::EMULATED,
                    reason: "listed from the session history this endpoint's hub keeps".into(),
                    ..oap_protocol::FeatureSupport::default()
                },
            );
        }
        if capabilities.bindings.is_empty() {
            capabilities.bindings = vec![oap_protocol::Binding {
                kind: "stdio".into(),
                serialization: "jsonl".into(),
            }];
        }
        self.reply(
            EnvelopeType::CAPABILITIES_RESPONSE,
            envelope,
            &capabilities,
            &descriptor.capability_revision,
        )
    }

    fn refused(&self, asked: &Envelope, refused: WireError) -> Envelope {
        let mut error = serde_json::json!({ "code": refused.code, "message": refused.message });
        if let Some(details) = refused.details {
            error["details"] = details;
        }
        let mut answer = Envelope::new(
            EnvelopeType::ERROR_RESPONSE,
            self.next_id("error"),
            &serde_json::json!({ "error": error }),
        )
        .unwrap_or_default();
        answer.in_reply_to = Some(asked.id.clone());
        answer.session_id = asked.session_id.clone();
        answer.run_id = asked.run_id.clone();
        answer
    }
}

fn hub_refusal(err: HubError) -> WireError {
    use oap_adapter::Error;
    if let Some(refused) = crate::stdio::refusal(&err) {
        return refused;
    }
    let code = match &err {
        HubError::UnknownAdapter(_) => "unknown_adapter",
        HubError::SessionExists(_) => "session_exists",
        HubError::UnknownSession(_) | HubError::Adapter(Error::UnknownSession(_)) => {
            "unknown_session"
        }
        HubError::ScopeMismatch { .. } => "scope_mismatch",
        HubError::SessionClosed(_) | HubError::Adapter(Error::SessionClosed) => "session_closed",
        HubError::Adapter(Error::RunAlreadyTerminal { .. }) => "run_terminal",
        HubError::Adapter(Error::RunNotFound) => "run_not_found",
        HubError::Adapter(Error::RunActive) => "run_active",
        HubError::Adapter(Error::InvalidSubmission | Error::UnsupportedInput) => {
            "invalid_submission"
        }
        HubError::Adapter(
            Error::InteractionNotFound
            | Error::InteractionResolved
            | Error::WrongResponder
            | Error::InvalidResolution,
        ) => "resolution_rejected",
        HubError::Adapter(Error::ToolCatalogUnavailable) => "tool_catalog_unavailable",
        _ => "internal",
    };
    wire(code, err)
}
