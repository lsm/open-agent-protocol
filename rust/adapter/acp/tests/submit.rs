use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::task::Context;
use std::time::{Duration, Instant};

use futures::{executor::block_on, task::noop_waker, StreamExt};
use oap_adapter::{Adapter as _, Error, OpenRequest, Session, SubmitRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, Participant, SessionStatus,
};
use serde_json::{json, Value};

static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture(PathBuf);

impl Fixture {
    fn new() -> Self {
        let path = std::env::temp_dir().join(format!(
            "oap-acp-submit-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir(&path).unwrap();
        std::fs::write(path.join("agent.py"), r#"import json, os, sys, time
from pathlib import Path
out = Path(sys.argv[1])
mode = sys.argv[2]
(out / 'pid').write_text(str(os.getpid()))
for line in sys.stdin:
    request = json.loads(line)
    with (out / 'wire').open('a') as wire:
        wire.write(line)
    method = request.get('method')
    if method == 'initialize':
        result = {'protocolVersion': 1, 'agentCapabilities': {}}
    elif method == 'session/new':
        result = {'sessionId': 'native'}
    elif method == 'session/prompt':
        (out / 'prompt').write_text('seen')
        if mode == 'quiet':
            while True:
                time.sleep(1)
        for text in ['hello ', '世界']:
            print(json.dumps({'jsonrpc':'2.0','method':'session/update','params':{'sessionId':'native','update':{'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':text}}}}), flush=True)
        result = {'stopReason':'end_turn'}
    else:
        continue
    print(json.dumps({'jsonrpc':'2.0','id':request['id'],'result':result}), flush=True)
    if method == 'session/new' and mode == 'blocked':
        (out / 'ready').write_text('quiet')
        while True:
            time.sleep(1)
"#).unwrap();
        Self(path)
    }

    fn config(&self, mode: &str) -> Config {
        Config {
            process: ProcessConfig {
                path: "/usr/bin/python3".into(),
                args: vec![
                    "-u".into(),
                    self.0.join("agent.py").to_string_lossy().into(),
                    self.0.to_string_lossy().into(),
                    mode.into(),
                ],
                dir: "/".into(),
                env: Some(Vec::new()),
                queue_capacity: 1,
                shutdown_timeout: Some(Duration::from_millis(100)),
                ..ProcessConfig::default()
            },
            ..Config::default()
        }
    }

    fn open(&self, mode: &str) -> Box<dyn Session> {
        block_on(Adapter::new(self.config(mode)).unwrap().open(OpenRequest {
            session_id: "session".into(),
            participant: Participant {
                id: "user".into(),
                ..Participant::default()
            },
            ..OpenRequest::default()
        }))
        .unwrap()
    }

    fn wait(&self, name: &str) {
        let until = Instant::now() + Duration::from_secs(5);
        while !self.0.join(name).exists() && Instant::now() < until {
            std::thread::sleep(Duration::from_millis(5));
        }
        assert!(self.0.join(name).exists(), "native never reached {name}");
    }

    fn reaped(&self) {
        let pid = std::fs::read_to_string(self.0.join("pid")).unwrap();
        assert!(
            !Command::new("/bin/kill")
                .args(["-0", &pid])
                .output()
                .unwrap()
                .status
                .success(),
            "native {pid} survived cleanup"
        );
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn submit(text: &str) -> SubmitRequest {
    SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text(text),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }
}

#[test]
fn normal_submit_orders_admission_text_completion_and_next_turn() {
    let fixture = Fixture::new();
    let session = fixture.open("complete");
    let mut previous = None;
    for _ in 0..2 {
        let (admission, stream) = block_on(session.submit(submit("hi"))).unwrap();
        assert!(admission.accepted);
        assert_eq!(admission.requested_delivery.as_str(), "auto");
        assert_eq!(admission.effective_delivery.as_str(), "start");
        assert_ne!(admission.run_id, previous);
        let events = block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .map(Result::unwrap)
            .collect::<Vec<_>>();
        let values = events
            .iter()
            .map(|event| serde_json::to_value(event).unwrap())
            .collect::<Vec<_>>();
        assert_eq!(
            values
                .iter()
                .map(|event| event["type"].as_str().unwrap())
                .collect::<Vec<_>>(),
            [
                "run.started",
                "content.delta",
                "content.delta",
                "run.completed"
            ]
        );
        assert_eq!(
            values.last().unwrap()["payload"]["final_response"]["content"],
            "hello 世界"
        );
        for (index, event) in events.iter().enumerate() {
            assert_eq!(event.sequence, Some(index as u64 + 1));
            assert_eq!(event.run_id, admission.run_id);
        }
        let until = Instant::now() + Duration::from_secs(5);
        loop {
            let state = block_on(session.state()).unwrap();
            if state.status == SessionStatus::IDLE {
                assert_eq!(state.transcript_cursor, "4");
                break;
            }
            assert!(Instant::now() < until);
            std::thread::yield_now();
        }
        previous = admission.run_id;
    }
    block_on(session.close()).unwrap();
    fixture.reaped();
}

#[test]
fn active_run_refuses_second_submit_and_default_close_then_drop_reaps() {
    let fixture = Fixture::new();
    let session = fixture.open("quiet");
    let (_, mut stream) = block_on(session.submit(submit("hi"))).unwrap();
    fixture.wait("prompt");
    assert_eq!(
        block_on(stream.next())
            .unwrap()
            .unwrap()
            .envelope_type
            .as_str(),
        "run.started"
    );
    assert!(matches!(
        block_on(session.submit(submit("again"))),
        Err(Error::RunActive)
    ));
    assert!(matches!(block_on(session.close()), Err(Error::RunActive)));
    drop(session);
    assert!(block_on(stream.next()).is_none());
    fixture.reaped();
}

#[test]
fn dropped_write_admission_future_reaps_a_quiet_native_reader() {
    let fixture = Fixture::new();
    let session = fixture.open("blocked");
    fixture.wait("ready");
    let request = submit(&"x".repeat(4 * 1024 * 1024));
    let mut admission = session.submit(request);
    let waker = noop_waker();
    assert!(admission
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    drop(admission);
    drop(session);
    fixture.reaped();
}

#[test]
fn submission_controls_and_staged_opt_in_are_explicit() {
    let fixture = Fixture::new();
    let session = fixture.open("complete");
    let mut request = submit("hi");
    request.request.allow_degraded_features.clear();
    assert!(matches!(
        block_on(session.submit(request)),
        Err(Error::DegradedControl(_))
    ));
    let mut request = submit("hi");
    request.request.instructions = Some(String::new());
    assert!(matches!(
        block_on(session.submit(request)),
        Err(Error::UnsupportedControl(_))
    ));
    let wire = std::fs::read_to_string(fixture.0.join("wire")).unwrap();
    let requests = wire
        .lines()
        .map(|line| serde_json::from_str::<Value>(line).unwrap())
        .collect::<Vec<_>>();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[1]["method"], json!("session/new"));
    block_on(session.close()).unwrap();
    fixture.reaped();
}
