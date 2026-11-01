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
    updated: Mutex<Option<Envelope>>,
    gate: Arc<Gate>,
    acknowledged: Mutex<Option<oap_protocol::RunId>>,
    state_sequences: Mutex<std::collections::HashMap<oap_protocol::SessionId, u64>>,
    stopping: Mutex<crate::stdio::Stopping>,
    finished: std::sync::atomic::AtomicBool,
    abandoned: std::sync::atomic::AtomicBool,
    signals_seen: std::sync::atomic::AtomicUsize,
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
            shutdown: options
                .shutdown
                .filter(|window| !window.is_zero())
                .unwrap_or(DEFAULT_SHUTDOWN),
            write_stall: options
                .write_stall
                .filter(|stall| !stall.is_zero())
                .unwrap_or(DEFAULT_WRITE_STALL),
            started: Mutex::default(),
            updated: Mutex::default(),
            gate: Arc::default(),
            acknowledged: Mutex::default(),
            state_sequences: Mutex::default(),
            stopping: Mutex::default(),
            finished: std::sync::atomic::AtomicBool::new(false),
            abandoned: std::sync::atomic::AtomicBool::new(false),
            signals_seen: std::sync::atomic::AtomicUsize::new(0),
        })
    }

    pub fn run(
        &self,
        input: impl BufRead + Send + 'static,
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
        let (feed, frames) = std::sync::mpsc::sync_channel(1);
        let limit = self.frame_limit;
        std::thread::spawn(move || {
            let mut input = input;
            loop {
                let frame = read_line(&mut input, limit);
                let ended = !matches!(frame, Ok(Some(_)));
                if feed.send(frame).is_err() || ended {
                    return;
                }
            }
        });
        let served = self.serve_lines(&frames, &out, &pumps);
        self.gate.lift_all();
        let forced = lock(&self.stopping).mode == Some(oap_protocol::ShutdownMode::FORCE)
            || self.abandoned.load(Ordering::SeqCst);
        let stalled = pumps.settle(served.is_err() || forced, self.shutdown, &self.abandoned);
        drop(out);
        if stalled {
            return Err(served.err().unwrap_or(EndpointError::ShutdownStalled));
        }
        let window = if self.abandoned.load(Ordering::SeqCst) {
            crate::stdio::DRAIN_POLL
        } else {
            self.shutdown
        };
        let flushed = match writer.recv_timeout(window) {
            Ok(flushed) => flushed.map_err(EndpointError::from),
            Err(_) => Err(EndpointError::ShutdownStalled),
        };
        served.and(flushed)
    }

    fn serve_lines(
        &self,
        frames: &std::sync::mpsc::Receiver<Result<Option<Vec<u8>>, EndpointError>>,
        out: &Out,
        pumps: &Pumps,
    ) -> Result<(), EndpointError> {
        loop {
            if self.finished.load(Ordering::SeqCst) || self.abandoned.load(Ordering::SeqCst) {
                return Ok(());
            }
            let draining = lock(&self.stopping).draining;
            if draining && crate::stdio::Stopping::drained(&self.hub) {
                return Ok(());
            }
            let frame = match frames.recv_timeout(crate::stdio::DRAIN_POLL) {
                Ok(frame) => frame,
                Err(std::sync::mpsc::RecvTimeoutError::Timeout) => continue,
                Err(std::sync::mpsc::RecvTimeoutError::Disconnected) => return Ok(()),
            };
            let Some(line) = frame? else {
                return Ok(());
            };
            if String::from_utf8_lossy(&line).trim().is_empty() {
                continue;
            }
            if let Some(answer) = self.handle(&line)? {
                out.send(answer)?;
            }
            if let Some(run) = lock(&self.acknowledged).take() {
                self.gate.lift(&run);
            }
            if let Some((subscription, run)) = lock(&self.started).take() {
                pumps.start(out.clone(), self.gate.clone(), subscription, run);
            }
            if let Some(updated) = lock(&self.updated).take() {
                if let Ok(line) = serde_json::to_vec(&updated) {
                    let _ = out.send(line);
                }
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
            let after = match &shape["after"] {
                Value::Null => None,
                after => Some(
                    after
                        .as_u64()
                        .ok_or_else(|| malformed("after is not an unsigned sequence".into()))?,
                ),
            };
            let asked = Control {
                id: text("id")?,
                session_id: text("session_id")?,
                run_id: text("run_id")?,
                after,
            };
            let answer = if control == "replay" {
                self.replay(&asked)
            } else {
                Control {
                    id: asked.id.clone(),
                    session_id: String::new(),
                    run_id: String::new(),
                    after: None,
                }
                .answer(
                    "replay.error",
                    serde_json::json!({
                        "code": "unsupported_control",
                        "message": format!("this endpoint serves no {control:?} control"),
                    }),
                )
            };
            return Ok(Some(
                serde_json::to_vec(&answer).map_err(std::io::Error::from)?,
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
        let admits = *kind == EnvelopeType::SESSION_OPEN_REQUEST
            || *kind == EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST
            || *kind == EnvelopeType::SESSION_COMPACT_REQUEST;
        if admits && lock(&self.stopping).mode.is_some() {
            return Err(wire(
                "shutting_down",
                "the endpoint is shutting down and admits no new work",
            ));
        }
        if *kind == EnvelopeType::ENDPOINT_SHUTDOWN_REQUEST {
            return self.shutdown_request(envelope);
        }
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
        if *kind == EnvelopeType::SESSION_COMPACT_REQUEST {
            return self.compact(envelope);
        }
        if *kind == EnvelopeType::MODELS_REQUEST {
            return self.models(envelope);
        }
        if *kind == EnvelopeType::ACTION_TOOLS_LIST_REQUEST {
            return self.tools(envelope);
        }
        if *kind == EnvelopeType::SESSION_LIST_REQUEST {
            return self.session_list(envelope);
        }
        if *kind == EnvelopeType::RUN_CANCEL_REQUEST {
            return self.cancel(envelope);
        }
        if *kind == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST
            || *kind == EnvelopeType::USER_INPUT_RESOLVE_REQUEST
            || *kind == EnvelopeType::ACTION_CALL_RESOLVE_REQUEST
        {
            return self.resolve(envelope);
        }
        if *kind == EnvelopeType::SESSION_SETTINGS_UPDATE_REQUEST {
            return self.update_settings(envelope);
        }
        if *kind == EnvelopeType::SESSION_MODEL_SWITCH_REQUEST {
            return self.switch_model(envelope);
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

    fn replay(&self, asked: &Control) -> Value {
        if asked.session_id.is_empty() {
            return asked.answer(
                "replay.error",
                serde_json::json!({"code": "invalid_request", "message": "a replay must name its session"}),
            );
        }
        let after = asked.after.unwrap_or(0);
        let run = (!asked.run_id.is_empty()).then(|| asked.run_id.as_str().into());
        let subscribed = self
            .hub
            .session(&asked.session_id.as_str().into())
            .and_then(|entry| {
                block_on(self.hub.subscribe_after(entry.id(), run, after)).map(|sub| (entry, sub))
            });
        let (entry, subscription) = match subscribed {
            Ok(subscribed) => subscribed,
            Err(HubError::Adapter(oap_adapter::Error::ReplayGap(gap))) => {
                return asked.answer(
                    "replay.gap",
                    serde_json::json!({
                        "requested_after": gap.requested_after,
                        "oldest_available": gap.oldest_available,
                        "latest_available": gap.latest_available,
                        "message": "the requested replay cursor is no longer retained; ask again from oldest_available - 1",
                    }),
                )
            }
            Err(err) => {
                use oap_adapter::Error;
                let code = match &err {
                    HubError::UnknownSession(_) | HubError::Adapter(Error::UnknownSession(_)) => {
                        "unknown_session"
                    }
                    HubError::SessionClosed(_) | HubError::Adapter(Error::SessionClosed) => {
                        "session_closed"
                    }
                    HubError::NoRunToResume => "no_run_to_resume",
                    HubError::Adapter(Error::RunNotFound) => "run_not_found",
                    HubError::Adapter(Error::ReplayCursorFuture) => "replay_cursor_future",
                    _ => "internal",
                };
                return asked.answer(
                    "replay.error",
                    serde_json::json!({"code": code, "message": err.to_string()}),
                );
            }
        };
        let run = match subscription.run_id().as_str() {
            "" => oap_protocol::RunId::from(asked.run_id.as_str()),
            run => run.into(),
        };
        let accepted = Control {
            id: asked.id.clone(),
            session_id: entry.id().as_str().to_owned(),
            run_id: run.as_str().to_owned(),
            after: Some(after),
        }
        .answer("replay.accepted", serde_json::json!({}));
        *lock(&self.started) = Some((subscription, run));
        accepted
    }

    pub fn signal(&self, count: usize) -> Option<oap_protocol::EndpointShutdownResponse> {
        use oap_protocol::ShutdownMode;
        if count > 2 {
            self.abandoned.store(true, Ordering::SeqCst);
            return None;
        }
        let mut stopping = lock(&self.stopping);
        if self.signals_seen.fetch_max(count, Ordering::SeqCst) >= count {
            return None;
        }
        let wanted = if count >= 2 || stopping.mode == Some(ShutdownMode::FORCE) {
            ShutdownMode::FORCE
        } else {
            ShutdownMode::GRACEFUL
        };
        let request = crate::stdio::Request {
            request: serde_json::value::to_raw_value(&serde_json::json!({ "mode": wanted })).ok(),
            ..crate::stdio::Request::default()
        };
        let (answer, finished) = crate::stdio::shutdown(&self.hub, &request, &mut stopping);
        if finished {
            self.finished.store(true, Ordering::SeqCst);
        }
        let answer = serde_json::from_value(answer.ok()?).ok()?;
        (stopping.mode == Some(wanted)).then_some(answer)
    }

    fn shutdown_request(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        crate::asked_shutdown(envelope.payload.as_deref()).map_err(|_| {
            wire(
                "invalid_payload",
                "endpoint.shutdown.request carries only mode, graceful or force",
            )
        })?;
        let request = crate::stdio::Request {
            request: envelope.payload.clone(),
            ..crate::stdio::Request::default()
        };
        let (answer, finished) =
            crate::stdio::shutdown(&self.hub, &request, &mut lock(&self.stopping));
        let answer = answer?;
        if finished {
            self.finished.store(true, Ordering::SeqCst);
        }
        self.reply(
            EnvelopeType::ENDPOINT_SHUTDOWN_RESPONSE,
            envelope,
            &answer,
            envelope.capability_revision.as_deref().unwrap_or_default(),
        )
    }

    fn compact(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let request = envelope
            .decode_payload::<oap_protocol::SessionCompactRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        let subscription = self.hub.subscribe(entry.id()).map_err(hub_refusal)?;
        let admission = block_on(entry.compact(oap_adapter::CompactRequest {
            request,
            envelope_id: envelope.id.clone(),
        }))
        .map_err(hub_refusal)?;
        let mut answer = self.session_reply(
            EnvelopeType::SESSION_COMPACT_RESPONSE,
            envelope,
            &admission,
            &admission.session_id,
        )?;
        answer.run_id = admission.run_id.clone();
        *lock(&self.started) = Some((subscription, admission.run_id.unwrap_or_default()));
        Ok(answer)
    }

    fn models(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let request = envelope
            .decode_payload::<oap_protocol::ModelsRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        let catalog = block_on(entry.models(request)).map_err(hub_refusal)?;
        let mut answer = self.reply(
            EnvelopeType::MODELS_RESPONSE,
            envelope,
            &catalog.models,
            &catalog.revision,
        )?;
        answer.session_id = Some(entry.id().clone());
        Ok(answer)
    }

    fn tools(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let request = envelope
            .decode_payload::<oap_protocol::ToolsListRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        let catalog = block_on(entry.tools(request)).map_err(hub_refusal)?;
        let mut answer = self.reply(
            EnvelopeType::ACTION_TOOLS_LIST_RESPONSE,
            envelope,
            &catalog.tools,
            &catalog.revision,
        )?;
        answer.session_id = Some(entry.id().clone());
        Ok(answer)
    }

    fn session_list(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let request = envelope
            .decode_payload::<oap_protocol::SessionListRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        if self.hub.bindings().is_none() {
            return Err(detailed(
                "unsupported_feature",
                HubError::NoSessionHistory,
                serde_json::json!({
                    "feature": oap_protocol::FEATURE_SESSION_LIST,
                    "reason": oap_adapter::CONTROL_UNADVERTISED,
                }),
            ));
        }
        let descriptor = self.probe()?;
        let page = self
            .hub
            .session_history(&request.cursor, request.limit)
            .map_err(|err| match err {
                HubError::Binding(oap_binding::Error::InvalidCursor) => {
                    wire("invalid_cursor", oap_binding::Error::InvalidCursor)
                }
                HubError::Binding(oap_binding::Error::InvalidLimit) => {
                    wire("invalid_request", oap_binding::Error::InvalidLimit)
                }
                _ => wire("history_failed", "the session history could not be read"),
            })?;
        self.reply(
            EnvelopeType::SESSION_LIST_RESPONSE,
            envelope,
            &page,
            &descriptor.capability_revision,
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

    fn cancel(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let request = envelope
            .decode_payload::<oap_protocol::RunCancelRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        if request.session_id != *entry.id() {
            return Err(wire(
                "scope_mismatch",
                format!(
                    "payload session_id {:?} does not match the addressed session {:?}",
                    request.session_id.as_str(),
                    entry.id().as_str()
                ),
            ));
        }
        self.gate.hold(&request.run_id);
        let ack = match block_on(entry.cancel(request.run_id.clone())) {
            Ok(ack) => ack,
            Err(err) => {
                self.gate.lift(&request.run_id);
                return Err(hub_refusal(err));
            }
        };
        *lock(&self.acknowledged) = Some(request.run_id.clone());
        let mut answer = self.session_reply(
            EnvelopeType::RUN_CANCEL_RESPONSE,
            envelope,
            &ack,
            entry.id(),
        )?;
        answer.run_id = Some(request.run_id);
        Ok(answer)
    }

    fn resolve(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let invalid = |err: oap_protocol::Error| wire("invalid_payload", err);
        let kind = &envelope.envelope_type;
        let mut resolution = oap_adapter::InteractionResolution::default();
        let (answering, interaction, session) =
            if *kind == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
                let asked = envelope
                    .decode_payload::<oap_protocol::PermissionResolveRequest>()
                    .map_err(invalid)?;
                resolution.run_id = asked.run_id.clone();
                resolution.responded_by = asked.responded_by.clone();
                let named = (
                    EnvelopeType::ACTION_PERMISSION_RESOLVE_RESPONSE,
                    asked.interaction_id.clone(),
                    asked.session_id.clone(),
                );
                resolution.permission = Some(asked);
                named
            } else if *kind == EnvelopeType::USER_INPUT_RESOLVE_REQUEST {
                let asked = envelope
                    .decode_payload::<oap_protocol::UserInputResolveRequest>()
                    .map_err(invalid)?;
                resolution.run_id = asked.run_id.clone();
                resolution.responded_by = asked.responded_by.clone();
                let named = (
                    EnvelopeType::USER_INPUT_RESOLVE_RESPONSE,
                    asked.interaction_id.clone(),
                    asked.session_id.clone(),
                );
                resolution.input = Some(asked);
                named
            } else {
                let asked = envelope
                    .decode_payload::<oap_protocol::ActionCallResolveRequest>()
                    .map_err(invalid)?;
                let run = asked.run_id.clone();
                let result = block_on(entry.resolve_call(oap_adapter::CallResolution {
                    request_id: envelope.id.clone(),
                    request: asked,
                }))
                .map_err(hub_refusal)?;
                let mut answer = self.session_reply(
                    EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE,
                    envelope,
                    &result,
                    entry.id(),
                )?;
                answer.run_id = Some(run);
                return Ok(answer);
            };
        let run = resolution.run_id.clone();
        block_on(entry.resolve(resolution)).map_err(hub_refusal)?;
        let body = oap_protocol::UserInputResolveResponse {
            interaction_id: interaction,
            session_id: session,
            run_id: run.clone(),
            accepted: true,
        };
        let mut answer = self.session_reply(answering, envelope, &body, entry.id())?;
        answer.run_id = Some(run);
        Ok(answer)
    }

    fn switch_model(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let request = envelope
            .decode_payload::<oap_protocol::SessionModelSwitchRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        let (response, state) = block_on(entry.switch_model(request)).map_err(hub_refusal)?;
        let answer = self.session_reply(
            EnvelopeType::SESSION_MODEL_SWITCH_RESPONSE,
            envelope,
            &response,
            entry.id(),
        )?;
        if response.previous_model_id != response.model_id {
            self.publish_state(envelope, entry.id(), &state)?;
        }
        Ok(answer)
    }

    fn publish_state(
        &self,
        envelope: &Envelope,
        session: &oap_protocol::SessionId,
        state: &oap_protocol::SessionState,
    ) -> Result<(), WireError> {
        let mut updated = Envelope::new(
            EnvelopeType::SESSION_STATE_UPDATED,
            self.next_id("event"),
            state,
        )
        .map_err(|err| wire("internal", err))?;
        updated.session_id = Some(session.clone());
        updated.capability_revision = envelope.capability_revision.clone();
        let mut sequences = lock(&self.state_sequences);
        let sequence = sequences.entry(session.clone()).or_default();
        *sequence += 1;
        updated.sequence = Some(*sequence);
        *lock(&self.updated) = Some(updated);
        Ok(())
    }

    fn update_settings(&self, envelope: &Envelope) -> Result<Envelope, WireError> {
        let entry = self.session(envelope)?;
        let request = envelope
            .decode_payload::<oap_protocol::SessionSettingsUpdateRequest>()
            .map_err(|err| wire("invalid_payload", err))?;
        let (response, state) = block_on(entry.update_settings(request)).map_err(hub_refusal)?;
        let answer = self.session_reply(
            EnvelopeType::SESSION_SETTINGS_UPDATE_RESPONSE,
            envelope,
            &response,
            entry.id(),
        )?;
        self.publish_state(envelope, entry.id(), &state)?;
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

struct Control {
    id: String,
    session_id: String,
    run_id: String,
    after: Option<u64>,
}

impl Control {
    fn answer(&self, control: &str, rest: Value) -> Value {
        let mut answer = serde_json::json!({ "control": control });
        for (key, value) in [
            ("id", &self.id),
            ("session_id", &self.session_id),
            ("run_id", &self.run_id),
        ] {
            if !value.is_empty() {
                answer[key] = value.as_str().into();
            }
        }
        if let (Some(after), "replay.accepted") = (self.after, control) {
            answer["after"] = after.into();
        }
        if let Value::Object(rest) = rest {
            for (key, value) in rest {
                let empty = value.as_u64() == Some(0) || value.as_str() == Some("");
                if !empty {
                    answer[key.as_str()] = value;
                }
            }
        }
        answer
    }
}

fn read_line(input: &mut impl BufRead, limit: usize) -> Result<Option<Vec<u8>>, EndpointError> {
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
        if line.len() > limit {
            return Err(EndpointError::FrameTooLarge(line.len()));
        }
        if ended {
            return Ok(Some(line));
        }
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
    fn start(
        &self,
        out: Out,
        gate: Arc<Gate>,
        mut subscription: Subscription,
        run: oap_protocol::RunId,
    ) {
        lock(&self.cancellers).push(subscription.canceller());
        *lock(&self.live.0) += 1;
        let live = self.live.clone();
        std::thread::spawn(move || {
            pump(&out, &gate, &mut subscription, &run);
            drop(subscription);
            *lock(&live.0) -= 1;
            live.1.notify_all();
        });
    }

    fn drained_within(&self, window: Duration, abandoned: &std::sync::atomic::AtomicBool) -> bool {
        let deadline = Instant::now() + window;
        let mut live = lock(&self.live.0);
        while *live > 0 {
            let left = deadline.saturating_duration_since(Instant::now());
            if left.is_zero() || abandoned.load(Ordering::SeqCst) {
                return false;
            }
            live = self
                .live
                .1
                .wait_timeout(live, left.min(crate::stdio::DRAIN_POLL))
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

    fn settle(
        &self,
        failed: bool,
        window: Duration,
        abandoned: &std::sync::atomic::AtomicBool,
    ) -> bool {
        if failed {
            self.cancel();
        }
        if self.drained_within(window, abandoned) {
            return false;
        }
        self.cancel();
        !self.drained_within(window, abandoned)
    }
}

#[derive(Default)]
struct Gate {
    held: Mutex<std::collections::HashSet<oap_protocol::RunId>>,
    lifted: Condvar,
    waiting: Mutex<Vec<std::task::Waker>>,
}

impl Gate {
    fn hold(&self, run: &oap_protocol::RunId) {
        lock(&self.held).insert(run.clone());
    }

    fn lift(&self, run: &oap_protocol::RunId) {
        lock(&self.held).remove(run);
        self.woken();
    }

    fn lift_all(&self) {
        lock(&self.held).clear();
        self.woken();
    }

    fn woken(&self) {
        self.lifted.notify_all();
        for waker in std::mem::take(&mut *lock(&self.waiting)) {
            waker.wake();
        }
    }

    fn holds(&self, run: Option<&oap_protocol::RunId>) -> bool {
        run.is_some_and(|run| lock(&self.held).contains(run))
    }

    fn wake_on_lift(&self, waker: &std::task::Waker) {
        lock(&self.waiting).push(waker.clone());
    }

    fn wait_lifted(&self, run: Option<&oap_protocol::RunId>) {
        let Some(run) = run else {
            return;
        };
        let mut held = lock(&self.held);
        while held.contains(run) {
            held = self
                .lifted
                .wait(held)
                .unwrap_or_else(std::sync::PoisonError::into_inner);
        }
    }
}

fn pump(out: &Out, gate: &Gate, subscription: &mut Subscription, run: &oap_protocol::RunId) {
    let mut delivered = 0;
    let mut withheld: std::collections::VecDeque<Envelope> = std::collections::VecDeque::new();
    let mut deliver = |withheld: &mut std::collections::VecDeque<Envelope>| {
        while let Some(envelope) = withheld.pop_front() {
            let sequence = envelope.sequence;
            let sent = serde_json::to_vec(&envelope)
                .map_err(|err| EndpointError::from(std::io::Error::from(err)))
                .and_then(|line| out.send(line));
            match sent {
                Ok(()) => delivered = sequence.unwrap_or(delivered),
                Err(EndpointError::FrameTooLarge(_)) => return Err(("frame_limit", delivered)),
                Err(_) => return Err(("stream_failed", delivered)),
            }
        }
        Ok(delivered)
    };
    let lost = loop {
        let next = block_on(std::future::poll_fn(|context| {
            let held_run = withheld
                .front()
                .and_then(|envelope| envelope.run_id.clone());
            if !withheld.is_empty() && !gate.holds(held_run.as_ref()) {
                return std::task::Poll::Ready(None);
            }
            match futures_core::Stream::poll_next(std::pin::Pin::new(&mut *subscription), context) {
                std::task::Poll::Ready(next) => std::task::Poll::Ready(Some(next)),
                std::task::Poll::Pending => {
                    if !withheld.is_empty() {
                        gate.wake_on_lift(context.waker());
                        if !gate.holds(held_run.as_ref()) {
                            return std::task::Poll::Ready(None);
                        }
                    }
                    std::task::Poll::Pending
                }
            }
        }));
        let ended = match next {
            None => None,
            Some(Some(Ok(envelope))) => {
                if !run.as_str().is_empty() && envelope.run_id.as_ref() != Some(run) {
                    continue;
                }
                let holding = !withheld.is_empty() || gate.holds(envelope.run_id.as_ref());
                withheld.push_back(envelope);
                if holding {
                    continue;
                }
                None
            }
            Some(None) => Some(None),
            Some(Some(Err(HubError::Overflow { .. }))) => Some(Some("overflow")),
            Some(Some(Err(_))) => Some(Some("stream_failed")),
        };
        if ended.is_some() {
            gate.wait_lifted(
                withheld
                    .front()
                    .and_then(|envelope| envelope.run_id.as_ref()),
            );
        }
        if let Err(lost) = deliver(&mut withheld) {
            break lost;
        }
        match ended {
            None => continue,
            Some(None) => return,
            Some(Some(code)) => break (code, deliver(&mut withheld).unwrap_or(0)),
        }
    };
    let (code, after) = lost;
    let mut frame = serde_json::json!({
        "control": "stream.lost",
        "after": after,
        "code": code,
        "message": "this run's events stopped reaching the host; replay from after to continue",
    });
    if !run.as_str().is_empty() {
        frame["run_id"] = run.as_str().into();
    }
    if let Ok(line) = serde_json::to_vec(&frame) {
        let _ = out.send(line);
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn abandoning_serving_ends_a_settlement_a_stuck_pump_would_hold_for_both_windows() {
        let pumps = Pumps::default();
        *lock(&pumps.live.0) = 1;
        let abandoned = Arc::new(std::sync::atomic::AtomicBool::new(false));
        let raised = abandoned.clone();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(100));
            raised.store(true, Ordering::SeqCst);
        });
        let started = Instant::now();
        assert!(pumps.settle(false, Duration::from_secs(30), &abandoned));
        assert!(started.elapsed() < Duration::from_secs(5));
    }
}
