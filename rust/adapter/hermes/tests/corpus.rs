mod support;

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::io::Cursor;
use std::path::{Component, Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::thread::ScopedJoinHandle;
use std::time::{Duration, Instant};

use crossbeam_channel::{Receiver, Sender};
use oap_adapter::gojson::members;
use oap_adapter::gojson::{go_marshal, go_quote};
use oap_adapter::jsonrpc::{Decoder, Kind, Message, Responder, DEFAULT_FRAME_LIMIT};
use oap_adapter::testkit::{drain, next, RunTrace};
use oap_adapter::{
    Error, EventStream, InteractionResolution, OpenRequest, Recovery, ResumeRequest,
};
use oap_adapter_hermes::adapter::{pin, Adapter, Config};
use oap_adapter_hermes::client::{resume_stored, Client, ClientFactory};
use oap_adapter_hermes::native::{
    decode_notification, SessionResumeResult, EVENT_GATEWAY_READY, METHOD_PROMPT_SUBMIT,
    METHOD_SESSION_CREATE, METHOD_SESSION_INTERRUPT, METHOD_SESSION_RESUME, REQUEST_APPROVAL,
    REQUEST_CLARIFY, REQUEST_SECRET, REQUEST_SUDO,
};
use oap_adapter_hermes::process::ProcessConfig;
use oap_adapter_hermes::rpc::{
    self, error, Fault, Inbound, IncomingRequest, NotificationMessage, DIALECT,
};
use oap_adapter_hermes::session::Session;
use oap_protocol::{
    Envelope, EnvelopeType, InputAnswer, InputQuestion, InputQuestionKind, MessageSubmitResponse,
    Participant, RunId, SessionStatus, UserInputResolveRequest,
};
use serde::de::DeserializeOwned;
use serde::Deserialize;
use serde_json::value::RawValue;
use serde_json::Value;
use support::{go_runtime, submission};

const REPOSITORY: &str = "https://github.com/NousResearch/hermes-agent";

const SOURCES: &[(&str, &str)] = &[
    ("contracts_tree", "4bdc4e3f9e5e56923028558497a8c086348858a9"),
    ("entry_py_blob", "ff2f9a899d33fc4466df85c4988e352b7c435228"),
    (
        "event_replay_py_blob",
        "20298bd23f80922c02d7b45a3c920716021f290b",
    ),
    (
        "methods_prompt_py_blob",
        "0b2baf072e82ecfeb8a06eb6dc97322941ad14dc",
    ),
    (
        "methods_session_py_blob",
        "5b1ca479044dc3c34e562ede151a25e34b9b229b",
    ),
    ("server_py_blob", "c3cc3d2cd6894f07092b7c51aa1d0ba6f2aabdcb"),
    (
        "server_requests_py_blob",
        "7d089f8ad2701f8b220ffc061d2872cf6ecf13ef",
    ),
    (
        "stdin_recovery_py_blob",
        "add5e841df4bead308a96bd8b0f0e3c5475bd167",
    ),
    (
        "transport_py_blob",
        "691f1d799027b211a073c685fbd7ee5d65fae3a6",
    ),
    ("ws_py_blob", "833751e0df038b0c551814ee3bfea95a27d0dabf"),
];

const LEDGER_FIXTURES: &[&str] = &[
    "ready-epoch",
    "ready-before-input",
    "malformed-frame",
    "submit-streaming",
    "busy-steered",
    "busy-queued",
    "submit-error-codes",
    "turn-open",
    "completed-turn",
    "interrupted-turn",
    "error-turn",
    "error-surface",
    "partial-error",
    "text-deltas",
    "reasoning-deltas",
    "interim",
    "scrubbed-provenance",
    "tool-lifecycle",
    "tool-failure-in-result",
    "approval-gate",
    "approval-choices",
    "clarify-gate",
    "sudo-gate",
    "secret-gate",
    "expire-sibling",
    "clarify-batch",
    "approval-interrupt",
    "btw-delivery",
    "background-prompt",
    "steer-run",
    "steer-rejected",
    "subagent-steer",
    "subagent-interrupt",
    "subagent-lifecycle",
    "subagent-complete-failed",
    "child-mirror-not-terminal",
    "background-reentry",
    "replay-in-window",
    "replay-truncated",
    "replay-unknown-session",
    "epoch-restart",
    "resume-live",
    "branch",
    "undo",
    "settled-session-info",
    "usage-ticker",
    "stdin-eof-exit",
    "process-exit",
    "pre-ready-observation",
    "no-turn-lifecycle-events",
    "global-events-unsequenced",
    "session-reclaimed",
    "bound-session-reopen",
    "reopen-auto-continue-refused",
];

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Manifest {
    version: u32,
    adapter: String,
    tag: String,
    commit: String,
    commit_tree: String,
    sources: BTreeMap<String, String>,
    cases: Vec<Entry>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Entry {
    id: String,
    path: String,
    ledger_fixtures: Vec<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Case {
    version: u32,
    id: String,
    native: String,
    expected_oap: String,
    mapping: String,
    omissions: String,
    provenance: Provenance,
    advertised_capabilities: BTreeMap<String, String>,
    identity_map: BTreeMap<String, String>,
    #[serde(default)]
    server_mode: String,
    #[serde(default)]
    open_error: bool,
    #[serde(default)]
    journal_capacity: usize,
    #[serde(default)]
    reopen: bool,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Provenance {
    repository: String,
    tag: String,
    commit: String,
    commit_tree: String,
    sources: BTreeMap<String, String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Frame {
    direction: String,
    classification: String,
    fidelity: String,
    action: String,
    raw: Option<Box<RawValue>>,
}

#[derive(Clone, Default, Deserialize)]
#[serde(deny_unknown_fields)]
struct Control {
    #[serde(rename = "type")]
    kind: String,
    #[serde(default)]
    op: String,
    #[serde(default)]
    error: String,
    #[serde(default, rename = "kind")]
    gate: String,
    #[serde(default)]
    answer: String,
    #[serde(default)]
    answers: Vec<String>,
    #[serde(default)]
    status: String,
    #[serde(default)]
    expect: String,
    #[serde(default)]
    run_id: String,
    #[serde(default)]
    after: u64,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Mapping {
    index: usize,
    #[serde(rename = "type")]
    kind: String,
    direction: String,
    classification: String,
    fidelity: String,
    #[serde(default)]
    oap: String,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Omission {
    index: usize,
    #[serde(rename = "type")]
    kind: String,
    reason: String,
}

#[derive(Default)]
struct Decoded {
    message: Option<Message>,
    event: Option<String>,
    control: Option<Control>,
    invalid: bool,
    request_method: String,
}

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&pin().corpus)
}

fn load<T: DeserializeOwned>(path: &Path) -> T {
    let text = std::fs::read_to_string(path).unwrap_or_else(|error| panic!("{path:?}: {error}"));
    serde_json::from_str(&text).unwrap_or_else(|error| panic!("decode {path:?}: {error}"))
}

fn safe_relative(path: &str) -> bool {
    !path.is_empty()
        && Path::new(path)
            .components()
            .all(|part| matches!(part, Component::Normal(_)))
}

fn file_in(dir: &Path, name: &str) -> PathBuf {
    assert!(
        safe_relative(name) && !name.contains('/'),
        "invalid corpus filename {name:?}"
    );
    dir.join(name)
}

fn sources() -> BTreeMap<String, String> {
    SOURCES
        .iter()
        .map(|(name, blob)| ((*name).to_owned(), (*blob).to_owned()))
        .collect()
}

fn wire(raw: &RawValue, index: usize) -> String {
    match raw.get().trim_start().as_bytes().first() {
        Some(b'"') => serde_json::from_str(raw.get())
            .unwrap_or_else(|error| panic!("frame {index}: raw string literal: {error}")),
        _ => raw.get().to_owned(),
    }
}

fn decode_wire(wire: &str) -> Result<Message, rpc::Error> {
    Decoder::new(
        DIALECT,
        Cursor::new(format!("{wire}\n")),
        DEFAULT_FRAME_LIMIT,
    )
    .decode()
}

fn id_key(message: &Message) -> String {
    message
        .id
        .as_ref()
        .map(ToString::to_string)
        .unwrap_or_default()
}

fn control_valid(control: &Control) -> bool {
    match (control.kind.as_str(), control.op.as_str()) {
        ("process_exit", _) => !control.error.is_empty(),
        ("oap_control", "resolve") => {
            gate_kind(&control.gate) && control.answer.is_empty() != control.answers.is_empty()
        }
        ("oap_control", "cancel" | "close") => true,
        ("oap_control", "assert-state") => matches!(control.status.as_str(), "idle" | "running"),
        ("oap_control", "overlap-submit") => control.expect == "run-active",
        ("oap_control", "submit-closed") => control.expect == "session-closed",
        ("oap_control", "resume") => match control.expect.as_str() {
            "replay" | "gap" => control.run_id.is_empty(),
            "run-not-found" => !control.run_id.is_empty() && control.after == 0,
            _ => false,
        },
        ("harness_sync", op) => op == "wait-submit",
        _ => false,
    }
}

fn gate_kind(kind: &str) -> bool {
    [
        REQUEST_APPROVAL,
        REQUEST_CLARIFY,
        REQUEST_SUDO,
        REQUEST_SECRET,
    ]
    .contains(&kind)
}

fn frames(path: &Path) -> (Vec<Frame>, Vec<Decoded>) {
    let text = std::fs::read_to_string(path).unwrap();
    let mut frames = Vec::new();
    let mut decoded = Vec::new();
    let mut method_by_id = HashMap::new();
    let mut request_by_id = HashMap::new();
    for (at, line) in text.lines().enumerate() {
        let index = at + 1;
        let frame: Frame = serde_json::from_str(line)
            .unwrap_or_else(|error| panic!("decode {path:?} frame {index}: {error}"));
        let mut entry = Decoded::default();
        let raw = frame.raw.as_deref().filter(|raw| raw.get() != "null");
        match frame.direction.as_str() {
            "gateway-to-host" => {
                let wire = wire(raw.expect("a gateway frame carries bytes"), index);
                let mut outcome = decode_wire(&wire).map_err(|error| error.to_string());
                if let Ok(message) = &outcome {
                    if message.kind == Kind::Request {
                        request_by_id.insert(id_key(message), message.method.clone());
                    }
                    if message.kind == Kind::Notification {
                        let params = message.params.as_deref().map_or("", RawValue::get);
                        match decode_notification(&message.method, params.as_bytes()) {
                            Ok(event) => entry.event = Some(event.kind),
                            Err(detail) => outcome = Err(detail),
                        }
                    }
                }
                match (frame.action.as_str(), outcome) {
                    ("decode-error", Ok(_)) => {
                        panic!("frame {index}: the production codec accepted an invalid frame")
                    }
                    ("decode-error", Err(_)) => entry.invalid = true,
                    (_, Ok(message)) => entry.message = Some(message),
                    (_, Err(detail)) => panic!("frame {index} production decode: {detail}"),
                }
            }
            "host-to-gateway" => {
                let Some(raw) = raw else {
                    assert_eq!(
                        frame.action, "open",
                        "frame {index}: host frame without raw bytes"
                    );
                    frames.push(frame);
                    decoded.push(entry);
                    continue;
                };
                let wire = wire(raw, index);
                let message = decode_wire(&wire)
                    .unwrap_or_else(|error| panic!("frame {index} request decode: {error}"));
                assert!(
                    matches!(message.kind, Kind::Request | Kind::Response),
                    "frame {index}: host frame is neither a request nor an answer"
                );
                assert_eq!(
                    message.encode(&DIALECT).unwrap(),
                    wire,
                    "frame {index} is not canonical outbound JSON"
                );
                if message.kind == Kind::Request {
                    method_by_id.insert(id_key(&message), message.method.clone());
                } else {
                    entry.request_method = request_by_id
                        .get(&id_key(&message))
                        .unwrap_or_else(|| panic!("frame {index}: answer to unknown request"))
                        .clone();
                }
                entry.message = Some(message);
            }
            "harness-control" => {
                let control: Control =
                    serde_json::from_str(raw.expect("a control frame carries its body").get())
                        .unwrap_or_else(|error| panic!("frame {index} control: {error}"));
                assert!(control_valid(&control), "frame {index} invalid control");
                entry.control = Some(control);
            }
            other => panic!("frame {index} invalid direction {other:?}"),
        }
        frames.push(frame);
        decoded.push(entry);
    }
    assert!(!frames.is_empty(), "{path:?} is an empty native transcript");
    for (at, entry) in decoded.iter_mut().enumerate() {
        if frames[at].direction != "gateway-to-host" || entry.event.is_some() {
            continue;
        }
        let Some(message) = &entry.message else {
            continue;
        };
        if matches!(message.kind, Kind::Response | Kind::Error) {
            entry.request_method = method_by_id
                .get(&id_key(message))
                .unwrap_or_else(|| panic!("frame {}: response to unknown request", at + 1))
                .clone();
        }
    }
    (frames, decoded)
}

fn frame_type(frame: &Frame, decoded: &Decoded) -> String {
    match frame.direction.as_str() {
        "harness-control" => {
            let control = decoded.control.as_ref().unwrap();
            match control.kind.as_str() {
                "process_exit" => "process_exit".into(),
                "harness_sync" => format!("sync:{}", control.op),
                _ => format!("oap_control:{}", control.op),
            }
        }
        "host-to-gateway" => match &decoded.message {
            Some(message) if message.kind == Kind::Response => {
                format!("answer:{}", decoded.request_method)
            }
            Some(message) => format!("request:{}", message.method),
            None => "open".into(),
        },
        _ => match (&decoded.event, &decoded.message) {
            _ if decoded.invalid => "codec-error".into(),
            (Some(event), _) => format!("event/{event}"),
            (None, Some(message)) if message.kind == Kind::Request => {
                format!("server-request:{}", message.method)
            }
            (None, Some(message)) if matches!(message.kind, Kind::Response | Kind::Error) => {
                format!("response:{}", decoded.request_method)
            }
            _ => "codec-error".into(),
        },
    }
}

fn classify(frames: &[Frame], decoded: &[Decoded], mappings: &[Mapping], omissions: &[Omission]) {
    assert_eq!(mappings.len(), frames.len(), "mapping count");
    let mut omitted = HashMap::new();
    for omission in omissions {
        assert!(
            (1..=frames.len()).contains(&omission.index)
                && !omission.reason.is_empty()
                && omitted.insert(omission.index, omission).is_none(),
            "invalid omission {}",
            omission.index
        );
    }
    for (at, (frame, mapping)) in frames.iter().zip(mappings).enumerate() {
        let index = at + 1;
        let kind = frame_type(frame, &decoded[at]);
        assert!(
            mapping.index == index
                && mapping.kind == kind
                && mapping.direction == frame.direction
                && mapping.classification == frame.classification
                && mapping.fidelity == frame.fidelity,
            "frame {index} mapping mismatch: type {kind}"
        );
        let omission = omitted.get(&index);
        assert!(
            omission.is_none_or(|omission| omission.kind == kind),
            "omission {index} type mismatch"
        );
        let consistent = match frame.classification.as_str() {
            "mapped" => omission.is_none() && !mapping.oap.is_empty(),
            "required-unmapped" => {
                omission.is_none() && mapping.oap.is_empty() && frame.fidelity == "unsupported"
            }
            "observed-only" => omission.is_some() && mapping.oap.is_empty(),
            other => panic!("frame {index} invalid classification {other:?}"),
        };
        assert!(consistent, "frame {index} classification is inconsistent");
        assert!(
            matches!(
                frame.fidelity.as_str(),
                "native" | "normalized" | "synthesized" | "lossy" | "unsupported"
            ),
            "frame {index} invalid fidelity"
        );
    }
}

fn check_actions(case: &Case, frames: &[Frame]) {
    for (at, frame) in frames.iter().enumerate() {
        let known: &[&str] = match frame.direction.as_str() {
            "gateway-to-host" => &["auto", "reply", "reply-error", "observe", "decode-error"],
            "host-to-gateway" => &["submit", "open", "expect-write"],
            _ => &["process-exit", "oap-control", "wait-submit"],
        };
        assert!(
            known.contains(&frame.action.as_str()),
            "frame {} has unknown action {:?}",
            at + 1,
            frame.action
        );
        let harness: &[&str] = if case.server_mode.is_empty() {
            &[
                "submit",
                "reply",
                "reply-error",
                "observe",
                "expect-write",
                "oap-control",
                "process-exit",
                "wait-submit",
            ]
        } else {
            &["open", "submit", "auto", "decode-error", "oap-control"]
        };
        assert!(
            harness.contains(&frame.action.as_str()),
            "frame {} action {:?} does not fit its harness",
            at + 1,
            frame.action
        );
    }
    assert!(
        !case.open_error || !case.server_mode.is_empty(),
        "open_error is only meaningful for a process case"
    );
}

#[derive(Default)]
struct Answered {
    sent: Mutex<Vec<Message>>,
    signal: Mutex<Option<Sender<()>>>,
}

impl Responder for Answered {
    fn send(&self, message: Message) -> Result<(), rpc::Error> {
        self.sent.lock().unwrap().push(message);
        if let Some(signal) = self.signal.lock().unwrap().as_ref() {
            let _ = signal.send(());
        }
        Ok(())
    }
}

type Reply = Result<String, String>;

struct Gateway {
    calls: Mutex<Vec<(String, String)>>,
    started: (Sender<String>, Receiver<String>),
    replies: (Sender<Reply>, Receiver<Reply>),
    answered: Arc<Answered>,
    answers: Receiver<()>,
    sender: Mutex<Option<Sender<Inbound>>>,
    inbound: Receiver<Inbound>,
    done: Mutex<Option<Sender<()>>>,
    finished: Receiver<()>,
    failure: Mutex<Option<String>>,
}

impl Gateway {
    fn new() -> Arc<Self> {
        let (sender, inbound) = crossbeam_channel::unbounded();
        let (done, finished) = crossbeam_channel::bounded(0);
        let (signal, answers) = crossbeam_channel::unbounded();
        Arc::new(Self {
            calls: Mutex::default(),
            started: crossbeam_channel::unbounded(),
            replies: crossbeam_channel::unbounded(),
            answered: Arc::new(Answered {
                sent: Mutex::default(),
                signal: Mutex::new(Some(signal)),
            }),
            answers,
            sender: Mutex::new(Some(sender)),
            inbound,
            done: Mutex::new(Some(done)),
            finished,
            failure: Mutex::default(),
        })
    }

    fn calls(&self) -> Vec<(String, String)> {
        self.calls.lock().unwrap().clone()
    }

    fn await_call(&self, method: &str) {
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            let left = deadline.saturating_duration_since(Instant::now());
            match self.started.1.recv_timeout(left) {
                Ok(called) if called == method => return,
                Ok(_) => {}
                Err(_) => panic!("native call {method} was not issued"),
            }
        }
    }

    fn await_answer(&self) {
        self.answers
            .recv_timeout(Duration::from_secs(5))
            .expect("the adapter did not answer the server request");
    }

    fn deliver(&self, message: &Message) {
        let sender = self.sender.lock().unwrap().clone();
        let sender = sender.expect("an observation after the transport closed");
        let inbound = if message.kind == Kind::Request {
            Inbound::Request(Arc::new(IncomingRequest::new(
                DIALECT,
                message.id.clone().unwrap(),
                &message.method,
                message.params.clone(),
                self.answered.clone(),
            )))
        } else {
            Inbound::Notification(NotificationMessage {
                method: message.method.clone(),
                params: message.params.clone(),
            })
        };
        sender.send(inbound).unwrap();
        let (ack, acked) = crossbeam_channel::bounded::<()>(0);
        sender.send(Inbound::Barrier(ack)).unwrap();
        assert!(
            matches!(
                acked.recv_timeout(Duration::from_secs(5)),
                Err(crossbeam_channel::RecvTimeoutError::Disconnected)
            ),
            "reducer barrier timed out"
        );
    }

    fn hang_up(&self, failure: &str) {
        *self.failure.lock().unwrap() = Some(failure.into());
        self.done.lock().unwrap().take();
        self.sender.lock().unwrap().take();
    }
}

impl Client for Gateway {
    fn call(
        &self,
        method: &str,
        params: Box<RawValue>,
        _: Option<Instant>,
    ) -> Result<Box<RawValue>, rpc::Error> {
        if ![
            METHOD_SESSION_CREATE,
            METHOD_PROMPT_SUBMIT,
            METHOD_SESSION_INTERRUPT,
        ]
        .contains(&method)
        {
            return Err(error(Fault::Transport(format!(
                "fixture client: unexpected native call {}",
                go_quote(method)
            ))));
        }
        self.calls
            .lock()
            .unwrap()
            .push((method.into(), params.get().into()));
        let _ = self.started.0.send(method.into());
        match self.replies.1.recv() {
            Ok(Ok(result)) => Ok(RawValue::from_string(result).unwrap()),
            Ok(Err(failure)) => Err(error(Fault::Transport(failure))),
            Err(_) => Err(error(Fault::Closed)),
        }
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.inbound.clone()
    }

    fn done(&self) -> Receiver<()> {
        self.finished.clone()
    }

    fn err(&self) -> Option<String> {
        Some(
            self.failure
                .lock()
                .unwrap()
                .clone()
                .unwrap_or_else(|| "EOF".into()),
        )
    }

    fn close(&self) -> Result<(), String> {
        self.done.lock().unwrap().take();
        Ok(())
    }
}

struct Factory {
    gateway: Arc<Gateway>,
    native_id: String,
}

impl ClientFactory for Factory {
    fn start(&self, _: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        Ok((self.gateway.clone(), self.native_id.clone()))
    }
}

fn opened() -> OpenRequest {
    OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

fn native_id(decoded: &[Decoded]) -> String {
    decoded
        .iter()
        .filter_map(|entry| entry.message.as_ref())
        .filter(|message| message.kind == Kind::Request && message.method == METHOD_PROMPT_SUBMIT)
        .filter_map(|message| {
            let params: Value = serde_json::from_str(message.params.as_deref()?.get()).ok()?;
            Some(params["session_id"].as_str()?.to_owned())
        })
        .find(|id| !id.is_empty())
        .unwrap_or_else(|| "sess0001".into())
}

fn same_json(left: &str, right: &str) -> bool {
    serde_json::from_str::<Value>(left).ok() == serde_json::from_str::<Value>(right).ok()
}

fn title(kind: &str) -> &'static str {
    match kind {
        REQUEST_APPROVAL => "Command approval",
        REQUEST_CLARIFY => "Clarification",
        REQUEST_SUDO => "Password required",
        _ => "Secret required",
    }
}

fn open_gate(session: &Session, kind: &str) -> (String, Vec<InputQuestion>) {
    let run = session
        .state()
        .unwrap()
        .active_run_id
        .expect("a gate needs a running run");
    let (recovery, mut replay) = session
        .resume(ResumeRequest {
            run_id: run,
            after_sequence: 0,
        })
        .unwrap();
    let mut requested = Vec::new();
    let mut resolved = HashSet::new();
    for _ in 0..recovery.replayed_through {
        let event = next(&mut replay, Duration::from_secs(5));
        let payload: Value = serde_json::from_str(event.payload.as_deref().unwrap().get()).unwrap();
        let id = payload["interaction_id"]
            .as_str()
            .unwrap_or_default()
            .to_owned();
        if event.envelope_type == EnvelopeType::USER_INPUT_REQUESTED
            && payload["title"] == title(kind)
        {
            let questions = serde_json::from_value(payload["questions"].clone()).unwrap();
            requested.push((id, questions));
        } else if event.envelope_type == EnvelopeType::USER_INPUT_RESOLVED {
            resolved.insert(id);
        }
    }
    requested
        .into_iter()
        .rev()
        .find(|(id, _)| !resolved.contains(id))
        .unwrap_or_else(|| panic!("no open {kind} gate"))
}

fn resolution(id: String, questions: &[InputQuestion], control: &Control) -> InteractionResolution {
    let values = match control.answers.is_empty() {
        true => vec![control.answer.clone()],
        false => control.answers.clone(),
    };
    let answers = questions
        .iter()
        .zip(values)
        .map(
            |(question, value)| match question.kind == InputQuestionKind::TEXT {
                true => InputAnswer {
                    question_id: question.id.clone(),
                    text: value,
                    ..InputAnswer::default()
                },
                false => InputAnswer {
                    question_id: question.id.clone(),
                    selected_option_ids: vec![value],
                    ..InputAnswer::default()
                },
            },
        )
        .collect();
    InteractionResolution {
        input: Some(UserInputResolveRequest {
            interaction_id: id.into(),
            session_id: "session".into(),
            answers,
            ..UserInputResolveRequest::default()
        }),
        ..InteractionResolution::default()
    }
}

type Submitted = Result<(MessageSubmitResponse, EventStream), Error>;

struct Resumed {
    index: usize,
    settled: usize,
    control: Control,
    outcome: Result<(Recovery, EventStream), Error>,
}

fn join<T>(handle: &mut Option<ScopedJoinHandle<'_, T>>) -> Option<T> {
    let handle = handle.take()?;
    let deadline = Instant::now() + Duration::from_secs(5);
    while !handle.is_finished() {
        assert!(
            Instant::now() < deadline,
            "a corpus operation did not settle"
        );
        std::thread::sleep(Duration::from_millis(1));
    }
    Some(handle.join().unwrap())
}

fn replay(case: &Case, frames: &[Frame], decoded: &[Decoded]) -> Vec<Envelope> {
    let gateway = Gateway::new();
    let adapter = Adapter::new(Config {
        factory: Some(Arc::new(Factory {
            gateway: gateway.clone(),
            native_id: native_id(decoded),
        })),
        model: "hermes-test".into(),
        journal_capacity: match case.journal_capacity {
            0 => 64,
            capacity => capacity,
        },
        runtime: go_runtime(),
        ..Config::default()
    })
    .unwrap();
    let descriptor = adapter.describe();
    for (feature, level) in &case.advertised_capabilities {
        let support = descriptor
            .capabilities
            .features
            .get(feature)
            .unwrap_or_else(|| panic!("the case advertises unknown capability {feature:?}"));
        assert_eq!(support.level.as_str(), level, "capability {feature}");
    }
    let session = adapter.open(opened()).unwrap();
    let mut settled: Vec<Submitted> = Vec::new();
    let mut resumes = Vec::new();
    std::thread::scope(|scope| {
        let mut pending: Option<ScopedJoinHandle<'_, Submitted>> = None;
        let mut resolving: Option<ScopedJoinHandle<'_, Result<(), Error>>> = None;
        let mut cancelling: Option<ScopedJoinHandle<'_, Result<(), Error>>> = None;
        for (at, (frame, entry)) in frames.iter().zip(decoded).enumerate() {
            let index = at + 1;
            match frame.action.as_str() {
                "submit" => {
                    settled.extend(join(&mut pending));
                    pending = Some(scope.spawn(|| session.submit(submission())));
                    gateway.await_call(METHOD_PROMPT_SUBMIT);
                    let (_, params) = gateway.calls().pop().unwrap();
                    let want = entry.message.as_ref().unwrap().params.as_deref().unwrap();
                    assert!(
                        same_json(&params, want.get()),
                        "frame {index}: the adapter wrote {params}"
                    );
                }
                "reply" => {
                    let message = entry.message.as_ref().unwrap();
                    let result = message.result.as_deref().map_or("null", RawValue::get);
                    gateway.replies.0.send(Ok(result.into())).unwrap();
                    if let Some(resolved) = join(&mut resolving) {
                        resolved.unwrap();
                    }
                    if let Some(cancelled) = join(&mut cancelling) {
                        cancelled.unwrap();
                    }
                }
                "reply-error" => {
                    let message = entry.message.as_ref().unwrap();
                    let failure = message.error.as_ref().expect("a JSON-RPC error response");
                    let text = format!("fixture rejection: {}", failure.message);
                    gateway.replies.0.send(Err(text)).unwrap();
                }
                "observe" => gateway.deliver(entry.message.as_ref().unwrap()),
                "expect-write" => {
                    let message = entry.message.as_ref().unwrap();
                    if message.kind == Kind::Response {
                        let answer = gateway.answered.sent.lock().unwrap().last().cloned();
                        let answer = answer.expect("no server-request answer was recorded");
                        let result = answer.result.as_deref().map_or("null", RawValue::get);
                        let want = message.result.as_deref().map_or("null", RawValue::get);
                        assert!(
                            answer.id == message.id && same_json(result, want),
                            "frame {index}: the adapter answered {result}"
                        );
                        if let Some(resolved) = join(&mut resolving) {
                            resolved.unwrap();
                        }
                    } else {
                        let (method, params) = gateway.calls().pop().expect("a native call");
                        let want = message.params.as_deref().map_or("null", RawValue::get);
                        assert!(
                            method == message.method && same_json(&params, want),
                            "frame {index}: the adapter wrote {method} {params}"
                        );
                    }
                }
                "oap-control" => {
                    let control = entry.control.clone().unwrap();
                    match control.op.as_str() {
                        "resolve" => {
                            let (id, questions) = open_gate(&session, &control.gate);
                            let chosen = resolution(id, &questions, &control);
                            let session = &session;
                            resolving = Some(scope.spawn(move || session.resolve(chosen)));
                            gateway.await_answer();
                        }
                        "cancel" => {
                            settled.extend(join(&mut pending));
                            let run = match settled.last() {
                                Some(Ok((admission, _))) => admission.run_id.clone().unwrap(),
                                _ => panic!("frame {index}: no admitted run to cancel"),
                            };
                            let session = &session;
                            cancelling = Some(scope.spawn(move || session.cancel(&run).map(drop)));
                            gateway.await_call(METHOD_SESSION_INTERRUPT);
                        }
                        "assert-state" => {
                            let state = session.state().unwrap();
                            assert_eq!(state.status.as_str(), control.status, "frame {index}");
                        }
                        "overlap-submit" | "submit-closed" => {
                            let calls = gateway.calls().len();
                            let refused = session.submit(submission()).err();
                            let want = match control.op.as_str() {
                                "overlap-submit" => Error::RunActive,
                                _ => Error::SessionClosed,
                            };
                            assert_eq!(refused, Some(want), "frame {index}");
                            assert_eq!(gateway.calls().len(), calls, "frame {index} wrote");
                        }
                        "resume" => {
                            let calls = gateway.calls().len();
                            if control.expect == "run-not-found" {
                                let refused = session.resume(ResumeRequest {
                                    run_id: RunId::from(control.run_id.as_str()),
                                    after_sequence: 0,
                                });
                                assert_eq!(refused.err(), Some(Error::RunNotFound));
                            } else {
                                settled.extend(join(&mut pending));
                                let target = settled.len() - 1;
                                let run = match &settled[target] {
                                    Ok((admission, _)) => admission.run_id.clone().unwrap(),
                                    Err(_) => panic!("frame {index}: no admitted run to resume"),
                                };
                                let outcome = session.resume(ResumeRequest {
                                    run_id: run,
                                    after_sequence: control.after,
                                });
                                resumes.push(Resumed {
                                    index,
                                    settled: target,
                                    control: control.clone(),
                                    outcome,
                                });
                            }
                            assert_eq!(gateway.calls().len(), calls, "frame {index} wrote");
                        }
                        "close" => {
                            settled.extend(join(&mut pending));
                            session.close().unwrap();
                        }
                        other => panic!("frame {index}: unsupported control {other:?}"),
                    }
                }
                "process-exit" => {
                    gateway.hang_up(&entry.control.as_ref().unwrap().error);
                    let deadline = Instant::now() + Duration::from_secs(5);
                    while session
                        .state()
                        .is_ok_and(|state| state.status == SessionStatus::RUNNING)
                    {
                        assert!(
                            Instant::now() < deadline,
                            "the transport failure did not settle"
                        );
                        std::thread::sleep(Duration::from_millis(1));
                    }
                }
                "wait-submit" => settled.extend(join(&mut pending)),
                other => panic!("frame {index}: unsupported action {other:?}"),
            }
        }
        settled.extend(join(&mut pending));
        if let Some(resolved) = join(&mut resolving) {
            resolved.unwrap();
        }
        if let Some(cancelled) = join(&mut cancelling) {
            cancelled.unwrap();
        }
    });
    let mut envelopes = Vec::new();
    for (at, outcome) in settled.into_iter().enumerate() {
        let Ok((admission, mut stream)) = outcome else {
            continue;
        };
        let delivered = drain(&mut stream, Duration::from_secs(5));
        if !delivered.is_empty() {
            RunTrace {
                opened: Some(opened()),
                ..RunTrace::new(admission, descriptor.clone(), delivered.clone())
            }
            .assert_valid();
        }
        for resumed in resumes.iter_mut().filter(|resumed| resumed.settled == at) {
            check_resume(resumed, &delivered);
        }
        envelopes.extend(delivered);
    }
    for (method, _) in gateway.calls() {
        assert!(
            [
                METHOD_SESSION_CREATE,
                METHOD_PROMPT_SUBMIT,
                METHOD_SESSION_INTERRUPT
            ]
            .contains(&method.as_str()),
            "the adapter wrote {method}"
        );
    }
    envelopes
}

const GATEWAY: &str = r#"set -u
d=$0
mode=$(cat "$d/mode")
if [ "$mode" = pre-observe ]; then
  head -n 1 "$d/script"
  k=0
  while [ "$k" -lt 200 ]; do sleep 0.05; k=$((k + 1)); done
  exit 0
fi
cat "$d/ready"
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$d/wire.log"
  id=$(printf '%s' "$line" | sed -n 's/^{"id":\([^,]*\),.*/\1/p')
  method=$(printf '%s' "$line" | sed -n 's/.*"method":"\([^"]*\)".*/\1/p')
  if [ -z "$id" ] || [ -z "$method" ]; then exit 0; fi
  if [ -f "$d/response.$id" ]; then cat "$d/response.$id"; fi
  if [ "$method" = prompt.submit ]; then
    cat "$d/script"
    if [ "$mode" = malformed ]; then exit 0; fi
  fi
done
"#;

fn stage_gateway(case: &Case, frames: &[Frame], workspace: &Path) {
    let mut ready = String::new();
    let mut script = String::new();
    for (at, frame) in frames.iter().enumerate() {
        if frame.direction != "gateway-to-host"
            || !matches!(frame.action.as_str(), "auto" | "decode-error")
        {
            continue;
        }
        let line = wire(frame.raw.as_deref().unwrap(), at + 1);
        let object = members(&line)
            .ok()
            .filter(|_| frame.action != "decode-error");
        let Some(object) = object else {
            script.push_str(&format!("{line}\n"));
            continue;
        };
        let member = |name: &str| {
            object
                .iter()
                .find(|(key, _)| key == name)
                .map(|(_, raw)| raw)
        };
        if member("method").is_none() {
            if let Some(id) = member("id") {
                std::fs::write(
                    workspace.join(format!("response.{}", id.get())),
                    format!("{line}\n"),
                )
                .unwrap();
            }
            continue;
        }
        let kind = member("params")
            .and_then(|params| serde_json::from_str::<Value>(params.get()).ok())
            .and_then(|params| params["type"].as_str().map(str::to_owned));
        match kind.as_deref() {
            Some("gateway.ready") => ready = format!("{line}\n"),
            _ => script.push_str(&format!("{line}\n")),
        }
    }
    std::fs::write(workspace.join("mode"), &case.server_mode).unwrap();
    std::fs::write(workspace.join("ready"), ready).unwrap();
    std::fs::write(workspace.join("script"), script).unwrap();
}

fn replay_process(case: &Case, frames: &[Frame], dir: &Path) -> Vec<Envelope> {
    let workspace = std::env::temp_dir()
        .join(format!("oap-hermes-corpus-{}", std::process::id()))
        .join(&case.id);
    let _ = std::fs::remove_dir_all(&workspace);
    std::fs::create_dir_all(&workspace).unwrap();
    stage_gateway(case, frames, &workspace);
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: "/bin/sh".into(),
            args: vec![
                "-c".into(),
                GATEWAY.into(),
                workspace.to_string_lossy().into_owned(),
            ],
            dir: workspace.to_string_lossy().into_owned(),
            env: Some(vec!["PATH=/usr/bin:/bin".into()]),
            exit_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        model: "hermes-test".into(),
        journal_capacity: match case.journal_capacity {
            0 => 64,
            capacity => capacity,
        },
        runtime: go_runtime(),
        ..Config::default()
    })
    .unwrap();
    let descriptor = adapter.describe();
    let mut session = None;
    let mut envelopes = Vec::new();
    let record = |envelopes: &mut Vec<Envelope>, submitted: Option<Submitted>| {
        let Some(Ok((admission, mut stream))) = submitted else {
            return;
        };
        let delivered = drain(&mut stream, Duration::from_secs(5));
        if !delivered.is_empty() {
            RunTrace {
                opened: Some(opened()),
                ..RunTrace::new(admission, descriptor.clone(), delivered.clone())
            }
            .assert_valid();
        }
        envelopes.extend(delivered);
    };
    std::thread::scope(|scope| {
        let mut pending: Option<ScopedJoinHandle<'_, Submitted>> = None;
        for (at, frame) in frames.iter().enumerate() {
            let index = at + 1;
            match frame.action.as_str() {
                "open" => {
                    assert!(session.is_none(), "frame {index}: the session opened twice");
                    match (adapter.open(opened()), case.open_error) {
                        (Ok(opened), false) => session = Some(Arc::new(opened)),
                        (Err(_), true) => {}
                        (outcome, _) => panic!("frame {index}: open {:?}", outcome.err()),
                    }
                }
                "submit" => {
                    record(&mut envelopes, join(&mut pending));
                    let session = session.clone().expect("a submit needs an open session");
                    pending = Some(scope.spawn(move || session.submit(submission())));
                }
                "oap-control" => {
                    record(&mut envelopes, join(&mut pending));
                    if let Some(open) = session.take() {
                        if let Err(failure) = open.close() {
                            panic!("{} close: {failure:?}", case.id);
                        }
                    }
                }
                "auto" | "decode-error" => {}
                other => panic!("frame {index}: action {other:?} needs the in-process harness"),
            }
        }
        record(&mut envelopes, join(&mut pending));
    });
    assert!(
        session.is_none(),
        "the process case ended without a close frame"
    );
    let logged = std::fs::read_to_string(workspace.join("wire.log")).unwrap_or_default();
    let written: Vec<String> = frames
        .iter()
        .enumerate()
        .filter(|(_, frame)| frame.direction == "host-to-gateway")
        .filter_map(|(at, frame)| {
            let raw = frame.raw.as_deref().filter(|raw| raw.get() != "null")?;
            Some(wire(raw, at + 1))
        })
        .collect();
    assert_eq!(
        logged.lines().collect::<Vec<_>>(),
        written,
        "{} host wire",
        case.id
    );
    let _ = dir;
    let _ = std::fs::remove_dir_all(&workspace);
    envelopes
}

struct Recorded {
    request: String,
    response: Message,
    calls: Mutex<Vec<String>>,
    finished: Receiver<()>,
}

impl Client for Recorded {
    fn call(
        &self,
        method: &str,
        params: Box<RawValue>,
        _: Option<Instant>,
    ) -> Result<Box<RawValue>, rpc::Error> {
        self.calls.lock().unwrap().push(method.into());
        let recorded: Value = serde_json::from_str(&self.request).unwrap();
        let sent: Value = serde_json::from_str(params.get()).unwrap();
        assert!(
            recorded["method"] == method && recorded["params"] == sent,
            "native call {method} {}, want the recorded {}",
            params.get(),
            self.request
        );
        match (&self.response.error, &self.response.result) {
            (Some(object), _) => Err(error(Fault::Remote {
                id: self.response.id.clone().unwrap(),
                object: object.clone(),
            })),
            (None, result) => Ok(result.clone().unwrap_or_default()),
        }
    }

    fn inbound(&self) -> Receiver<Inbound> {
        crossbeam_channel::never()
    }

    fn done(&self) -> Receiver<()> {
        self.finished.clone()
    }

    fn err(&self) -> Option<String> {
        None
    }

    fn close(&self) -> Result<(), String> {
        Ok(())
    }
}

struct Reopener(Arc<Recorded>);

impl ClientFactory for Reopener {
    fn start(&self, _: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        panic!("a reopen corpus case never creates a session")
    }

    fn reopen(
        &self,
        stored: &str,
        deadline: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, SessionResumeResult), String> {
        let resumed = resume_stored(self.0.as_ref(), stored, deadline)?;
        Ok((self.0.clone(), resumed))
    }
}

fn replay_reopen(labels: &[String], frames: &[Frame], decoded: &[Decoded], expected: &Path) {
    assert!(
        frames.len() == 3
            && decoded[0].event.as_deref() == Some(EVENT_GATEWAY_READY)
            && decoded[2].message.is_some(),
        "a reopen case is gateway.ready, session.resume and its answer"
    );
    let request = wire(frames[1].raw.as_deref().unwrap(), 2);
    let stored: Value = serde_json::from_str(&request).unwrap();
    let stored = stored["params"]["session_id"].as_str().unwrap().to_owned();
    let (_keep, finished) = crossbeam_channel::bounded::<()>(0);
    let recorded = Arc::new(Recorded {
        request,
        response: decoded[2].message.clone().unwrap(),
        calls: Mutex::default(),
        finished,
    });
    let adapter = Adapter::new(Config {
        factory: Some(Arc::new(Reopener(recorded.clone()))),
        model: "hermes-test".into(),
        runtime: go_runtime(),
        ..Config::default()
    })
    .unwrap();
    let reopened = adapter.open(OpenRequest {
        reopen: true,
        native_session_id: stored,
        ..opened()
    });
    let expected: Value = load(expected);
    for label in labels {
        match (label.as_str(), &reopened) {
            ("bound-session-reopen", Ok(session)) => {
                let mut state = serde_json::to_value(session.state().unwrap()).unwrap();
                state.as_object_mut().unwrap().remove("updated_at_ms");
                assert_eq!(state, expected, "the reopened state");
            }
            ("reopen-auto-continue-refused", Err(Error::UnsupportedControl(control))) => {
                assert!(reopened.as_ref().err().unwrap().is_unsupported_input());
                assert_eq!(
                    (control.feature.as_str(), control.reason),
                    (
                        expected["feature"].as_str().unwrap(),
                        expected["reason"].as_str().unwrap()
                    ),
                    "the reopen refusal"
                );
            }
            (label, outcome) => panic!(
                "ledger fixture {label:?} reopened {:?}",
                outcome.as_ref().err()
            ),
        }
    }
    assert_eq!(*recorded.calls.lock().unwrap(), [METHOD_SESSION_RESUME]);
}

fn check_resume(resumed: &mut Resumed, delivered: &[Envelope]) {
    let index = resumed.index;
    let after = resumed.control.after;
    let latest = delivered
        .last()
        .and_then(|event| event.sequence)
        .unwrap_or_else(|| panic!("frame {index}: the resumed run delivered nothing"));
    match (resumed.control.expect.as_str(), &mut resumed.outcome) {
        ("replay", Ok((recovery, stream))) => {
            assert!(recovery.replay_gap.is_none(), "frame {index}: a gap");
            let suffix: Vec<_> = delivered
                .iter()
                .filter(|event| event.sequence.is_some_and(|sequence| sequence > after))
                .cloned()
                .collect();
            let replayed = drain(stream, Duration::from_secs(5));
            assert!(
                !suffix.is_empty()
                    && serde_json::to_string(&replayed).unwrap()
                        == serde_json::to_string(&suffix).unwrap(),
                "frame {index}: the replay differs from the delivery"
            );
        }
        ("gap", Err(Error::ReplayGap(gap))) => assert!(
            gap.requested_after == after
                && gap.latest_available == latest
                && (gap.oldest_available == 0 || gap.oldest_available > after + 1),
            "frame {index}: gap {gap:?}"
        ),
        (expect, _) => panic!("frame {index}: the resume did not {expect}"),
    }
}

fn inventory(root: &Path, manifest: &Manifest) {
    let mut listed = BTreeSet::from(["manifest.json".to_owned()]);
    for entry in &manifest.cases {
        let case: Case = load(&root.join(&entry.path).join("case.json"));
        for name in [
            "case.json",
            &case.native,
            &case.expected_oap,
            &case.mapping,
            &case.omissions,
        ] {
            assert!(safe_relative(name) && !name.contains('/'), "{name:?}");
            listed.insert(format!("{}/{name}", entry.path));
        }
    }
    let mut found = BTreeSet::new();
    let mut pending = vec![root.to_path_buf()];
    while let Some(dir) = pending.pop() {
        for item in std::fs::read_dir(&dir).unwrap() {
            let path = item.unwrap().path();
            if path.is_dir() {
                pending.push(path);
            } else {
                let relative = path.strip_prefix(root).unwrap();
                let parts: Vec<_> = relative
                    .components()
                    .map(|part| part.as_os_str().to_string_lossy().into_owned())
                    .collect();
                found.insert(parts.join("/"));
            }
        }
    }
    let unlisted: Vec<_> = found.difference(&listed).collect();
    assert!(unlisted.is_empty(), "unlisted corpus files: {unlisted:?}");
}

fn source() -> &'static oap_harness::Source {
    pin()
        .sources
        .iter()
        .find(|source| source.component == "hermes-agent")
        .expect("the catalog pins the hermes-agent source")
}

#[test]
fn every_corpus_case_replays_to_the_outcome_go_reaches() {
    let root = root();
    let manifest: Manifest = load(&root.join("manifest.json"));
    let pinned = source();
    assert!(
        manifest.version == 1
            && manifest.adapter == "hermes-tui-gateway"
            && manifest.tag == pinned.tag
            && manifest.commit == pinned.commit
            && manifest.commit_tree == pinned.tree
            && manifest.sources == sources(),
        "corpus provenance pin mismatch"
    );
    let mut ids = HashSet::new();
    let mut paths = HashSet::new();
    let mut covered = HashSet::new();
    for entry in &manifest.cases {
        assert!(
            ids.insert(entry.id.clone())
                && paths.insert(entry.path.clone())
                && safe_relative(&entry.path)
                && !entry.ledger_fixtures.is_empty(),
            "invalid manifest entry {}",
            entry.id
        );
        for fixture in &entry.ledger_fixtures {
            assert!(
                LEDGER_FIXTURES.contains(&fixture.as_str()) && covered.insert(fixture.clone()),
                "invalid or duplicate ledger fixture {fixture:?}"
            );
        }
        let dir = root.join(&entry.path);
        let case: Case = load(&dir.join("case.json"));
        let provenance = &case.provenance;
        assert!(
            case.version == 1
                && case.id == entry.id
                && provenance.repository == REPOSITORY
                && provenance.tag == pinned.tag
                && provenance.commit == pinned.commit
                && provenance.commit_tree == pinned.tree
                && provenance.sources == sources()
                && !case.advertised_capabilities.is_empty()
                && !case.identity_map.is_empty(),
            "invalid case metadata {}",
            entry.id
        );
        let (frames, decoded) = frames(&file_in(&dir, &case.native));
        let mappings: Vec<Mapping> = load(&file_in(&dir, &case.mapping));
        let omissions: Vec<Omission> = load(&file_in(&dir, &case.omissions));
        classify(&frames, &decoded, &mappings, &omissions);
        check_actions(&case, &frames);
        if case.reopen {
            let expected = file_in(&dir, &case.expected_oap);
            replay_reopen(&entry.ledger_fixtures, &frames, &decoded, &expected);
            continue;
        }
        let envelopes = match case.server_mode.is_empty() {
            true => replay(&case, &frames, &decoded),
            false => replay_process(&case, &frames, &dir),
        };
        let expected = std::fs::read_to_string(file_in(&dir, &case.expected_oap)).unwrap();
        assert_eq!(
            go_marshal(&serde_json::to_string(&envelopes).unwrap()),
            match go_marshal(&expected).as_str() {
                "null" => "[]".to_owned(),
                want => want.to_owned(),
            },
            "{}",
            entry.id
        );
    }
    for fixture in LEDGER_FIXTURES {
        assert!(
            covered.contains(*fixture),
            "ledger fixture {fixture:?} has no case"
        );
    }
    inventory(&root, &manifest);
}
