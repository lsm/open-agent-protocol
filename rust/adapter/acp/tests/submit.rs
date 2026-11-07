use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::task::{Context, Poll};
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
(out / 'python').write_text(sys.executable)
for line in sys.stdin:
    request = json.loads(line)
    with (out / 'wire').open('a') as wire:
        wire.write(line)
    method = request.get('method')
    if method == 'initialize':
        result = {'protocolVersion': 1, 'agentCapabilities': {}}
    elif method == 'session/new':
        result = {'sessionId': 'native', 'configOptions': [{'id':'model','category':'model','type':'select','currentValue':'sol','options':[{'value':'sol','name':'Sol'},{'value':'luna','name':'Luna'}]}]}
    elif method == 'session/prompt':
        (out / 'prompt').write_text('seen')
        if mode == 'switch-quiet':
            continue
        if mode == 'quiet':
            while True:
                time.sleep(1)
        for text in ['hello ', '世界']:
            print(json.dumps({'jsonrpc':'2.0','method':'session/update','params':{'sessionId':'native','update':{'sessionUpdate':'agent_message_chunk','content':{'type':'text','text':text}}}}), flush=True)
        result = {'stopReason':'end_turn'}
    elif method == 'session/set_config_option':
        result = {'configOptions':[{'id':'model','type':'select','currentValue':request['params']['value']}]}
    else:
        continue
    print(json.dumps({'jsonrpc':'2.0','id':request['id'],'result':result}), flush=True)
    if method == 'session/new' and mode in ['blocked', 'blocked-abort']:
        (out / 'ready').write_text('quiet')
        while True:
            time.sleep(1)
"#).unwrap();
        Self(path)
    }

    fn config(&self, mode: &str) -> Config {
        let selected = Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(selected.status.success());
        let path = String::from_utf8(selected.stdout).unwrap();
        Config {
            process: ProcessConfig {
                path: path.trim().into(),
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
            abort_active_on_close: mode == "blocked-abort",
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

#[test]
fn model_switch_snapshot_preserves_the_active_text_run() {
    let fixture = Fixture::new();
    let session = fixture.open("switch-quiet");
    let (admission, stream) = block_on(session.submit(submit("hi"))).unwrap();
    fixture.wait("prompt");
    let (_, state) = block_on(session.model_switcher().unwrap().switch_model(
        oap_protocol::SessionModelSwitchRequest {
            session_id: "session".into(),
            model_id: "luna".into(),
            allow_degraded_features: vec!["session.model.switch".into()],
        },
    ))
    .unwrap();
    assert_eq!(state.current_model_id, "luna");
    assert_eq!(state.status, SessionStatus::RUNNING);
    assert_eq!(state.active_run_id, admission.run_id);
    assert_eq!(block_on(session.state()).unwrap().current_model_id, "luna");
    drop(session);
    let events = block_on(stream.collect::<Vec<_>>());
    assert_eq!(events.len(), 1);
    fixture.reaped();
}

#[test]
fn invalid_scope_role_content_and_delivery_preserve_idle_session() {
    let fixture = Fixture::new();
    let session = fixture.open("complete");
    let mut wrong_scope = submit("hi");
    wrong_scope.request.session_id = "other".into();
    assert!(matches!(
        block_on(session.submit(wrong_scope)),
        Err(Error::RunNotFound)
    ));
    let mut role = submit("hi");
    role.request.messages[0].role = MessageRole::ASSISTANT;
    assert!(matches!(
        block_on(session.submit(role)),
        Err(Error::UnsupportedInput)
    ));
    let mut parts = submit("hi");
    parts.request.messages[0].content = MessageContent::from_parts(&[]);
    assert!(matches!(
        block_on(session.submit(parts)),
        Err(Error::UnsupportedInput)
    ));
    let mut delivery = submit("hi");
    delivery.request.delivery = "future".into();
    assert!(matches!(
        block_on(session.submit(delivery)),
        Err(Error::InvalidSubmission)
    ));
    assert_eq!(
        block_on(session.state()).unwrap().status,
        SessionStatus::IDLE
    );
    let lines = std::fs::read_to_string(fixture.0.join("wire")).unwrap();
    assert_eq!(lines.lines().count(), 2);
    block_on(session.close()).unwrap();
    fixture.reaped();
}

#[test]
fn fixture_uses_the_python_interpreter_selected_from_path() {
    let fixture = Fixture::new();
    let session = fixture.open("complete");
    let selected = Command::new("python3")
        .args(["-c", "import sys; print(sys.executable)"])
        .output()
        .unwrap();
    assert!(selected.status.success());
    let selected = String::from_utf8(selected.stdout).unwrap();
    let observed = std::fs::read_to_string(fixture.0.join("python")).unwrap();
    assert_eq!(observed, selected.trim());
    block_on(session.close()).unwrap();
    fixture.reaped();
}

#[test]
fn default_close_refuses_an_active_run_during_blocked_write_admission() {
    let fixture = Fixture::new();
    let session = fixture.open("blocked");
    fixture.wait("ready");
    let mut submit = session.submit(submit(&"x".repeat(4 * 1024 * 1024)));
    let waker = noop_waker();
    let mut context = Context::from_waker(&waker);
    assert!(submit.as_mut().poll(&mut context).is_pending());
    assert_eq!(
        block_on(session.state()).unwrap().status,
        SessionStatus::RUNNING
    );
    let mut close = session.close();
    assert!(matches!(
        close.as_mut().poll(&mut context),
        Poll::Ready(Err(Error::RunActive))
    ));
    drop(close);
    drop(submit);
    drop(session);
    fixture.reaped();
}

#[test]
fn opt_in_close_settles_blocked_submission_as_session_closed() {
    let fixture = Fixture::new();
    let session = fixture.open("blocked-abort");
    fixture.wait("ready");
    let mut submission = session.submit(submit(&"x".repeat(4 * 1024 * 1024)));
    let waker = noop_waker();
    assert!(submission
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    let _ = block_on(session.close());
    let error = block_on(submission)
        .err()
        .expect("closed admission was accepted");
    assert!(matches!(error, Error::SessionClosed), "{error:?}");
    drop(session);
    fixture.reaped();
}
