use std::future::Future;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::{task::noop_waker, StreamExt};
use oap_adapter::{Adapter as _, InteractionResolution, OpenRequest, Session, SubmitRequest};
use oap_adapter_acp::{pin, process::ProcessConfig, Adapter, Config, Runtime};
use oap_protocol::{
    Envelope, Message, MessageContent, MessageRole, MessageSubmitRequest, Participant,
    PermissionResolveRequest, RequestedDeliveryMode,
};
use serde_json::Value;

static NEXT: AtomicU64 = AtomicU64::new(0);

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

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&pin().corpus)
}

fn read(path: &Path) -> Value {
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
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
            "oap-acp-corpus-{id}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&directory).unwrap();
        std::fs::copy(&native, directory.join("native.jsonl")).unwrap();
        std::fs::write(
            directory.join("agent.py"),
            include_str!("support/corpus_agent.py"),
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

    fn has(&self, action: &str) -> bool {
        self.frames.iter().any(|frame| frame["action"] == action)
    }

    fn adapter(&self) -> Adapter {
        let python = Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(python.status.success());
        let ticks = AtomicI64::new(1);
        let ids = AtomicU64::new(0);
        let environment = self
            .definition
            .get("environment")
            .cloned()
            .map(serde_json::from_value)
            .transpose()
            .unwrap()
            .unwrap_or_default();
        Adapter::new(Config {
            runtime: Some(Runtime::new(
                move || ticks.fetch_add(1, Ordering::SeqCst),
                move |kind| {
                    let n = ids.fetch_add(1, Ordering::SeqCst) as u8;
                    format!("{kind}-{}", char::from(b'a' + n))
                },
            )),
            journal_capacity: self.definition["journal_capacity"].as_u64().unwrap_or(64) as usize,
            process: ProcessConfig {
                path: String::from_utf8(python.stdout).unwrap().trim().into(),
                args: vec![
                    "-u".into(),
                    self.directory.join("agent.py").to_string_lossy().into(),
                    self.directory.to_string_lossy().into(),
                ],
                dir: "/".into(),
                env: Some(environment),
                shutdown_timeout: Some(Duration::from_secs(1)),
                ..ProcessConfig::default()
            },
            ..Config::default()
        })
        .unwrap()
    }

    fn replay(&self) -> Vec<Envelope> {
        let open = OpenRequest {
            session_id: "session".into(),
            participant: Participant {
                id: "user".into(),
                ..Participant::default()
            },
            tool_sources: self
                .definition
                .get("tool_sources")
                .cloned()
                .map(serde_json::from_value)
                .transpose()
                .unwrap()
                .unwrap_or_default(),
            ..OpenRequest::default()
        };
        let session = wait(self.adapter().open(open)).unwrap();
        let (admission, mut stream) = wait(session.submit(SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "session".into(),
                delivery: RequestedDeliveryMode::AUTO,
                messages: vec![Message {
                    role: MessageRole::USER,
                    content: MessageContent::from_text("hello"),
                    ..Message::default()
                }],
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        }))
        .unwrap();
        let run_id = admission.run_id.clone().unwrap();
        let mut events = Vec::new();
        while let Some(event) = wait(stream.next()) {
            let event = event.unwrap();
            if events.is_empty() && self.has("cancel") {
                wait(session.cancel(run_id.clone())).unwrap();
            }
            if event.envelope_type.as_str() == "action.permission.requested" {
                self.resolve(&*session, &event, &admission.session_id, &run_id);
            }
            events.push(event);
        }
        if self.has("complete-await") {
            std::fs::write(self.directory.join("drained"), "").unwrap();
            let deadline = Instant::now() + Duration::from_secs(10);
            while !self.directory.join("replayed").exists() {
                assert!(Instant::now() < deadline, "agent did not finish");
                std::thread::sleep(Duration::from_millis(1));
            }
        }
        wait(session.close()).unwrap();
        events
    }

    fn resolve(
        &self,
        session: &dyn Session,
        event: &Envelope,
        session_id: &oap_protocol::SessionId,
        run_id: &oap_protocol::RunId,
    ) {
        let frame = self
            .frames
            .iter()
            .find(|frame| frame["action"] == "permission")
            .unwrap();
        let payload: Value = serde_json::from_str(event.payload.as_ref().unwrap().get()).unwrap();
        wait(session.resolve(InteractionResolution {
            run_id: run_id.clone(),
            responded_by: "user".into(),
            permission: Some(PermissionResolveRequest {
                interaction_id: payload["interaction_id"].as_str().unwrap().into(),
                requested_by: payload["requested_by"].as_str().unwrap().into(),
                responded_by: "user".into(),
                session_id: session_id.clone(),
                run_id: run_id.clone(),
                choice_id: frame["choice_id"].as_str().unwrap_or_default().into(),
                granted: frame["granted"].as_bool().unwrap(),
                ..PermissionResolveRequest::default()
            }),
            input: None,
        }))
        .unwrap();
    }

    fn expected(&self) -> String {
        let name = self.definition["expected_oap"].as_str().unwrap();
        compact(&std::fs::read_to_string(self.source.join(name)).unwrap())
    }
}

impl Drop for Case {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.directory);
    }
}

#[test]
fn every_prompted_case_replays_its_recorded_trace_byte_for_byte() {
    let manifest = read(&root().join("manifest.json"));
    let mut mismatched = Vec::new();
    for entry in manifest["cases"].as_array().unwrap() {
        let id = entry["id"].as_str().unwrap();
        if id == "session-reopen" {
            continue;
        }
        let case = Case::new(id, entry["path"].as_str().unwrap());
        let events = case.replay();
        let mut actual = oap_adapter::gojson::go_marshal(&serde_json::to_string(&events).unwrap());
        if case.has("process-exit") {
            actual = actual.replace(
                r#""message":"EOF""#,
                r#""message":"fixture process exited""#,
            );
        }
        if actual != case.expected() {
            mismatched.push(format!(
                "{}\n want: {}\n  got: {actual}",
                case.id,
                case.expected()
            ));
        }
    }
    assert!(mismatched.is_empty(), "{}", mismatched.join("\n"));
}
