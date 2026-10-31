use std::fmt;
use std::io::{BufRead, Write};
use std::sync::Arc;

use serde::de::{self, MapAccess, Visitor};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use serde_json::Value;

use crate::live::block_on;
use crate::{Hub, HubError, HubSession, Subscription};

pub const DEFAULT_FRAME_LIMIT: usize = (16 << 20) + (2 << 20);

pub const MIN_FRAME_LIMIT: usize = 256;

const MESSAGE_LIMIT: usize = 300;

const PARAMS: [&str; 8] = [
    "adapter",
    "session_id",
    "run_id",
    "after",
    "request",
    "cursor",
    "limit",
    "allow_degraded_features",
];

#[derive(Clone, Debug)]
pub struct StdioOptions {
    pub frame_limit: usize,
    pub max_subscriptions: usize,
}

pub const DEFAULT_MAX_SUBSCRIPTIONS: usize = 64;

impl Default for StdioOptions {
    fn default() -> Self {
        Self {
            frame_limit: DEFAULT_FRAME_LIMIT,
            max_subscriptions: DEFAULT_MAX_SUBSCRIPTIONS,
        }
    }
}

#[derive(Debug, thiserror::Error)]
pub enum StdioError {
    #[error(
        "servestdio: frame limit {0} is below the 256-byte minimum a correlated refusal needs"
    )]
    FrameLimit(usize),
    #[error("line {line} is not a valid request: {detail}")]
    MalformedLine { line: usize, detail: String },
    #[error("servestdio: encoded line exceeds the frame limit")]
    LineTooLarge,
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

#[derive(Debug, Default)]
struct Request {
    id: i64,
    op: String,
    session_id: String,
    request: Option<Box<RawValue>>,
    run_id: String,
    adapter: String,
    cursor: String,
    limit: Option<i64>,
    allow_degraded_features: Vec<String>,
    after: Option<Box<RawValue>>,
    present: Vec<String>,
}

#[derive(Serialize)]
struct WireError {
    code: &'static str,
    message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    details: Option<Value>,
}

#[derive(Serialize)]
struct Response<'a> {
    id: i64,
    ok: bool,
    result: &'a RawValue,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<WireError>,
}

pub const WRITE_QUEUE: usize = 256;

pub const SHUTDOWN_WINDOW: std::time::Duration = std::time::Duration::from_secs(5);

type Lines = std::sync::mpsc::SyncSender<Vec<u8>>;

pub fn serve(
    hub: &Hub,
    input: impl BufRead,
    mut output: impl Write + Send + 'static,
    options: &StdioOptions,
) -> Result<(), StdioError> {
    if options.frame_limit < MIN_FRAME_LIMIT {
        return Err(StdioError::FrameLimit(options.frame_limit));
    }
    let (writer, queue) = std::sync::mpsc::sync_channel::<Vec<u8>>(WRITE_QUEUE);
    let (drained, written) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        for line in queue {
            let wrote = output
                .write_all(&line)
                .and_then(|()| output.write_all(b"\n"))
                .and_then(|()| output.flush());
            if wrote.is_err() {
                break;
            }
        }
        let _ = drained.send(());
    });
    let mut pumps = Vec::new();
    let served = serve_lines(hub, input, &writer, options, |pump| {
        pumps.push((pump.subscription.canceller(), pump.attached()));
        let writer = writer.clone();
        let limit = options.frame_limit;
        std::thread::spawn(move || pump.run(&writer, limit));
    });
    let deadline = std::time::Instant::now() + SHUTDOWN_WINDOW;
    for (canceller, _) in &pumps {
        canceller.cancel();
    }
    while pumps
        .iter()
        .any(|(_, attached)| attached.load(std::sync::atomic::Ordering::SeqCst) > 0)
        && std::time::Instant::now() < deadline
    {
        std::thread::sleep(std::time::Duration::from_millis(5));
    }
    drop(writer);
    let _ = written.recv_timeout(deadline.saturating_duration_since(std::time::Instant::now()));
    served
}

fn emit(writer: &Lines, line: &[u8]) -> std::io::Result<()> {
    writer
        .send(line.to_vec())
        .map_err(|_| std::io::Error::new(std::io::ErrorKind::BrokenPipe, "the output closed"))
}

fn serve_lines(
    hub: &Hub,
    mut input: impl BufRead,
    writer: &Lines,
    options: &StdioOptions,
    mut start: impl FnMut(Pump),
) -> Result<(), StdioError> {
    let mut number = 0;
    let mut ids = 0;
    let attached = Arc::new(std::sync::atomic::AtomicUsize::new(0));
    let cap = match options.max_subscriptions {
        0 => DEFAULT_MAX_SUBSCRIPTIONS,
        cap => cap,
    };
    loop {
        number += 1;
        let malformed = |detail: String| StdioError::MalformedLine {
            line: number,
            detail,
        };
        let Some(frame) = read_frame(&mut input, options.frame_limit).map_err(|err| match err {
            FrameError::Defect(detail) => malformed(detail),
            FrameError::Io(err) => StdioError::Io(err),
        })?
        else {
            return Ok(());
        };
        let request = decode_request(&frame).map_err(malformed)?;
        let (answer, pump) = if request.op == "open" {
            match open(
                hub,
                &request,
                &mut ids,
                || Attached::claim(&attached, cap),
                options.frame_limit,
            ) {
                Ok((answer, pump)) => (Ok(answer), pump),
                Err(error) => (Err(error), None),
            }
        } else if request.op == "events" {
            match events(hub, &request, Attached::claim(&attached, cap)) {
                Ok(pump) => (Ok(Value::Null), Some(pump)),
                Err(Subscribing::Refused(error)) => (Err(error), None),
                Err(Subscribing::Gap(gap)) => {
                    if let Some(line) = fitted(request.id, Ok(Value::Null), options.frame_limit)? {
                        emit(writer, &line)?;
                    }
                    let full = GapLine {
                        event: "oap-replay-gap",
                        id: request.id,
                        session_id: &request.session_id,
                        requested_after: gap.requested_after,
                        oldest_available: gap.oldest_available,
                        latest_available: gap.latest_available,
                        message: "requested replay cursor is no longer retained; resume with a cursor at or after oldest_available - 1",
                    };
                    let minimal = GapLine {
                        session_id: "",
                        message: "",
                        ..full
                    };
                    let fitting = [&full, &minimal].into_iter().find_map(|gap| {
                        serde_json::to_vec(gap)
                            .ok()
                            .filter(|line| line.len() <= options.frame_limit)
                    });
                    if let Some(line) = fitting {
                        emit(writer, &line)?;
                    }
                    continue;
                }
            }
        } else {
            (dispatch(hub, &request, &mut ids, options.frame_limit), None)
        };
        if let Some(line) = fitted(request.id, answer, options.frame_limit)? {
            emit(writer, &line)?;
        }
        if let Some(pump) = pump {
            start(pump);
        }
    }
}

enum Subscribing {
    Refused(WireError),
    Gap(oap_adapter::ReplayGap),
}

impl From<WireError> for Subscribing {
    fn from(error: WireError) -> Self {
        Subscribing::Refused(error)
    }
}

struct Attached(Arc<std::sync::atomic::AtomicUsize>);

impl Attached {
    fn claim(count: &Arc<std::sync::atomic::AtomicUsize>, cap: usize) -> Result<Self, WireError> {
        use std::sync::atomic::Ordering;
        let mut held = count.load(Ordering::SeqCst);
        while held < cap {
            match count.compare_exchange(held, held + 1, Ordering::SeqCst, Ordering::SeqCst) {
                Ok(_) => return Ok(Attached(count.clone())),
                Err(now) => held = now,
            }
        }
        Err(wire(
            "busy",
            format!("the frontend is already serving {cap} subscriptions; a subscription ends at its run's terminal, at an overflow or stream failure, or when its session closes \u{2014} send this request again once one has"),
        ))
    }
}

impl Drop for Attached {
    fn drop(&mut self) {
        self.0.fetch_sub(1, std::sync::atomic::Ordering::SeqCst);
    }
}

struct Pump {
    _attached: Attached,
    id: i64,
    entry: Arc<HubSession>,
    subscription: Subscription,
    resume_from: u64,
}

fn detailed(code: &'static str, message: impl fmt::Display, details: Value) -> WireError {
    WireError {
        code,
        message: trim(&message.to_string()),
        details: Some(details),
    }
}

fn refusal(err: &HubError) -> Option<WireError> {
    use oap_adapter::Error;
    match err {
        HubError::StaleRevision { expected, current } => Some(detailed(
            "stale_capabilities",
            err,
            serde_json::json!({ "expected_revision": expected, "current_revision": current }),
        )),
        HubError::Adapter(Error::UnsupportedControl(control)) => {
            let mut details =
                serde_json::json!({ "feature": control.feature, "reason": control.reason });
            for (key, value) in [
                ("tool", &control.tool),
                ("field", &control.field),
                ("source", &control.source),
            ] {
                if !value.is_empty() {
                    details[key] = Value::String(value.clone());
                }
            }
            Some(detailed("unsupported_feature", err, details))
        }
        HubError::Adapter(Error::DegradedControl(feature)) => Some(detailed(
            "capability_degraded",
            err,
            serde_json::json!({ "feature": feature }),
        )),
        HubError::Adapter(Error::InvalidSteerTarget { reason, .. }) => Some(detailed(
            "invalid_steer_target",
            err,
            serde_json::json!({ "reason": reason }),
        )),
        HubError::Adapter(Error::ModelNotFound(model)) => Some(detailed(
            "model_not_found",
            err,
            serde_json::json!({ "model_id": model }),
        )),
        _ => None,
    }
}

fn open(
    hub: &Hub,
    request: &Request,
    ids: &mut u64,
    claim: impl FnOnce() -> Result<Attached, WireError>,
    limit: usize,
) -> Result<(Value, Option<Pump>), WireError> {
    request.only(&["adapter", "request"])?;
    if request.adapter.is_empty() {
        return Err(wire("invalid_request", "adapter is required"));
    }
    let envelope = gate_request(
        request.request.as_deref(),
        &[oap_protocol::EnvelopeType::SESSION_OPEN_REQUEST],
    )?;
    let asked: oap_protocol::SessionOpenRequest = payload(&envelope)?;
    let attached = asked.subscribe.then(claim).transpose()?;
    if hub.registry().lookup(&request.adapter).is_none() {
        return Err(wire(
            "unknown_adapter",
            format!("no adapter \"{}\"", trim(&request.adapter)),
        ));
    }
    let cited = envelope.capability_revision.clone().unwrap_or_default();
    let gated = block_on(async {
        let attaching = crate::attachment_gate(hub, &request.adapter, &cited, &asked).await?;
        let electing = crate::election_gate(hub, &request.adapter, &cited, &asked).await?;
        Ok::<String, HubError>(if electing.is_empty() {
            attaching
        } else {
            electing
        })
    })
    .map_err(|err| refusal(&err).unwrap_or_else(|| wire("probe_failed", err)))?;
    let tool_sources =
        crate::resolve_attachments(hub.registry(), &asked.tool_sources).map_err(|refused| {
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
        let value: Value = serde_json::from_str(raw.get()).map_err(|err| {
            wire(
                "invalid_payload",
                format!("metadata {:?}: {err}", trim(key)),
            )
        })?;
        metadata.insert(key.clone(), value);
    }
    let opening = oap_adapter::OpenRequest {
        session_id: asked.session_id.clone().unwrap_or_default(),
        participant: oap_protocol::Participant {
            id: crate::DEFAULT_PARTICIPANT.into(),
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
    let opened = block_on(hub.open(&request.adapter, opening)).map_err(open_refusal)?;
    let named = asked
        .session_id
        .as_ref()
        .is_some_and(|id| !id.as_str().is_empty());
    let pump = match attached {
        Some(attached) => match hub.subscribe(opened.session.id()) {
            Ok(subscription) => Some(Pump {
                id: request.id,
                entry: opened.session.clone(),
                subscription,
                resume_from: 0,
                _attached: attached,
            }),
            Err(err) => {
                let _ = block_on(opened.session.rollback());
                return Err(open_refusal(err));
            }
        },
        None => None,
    };
    let mut admitted_run = None;
    let state = match &asked.message {
        Some(message) => {
            let submitted = block_on(opened.session.submit(oap_adapter::SubmitRequest {
                request: message.submit(opened.session.id().clone()),
                envelope_id: envelope.id.clone(),
            }));
            match submitted {
                Ok(admission) => {
                    admitted_run = admission
                        .run_id
                        .clone()
                        .filter(|_| admission.admission != oap_protocol::Admission::STEERED);
                    crate::compound::with_admitted_run(
                        opened.state.clone(),
                        &admission,
                        &envelope.id,
                    )
                }
                Err(err) => {
                    drop(pump);
                    let _ = block_on(opened.session.rollback());
                    return Err(open_refusal(err));
                }
            }
        }
        None => opened.state.clone(),
    };
    let mut response = oap_protocol::Envelope::new(
        oap_protocol::EnvelopeType::SESSION_OPEN_RESPONSE,
        next_id(ids, "response"),
        &state,
    )
    .map_err(|err| wire("internal", err))?;
    response.in_reply_to = Some(envelope.id.clone());
    response.session_id = Some(opened.state.session_id.clone());
    response.capability_revision = if gated.is_empty() {
        envelope.capability_revision.clone()
    } else {
        Some(gated)
    };
    let answer = serde_json::to_value(&response).map_err(|err| wire("internal", err))?;
    let fits = response_line(request.id, Ok(answer.clone())).is_ok_and(|line| line.len() <= limit);
    if !fits {
        let outcome = if named && asked.message.is_none() {
            "the session is open under the session_id the request supplied"
        } else {
            drop(pump);
            if let Some(run) = &admitted_run {
                let _ = rollback_run(hub, &opened.session, run);
            }
            match block_on(opened.session.rollback()) {
                Ok(()) | Err(HubError::Adapter(oap_adapter::Error::SessionClosed)) => {
                    "the session was rolled back"
                }
                Err(_) => "rolling the session back failed and it may still be live",
            }
        };
        return Err(wire(
            "response_too_large",
            format!("the open response exceeds the frame limit; {outcome}"),
        ));
    }
    Ok((answer, pump))
}

fn open_refusal(err: HubError) -> WireError {
    {
        refusal(&err).unwrap_or_else(|| {
            wire(
                match &err {
                    HubError::UnknownAdapter(_) => "unknown_adapter",
                    HubError::SessionExists(_) => "session_exists",
                    HubError::UnknownSession(_) => "unknown_session",
                    HubError::ClosedAtOpen(_)
                    | HubError::SessionClosed(_)
                    | HubError::Adapter(oap_adapter::Error::SessionClosed) => "session_closed",
                    _ => "open_failed",
                },
                err,
            )
        })
    }
}

fn events(
    hub: &Hub,
    request: &Request,
    attached: Result<Attached, WireError>,
) -> Result<Pump, Subscribing> {
    request.only(&["session_id", "run_id", "after"])?;
    let entry = session(hub, &request.session_id)?;
    if entry.is_closed() {
        return Err(wire("session_closed", "the session is closed").into());
    }
    let after = match request.after.as_deref().map(RawValue::get) {
        Some(text) if text != "null" => Some(
            text.parse::<u64>()
                .ok()
                .filter(|_| text.bytes().all(|byte| byte.is_ascii_digit()))
                .ok_or_else(|| {
                    wire(
                        "invalid_cursor",
                        format!("cursor {:?} is not an unsigned sequence", trim(text)),
                    )
                })?,
        ),
        _ if !request.run_id.is_empty() => {
            return Err(wire(
                "invalid_cursor",
                "run_id names the run a cursor belongs to; it has no meaning without after",
            )
            .into())
        }
        _ => None,
    };
    let attached = attached?;
    let subscribed = match after {
        Some(after) => {
            let run = (!request.run_id.is_empty()).then(|| request.run_id.as_str().into());
            block_on(hub.subscribe_after(entry.id(), run, after))
        }
        None => hub.subscribe(entry.id()),
    };
    let subscription = subscribed.map_err(|err| match err {
        HubError::Adapter(oap_adapter::Error::ReplayGap(gap)) => Subscribing::Gap(gap),
        err => Subscribing::Refused(wire(
            match &err {
                HubError::Adapter(oap_adapter::Error::SessionClosed)
                | HubError::SessionClosed(_) => "session_closed",
                HubError::NoRunToResume => "no_run_to_resume",
                HubError::Adapter(oap_adapter::Error::ReplayCursorFuture) => "replay_cursor_future",
                HubError::Adapter(oap_adapter::Error::RunNotFound) => "run_not_found",
                HubError::UnknownSession(_) => "unknown_session",
                _ => "internal",
            },
            err,
        )),
    })?;
    Ok(Pump {
        id: request.id,
        entry,
        subscription,
        resume_from: after.unwrap_or(0),
        _attached: attached,
    })
}

#[derive(Serialize)]
struct GapLine<'a> {
    event: &'static str,
    id: i64,
    #[serde(skip_serializing_if = "str::is_empty")]
    session_id: &'a str,
    requested_after: u64,
    oldest_available: u64,
    latest_available: u64,
    #[serde(skip_serializing_if = "str::is_empty")]
    message: &'static str,
}

#[derive(Serialize)]
struct Signal<'a> {
    event: &'static str,
    id: i64,
    #[serde(skip_serializing_if = "str::is_empty")]
    session_id: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    run_id: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    sequence: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    last_sequence: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    joined_after: Option<u64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    envelope: Option<&'a Value>,
    #[serde(skip_serializing_if = "str::is_empty")]
    message: &'static str,
}

impl Signal<'_> {
    fn bare(event: &'static str, id: i64) -> Self {
        Signal {
            event,
            id,
            session_id: "",
            run_id: "",
            sequence: None,
            last_sequence: None,
            joined_after: None,
            envelope: None,
            message: "",
        }
    }
}

impl Pump {
    fn send(&self, writer: &Lines, limit: usize, full: &Signal, minimal: &Signal) -> bool {
        for signal in [full, minimal] {
            if let Ok(line) = serde_json::to_vec(signal) {
                if line.len() <= limit {
                    return emit(writer, &line).is_ok();
                }
            }
        }
        false
    }

    fn attached(&self) -> Arc<std::sync::atomic::AtomicUsize> {
        self._attached.0.clone()
    }

    fn run(mut self, writer: &Lines, limit: usize) {
        let id = self.id;
        let session = self.entry.id().to_string();
        if let Some((run, joined)) = self.subscription.joined_at() {
            let full = Signal {
                session_id: &session,
                run_id: run.as_str(),
                joined_after: Some(joined),
                message: "the subscription begins after this sequence; resubscribe with a cursor at or before it to replay what preceded this point",
                ..Signal::bare("oap-subscribed", id)
            };
            let minimal = Signal {
                joined_after: Some(joined),
                ..Signal::bare("oap-subscribed", id)
            };
            if !self.send(writer, limit, &full, &minimal) {
                return;
            }
        }
        let mut delivered_run = oap_protocol::RunId::default();
        let mut delivered = self.resume_from;
        loop {
            match block_on(futures_next(&mut self.subscription)) {
                Some(Ok(envelope)) => {
                    let sequence = envelope.sequence;
                    let run = envelope.run_id.clone().unwrap_or_default();
                    let value = serde_json::to_value(&envelope).unwrap_or(Value::Null);
                    let line = Signal {
                        session_id: &session,
                        sequence,
                        envelope: Some(&value),
                        ..Signal::bare("envelope", id)
                    };
                    let fits = serde_json::to_vec(&line)
                        .ok()
                        .filter(|line| line.len() <= limit);
                    let Some(line) = fits else {
                        let at = sequence.unwrap_or(delivered);
                        let full = Signal {
                            session_id: &session,
                            run_id: run.as_str(),
                            sequence: Some(at),
                            message: "envelope exceeds the frame limit; the subscription ended \u{2014} resume with a cursor after this sequence to continue past it",
                            ..Signal::bare("oap-frame-limit", id)
                        };
                        let minimal = Signal {
                            sequence: Some(at),
                            ..Signal::bare("oap-frame-limit", id)
                        };
                        self.send(writer, limit, &full, &minimal);
                        return;
                    };
                    if emit(writer, &line).is_err() {
                        return;
                    }
                    let next = sequence.unwrap_or(0);
                    if run != delivered_run {
                        delivered_run = run;
                        delivered = next;
                    } else {
                        delivered = delivered.max(next);
                    }
                }
                Some(Err(HubError::Overflow {
                    run_id,
                    last_sequence,
                })) => {
                    let full = Signal {
                        session_id: &session,
                        run_id: run_id.as_str(),
                        last_sequence: Some(last_sequence),
                        message: "event stream consumer fell behind; resume with a cursor after this sequence",
                        ..Signal::bare("oap-overflow", id)
                    };
                    let minimal = Signal {
                        last_sequence: Some(last_sequence),
                        ..Signal::bare("oap-overflow", id)
                    };
                    self.send(writer, limit, &full, &minimal);
                    return;
                }
                Some(Err(_)) => {
                    let full = Signal {
                        session_id: &session,
                        run_id: delivered_run.as_str(),
                        sequence: Some(delivered),
                        message: "the run's event stream failed; resume with a cursor after this sequence",
                        ..Signal::bare("oap-stream-failed", id)
                    };
                    let minimal = Signal {
                        sequence: Some(delivered),
                        ..Signal::bare("oap-stream-failed", id)
                    };
                    self.send(writer, limit, &full, &minimal);
                    return;
                }
                None => {
                    if self.entry.is_closed() {
                        let full = Signal {
                            session_id: &session,
                            message: "the session is closed",
                            ..Signal::bare("oap-session-closed", id)
                        };
                        self.send(
                            writer,
                            limit,
                            &full,
                            &Signal::bare("oap-session-closed", id),
                        );
                    }
                    return;
                }
            }
        }
    }
}

fn futures_next(
    subscription: &mut Subscription,
) -> impl std::future::Future<Output = Option<Result<oap_protocol::Envelope, HubError>>> + '_ {
    std::future::poll_fn(move |context| {
        futures_core::Stream::poll_next(std::pin::Pin::new(&mut *subscription), context)
    })
}

enum FrameError {
    Defect(String),
    Io(std::io::Error),
}

fn read_frame(input: &mut impl BufRead, limit: usize) -> Result<Option<Vec<u8>>, FrameError> {
    let mut frame = Vec::new();
    loop {
        let available = input.fill_buf().map_err(FrameError::Io)?;
        if available.is_empty() {
            if frame.is_empty() {
                return Ok(None);
            }
            return Err(FrameError::Defect("unterminated final line".into()));
        }
        let (taken, ended) = match available.iter().position(|byte| *byte == b'\n') {
            Some(at) => (at + 1, true),
            None => (available.len(), false),
        };
        if frame.len() + taken > limit + 1 {
            return Err(FrameError::Defect(format!(
                "line exceeds the {limit}-byte frame limit"
            )));
        }
        frame.extend_from_slice(&available[..taken]);
        input.consume(taken);
        if !ended {
            continue;
        }
        frame.pop();
        if frame.contains(&b'\r') {
            return Err(FrameError::Defect(
                "carriage return is not valid framing".into(),
            ));
        }
        if frame.is_empty() {
            return Err(FrameError::Defect("empty line".into()));
        }
        if std::str::from_utf8(&frame).is_err() {
            return Err(FrameError::Defect("line is not UTF-8".into()));
        }
        return Ok(Some(frame));
    }
}

struct Members(Vec<(String, Box<RawValue>)>);

impl<'de> Deserialize<'de> for Members {
    fn deserialize<D: de::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct Collect;

        impl<'de> Visitor<'de> for Collect {
            type Value = Members;

            fn expecting(&self, formatter: &mut fmt::Formatter) -> fmt::Result {
                formatter.write_str("request is not a JSON object")
            }

            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Members, A::Error> {
                let mut members = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    members.push((key, map.next_value::<Box<RawValue>>()?));
                }
                Ok(Members(members))
            }
        }

        deserializer.deserialize_map(Collect)
    }
}

fn invalid(detail: impl fmt::Display) -> String {
    trim(&format!("invalid JSON request: {detail}"))
}

fn decode_request(frame: &[u8]) -> Result<Request, String> {
    let mut deserializer = serde_json::Deserializer::from_slice(frame);
    let Members(members) = Members::deserialize(&mut deserializer).map_err(invalid)?;
    deserializer
        .end()
        .map_err(|_| invalid("trailing data after the request object"))?;
    let mut request = Request::default();
    let mut id = None;
    for (key, value) in members {
        if request.present.contains(&key) {
            return Err(invalid(format!("repeated key {key:?}")));
        }
        let typed = |ok: bool| {
            if ok {
                Ok(())
            } else {
                Err(invalid(format!("field {key:?} has the wrong type")))
            }
        };
        let raw = matches!(key.as_str(), "after" | "request");
        let parsed: Value = if raw {
            Value::Null
        } else {
            serde_json::from_str(value.get()).map_err(invalid)?
        };
        match key.as_str() {
            "id" => {
                id = parsed.as_i64();
                typed(id.is_some())?;
            }
            "op" => {
                typed(parsed.is_string())?;
                request.op = parsed.as_str().unwrap_or_default().to_owned();
            }
            "adapter" | "session_id" | "run_id" | "cursor" => {
                typed(parsed.is_string() || parsed.is_null())?;
                let text = parsed.as_str().unwrap_or_default().to_owned();
                match key.as_str() {
                    "session_id" => request.session_id = text,
                    "run_id" => request.run_id = text,
                    "adapter" => request.adapter = text,
                    _ => request.cursor = text,
                }
            }
            "limit" => {
                typed(parsed.is_i64() || parsed.is_null())?;
                request.limit = parsed.as_i64();
            }
            "allow_degraded_features" => {
                typed(match &parsed {
                    Value::Array(items) => items.iter().all(Value::is_string),
                    Value::Null => true,
                    _ => false,
                })?;
                request.allow_degraded_features = parsed
                    .as_array()
                    .into_iter()
                    .flatten()
                    .filter_map(|item| item.as_str().map(str::to_owned))
                    .collect();
            }
            "request" => request.request = Some(value),
            "after" => request.after = Some(value),
            _ => {
                return Err(invalid(format!(
                    "field {key:?} must use its exact protocol spelling"
                )))
            }
        }
        request.present.push(key);
    }
    request.id = id.ok_or_else(|| invalid("id is required"))?;
    if request.op.is_empty() {
        return Err(invalid("op is required"));
    }
    Ok(request)
}

fn trim(message: &str) -> String {
    match message.char_indices().nth(MESSAGE_LIMIT) {
        Some((at, _)) => format!("{}…", &message[..at]),
        None => message.to_owned(),
    }
}

type Answer = Result<Value, WireError>;

impl Request {
    fn only(&self, allowed: &[&str]) -> Result<(), WireError> {
        let extra: Vec<&str> = PARAMS
            .into_iter()
            .filter(|param| !allowed.contains(param) && self.present.iter().any(|key| key == param))
            .collect();
        if extra.is_empty() {
            return Ok(());
        }
        Err(WireError {
            code: "invalid_request",
            message: format!(
                "op {:?} accepts no {} parameter",
                trim(&self.op),
                extra.join(", ")
            ),
            details: None,
        })
    }
}

fn next_id(ids: &mut u64, kind: &str) -> String {
    *ids += 1;
    format!("oap-{kind}-{ids}")
}

fn session(hub: &Hub, id: &str) -> Result<std::sync::Arc<crate::HubSession>, WireError> {
    hub.session(&id.into()).map_err(|err| WireError {
        code: "unknown_session",
        message: trim(&err.to_string()),
        details: None,
    })
}

fn wire(code: &'static str, message: impl fmt::Display) -> WireError {
    WireError {
        code,
        message: trim(&message.to_string()),
        details: None,
    }
}

const MAX_ENVELOPE_BYTES: usize = 16 << 20;

fn gate_request(
    raw: Option<&RawValue>,
    want: &[oap_protocol::EnvelopeType],
) -> Result<oap_protocol::Envelope, WireError> {
    let raw = raw
        .map(RawValue::get)
        .filter(|text| *text != "null")
        .ok_or_else(|| wire("invalid_request", "the op requires a \"request\" envelope"))?;
    if raw.len() > MAX_ENVELOPE_BYTES {
        return Err(wire(
            "request_too_large",
            "request envelope exceeds the daemon limit",
        ));
    }
    let value: Value = serde_json::from_str(raw).map_err(|err| wire("malformed_json", err))?;
    if !value.is_object() {
        return Err(wire(
            "malformed_json",
            "the request envelope is not an object",
        ));
    }
    let validator = crate::schema::envelope_validator().map_err(|err| wire("internal", err))?;
    validator
        .validate(&value)
        .map_err(|err| wire("schema_invalid", err))?;
    let envelope =
        oap_protocol::Envelope::parse(raw.as_bytes()).map_err(|err| wire("malformed_json", err))?;
    if !want.contains(&envelope.envelope_type) {
        let names: Vec<&str> = want.iter().map(|kind| kind.as_str()).collect();
        return Err(wire(
            "type_mismatch",
            format!(
                "op expects {}, got {}",
                names.join(" or "),
                envelope.envelope_type.as_str()
            ),
        ));
    }
    Ok(envelope)
}

fn payload<T: serde::de::DeserializeOwned>(
    envelope: &oap_protocol::Envelope,
) -> Result<T, WireError> {
    envelope
        .decode_payload()
        .map_err(|err| wire("invalid_payload", err))
}

fn reply(
    kind: oap_protocol::EnvelopeType,
    request: &oap_protocol::Envelope,
    session_id: oap_protocol::SessionId,
    run_id: oap_protocol::RunId,
    body: &impl Serialize,
    ids: &mut u64,
) -> Answer {
    let mut response = oap_protocol::Envelope::new(kind, next_id(ids, "response"), body)
        .map_err(|err| wire("internal", err))?;
    response.in_reply_to = Some(request.id.clone());
    response.session_id = Some(session_id);
    response.run_id = Some(run_id);
    response.capability_revision = request.capability_revision.clone();
    serde_json::to_value(&response).map_err(|err| wire("internal", err))
}

fn adapter_code(err: &crate::HubError) -> &'static str {
    use oap_adapter::Error;
    match err {
        crate::HubError::ScopeMismatch { .. } => "scope_mismatch",
        crate::HubError::Adapter(Error::SessionClosed) => "session_closed",
        crate::HubError::Adapter(Error::RunNotFound) => "run_not_found",
        crate::HubError::Adapter(Error::RunAlreadyTerminal { .. }) => "run_terminal",
        crate::HubError::Adapter(
            Error::InteractionNotFound
            | Error::InteractionResolved
            | Error::WrongResponder
            | Error::InvalidResolution,
        ) => "resolution_rejected",
        _ => "internal",
    }
}

fn submit_refusal(err: HubError) -> WireError {
    refusal(&err).unwrap_or_else(|| {
        wire(
            match &err {
                HubError::ScopeMismatch { .. } => "scope_mismatch",
                HubError::SessionClosed(_)
                | HubError::Adapter(oap_adapter::Error::SessionClosed) => "session_closed",
                HubError::Adapter(oap_adapter::Error::RunActive) => "run_active",
                HubError::Adapter(
                    oap_adapter::Error::InvalidSubmission | oap_adapter::Error::UnsupportedInput,
                ) => "invalid_submission",
                _ => "internal",
            },
            err,
        )
    })
}

fn settings(hub: &Hub, request: &Request, ids: &mut u64) -> Answer {
    let envelope = gate_request(
        request.request.as_deref(),
        &[oap_protocol::EnvelopeType::SESSION_SETTINGS_UPDATE_REQUEST],
    )?;
    let asked: oap_protocol::SessionSettingsUpdateRequest = payload(&envelope)?;
    let entry = session(hub, &request.session_id)?;
    let (updated, _) = block_on(entry.update_settings(asked)).map_err(submit_refusal)?;
    let mut response = oap_protocol::Envelope::new(
        oap_protocol::EnvelopeType::SESSION_SETTINGS_UPDATE_RESPONSE,
        next_id(ids, "response"),
        &updated,
    )
    .map_err(|err| wire("internal", err))?;
    response.in_reply_to = Some(envelope.id.clone());
    response.session_id = Some(updated.session_id.clone());
    response.capability_revision = envelope.capability_revision.clone();
    serde_json::to_value(&response).map_err(|err| wire("internal", err))
}

fn catalog_refusal(err: HubError, otherwise: &'static str) -> WireError {
    if let Some(refused) = refusal(&err) {
        return refused;
    }
    match err {
        HubError::Adapter(oap_adapter::Error::ToolCatalogUnavailable) => detailed(
            "unsupported_feature",
            &err,
            serde_json::json!({
                "feature": oap_protocol::FEATURE_TOOLS_LIST,
                "reason": oap_adapter::CONTROL_UNADVERTISED,
            }),
        ),
        HubError::ScopeMismatch { .. } => wire("scope_mismatch", err),
        HubError::SessionClosed(_) | HubError::Adapter(oap_adapter::Error::SessionClosed) => {
            wire("session_closed", err)
        }
        _ => wire(otherwise, err),
    }
}

fn listing(
    ids: &mut u64,
    kind: oap_protocol::EnvelopeType,
    payload: &impl Serialize,
    session_id: &oap_protocol::SessionId,
    revision: String,
) -> Answer {
    let mut response = oap_protocol::Envelope::new(kind, next_id(ids, "response"), payload)
        .map_err(|err| wire("internal", err))?;
    response.in_reply_to = Some(next_id(ids, "request").into());
    response.session_id = Some(session_id.clone());
    response.capability_revision = Some(revision);
    serde_json::to_value(&response).map_err(|err| wire("internal", err))
}

fn submit(hub: &Hub, request: &Request, ids: &mut u64, limit: usize) -> Answer {
    use oap_protocol::EnvelopeType;
    let envelope = gate_request(
        request.request.as_deref(),
        &[
            EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
            EnvelopeType::SESSION_COMPACT_REQUEST,
        ],
    )?;
    let compacting = envelope.envelope_type == EnvelopeType::SESSION_COMPACT_REQUEST;
    let (message, compaction) = if compacting {
        (
            None,
            Some(payload::<oap_protocol::SessionCompactRequest>(&envelope)?),
        )
    } else {
        (
            Some(payload::<oap_protocol::MessageSubmitRequest>(&envelope)?),
            None,
        )
    };
    let entry = session(hub, &request.session_id)?;
    let admitted = match (message, compaction) {
        (Some(message), _) => block_on(entry.submit(oap_adapter::SubmitRequest {
            request: message,
            envelope_id: envelope.id.clone(),
        }))
        .map(|admission| {
            let (session, run) = (admission.session_id.clone(), admission.run_id.clone());
            let steered = admission.admission == oap_protocol::Admission::STEERED;
            (
                EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE,
                serde_json::to_value(&admission),
                session,
                run,
                steered,
            )
        }),
        (None, compaction) => block_on(entry.compact(oap_adapter::CompactRequest {
            request: compaction.unwrap_or_default(),
            envelope_id: envelope.id.clone(),
        }))
        .map(|admission| {
            let (session, run) = (admission.session_id.clone(), admission.run_id.clone());
            let steered = admission.admission == oap_protocol::Admission::STEERED;
            (
                EnvelopeType::SESSION_COMPACT_RESPONSE,
                serde_json::to_value(&admission),
                session,
                run,
                steered,
            )
        }),
    };
    let (kind, admission, session_id, run_id, steered) = admitted.map_err(submit_refusal)?;
    let admission = admission.map_err(|err| wire("internal", err))?;
    let mut response = oap_protocol::Envelope::new(kind, next_id(ids, "response"), &admission)
        .map_err(|err| wire("internal", err))?;
    response.in_reply_to = Some(envelope.id.clone());
    response.session_id = Some(session_id);
    response.run_id = run_id.clone();
    response.capability_revision = envelope.capability_revision.clone();
    let answer = serde_json::to_value(&response).map_err(|err| wire("internal", err))?;
    if response_line(request.id, Ok(answer.clone())).is_ok_and(|line| line.len() <= limit) {
        return Ok(answer);
    }
    let outcome = match run_id {
        Some(_) if steered => "the steered run was left running".to_owned(),
        Some(run) => match rollback_run(hub, &entry, &run) {
            Ok(Some(settled)) if settled == EnvelopeType::RUN_CANCELLED => {
                "the run was cancelled".to_owned()
            }
            Ok(Some(settled)) => format!("the run settled before the rollback ({settled})"),
            Ok(None) => "the run's cancellation did not settle within the rollback window and may still be live".to_owned(),
            Err(
                HubError::Adapter(
                    oap_adapter::Error::RunAlreadyTerminal { .. }
                    | oap_adapter::Error::RunNotFound
                    | oap_adapter::Error::SessionClosed,
                )
                | HubError::SessionClosed(_),
            ) => "the run settled before the rollback".to_owned(),
            Err(_) => "rolling the run back failed and the run may still be live".to_owned(),
        },
        None => "the run settled before the rollback".to_owned(),
    };
    Err(wire(
        "response_too_large",
        format!("the submit acknowledgement exceeds the frame limit; {outcome}"),
    ))
}

fn rollback_run(
    hub: &Hub,
    entry: &HubSession,
    run: &oap_protocol::RunId,
) -> Result<Option<oap_protocol::EnvelopeType>, HubError> {
    use oap_protocol::EnvelopeType;
    let mut subscription = hub.subscribe(entry.id())?;
    let canceller = subscription.canceller();
    let (done, waiting) = std::sync::mpsc::channel::<()>();
    std::thread::spawn(move || {
        if waiting.recv_timeout(SHUTDOWN_WINDOW) == Err(std::sync::mpsc::RecvTimeoutError::Timeout)
        {
            canceller.cancel();
        }
    });
    block_on(entry.cancel(run.clone()))?;
    let settled = loop {
        match block_on(futures_next(&mut subscription)) {
            Some(Ok(envelope)) if envelope.run_id.as_ref() == Some(run) => {
                let kind = envelope.envelope_type;
                if [
                    EnvelopeType::RUN_CANCELLED,
                    EnvelopeType::RUN_COMPLETED,
                    EnvelopeType::RUN_FAILED,
                ]
                .contains(&kind)
                {
                    break Some(kind);
                }
            }
            Some(Ok(_)) => {}
            _ => break None,
        }
    };
    drop(done);
    Ok(settled)
}

fn cancel(hub: &Hub, request: &Request, ids: &mut u64) -> Answer {
    let envelope = gate_request(
        request.request.as_deref(),
        &[oap_protocol::EnvelopeType::RUN_CANCEL_REQUEST],
    )?;
    let asked: oap_protocol::RunCancelRequest = payload(&envelope)?;
    let entry = session(hub, &request.session_id)?;
    if &asked.session_id != entry.id() {
        return Err(wire(
            "scope_mismatch",
            format!(
                "payload session_id \"{}\" does not match the addressed session \"{}\"",
                asked.session_id,
                entry.id()
            ),
        ));
    }
    let ack = block_on(entry.cancel(asked.run_id)).map_err(|err| wire(adapter_code(&err), err))?;
    reply(
        oap_protocol::EnvelopeType::RUN_CANCEL_RESPONSE,
        &envelope,
        ack.session_id.clone(),
        ack.run_id.clone(),
        &ack,
        ids,
    )
}

fn resolve_call(entry: &HubSession, envelope: &oap_protocol::Envelope, ids: &mut u64) -> Answer {
    let asked: oap_protocol::ActionCallResolveRequest = payload(envelope)?;
    let run = asked.run_id.clone();
    let answer = block_on(entry.resolve_call(oap_adapter::CallResolution {
        request_id: envelope.id.clone(),
        request: asked,
    }))
    .map_err(|err| {
        refusal(&err).unwrap_or_else(|| {
            wire(
                match &err {
                    HubError::ScopeMismatch { .. } => "scope_mismatch",
                    HubError::Adapter(oap_adapter::Error::UnsupportedInput) => {
                        "unsupported_feature"
                    }
                    HubError::SessionClosed(_)
                    | HubError::Adapter(oap_adapter::Error::SessionClosed) => "session_closed",
                    _ => "internal",
                },
                err,
            )
        })
    })?;
    let mut response = oap_protocol::Envelope::new(
        oap_protocol::EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE,
        next_id(ids, "response"),
        &answer,
    )
    .map_err(|err| wire("internal", err))?;
    response.in_reply_to = Some(envelope.id.clone());
    response.session_id = Some(entry.id().clone());
    response.run_id = Some(run);
    response.capability_revision = envelope.capability_revision.clone();
    serde_json::to_value(&response).map_err(|err| wire("internal", err))
}

fn resolve(hub: &Hub, request: &Request, ids: &mut u64) -> Answer {
    use oap_protocol::EnvelopeType;
    let envelope = gate_request(
        request.request.as_deref(),
        &[
            EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST,
            EnvelopeType::USER_INPUT_RESOLVE_REQUEST,
            EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
        ],
    )?;
    let entry = session(hub, &request.session_id)?;
    let mut resolution = oap_adapter::InteractionResolution::default();
    let (kind, interaction, session_id, _) =
        if envelope.envelope_type == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
            let asked: oap_protocol::PermissionResolveRequest = payload(&envelope)?;
            let named = (
                EnvelopeType::ACTION_PERMISSION_RESOLVE_RESPONSE,
                asked.interaction_id.clone(),
                asked.session_id.clone(),
                asked.run_id.clone(),
            );
            resolution.run_id = asked.run_id.clone();
            resolution.responded_by = asked.responded_by.clone();
            resolution.permission = Some(asked);
            named
        } else if envelope.envelope_type == EnvelopeType::USER_INPUT_RESOLVE_REQUEST {
            let asked: oap_protocol::UserInputResolveRequest = payload(&envelope)?;
            let named = (
                EnvelopeType::USER_INPUT_RESOLVE_RESPONSE,
                asked.interaction_id.clone(),
                asked.session_id.clone(),
                asked.run_id.clone(),
            );
            resolution.run_id = asked.run_id.clone();
            resolution.responded_by = asked.responded_by.clone();
            resolution.input = Some(asked);
            named
        } else {
            return resolve_call(&entry, &envelope, ids);
        };
    let run = resolution.run_id.clone();
    block_on(entry.resolve(resolution)).map_err(|err| wire(adapter_code(&err), err))?;
    let body = oap_protocol::UserInputResolveResponse {
        interaction_id: interaction,
        session_id,
        run_id: run.clone(),
        accepted: true,
    };
    reply(kind, &envelope, entry.id().clone(), run, &body, ids)
}

fn rfc3339(ms: i64) -> String {
    let seconds = ms.div_euclid(1000);
    let (days, rest) = (seconds.div_euclid(86_400), seconds.rem_euclid(86_400));
    let shifted = days + 719_468;
    let era = shifted.div_euclid(146_097);
    let of_era = shifted.rem_euclid(146_097);
    let year_of_era = (of_era - of_era / 1460 + of_era / 36_524 - of_era / 146_096) / 365;
    let day_of_year = of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let month_index = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * month_index + 2) / 5 + 1;
    let month = if month_index < 10 {
        month_index + 3
    } else {
        month_index - 9
    };
    let year = year_of_era + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
        rest / 3600,
        rest / 60 % 60,
        rest % 60
    )
}

fn dispatch(hub: &Hub, request: &Request, ids: &mut u64, limit: usize) -> Answer {
    match request.op.as_str() {
        "capabilities" => {
            request.only(&["adapter"])?;
            if request.adapter.is_empty() {
                return Err(wire("invalid_request", "adapter is required"));
            }
            let correlation = next_id(ids, "request");
            let descriptor = block_on(hub.probe(&request.adapter)).map_err(|err| match err {
                HubError::UnknownAdapter(_) => wire("unknown_adapter", err),
                _ => wire("probe_failed", err),
            })?;
            let mut response = oap_protocol::Envelope::new(
                oap_protocol::EnvelopeType::CAPABILITIES_RESPONSE,
                next_id(ids, "response"),
                &descriptor.capabilities,
            )
            .map_err(|err| wire("internal", err))?;
            response.in_reply_to = Some(correlation.into());
            response.capability_revision = Some(descriptor.capability_revision);
            serde_json::to_value(&response).map_err(|err| wire("internal", err))
        }
        "models" => {
            request.only(&["session_id", "allow_degraded_features"])?;
            let entry = session(hub, &request.session_id)?;
            let catalog = block_on(entry.models(oap_protocol::ModelsRequest {
                session_id: entry.id().clone(),
                allow_degraded_features: request.allow_degraded_features.clone(),
            }))
            .map_err(|err| catalog_refusal(err, "internal"))?;
            listing(
                ids,
                oap_protocol::EnvelopeType::MODELS_RESPONSE,
                &catalog.models,
                entry.id(),
                catalog.revision,
            )
        }
        "tools" => {
            request.only(&["session_id", "allow_degraded_features"])?;
            let entry = session(hub, &request.session_id)?;
            let catalog = block_on(entry.tools(oap_protocol::ToolsListRequest {
                session_id: Some(entry.id().clone()),
                allow_degraded_features: request.allow_degraded_features.clone(),
            }))
            .map_err(|err| catalog_refusal(err, "tools_failed"))?;
            listing(
                ids,
                oap_protocol::EnvelopeType::ACTION_TOOLS_LIST_RESPONSE,
                &catalog.tools,
                entry.id(),
                catalog.revision,
            )
        }
        "history" => {
            request.only(&["cursor", "limit"])?;
            if request
                .limit
                .is_some_and(|limit| !(1..=oap_binding::MAX_LIMIT as i64).contains(&limit))
            {
                return Err(wire("invalid_request", oap_binding::Error::InvalidLimit));
            }
            let page = hub
                .session_history(&request.cursor, request.limit.unwrap_or(0))
                .map_err(|err| match err {
                    HubError::NoSessionHistory => detailed(
                        "unsupported_feature",
                        &err,
                        serde_json::json!({
                            "feature": oap_protocol::FEATURE_SESSION_LIST,
                            "reason": oap_adapter::CONTROL_UNADVERTISED,
                        }),
                    ),
                    HubError::Binding(oap_binding::Error::InvalidCursor) => {
                        wire("invalid_cursor", oap_binding::Error::InvalidCursor)
                    }
                    HubError::Binding(oap_binding::Error::InvalidLimit) => {
                        wire("invalid_request", oap_binding::Error::InvalidLimit)
                    }
                    _ => wire("history_failed", "the session history could not be read"),
                })?;
            serde_json::to_value(&page).map_err(|err| wire("internal", err))
        }
        "submit" => {
            request.only(&["session_id", "request"])?;
            submit(hub, request, ids, limit)
        }
        "sessions" => {
            request.only(&[])?;
            let listed: Vec<Value> = block_on(hub.session_statuses())
                .into_iter()
                .map(|status| {
                    let mut info = serde_json::json!({
                        "session_id": status.session_id,
                        "adapter": status.adapter,
                        "status": status.status,
                        "created_at": rfc3339(status.created_ms),
                    });
                    if let Some(run) = status.active_run_id {
                        info["active_run_id"] = serde_json::json!(run);
                    }
                    if !status.active_runs.is_empty() {
                        info["active_runs"] = serde_json::json!(status.active_runs);
                    }
                    info
                })
                .collect();
            Ok(serde_json::json!({ "sessions": listed }))
        }
        "state" => {
            request.only(&["session_id"])?;
            let entry = session(hub, &request.session_id)?;
            let state = match block_on(entry.state()) {
                Ok(state) => state,
                Err(crate::HubError::Adapter(oap_adapter::Error::SessionClosed)) => {
                    oap_protocol::SessionState {
                        session_id: entry.id().clone(),
                        status: oap_protocol::SessionStatus::CLOSED,
                        ..oap_protocol::SessionState::default()
                    }
                }
                Err(err) => {
                    return Err(WireError {
                        code: "state_failed",
                        message: trim(&err.to_string()),
                        details: None,
                    })
                }
            };
            let mut envelope = oap_protocol::Envelope::new(
                oap_protocol::EnvelopeType::SESSION_STATE_RESPONSE,
                next_id(ids, "response"),
                &state,
            )
            .map_err(|err| WireError {
                code: "internal",
                message: trim(&err.to_string()),
                details: None,
            })?;
            envelope.in_reply_to = Some(next_id(ids, "request").into());
            envelope.session_id = Some(state.session_id.clone());
            Ok(serde_json::to_value(&envelope).unwrap_or(Value::Null))
        }
        "settings" => {
            request.only(&["session_id", "request"])?;
            settings(hub, request, ids)
        }
        "cancel" => {
            request.only(&["session_id", "request"])?;
            cancel(hub, request, ids)
        }
        "resolve" => {
            request.only(&["session_id", "request"])?;
            resolve(hub, request, ids)
        }
        "close" => {
            request.only(&["session_id"])?;
            let entry = session(hub, &request.session_id)?;
            block_on(entry.close()).map_err(|err| WireError {
                code: match &err {
                    crate::HubError::Adapter(oap_adapter::Error::RunActive) => "run_active",
                    crate::HubError::Adapter(oap_adapter::Error::SessionClosed) => "session_closed",
                    _ => "internal",
                },
                message: trim(&err.to_string()),
                details: None,
            })?;
            Ok(Value::Null)
        }
        "adapters" => {
            request.only(&[])?;
            Ok(adapters(hub))
        }
        _ => Err(WireError {
            code: "unknown_op",
            message: format!("no op {:?}", trim(&request.op)),
            details: None,
        }),
    }
}

#[derive(Serialize)]
struct AdapterInfo {
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    capability_revision: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    capabilities: Option<oap_protocol::CapabilityDescriptor>,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
}

fn adapters(hub: &Hub) -> Value {
    let listed: Vec<AdapterInfo> = block_on(hub.adapters())
        .into_iter()
        .map(|(name, probed)| match probed {
            Ok(descriptor) => AdapterInfo {
                name,
                capability_revision: descriptor.capability_revision,
                capabilities: Some(descriptor.capabilities),
                error: String::new(),
            },
            Err(err) => AdapterInfo {
                name,
                capability_revision: String::new(),
                capabilities: None,
                error: trim(&err.to_string()),
            },
        })
        .collect();
    serde_json::json!({ "adapters": listed })
}

fn fitted(id: i64, answer: Answer, limit: usize) -> Result<Option<Vec<u8>>, StdioError> {
    let reduced = match &answer {
        Err(error) => Some(WireError {
            code: error.code,
            message: trim(&error.message),
            details: None,
        }),
        Ok(_) => None,
    };
    let fallback = WireError {
        code: "response_too_large",
        message: "the encoded response exceeds the frame limit".into(),
        details: None,
    };
    for candidate in [Some(answer), reduced.map(Err), Some(Err(fallback))]
        .into_iter()
        .flatten()
    {
        let line = response_line(id, candidate)?;
        if line.len() <= limit {
            return Ok(Some(line));
        }
    }
    Ok(None)
}

fn response_line(id: i64, answer: Answer) -> Result<Vec<u8>, StdioError> {
    let (result, error) = match answer {
        Ok(value) => (value, None),
        Err(error) => (Value::Null, Some(error)),
    };
    let result = serde_json::value::to_raw_value(&result).map_err(|_| StdioError::LineTooLarge)?;
    serde_json::to_vec(&Response {
        id,
        ok: error.is_none(),
        result: &result,
        error,
    })
    .map_err(|_| StdioError::LineTooLarge)
}

#[cfg(test)]
mod tests {
    #[test]
    fn rfc3339_spells_utc_seconds_across_leap_days_and_centuries() {
        for (ms, want) in [
            (0, "1970-01-01T00:00:00Z"),
            (951_782_400_000, "2000-02-29T00:00:00Z"),
            (1_761_782_400_123, "2025-10-30T00:00:00Z"),
            (4_102_444_799_000, "2099-12-31T23:59:59Z"),
        ] {
            assert_eq!(super::rfc3339(ms), want);
        }
    }
}
