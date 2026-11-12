#![cfg(unix)]

use std::future::Future;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::task::{Context, Poll};
use std::thread::JoinHandle;
use std::time::{Duration, Instant};

use futures::task::noop_waker;
use oap_adapter::{
    testkit, Adapter as _, Descriptor, Error, EventStream, InteractionResolution, OpenRequest,
    ResumeRequest, Session, SubmitRequest,
};
use oap_adapter_deepseek::adapter::{pin, Adapter, Config};
use oap_adapter_deepseek::native::{SERVER_NAME, SERVER_VERSION};
use oap_adapter_deepseek::process::ProcessConfig;
use oap_adapter_deepseek::runtime::Runtime;
use oap_protocol::{
    Envelope, Message, MessageContent, MessageRole, MessageSubmitRequest, MessageSubmitResponse,
    Participant, RequestedDeliveryMode, SessionStatus,
};
use serde_json::Value;

static NEXT: AtomicU64 = AtomicU64::new(0);

type Submitted = Result<(MessageSubmitResponse, EventStream), Error>;

fn wait<F: Future>(future: F) -> F::Output {
    let mut future = Box::pin(future);
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match future
            .as_mut()
            .poll(&mut Context::from_waker(&noop_waker()))
        {
            Poll::Ready(value) => return value,
            Poll::Pending => {
                assert!(Instant::now() < deadline, "corpus operation timed out");
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

fn until(what: &str, mut ready: impl FnMut() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(10);
    while !ready() {
        assert!(Instant::now() < deadline, "{what}");
        std::thread::sleep(Duration::from_millis(1));
    }
}

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&pin().corpus)
}

fn read(path: &Path) -> Value {
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn request(text: &str) -> SubmitRequest {
    SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text(text),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }
}

fn opening() -> OpenRequest {
    OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

fn compact(json: &str) -> String {
    let mut out = String::with_capacity(json.len());
    let (mut quoted, mut escaped) = (false, false);
    for character in json.chars() {
        if quoted {
            out.push(character);
            if escaped {
                escaped = false;
            } else if character == '\\' {
                escaped = true;
            } else if character == '"' {
                quoted = false;
            }
        } else if character == '"' {
            quoted = true;
            out.push(character);
        } else if !character.is_whitespace() {
            out.push(character);
        }
    }
    out
}

struct Case {
    id: String,
    source: PathBuf,
    definition: Value,
    frames: Vec<Value>,
    directory: PathBuf,
}

struct Replay {
    descriptor: Descriptor,
    session: Arc<dyn Session>,
    pending: Option<JoinHandle<Submitted>>,
    settled: Vec<(MessageSubmitResponse, EventStream)>,
    runs: Vec<Vec<Envelope>>,
    outcome: Outcome,
}

#[derive(Default)]
struct Outcome {
    envelopes: Vec<Envelope>,
    admitted: usize,
    refused: usize,
}

impl Replay {
    fn new(descriptor: Descriptor, session: Arc<dyn Session>) -> Self {
        Self {
            descriptor,
            session,
            pending: None,
            settled: Vec::new(),
            runs: Vec::new(),
            outcome: Outcome::default(),
        }
    }

    fn admit(&mut self) {
        match self.pending.take().map(|pending| pending.join().unwrap()) {
            Some(Ok(settled)) => self.settled.push(settled),
            Some(Err(_)) => self.outcome.refused += 1,
            None => {}
        }
    }

    fn record(&mut self) {
        self.admit();
        for (admission, mut stream) in std::mem::take(&mut self.settled) {
            let events = testkit::drain(&mut stream, Duration::from_secs(10));
            testkit::RunTrace {
                submitted: Some(request("hello").request),
                opened: Some(opening()),
                ..testkit::RunTrace::new(admission, self.descriptor.clone(), events.clone())
            }
            .assert_valid();
            self.outcome.admitted += 1;
            self.runs.push(events.clone());
            self.outcome.envelopes.extend(events);
        }
    }
}

impl Case {
    fn new(id: &str, path: &str) -> Self {
        let source = root().join(path);
        let definition = read(&source.join("case.json"));
        let native = source.join(definition["native"].as_str().unwrap());
        let frames = std::fs::read_to_string(&native)
            .unwrap()
            .lines()
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        let directory = std::env::temp_dir().join(format!(
            "oap-deepseek-corpus-{id}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&directory).unwrap();
        std::fs::copy(&native, directory.join("native.jsonl")).unwrap();
        std::fs::write(
            directory.join("runtime.py"),
            include_str!("support/corpus_runtime.py"),
        )
        .unwrap();
        Self {
            id: id.into(),
            source,
            definition,
            frames,
            directory,
        }
    }

    fn adapter(&self, mode: &str) -> Adapter {
        let python = Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(python.status.success());
        let ticks = AtomicI64::new(1);
        let ids = AtomicU64::new(0);
        Adapter::new(Config {
            process: ProcessConfig {
                path: String::from_utf8(python.stdout).unwrap().trim().into(),
                args: vec![
                    "-u".into(),
                    self.directory.join("runtime.py").to_string_lossy().into(),
                    self.directory.to_string_lossy().into(),
                    SERVER_NAME.into(),
                    SERVER_VERSION.to_string(),
                    mode.into(),
                ],
                dir: self.directory.to_string_lossy().into(),
                env: Some(Vec::new()),
                shutdown_timeout: Some(Duration::from_secs(1)),
                ..ProcessConfig::default()
            },
            provider: "fixture-provider".into(),
            model: match mode {
                "" => "deepseek-chat".into(),
                _ => "fixture-model".into(),
            },
            runtime: Runtime::new(
                move || ticks.fetch_add(1, Ordering::SeqCst),
                move |kind| {
                    let n = ids.fetch_add(1, Ordering::SeqCst) as u8;
                    format!("{kind}-{}", char::from(b'a' + n))
                },
            ),
            journal_capacity: 64,
            ..Config::default()
        })
        .unwrap()
    }

    fn prompts(&self) -> Vec<Value> {
        std::fs::read_to_string(self.directory.join("host.jsonl"))
            .unwrap_or_default()
            .lines()
            .map(|line| serde_json::from_str::<Value>(line).unwrap())
            .filter(|frame| frame["method"] == "session/prompt")
            .collect()
    }

    fn at(&self, index: usize) {
        let marker = self.directory.join(format!("at-{index}"));
        until(
            &format!("{}: runtime never reached frame {index}", self.id),
            || marker.exists(),
        );
    }

    fn go(&self, index: usize) {
        std::fs::write(self.directory.join(format!("go-{index}")), "").unwrap();
    }

    fn assert_capabilities(&self, descriptor: &Descriptor) {
        let features = serde_json::to_value(&descriptor.capabilities).unwrap()["features"].clone();
        for (feature, level) in self.definition["advertised_capabilities"]
            .as_object()
            .unwrap()
        {
            assert_eq!(
                &features[feature]["level"], level,
                "{}: advertised {feature}",
                self.id
            );
        }
    }

    fn wire(&self) -> Vec<String> {
        std::fs::read_to_string(
            self.source
                .join(self.definition["native"].as_str().unwrap()),
        )
        .unwrap()
        .lines()
        .filter_map(|line| {
            let frame: std::collections::HashMap<String, Box<serde_json::value::RawValue>> =
                serde_json::from_str(line).unwrap();
            (frame["direction"].get() == r#""host-to-dsh""#).then(|| {
                frame["raw"]
                    .get()
                    .replace("WORKSPACE_PATH", &self.directory.to_string_lossy())
            })
        })
        .collect()
    }

    fn served(&self) -> Outcome {
        let adapter = self.adapter(self.definition["server_mode"].as_str().unwrap());
        let descriptor = wait(adapter.probe()).unwrap();
        self.assert_capabilities(&descriptor);
        let mut replay: Option<Replay> = None;
        let mut outcome = Outcome::default();
        for (index, frame) in self.frames.iter().enumerate() {
            match frame["action"].as_str().unwrap() {
                "open" => {
                    let opened = wait(oap_adapter::Adapter::open(&adapter, opening()));
                    if self.definition["open_error"] == true {
                        assert!(opened.is_err(), "{}: the handshake succeeded", self.id);
                        continue;
                    }
                    replay = Some(Replay::new(descriptor.clone(), Arc::from(opened.unwrap())));
                }
                "submit" => {
                    let replay = replay.as_mut().expect("submit without an open session");
                    replay.record();
                    let session = replay.session.clone();
                    replay.pending = Some(std::thread::spawn(move || {
                        wait(session.submit(request("hello")))
                    }));
                }
                "shutdown" => {
                    let mut open = replay.take().expect("shutdown without an open session");
                    open.record();
                    wait(open.session.close()).unwrap();
                    outcome = open.outcome;
                }
                "auto" => {}
                action => panic!(
                    "{}: frame {}: unsupported action {action}",
                    self.id,
                    index + 1
                ),
            }
        }
        assert!(replay.is_none(), "{}: ended without a shutdown", self.id);
        let wanted = self.wire();
        until(&format!("{}: host wire incomplete", self.id), || {
            std::fs::read_to_string(self.directory.join("host.jsonl"))
                .unwrap_or_default()
                .lines()
                .count()
                >= wanted.len()
        });
        let written: Vec<String> = std::fs::read_to_string(self.directory.join("host.jsonl"))
            .unwrap()
            .lines()
            .map(str::to_owned)
            .collect();
        assert_eq!(written, wanted, "{}: host wire", self.id);
        outcome
    }

    fn framed(&self) -> Outcome {
        let mut checked = 0;
        for (index, frame) in self.frames.iter().enumerate() {
            if !frame["action"]
                .as_str()
                .unwrap()
                .starts_with("decode-error")
            {
                continue;
            }
            let adapter = self.adapter(&format!("framing:{index}"));
            let session = wait(oap_adapter::Adapter::open(&adapter, opening())).unwrap();
            until(
                &format!("{}: frame {} was accepted", self.id, index + 1),
                || wait(session.state()).err() == Some(Error::SessionClosed),
            );
            checked += 1;
        }
        assert!(checked > 0, "{}: no framing violations", self.id);
        Outcome::default()
    }

    fn replay(&self) -> Outcome {
        if self.definition.get("server_mode").is_some() {
            return self.served();
        }
        if self.definition["codec_only"] == true {
            return self.framed();
        }
        let adapter = self.adapter("");
        let descriptor = wait(adapter.probe()).unwrap();
        self.assert_capabilities(&descriptor);
        let session: Arc<dyn Session> =
            Arc::from(wait(oap_adapter::Adapter::open(&adapter, opening())).unwrap());
        let mut replay = Replay::new(descriptor, session);
        let mut submits = 0;
        for (index, frame) in self.frames.iter().enumerate() {
            let raw = &frame["raw"];
            match frame["action"].as_str().unwrap() {
                "submit" => {
                    replay.admit();
                    let session = replay.session.clone();
                    until(
                        &format!("{}: session busy at frame {}", self.id, index + 1),
                        || {
                            wait(session.state())
                                .map_or(true, |state| state.status == SessionStatus::IDLE)
                        },
                    );
                    submits += 1;
                    let session = replay.session.clone();
                    replay.pending = Some(std::thread::spawn(move || {
                        wait(session.submit(request("hello")))
                    }));
                    until(
                        &format!("{}: prompt {submits} not written", self.id),
                        || self.prompts().len() == submits,
                    );
                    assert_eq!(
                        self.prompts()[submits - 1]["params"],
                        raw["params"],
                        "{}: frame {} prompt params",
                        self.id,
                        index + 1
                    );
                }
                "overlap-submit" => {
                    self.at(index);
                    let refused = wait(replay.session.submit(request("hello again")));
                    assert_eq!(refused.err(), Some(Error::RunActive), "{}", self.id);
                    assert_eq!(self.prompts().len(), submits, "{}: overlap wrote", self.id);
                    self.go(index);
                }
                "wait-submit" => {
                    self.at(index);
                    replay.admit();
                    self.go(index);
                }
                "oap-control" => {
                    self.at(index);
                    self.control(&mut replay, raw);
                    self.go(index);
                }
                "observe" | "observe-invalid" | "reply" | "reply-error" | "process-exit" => {}
                action => panic!(
                    "{}: frame {}: unsupported action {action}",
                    self.id,
                    index + 1
                ),
            }
        }
        replay.record();
        assert_eq!(self.prompts().len(), submits, "{}: native prompts", self.id);
        assert!(
            !self.directory.join("unsupported").exists(),
            "{}: the runtime met an unsupported frame",
            self.id
        );
        let _ = wait(replay.session.close());
        replay.outcome
    }

    fn control(&self, replay: &mut Replay, control: &Value) {
        let session = replay.session.clone();
        match control["op"].as_str().unwrap() {
            "cancel" => {
                let refused = wait(session.cancel("run".into())).err();
                assert_eq!(
                    refused,
                    Some(Error::Harness(
                        "deepseek adapter: operation unavailable".into()
                    )),
                    "{}",
                    self.id
                );
            }
            "resolve" => {
                let refused = wait(session.resolve(InteractionResolution::default())).err();
                assert_eq!(refused, Some(Error::InteractionNotFound), "{}", self.id);
            }
            "resume" if control["expect"] == "run-not-found" => {
                let refused = wait(session.resume(ResumeRequest {
                    run_id: control["run_id"].as_str().unwrap().into(),
                    after_sequence: 0,
                }))
                .err();
                assert_eq!(refused, Some(Error::RunNotFound), "{}", self.id);
            }
            "resume" => {
                replay.record();
                let delivered = replay.runs.last().expect("a delivered run to replay");
                let (recovery, mut stream) = wait(session.resume(ResumeRequest {
                    run_id: delivered[0].run_id.clone().unwrap(),
                    after_sequence: 0,
                }))
                .unwrap();
                assert!(recovery.replay_gap.is_none(), "{}", self.id);
                let replayed = testkit::drain(&mut stream, Duration::from_secs(10));
                assert_eq!(
                    serde_json::to_string(&replayed).unwrap(),
                    serde_json::to_string(delivered).unwrap(),
                    "{}: replay differs from delivery",
                    self.id
                );
            }
            "assert-state" => {
                let status = control["status"].as_str().unwrap();
                let mut seen = String::new();
                until(
                    &format!("{}: state stayed {seen:?}, want {status}", self.id),
                    || {
                        seen = wait(session.state()).unwrap().status.as_str().to_owned();
                        seen == status
                    },
                );
            }
            op => panic!("{}: unsupported oap control {op}", self.id),
        }
    }

    fn expected(&self) -> Value {
        let name = self.definition["expected_oap"].as_str().unwrap();
        match read(&self.source.join(name)) {
            Value::Null => Value::Array(Vec::new()),
            trace => trace,
        }
    }

    fn expected_bytes(&self) -> String {
        let name = self.definition["expected_oap"].as_str().unwrap();
        match compact(&std::fs::read_to_string(self.source.join(name)).unwrap()).as_str() {
            "null" => "[]".into(),
            trace => trace.into(),
        }
    }

    fn exit_reason(&self) -> Option<&str> {
        self.frames
            .iter()
            .find(|frame| frame["action"] == "process-exit")
            .and_then(|frame| frame["raw"]["error"].as_str())
    }
}

impl Drop for Case {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.directory);
    }
}

#[test]
fn every_case_replays_its_recorded_trace() {
    let manifest = read(&root().join("manifest.json"));
    let mut replayed = 0;
    let mut mismatched = Vec::new();
    for entry in manifest["cases"].as_array().unwrap() {
        let case = Case::new(
            entry["id"].as_str().unwrap(),
            entry["path"].as_str().unwrap(),
        );
        replayed += 1;
        let outcome = case.replay();
        let submits = case
            .frames
            .iter()
            .filter(|frame| frame["action"] == "submit")
            .count();
        let started = case
            .expected()
            .as_array()
            .unwrap()
            .iter()
            .filter(|event| event["type"] == "run.started")
            .count();
        assert_eq!(
            (outcome.admitted, outcome.refused),
            (started, submits - started),
            "{}: admitted and refused submissions",
            case.id
        );
        let mut actual =
            oap_adapter::gojson::go_marshal(&serde_json::to_string(&outcome.envelopes).unwrap());
        if let Some(reason) = case.exit_reason() {
            actual = actual.replace(
                r#""message":"EOF""#,
                &format!(r#""message":{}"#, serde_json::to_string(reason).unwrap()),
            );
        }
        let wanted = case.expected_bytes();
        if actual != wanted {
            mismatched.push(format!("{}\n want: {wanted}\n  got: {actual}", case.id));
        }
    }
    assert_eq!(replayed, 17);
    assert!(mismatched.is_empty(), "{}", mismatched.join("\n"));
}
