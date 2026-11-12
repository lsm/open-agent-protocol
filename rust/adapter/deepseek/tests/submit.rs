#![cfg(unix)]

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use oap_adapter::{Error, EventStream, OpenRequest};
use oap_adapter_deepseek::adapter::{Adapter, Config};
use oap_adapter_deepseek::native::{SERVER_NAME, SERVER_VERSION};
use oap_adapter_deepseek::process::ProcessConfig;
use oap_adapter_deepseek::runtime::Runtime;
use oap_adapter_deepseek::session::Session;
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, MessageSubmitResponse, Participant,
    SessionStatus,
};
use serde_json::Value;

static SPAWNING: Mutex<()> = Mutex::new(());
static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture {
    _serial: MutexGuard<'static, ()>,
    dir: PathBuf,
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn notify(method: &str, params: &str) -> String {
    format!(
        "printf '%s\\n' '{{\"jsonrpc\":\"2.0\",\"method\":\"{method}\",\"params\":{params}}}'\n"
    )
}

fn event(seq: i64, kind: &str, data: &str) -> String {
    notify(
        "session.event",
        &format!(
            r#"{{"sessionId":"s","event":{{"type":"{kind}","seq":{seq},"time":1,"data":{data}}}}}"#
        ),
    )
}

fn answer(result: &str) -> String {
    format!("printf '{{\"jsonrpc\":\"2.0\",\"id\":%s,{result}}}\\n' \"$id\"\n")
}

fn receipt() -> String {
    answer(r#""result":{"messageId":"receipt"}"#)
}

impl Fixture {
    fn new() -> Self {
        let serial = SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner());
        let dir = std::env::temp_dir().join(format!(
            "oap-deepseek-submit-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        Self {
            _serial: serial,
            dir,
        }
    }

    fn open(&self, on_prompt: &str) -> Arc<Session> {
        let dir = self.dir.to_string_lossy().into_owned();
        let info = format!(
            r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
            *SERVER_VERSION
        );
        let script = format!(
            r#"while read line; do
printf '%s\n' "$line" >> "{dir}/wire"
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
case "$line" in
*'"method":"initialize"'*) printf '{{"jsonrpc":"2.0","id":%s,"result":{info}}}\n' "$id" ;;
*'"method":"session/prompt"'*)
{on_prompt};;
*'"method":"shutdown"'*) printf '{{"jsonrpc":"2.0","id":%s,"result":{{}}}}\n' "$id"; exit 0 ;;
esac
done"#
        );
        Adapter::new(Config {
            process: ProcessConfig {
                path: "/bin/sh".into(),
                args: vec!["-c".into(), script, "sh".into()],
                dir,
                env: Some(vec!["PATH=/usr/bin:/bin".into()]),
                shutdown_timeout: Some(Duration::from_secs(2)),
                ..ProcessConfig::default()
            },
            provider: "deepseek".into(),
            model: "m".into(),
            runtime: Runtime::new(|| 42, |kind| format!("{kind}-minted")),
            ..Config::default()
        })
        .unwrap()
        .open(OpenRequest {
            session_id: "s".into(),
            participant: Participant {
                id: "user".into(),
                ..Participant::default()
            },
            ..OpenRequest::default()
        })
        .map(Arc::new)
        .unwrap()
    }

    fn held_until_released(&self) -> String {
        format!(
            "until [ -f \"{}/release\" ]; do sleep 0.05; done; exit 0\n",
            self.dir.display()
        )
    }

    fn release(&self) {
        std::fs::write(self.dir.join("release"), "").unwrap();
    }

    fn prompts(&self) -> Vec<Value> {
        std::fs::read_to_string(self.dir.join("wire"))
            .unwrap_or_default()
            .lines()
            .map(|line| serde_json::from_str::<Value>(line).unwrap())
            .filter(|frame| frame["method"] == "session/prompt")
            .collect()
    }
}

fn hello(session: &str) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: session.into(),
        messages: vec![Message {
            id: Some("m1".into()),
            role: MessageRole::USER,
            content: MessageContent::from_text("hello"),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    }
}

fn submit(
    session: &Arc<Session>,
    request: MessageSubmitRequest,
) -> Result<(MessageSubmitResponse, EventStream), Error> {
    let (send, outcome) = std::sync::mpsc::channel();
    let session = session.clone();
    std::thread::spawn(move || {
        let _ = send.send(session.submit(request));
    });
    outcome
        .recv_timeout(Duration::from_secs(5))
        .expect("submit neither admitted nor refused the run")
}

fn harness(error: Error) -> String {
    match error {
        Error::Harness(text) => text,
        other => panic!("not a harness error: {other:?}"),
    }
}

#[test]
fn submit_refuses_another_sessions_id_and_a_closed_session() {
    let fixture = Fixture::new();
    let session = fixture.open(&receipt());
    assert_eq!(submit(&session, hello("t")).err(), Some(Error::RunNotFound));
    session.close().unwrap();
    assert_eq!(
        submit(&session, hello("s")).err(),
        Some(Error::SessionClosed)
    );
    assert!(fixture.prompts().is_empty());
}

#[test]
fn a_prompt_the_runtime_refuses_fails_submit_and_leaves_the_session_idle() {
    let fixture = Fixture::new();
    let session = fixture.open(&answer(r#""error":{"code":-32000,"message":"busy"}"#));
    let refused = harness(submit(&session, hello("s")).err().unwrap());
    assert!(refused.contains("busy"), "{refused}");
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
}

#[test]
fn a_prompt_response_without_a_message_id_retires_the_session() {
    let fixture = Fixture::new();
    let session = fixture.open(&answer(r#""result":{}"#));
    assert_eq!(
        harness(submit(&session, hello("s")).err().unwrap()),
        "deepseek adapter: invalid native protocol observation: prompt response omitted messageId"
    );
    assert_eq!(session.state().err(), Some(Error::SessionClosed));
}

fn settles_closed(session: &Session) -> bool {
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while std::time::Instant::now() < deadline {
        if session.state().err() == Some(Error::SessionClosed) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    false
}

#[test]
fn a_runtime_that_exits_while_the_prompt_is_pending_fails_submit() {
    let fixture = Fixture::new();
    let session = fixture.open("exit 0\n");
    assert!(submit(&session, hello("s")).is_err());
    assert!(settles_closed(&session), "{:?}", session.state());
}

#[test]
fn a_notification_while_the_prompt_is_pending_does_not_retire_the_session() {
    let fixture = Fixture::new();
    let reply = event(1, "turn/start", r#"{"turn":1}"#)
        + &answer(r#""error":{"code":-32000,"message":"busy"}"#);
    let session = fixture.open(&reply);
    let refused = harness(submit(&session, hello("s")).err().unwrap());
    assert!(refused.contains("busy"), "{refused}");
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
}

fn pending(
    fixture: &Fixture,
    session: &Arc<Session>,
) -> std::thread::JoinHandle<Result<(MessageSubmitResponse, EventStream), Error>> {
    let first = {
        let session = session.clone();
        std::thread::spawn(move || session.submit(hello("s")))
    };
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while fixture.prompts().is_empty() && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(10));
    }
    assert_eq!(fixture.prompts().len(), 1);
    first
}

#[test]
fn a_second_submit_or_a_close_while_the_prompt_is_pending_is_refused() {
    let fixture = Fixture::new();
    let session = fixture.open(&fixture.held_until_released());
    let first = pending(&fixture, &session);
    assert_eq!(submit(&session, hello("s")).err(), Some(Error::RunActive));
    assert_eq!(session.close().err(), Some(Error::RunActive));
    fixture.release();
    assert!(first.join().unwrap().is_err());
    assert_eq!(fixture.prompts().len(), 1);
}

#[test]
fn close_is_refused_after_the_runtime_answers_the_prompt_with_its_receipt() {
    let fixture = Fixture::new();
    let answered = format!("touch \"{}/answered\"\n", fixture.dir.display());
    let session = fixture.open(&(receipt() + &answered + &fixture.held_until_released()));
    let first = pending(&fixture, &session);
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while !fixture.dir.join("answered").exists() && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(10));
    }
    assert!(fixture.dir.join("answered").exists());
    assert_eq!(session.close().err(), Some(Error::RunActive));
    fixture.release();
    assert!(first.join().unwrap().is_err());
}
