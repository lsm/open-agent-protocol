use std::future::Future;
use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::{task::noop_waker, StreamExt};
use oap_adapter::{Adapter as _, OpenRequest, ResumeRequest, SubmitRequest};
use oap_adapter_acp::{pin, process::ProcessConfig, Adapter, Config};
use oap_protocol::{Message, MessageContent, MessageRole, MessageSubmitRequest, Participant};
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
                assert!(
                    Instant::now() < deadline,
                    "public corpus operation timed out"
                );
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

fn corpus_case(id: &str) -> (PathBuf, Value) {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&pin().corpus);
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let entry = manifest["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|entry| entry["id"] == id)
        .unwrap();
    let directory = root.join(entry["path"].as_str().unwrap());
    let definition =
        serde_json::from_slice(&std::fs::read(directory.join("case.json")).unwrap()).unwrap();
    (directory, definition)
}

struct Fixture {
    directory: PathBuf,
    retained: bool,
    environment: Vec<String>,
}

impl Fixture {
    fn new(id: &str) -> Self {
        let retained = std::env::var_os("OAP_ACP_CORPUS_EVIDENCE").map(PathBuf::from);
        let base = retained.clone().unwrap_or_else(std::env::temp_dir);
        let directory = base.join(format!(
            "{id}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&directory).unwrap();
        let (source, definition) = corpus_case(id);
        std::fs::copy(
            source.join(definition["native"].as_str().unwrap()),
            directory.join("native.jsonl"),
        )
        .unwrap();
        std::fs::write(
            directory.join("agent.py"),
            r#"import json, os, sys
from pathlib import Path
root = Path(sys.argv[1])
(root / 'pid').write_text(str(os.getpid()))
frames = [json.loads(line) for line in (root / 'native.jsonl').read_text().splitlines()]
for line in sys.stdin:
    request = json.loads(line)
    with (root / 'host.jsonl').open('a') as wire:
        wire.write(line)
    method = request.get('method')
    if method == 'initialize':
        selected = frames[:1]
    elif method == 'session/new':
        selected = frames[1:2]
    elif method == 'session/prompt':
        selected = frames[2:]
    else:
        continue
    for frame in selected:
        raw = frame['raw']
        if 'result' in raw or 'error' in raw:
            assert raw['id'] == request['id'], (raw, request)
        with (root / 'agent.jsonl').open('a') as wire:
            wire.write(json.dumps(raw) + '\n')
        print(json.dumps(raw), flush=True)
"#,
        )
        .unwrap();
        let environment = definition
            .get("environment")
            .cloned()
            .map(serde_json::from_value)
            .transpose()
            .unwrap()
            .unwrap_or_default();
        Self {
            directory,
            retained: retained.is_some(),
            environment,
        }
    }

    fn adapter(&self) -> Adapter {
        let selected = Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(selected.status.success());
        Adapter::new(Config {
            process: ProcessConfig {
                path: String::from_utf8(selected.stdout).unwrap().trim().into(),
                args: vec![
                    "-u".into(),
                    self.directory.join("agent.py").to_string_lossy().into(),
                    self.directory.to_string_lossy().into(),
                ],
                dir: "/".into(),
                env: Some(self.environment.clone()),
                shutdown_timeout: Some(Duration::from_secs(1)),
                ..ProcessConfig::default()
            },
            ..Config::default()
        })
        .unwrap()
    }

    fn reaped(&self) {
        let path = self.directory.join("pid");
        if path.exists() {
            let pid = std::fs::read_to_string(path).unwrap();
            assert!(
                !Command::new("/bin/kill")
                    .args(["-0", pid.trim()])
                    .output()
                    .unwrap()
                    .status
                    .success(),
                "native corpus child survived close"
            );
        }
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        if !self.retained {
            std::fs::remove_dir_all(&self.directory).unwrap();
        }
    }
}

fn request() -> OpenRequest {
    OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

#[test]
fn the_captured_replay_case_resumes_the_actual_completed_public_run() {
    let fixture = Fixture::new("replay-degradation");
    let session = wait(fixture.adapter().open(request())).unwrap();
    let (admission, mut stream) = wait(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("hello"),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap();
    let mut events = Vec::new();
    while let Some(event) = wait(stream.next()) {
        events.push(event.unwrap());
    }
    assert_eq!(events.len(), 3);
    let (_, definition) = corpus_case("replay-degradation");
    let after = definition["replay_after"].as_u64().unwrap();
    let result = wait(session.resume(ResumeRequest {
        run_id: admission.run_id.unwrap(),
        after_sequence: after,
    }));
    let observed = match result {
        Ok((recovery, mut replay)) => {
            let mut suffix = Vec::new();
            while let Some(event) = wait(replay.next()) {
                suffix.push(event.unwrap());
            }
            assert_eq!(recovery.replayed_from, after + 1);
            assert_eq!(recovery.replayed_through, events.len() as u64);
            assert_eq!(
                serde_json::to_value(suffix).unwrap(),
                serde_json::to_value(&events[after as usize..]).unwrap()
            );
            None
        }
        Err(error) => Some(format!("{error:?}")),
    };
    wait(session.close()).unwrap();
    fixture.reaped();
    std::fs::write(
        fixture.directory.join("events.json"),
        serde_json::to_vec_pretty(&events).unwrap(),
    )
    .unwrap();
    assert_eq!(observed, None, "captured public replay was refused");
}
