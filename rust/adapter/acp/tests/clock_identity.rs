use std::path::Path;
use std::sync::atomic::{AtomicI64, Ordering};

use futures::{executor::block_on, StreamExt};
use oap_adapter_acp::{Runtime, TextRun, TextRunContext};
use serde_json::Value;

fn corpus_file(name: &str) -> std::path::PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&oap_adapter_acp::pin().corpus)
        .join("cases/new-prompt-completed")
        .join(name)
}

fn expected() -> Vec<Value> {
    serde_json::from_slice(&std::fs::read(corpus_file("expected-oap.json")).unwrap()).unwrap()
}

#[test]
fn the_recorded_start_payload_and_envelope_have_distinct_clock_reads() {
    let expected = expected();
    let event = &expected[0];
    let ticks = AtomicI64::new(3);
    let mut run = TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native-session".into(),
            run_id: "run-b".into(),
            message_id: "session/message-c".into(),
            capability_revision: Some(event["capability_revision"].as_str().unwrap().into()),
        },
        |_| "event-d".into(),
    )
    .with_runtime(Runtime::new(
        move || ticks.fetch_add(1, Ordering::SeqCst),
        |_| "event-d".into(),
    ));
    let mut stream = run.start(3).unwrap();
    let actual = block_on(stream.next()).unwrap().unwrap();
    assert_eq!(serde_json::to_value(actual).unwrap(), *event);
}

static NEXT_FIXTURE: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);

struct Fixture(std::path::PathBuf);

impl Fixture {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "oap-acp-clock-{}-{}",
            std::process::id(),
            NEXT_FIXTURE.fetch_add(1, Ordering::SeqCst)
        ));
        std::fs::create_dir(&path).unwrap();
        std::fs::write(
            path.join("agent.py"),
            r#"import json, os, sys
from pathlib import Path
root = Path(sys.argv[1])
frames = [json.loads(line)['raw'] for line in Path(sys.argv[2]).read_text().splitlines()]
(root / 'pid').write_text(str(os.getpid()))
for line in sys.stdin:
    request = json.loads(line)
    with (root / 'wire').open('a') as wire:
        wire.write(line)
    method = request.get('method')
    if method == 'initialize':
        replies = frames[:1]
    elif method == 'session/new':
        replies = frames[1:2]
    elif method == 'session/prompt':
        replies = frames[2:]
    else:
        continue
    for reply in replies:
        print(json.dumps(reply), flush=True)
"#,
        )
        .unwrap();
        Self(path)
    }

    fn config(&self, runtime: Option<Runtime>) -> oap_adapter_acp::Config {
        let python = std::process::Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(python.status.success());
        let corpus = corpus_file("native.jsonl");
        oap_adapter_acp::Config {
            runtime,
            process: oap_adapter_acp::process::ProcessConfig {
                path: String::from_utf8(python.stdout).unwrap().trim().into(),
                dir: self.0.to_string_lossy().into(),
                args: vec![
                    "-u".into(),
                    self.0.join("agent.py").to_string_lossy().into(),
                    self.0.to_string_lossy().into(),
                    corpus.to_string_lossy().into(),
                ],
                env: Some(Vec::new()),
                ..Default::default()
            },
            ..Default::default()
        }
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

#[test]
fn the_native_session_preserves_recorded_clock_and_identity_order() {
    use oap_adapter::{Adapter as _, OpenRequest, SubmitRequest};
    use oap_protocol::{Message, MessageContent, MessageRole, MessageSubmitRequest, Participant};
    let fixture = Fixture::new();
    let ticks = AtomicI64::new(1);
    let ids = std::sync::atomic::AtomicU64::new(0);
    let runtime = Runtime::new(
        move || ticks.fetch_add(1, Ordering::SeqCst),
        move |kind| {
            format!(
                "{kind}-{}",
                char::from(b'a' + ids.fetch_add(1, Ordering::SeqCst) as u8)
            )
        },
    );
    let adapter = oap_adapter_acp::Adapter::new(fixture.config(Some(runtime))).unwrap();
    let revision = adapter.describe().capability_revision;
    let session = block_on(adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "caller".into(),
            ..Default::default()
        },
        ..Default::default()
    }))
    .unwrap();
    assert_eq!(block_on(session.state()).unwrap().updated_at_ms, 1);
    let (response, mut stream) = block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("hello"),
                ..Default::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..Default::default()
        },
        ..Default::default()
    }))
    .unwrap();
    assert_eq!(response.run_id.unwrap().as_str(), "run-b");
    assert_eq!(response.submission_id.as_str(), "submission-e");
    assert_eq!(response.message_ids[0].as_str(), "message-a");
    let actual = block_on(async {
        let mut events = Vec::new();
        while let Some(event) = stream.next().await {
            events.push(serde_json::to_value(event.unwrap()).unwrap());
        }
        events
    });
    let expected = expected();
    assert_eq!(actual.len(), expected.len());
    for (actual, expected) in actual.iter().zip(&expected) {
        for field in [
            "type",
            "id",
            "payload",
            "sequence",
            "timestamp_ms",
            "session_id",
            "run_id",
        ] {
            assert_eq!(actual[field], expected[field], "{field}: {actual}");
        }
        assert_eq!(actual["capability_revision"], revision);
    }
    assert_eq!(block_on(session.state()).unwrap().updated_at_ms, 6);
    block_on(session.close()).unwrap();
    let wire = std::fs::read_to_string(fixture.0.join("wire")).unwrap();
    let requests: Vec<Value> = wire
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(
        requests
            .iter()
            .filter_map(|r| r["id"].as_u64())
            .collect::<Vec<_>>(),
        [1, 2, 3]
    );
    let pid = std::fs::read_to_string(fixture.0.join("pid")).unwrap();
    assert!(!std::process::Command::new("/bin/kill")
        .args(["-0", &pid])
        .output()
        .unwrap()
        .status
        .success());
}

#[test]
fn generated_session_ids_share_one_source_per_adapter() {
    use oap_adapter::{Adapter as _, OpenRequest};
    use oap_protocol::Participant;
    let ids = std::sync::atomic::AtomicU64::new(0);
    for (runtime, prefix) in [
        (None, "session"),
        (
            Some(Runtime::new(
                || 123,
                move |kind| format!("injected-{kind}-{}", ids.fetch_add(1, Ordering::SeqCst) + 1),
            )),
            "injected-session",
        ),
    ] {
        let fixture = Fixture::new();
        let adapter = oap_adapter_acp::Adapter::new(fixture.config(runtime)).unwrap();
        for number in 1..=2 {
            let session = block_on(adapter.open(OpenRequest {
                participant: Participant {
                    id: "caller".into(),
                    ..Default::default()
                },
                ..Default::default()
            }))
            .unwrap();
            let state = block_on(session.state()).unwrap();
            assert_eq!(state.session_id.as_str(), format!("{prefix}-{number}"));
            assert!(state.updated_at_ms > 0);
            block_on(session.close()).unwrap();
        }
    }
}
