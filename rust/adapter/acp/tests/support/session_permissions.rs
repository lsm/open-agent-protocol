use std::cell::RefCell;
use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, Weak};
use std::time::Duration;

use futures::{executor::block_on, StreamExt};
use oap_adapter::{Adapter as _, InteractionResolution, OpenRequest, SubmitRequest};
use oap_protocol::{
    Message, MessageContent, MessageSubmitRequest, Participant, PermissionRequestedPayload,
    PermissionResolveRequest,
};

use super::Inner;
use crate::{process::ProcessConfig, Adapter, Config};

thread_local! {
    pub(super) static LAST_SESSION: RefCell<Option<Weak<Mutex<Inner>>>> = const { RefCell::new(None) };
}

static SERIAL: Mutex<()> = Mutex::new(());
static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture(PathBuf);
impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn open(mode: &str, turns: usize) -> (Fixture, Box<dyn oap_adapter::Session>, Arc<Mutex<Inner>>) {
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-permission-hot-path-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    std::fs::write(fixture.0.join("agent.py"),r#"import json,os,sys,time
from pathlib import Path
out=Path(sys.argv[1])
mode=sys.argv[2]
turns=int(sys.argv[3])
(out/'pid').write_text(str(os.getpid()))
def read(): return json.loads(sys.stdin.readline())
def send(value): print(json.dumps(dict(jsonrpc='2.0',**value)),flush=True)
def wait(name):
 deadline=time.monotonic()+10
 while not (out/name).exists() and time.monotonic()<deadline: time.sleep(.001)
 assert (out/name).exists(),name
request=read()
send(dict(id=request['id'],result=dict(protocolVersion=1,agentCapabilities={})))
request=read()
send(dict(id=request['id'],result=dict(sessionId='native')))
for index in range(turns):
 prompt=read()
 if mode=='error':
  wait('start')
  send(dict(id=prompt['id'],error=dict(code=-32000,message='synthetic refusal')))
  continue
 if mode=='chunks':
  wait('start')
  for chunk in range(100):
   send(dict(method='session/update',params=dict(sessionId='native',update=dict(sessionUpdate='agent_message_chunk',content=dict(type='text',text='x')))))
   wait('chunk-'+str(chunk))
  wait('finish')
 else:
  send(dict(id='ask-'+str(index),method='session/request_permission',params=dict(sessionId='native',toolCall=dict(toolCallId='tool-'+str(index),title='Act'),options=[dict(optionId='allow',name='Allow',kind='allow_once')])))
  if mode=='parent': send(dict(id=prompt['id'],result=dict(stopReason='end_turn')))
  answer=read()
  expected=dict(outcome='cancelled') if mode=='parent' else dict(outcome='selected',optionId='allow')
  assert answer['result']['outcome']==expected,answer
  if mode=='parent': continue
 send(dict(id=prompt['id'],result=dict(stopReason='end_turn')))
sys.stdin.read()
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
                turns.to_string(),
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
    let inner = LAST_SESSION.with(|slot| slot.borrow().as_ref().unwrap().upgrade().unwrap());
    (fixture, session, inner)
}

fn submit(session: &dyn oap_adapter::Session) -> oap_adapter::EventStream {
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
    .1
}

fn reaped(fixture: &Fixture) {
    let pid = std::fs::read_to_string(fixture.0.join("pid")).unwrap();
    assert!(!std::process::Command::new("/bin/kill")
        .args(["-0", pid.trim()])
        .output()
        .unwrap()
        .status
        .success());
}

#[test]
fn streamed_notifications_create_no_operation_workers() {
    let _serial = SERIAL.lock().unwrap_or_else(|p| p.into_inner());
    let (fixture, session, _inner) = open("chunks", 1);
    let mut stream = submit(&*session);
    assert_eq!(
        block_on(stream.next())
            .unwrap()
            .unwrap()
            .envelope_type
            .as_str(),
        "run.started"
    );
    let before = crate::operation::SPAWNS.load(Ordering::Relaxed);
    std::fs::write(fixture.0.join("start"), b"start").unwrap();
    for index in 0..100 {
        assert_eq!(
            block_on(stream.next())
                .unwrap()
                .unwrap()
                .envelope_type
                .as_str(),
            "content.delta"
        );
        std::fs::write(fixture.0.join(format!("chunk-{index}")), b"received").unwrap();
    }
    let spawned = crate::operation::SPAWNS.load(Ordering::Relaxed) - before;
    std::fs::write(fixture.0.join("finish"), b"finish").unwrap();
    let events = block_on(stream.collect::<Vec<_>>());
    let total = crate::operation::SPAWNS.load(Ordering::Relaxed) - before;
    block_on(session.close()).unwrap();
    reaped(&fixture);
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
    println!("hot-path-proof chunks=100 notification_workers={spawned} total_workers={total}");
    assert_eq!(
        spawned, 0,
        "each native text notification created an operation worker"
    );
    assert_eq!(
        total, 0,
        "a synchronous completion created an operation worker"
    );
}

#[test]
fn a_resolved_run_retains_no_permission_routes() {
    let _serial = SERIAL.lock().unwrap_or_else(|p| p.into_inner());
    let (fixture, session, inner) = open("permission", 1);
    let mut stream = submit(&*session);
    while let Some(event) = block_on(stream.next()) {
        let event = event.unwrap();
        if event.envelope_type.as_str() == "action.permission.requested" {
            let permission = event
                .decode_payload::<PermissionRequestedPayload>()
                .unwrap();
            block_on(session.resolve(InteractionResolution {
                run_id: permission.run_id.clone(),
                responded_by: permission.responded_by.clone(),
                permission: Some(PermissionResolveRequest {
                    interaction_id: permission.interaction_id,
                    requested_by: permission.requested_by,
                    responded_by: permission.responded_by,
                    session_id: permission.session_id,
                    run_id: permission.run_id,
                    choice_id: "allow".into(),
                    granted: true,
                    ..PermissionResolveRequest::default()
                }),
                ..InteractionResolution::default()
            }))
            .unwrap();
        }
    }
    assert_eq!(block_on(session.state()).unwrap().status.as_str(), "idle");
    let retained = inner.lock().unwrap().permission_runs.len();
    block_on(session.close()).unwrap();
    reaped(&fixture);
    println!("permission-route-proof retained={retained}");
    assert_eq!(
        retained, 0,
        "a completed run retained permission routing entries"
    );
}

#[test]
fn a_prompt_error_without_pending_gates_creates_no_operation_worker() {
    let _serial = SERIAL.lock().unwrap_or_else(|p| p.into_inner());
    let (fixture, session, _inner) = open("error", 1);
    let mut stream = submit(&*session);
    assert_eq!(
        block_on(stream.next())
            .unwrap()
            .unwrap()
            .envelope_type
            .as_str(),
        "run.started"
    );
    let before = crate::operation::SPAWNS.load(Ordering::Relaxed);
    std::fs::write(fixture.0.join("start"), b"start").unwrap();
    let events = block_on(stream.collect::<Vec<_>>());
    let spawned = crate::operation::SPAWNS.load(Ordering::Relaxed) - before;
    block_on(session.close()).unwrap();
    reaped(&fixture);
    assert_eq!(
        events
            .last()
            .unwrap()
            .as_ref()
            .unwrap()
            .envelope_type
            .as_str(),
        "run.failed"
    );
    println!("prompt-error-proof operation_workers={spawned}");
    assert_eq!(
        spawned, 0,
        "a synchronous prompt error created an operation worker"
    );
}

#[test]
fn settled_permission_history_has_a_fixed_capacity() {
    let _serial = SERIAL.lock().unwrap_or_else(|p| p.into_inner());
    let (fixture, session, inner) = open("permission", 80);
    let mut first: Option<InteractionResolution> = None;
    let mut last: Option<InteractionResolution> = None;
    for _ in 0..80 {
        let mut stream = submit(&*session);
        while let Some(event) = block_on(stream.next()) {
            let event = event.unwrap();
            if event.envelope_type.as_str() == "action.permission.requested" {
                let permission = event
                    .decode_payload::<PermissionRequestedPayload>()
                    .unwrap();
                let answer = InteractionResolution {
                    run_id: permission.run_id.clone(),
                    responded_by: permission.responded_by.clone(),
                    permission: Some(PermissionResolveRequest {
                        interaction_id: permission.interaction_id,
                        requested_by: permission.requested_by,
                        responded_by: permission.responded_by,
                        session_id: permission.session_id,
                        run_id: permission.run_id,
                        choice_id: "allow".into(),
                        granted: true,
                        ..PermissionResolveRequest::default()
                    }),
                    ..InteractionResolution::default()
                };
                block_on(session.resolve(answer.clone())).unwrap();
                if first.is_none() {
                    first = Some(answer.clone());
                }
                last = Some(answer);
            }
        }
        block_on(session.state()).unwrap();
        let inner = inner.lock().unwrap();
        assert!(inner.permission_runs.is_empty());
        assert!(inner.resolved_permissions.len() <= super::RESOLVED_PERMISSION_CAPACITY);
    }
    let retained = inner.lock().unwrap().resolved_permissions.len();
    assert_eq!(
        block_on(session.resolve(first.unwrap())),
        Err(oap_adapter::Error::InteractionNotFound)
    );
    assert_eq!(
        block_on(session.resolve(last.unwrap())),
        Err(oap_adapter::Error::InteractionResolved)
    );
    block_on(session.close()).unwrap();
    reaped(&fixture);
    println!("permission-cache-proof runs=80 routes=0 recent={retained}");
    assert_eq!(retained, 64);
}

#[test]
fn a_parent_settled_run_retains_no_permission_routes() {
    let _serial = SERIAL.lock().unwrap_or_else(|p| p.into_inner());
    let (fixture, session, inner) = open("parent", 1);
    let events = block_on(submit(&*session).collect::<Vec<_>>());
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
    block_on(session.state()).unwrap();
    let retained = inner.lock().unwrap().permission_runs.len();
    block_on(session.close()).unwrap();
    reaped(&fixture);
    println!("parent-route-proof retained={retained}");
    assert_eq!(retained, 0);
}
