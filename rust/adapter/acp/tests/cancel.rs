use std::future::Future;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::task::Context;
use std::time::Duration;

use futures::{executor::block_on, task::noop_waker, StreamExt};
use oap_adapter::{Adapter as _, Error, OpenRequest, SubmitRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{Message, MessageContent, MessageSubmitRequest, Participant};
use serde_json::{json, Value};

static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture(PathBuf);

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

impl Fixture {
    fn open(mode: &str) -> (Self, Box<dyn oap_adapter::Session>) {
        let fixture = Self(std::env::temp_dir().join(format!(
            "oap-acp-cancel-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        )));
        std::fs::create_dir(&fixture.0).unwrap();
        std::fs::write(fixture.0.join("agent.py"), r#"import json,os,sys,time
from pathlib import Path
out=Path(sys.argv[1])
mode=sys.argv[2]
(out/'pid').write_text(str(os.getpid()))
def read():
 line=sys.stdin.readline()
 if line:
  with (out/'wire.jsonl').open('a') as wire: wire.write(line)
 return json.loads(line) if line else None
def send(value): print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
def wait(name):
 deadline=time.monotonic()+10
 while not (out/name).exists() and time.monotonic()<deadline: time.sleep(.001)
 assert (out/name).exists(),name
def chunk(text): send(dict(method='session/update',params=dict(sessionId='native',update=dict(sessionUpdate='agent_message_chunk',content=dict(type='text',text=text)))))
def ask(name): send(dict(id=name,method='session/request_permission',params=dict(sessionId='native',toolCall=dict(toolCallId=name,title='Act'),options=[dict(optionId='allow',name='Allow',kind='allow_once')])))
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
request=read()
send(dict(id=request['id'],result=dict(sessionId='native')))
prompt=read()
if prompt is not None:
 chunk('before')
 if mode=='auto':
  send(dict(id=prompt['id'],result=dict(stopReason='end_turn')))
 else:
  if mode=='gates': ask('initial')
  cancel=read()
  assert cancel==dict(jsonrpc='2.0',method='session/cancel',params=dict(sessionId='native')),cancel
  (out/'cancel').write_text(json.dumps(cancel))
  if mode=='gates':
   (out/'gate-answer.json').write_text(json.dumps(read()))
   ask('late')
   (out/'late-answer.json').write_text(json.dumps(read()))
  wait('stream')
  chunk('after')
  wait('finish')
  if mode=='remote': send(dict(id=prompt['id'],error=dict(code=-32800,message='cancel acknowledged')))
  else: send(dict(id=prompt['id'],result=dict(stopReason='end_turn' if mode=='complete' else 'cancelled',usage=dict(inputTokens=3,outputTokens=1,totalTokens=4))))
 while read() is not None: pass
"#).unwrap();
        let python = std::process::Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(python.status.success());
        let adapter = Adapter::new(Config {
            process: ProcessConfig {
                path: String::from_utf8(python.stdout).unwrap().trim().into(),
                args: vec![
                    "-u".into(),
                    fixture.0.join("agent.py").to_string_lossy().into(),
                    fixture.0.to_string_lossy().into(),
                    mode.into(),
                ],
                dir: "/".into(),
                env: Some(Vec::new()),
                queue_capacity: 1,
                write_queue_capacity: 1,
                shutdown_timeout: Some(Duration::from_secs(1)),
                ..ProcessConfig::default()
            },
            ..Config::default()
        })
        .unwrap();
        let session = block_on(adapter.open(OpenRequest {
            session_id: "session".into(),
            participant: Participant {
                id: "user".into(),
                ..Participant::default()
            },
            ..OpenRequest::default()
        }))
        .unwrap();
        (fixture, session)
    }

    fn reaped(&self) {
        let pid = std::fs::read_to_string(self.0.join("pid")).unwrap();
        assert!(!std::process::Command::new("/bin/kill")
            .args(["-0", pid.trim()])
            .output()
            .unwrap()
            .status
            .success());
    }

    fn release(&self, phase: &str) {
        std::fs::write(self.0.join(phase), b"go").unwrap();
    }
}

fn submit(
    session: &dyn oap_adapter::Session,
) -> (
    oap_protocol::MessageSubmitResponse,
    oap_adapter::EventStream,
) {
    block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                role: "user".into(),
                content: MessageContent::from_text("synthetic"),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap()
}

fn exercise(mode: &str) {
    let (fixture, session) = Fixture::open(mode);
    let (admission, mut stream) = submit(&*session);
    let run = admission.run_id.clone().unwrap();
    let mut events = Vec::new();
    let prefix = if mode == "gates" { 4 } else { 2 };
    for _ in 0..prefix {
        events.push(serde_json::to_value(block_on(stream.next()).unwrap().unwrap()).unwrap());
    }
    let cancelled = block_on(session.cancel(run.clone()));
    if cancelled.is_err() {
        drop(session);
        fixture.reaped();
        panic!("public native cancellation refused: {cancelled:?}");
    }
    let cancelled = cancelled.unwrap();
    assert!(cancelled.accepted);
    assert_eq!(cancelled.status.as_str(), "cancelling");
    assert_eq!(cancelled.run_id, run);
    assert_eq!(block_on(session.close()), Err(Error::RunActive));
    fixture.release("stream");
    loop {
        let event = serde_json::to_value(block_on(stream.next()).unwrap().unwrap()).unwrap();
        let after = event["type"] == "content.delta" && event["payload"]["part"]["text"] == "after";
        events.push(event);
        if after {
            break;
        }
    }
    let state = block_on(session.state()).unwrap();
    assert_eq!(state.status.as_str(), "running");
    assert_eq!(state.active_run_id.as_ref(), Some(&run));
    assert_eq!(
        block_on(session.cancel(run.clone()))
            .unwrap()
            .status
            .as_str(),
        "cancelling"
    );
    let waker = noop_waker();
    assert!(Box::pin(stream.next())
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    fixture.release("finish");
    for event in block_on(stream.collect::<Vec<_>>()) {
        events.push(serde_json::to_value(event.unwrap()).unwrap());
    }
    let retry = block_on(session.cancel(run.clone()));
    let final_state = block_on(session.state()).unwrap();
    block_on(session.close()).unwrap();
    fixture.reaped();
    let wire: Vec<Value> = std::fs::read_to_string(fixture.0.join("wire.jsonl"))
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    assert_eq!(
        wire.iter()
            .filter(|frame| frame["method"] == "session/cancel")
            .count(),
        1
    );
    assert_eq!(
        events
            .iter()
            .filter(|event| event["type"] == "run.status.updated")
            .count(),
        1
    );
    let terminal = events.last().unwrap();
    assert_eq!(
        terminal["type"],
        if mode == "complete" {
            "run.completed"
        } else {
            "run.cancelled"
        }
    );
    if mode == "complete" {
        assert_eq!(
            retry,
            Err(Error::RunAlreadyTerminal {
                run_id: Some(run),
                status: Some("completed".into())
            })
        );
    } else {
        assert_eq!(retry.unwrap().status.as_str(), "cancelled");
        assert_eq!(
            terminal["payload"]["reason"],
            if mode == "remote" {
                "ACP prompt cancellation confirmed"
            } else {
                "ACP prompt returned cancelled"
            }
        );
    }
    assert_eq!(final_state.status.as_str(), "idle");
    assert!(final_state.active_run_id.is_none());
    for (index, event) in events.iter().enumerate() {
        assert_eq!(event["sequence"], index + 1);
    }
    if mode == "gates" {
        for name in ["gate-answer.json", "late-answer.json"] {
            let answer: Value =
                serde_json::from_slice(&std::fs::read(fixture.0.join(name)).unwrap()).unwrap();
            assert_eq!(answer["result"]["outcome"]["outcome"], "cancelled");
        }
        assert_eq!(
            events
                .iter()
                .filter(|event| event["type"] == "action.permission.requested")
                .count(),
            1
        );
        let resolved = events
            .iter()
            .find(|event| event["type"] == "action.permission.resolved")
            .unwrap();
        assert_eq!(resolved["payload"]["reason"]["code"], "run_cancelling");
        assert_eq!(
            resolved["in_reply_to"],
            events
                .iter()
                .find(|event| event["type"] == "action.permission.requested")
                .unwrap()["id"]
        );
    }
    println!(
        "cancel-native-proof {}",
        json!({"mode":mode,"cancel":cancelled,"events":events,"state":final_state,"wire":wire})
    );
}

#[test]
fn cancellation_waits_for_the_native_cancelled_result() {
    exercise("cancelled");
}

#[test]
fn remote_cancellation_confirmation_is_distinct_from_prompt_failure() {
    exercise("remote");
}

#[test]
fn completion_wins_after_accepted_cancel_intent() {
    exercise("complete");
}

#[test]
fn pending_and_late_permissions_are_cancelled_without_waiting_for_confirmation() {
    exercise("gates");
}

#[test]
fn unknown_and_completed_run_controls_return_typed_refusals() {
    let (fixture, session) = Fixture::open("auto");
    let unknown = block_on(session.cancel("missing".into()));
    let (admission, stream) = submit(&*session);
    let events = block_on(stream.collect::<Vec<_>>());
    let terminal = block_on(session.cancel(admission.run_id.clone().unwrap()));
    block_on(session.close()).unwrap();
    fixture.reaped();
    assert_eq!(unknown, Err(Error::RunNotFound));
    assert_eq!(
        events
            .last()
            .unwrap()
            .as_ref()
            .unwrap()
            .envelope_type
            .as_str(),
        "run.completed"
    );
    assert_eq!(
        terminal,
        Err(Error::RunAlreadyTerminal {
            run_id: admission.run_id,
            status: Some("completed".into())
        })
    );
}
