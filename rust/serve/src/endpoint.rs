use std::io::{BufRead, Write};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Condvar, Mutex};
use std::time::{Duration, Instant};

use oap_protocol::{Envelope, EnvelopeType};
use serde_json::Value;

use crate::live::block_on;
use crate::stdio::{detailed, wire, WireError};
use crate::{Hub, HubError, Subscription};

pub const DEFAULT_FRAME_LIMIT: usize = 1 << 20;
pub const DEFAULT_SHUTDOWN: Duration = Duration::from_secs(10);
pub const DEFAULT_WRITE_STALL: Duration = Duration::from_secs(120);

const WRITE_QUEUE: usize = 64;

const BASE_MEMBERS: [&str; 6] = ["protocol", "version", "profile", "type", "id", "payload"];

#[derive(Clone, Debug, Default)]
pub struct EndpointOptions {
    pub adapter: String,
    pub frame_limit: usize,
    pub shutdown: Option<Duration>,
    pub write_stall: Option<Duration>,
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
    #[error(
        "serveendpoint: shutdown outlived its bounded window; the stalled run stream was abandoned"
    )]
    ShutdownStalled,
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

pub struct Endpoint {
    hub: Hub,
    adapter: String,
    frame_limit: usize,
    ids: AtomicU64,
    participant: std::sync::Mutex<String>,
    shutdown: Duration,
    write_stall: Duration,
    started: Mutex<Option<(Subscription, oap_protocol::RunId)>>,
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
            participant: std::sync::Mutex::default(),
            shutdown: options.shutdown.unwrap_or(DEFAULT_SHUTDOWN),
            write_stall: options.write_stall.unwrap_or(DEFAULT_WRITE_STALL),
            started: Mutex::default(),
        })
    }

    pub fn run(
        &self,
        mut input: impl BufRead,
        output: impl Write + Send + 'static,
    ) -> Result<(), EndpointError> {
        let (sender, lines) = std::sync::mpsc::sync_channel(WRITE_QUEUE);
        let (written, writer) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let _ = written.send(write_lines(output, &lines));
        });
        let out = Out {
            sender,
            limit: self.frame_limit,
            stall: self.write_stall,
        };
        let pumps = Pumps::default();
        let served = self.serve_lines(&mut input, &out, &pumps);
        let stalled = pumps.settle(served.is_err(), self.shutdown);
        drop(out);
        if stalled {
            return Err(served.err().unwrap_or(EndpointError::ShutdownStalled));
        }
        let flushed = match writer.recv_timeout(self.shutdown) {
            Ok(flushed) => flushed.map_err(EndpointError::from),
            Err(_) => Err(EndpointError::ShutdownStalled),
        };
        served.and(flushed)
    }

    fn serve_lines(
        &self,
        input: &mut impl BufRead,
        out: &Out,
        pumps: &Pumps,
    ) -> Result<(), EndpointError> {
        while let Some(line) = self.read_line(input)? {
            if String::from_utf8_lossy(&line).trim().is_empty() {
                continue;
            }
            if let Some(answer) = self.handle(&line)? {
                out.send(answer)?;
            }
            if let Some((subscription, run)) = lock(&self.started).take() {
                pumps.start(out.clone(), subscription, run);
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
        if *kind == EnvelopeType::SESSION_OPEN_REQUEST {
            return self.open(envelope);
        }
        if *kind == EnvelopeType::SESSION_STATE_REQUEST {
            return self.state(envelope);
        }
        if *kind == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST {
            return self.submit(envelope);
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
        let asked = envelope
            .decode_payload::<oap_protocol::InitializeRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        if let Some(named) = asked
            .participant
            .map(|participant| participant.id)
            .filter(|id| !id.as_str().is_empty())
        {
            *self
                .participant
                .lock()
                .unwrap_or_else(std::sync::PoisonError::into_inner) = named.as_str().to_owned();
        }
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

    fn open(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let asked = envelope
            .decode_payload::<oap_protocol::SessionOpenRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        block_on(async {
            crate::election_gate(&self.hub, &self.adapter, "", &asked).await?;
            crate::attachment_gate(&self.hub, &self.adapter, "", &asked).await
        })
        .map_err(hub_refusal)?;
        if asked.message.is_some() {
            return Err(hub_refusal(HubError::Adapter(
                oap_adapter::Error::UnsupportedControl(Box::new(oap_adapter::UnsupportedControl {
                    feature: "session.message.submit".into(),
                    reason: oap_adapter::CONTROL_UNSATISFIABLE,
                    field: "message".into(),
                    ..oap_adapter::UnsupportedControl::default()
                })),
            )));
        }
        let tool_sources = crate::resolve_attachments(self.hub.registry(), &asked.tool_sources)
            .map_err(|refused| {
                detailed(
                    "unsupported_feature",
                    &refused,
                    serde_json::json!({
                        "feature": oap_protocol::FEATURE_TOOL_SOURCES_ATTACH,
                        "reason": oap_adapter::CONTROL_UNSATISFIABLE,
                        "source": refused.tool_source,
                    }),
                )
            })?;
        let mut metadata = serde_json::Map::new();
        for (key, raw) in &asked.metadata {
            let value: Value = serde_json::from_str(raw.get())
                .map_err(|err| wire("invalid_payload", format!("metadata {key:?}: {err}")))?;
            metadata.insert(key.clone(), value);
        }
        let participant = self
            .participant
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
            .clone();
        let opening = oap_adapter::OpenRequest {
            session_id: asked.session_id.clone().unwrap_or_default(),
            participant: oap_protocol::Participant {
                id: if participant.is_empty() {
                    crate::DEFAULT_PARTICIPANT.into()
                } else {
                    participant.as_str().into()
                },
                ..oap_protocol::Participant::default()
            },
            metadata,
            tool_sources,
            tools: asked.tools.clone(),
            allow_degraded_features: asked.allow_degraded_features.clone(),
            reopen: asked.reopen,
            reasoning_level: asked.reasoning_level.clone(),
            compaction_policy: asked.compaction_policy.clone(),
            ..oap_adapter::OpenRequest::default()
        };
        let opened = block_on(self.hub.open(&self.adapter, opening)).map_err(hub_refusal)?;
        self.session_reply(
            EnvelopeType::SESSION_OPEN_RESPONSE,
            envelope,
            &opened.state,
            opened.session.id(),
        )
    }

    fn submit(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let request = envelope
            .decode_payload::<oap_protocol::MessageSubmitRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        let subscription = self.hub.subscribe(entry.id()).map_err(hub_refusal)?;
        let admission = block_on(entry.submit(oap_adapter::SubmitRequest {
            request,
            envelope_id: envelope.id.clone(),
        }))
        .map_err(hub_refusal)?;
        let mut answer = self.session_reply(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE,
            envelope,
            &admission,
            &admission.session_id,
        )?;
        answer.run_id = admission.run_id.clone();
        if admission.admission != oap_protocol::Admission::STEERED {
            *lock(&self.started) = Some((subscription, admission.run_id.unwrap_or_default()));
        }
        Ok(answer)
    }

    fn state(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let state = block_on(entry.state()).map_err(hub_refusal)?;
        self.session_reply(
            EnvelopeType::SESSION_STATE_RESPONSE,
            envelope,
            &state,
            entry.id(),
        )
    }

    fn session(&self, envelope: &Envelope) -> Result<std::sync::Arc<crate::HubSession>, WireError> {
        let id = envelope
            .session_id
            .clone()
            .filter(|id| !id.as_str().is_empty())
            .ok_or_else(|| wire("invalid_request", "this request must name its session"))?;
        self.hub.session(&id).map_err(hub_refusal)
    }

    fn session_reply(
        &self,
        kind: EnvelopeType,
        asked: &Envelope,
        payload: &impl serde::Serialize,
        session: &oap_protocol::SessionId,
    ) -> Result<Envelope, WireError> {
        let mut answer = self.reply(
            kind,
            asked,
            payload,
            asked.capability_revision.as_deref().unwrap_or_default(),
        )?;
        answer.session_id = Some(session.clone());
        Ok(answer)
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

fn lock<T>(mutex: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    mutex
        .lock()
        .unwrap_or_else(std::sync::PoisonError::into_inner)
}

#[derive(Clone)]
struct Out {
    sender: std::sync::mpsc::SyncSender<Vec<u8>>,
    limit: usize,
    stall: Duration,
}

impl Out {
    fn send(&self, mut line: Vec<u8>) -> Result<(), EndpointError> {
        if line.len() + 1 > self.limit {
            return Err(EndpointError::FrameTooLarge(line.len()));
        }
        line.push(b'\n');
        let deadline = Instant::now() + self.stall;
        loop {
            match self.sender.try_send(line) {
                Ok(()) => return Ok(()),
                Err(std::sync::mpsc::TrySendError::Full(back)) => {
                    if Instant::now() >= deadline {
                        return Err(EndpointError::ShutdownStalled);
                    }
                    line = back;
                    std::thread::sleep(Duration::from_millis(1));
                }
                Err(std::sync::mpsc::TrySendError::Disconnected(_)) => {
                    return Err(std::io::Error::from(std::io::ErrorKind::BrokenPipe).into())
                }
            }
        }
    }
}

fn write_lines(
    mut output: impl Write,
    lines: &std::sync::mpsc::Receiver<Vec<u8>>,
) -> std::io::Result<()> {
    let mut failure = None;
    for line in lines {
        if failure.is_none() {
            failure = output.write_all(&line).and_then(|()| output.flush()).err();
        }
    }
    failure.map_or(Ok(()), Err)
}

#[derive(Default)]
struct Pumps {
    live: Arc<(Mutex<usize>, Condvar)>,
    cancellers: Mutex<Vec<crate::live::Canceller>>,
}

impl Pumps {
    fn start(&self, out: Out, mut subscription: Subscription, run: oap_protocol::RunId) {
        lock(&self.cancellers).push(subscription.canceller());
        *lock(&self.live.0) += 1;
        let live = self.live.clone();
        std::thread::spawn(move || {
            pump(&out, &mut subscription, &run);
            drop(subscription);
            *lock(&live.0) -= 1;
            live.1.notify_all();
        });
    }

    fn drained_within(&self, window: Duration) -> bool {
        let deadline = Instant::now() + window;
        let mut live = lock(&self.live.0);
        while *live > 0 {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return false;
            }
            live = self
                .live
                .1
                .wait_timeout(live, left)
                .unwrap_or_else(std::sync::PoisonError::into_inner)
                .0;
        }
        true
    }

    fn cancel(&self) {
        for canceller in lock(&self.cancellers).iter() {
            canceller.cancel();
        }
    }

    fn settle(&self, failed: bool, window: Duration) -> bool {
        if failed {
            self.cancel();
        }
        if self.drained_within(window) {
            return false;
        }
        self.cancel();
        !self.drained_within(window)
    }
}

fn pump(out: &Out, subscription: &mut Subscription, run: &oap_protocol::RunId) {
    let mut delivered = 0;
    loop {
        let next = block_on(std::future::poll_fn(|context| {
            futures_core::Stream::poll_next(std::pin::Pin::new(&mut *subscription), context)
        }));
        let code = match next {
            None => return,
            Some(Ok(envelope)) => {
                if !run.as_str().is_empty() && envelope.run_id.as_ref() != Some(run) {
                    continue;
                }
                let sequence = envelope.sequence;
                let sent = serde_json::to_vec(&envelope)
                    .map_err(|err| EndpointError::from(std::io::Error::from(err)))
                    .and_then(|line| out.send(line));
                match sent {
                    Ok(()) => {
                        delivered = sequence.unwrap_or(delivered);
                        continue;
                    }
                    Err(EndpointError::FrameTooLarge(_)) => "frame_limit",
                    Err(_) => "stream_failed",
                }
            }
            Some(Err(HubError::Overflow { .. })) => "overflow",
            Some(Err(_)) => "stream_failed",
        };
        let mut lost = serde_json::json!({
            "control": "stream.lost",
            "after": delivered,
            "code": code,
            "message": "this run's events stopped reaching the host; replay from after to continue",
        });
        if !run.as_str().is_empty() {
            lost["run_id"] = run.as_str().into();
        }
        if let Ok(line) = serde_json::to_vec(&lost) {
            let _ = out.send(line);
        }
        return;
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
