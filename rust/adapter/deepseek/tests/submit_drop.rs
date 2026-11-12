#![cfg(unix)]

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::task::{Context, Wake, Waker};
use std::time::{Duration, Instant};

use oap_adapter::{OpenRequest, ResumeRequest, SubmitRequest};
use oap_adapter_deepseek::adapter::{Adapter, Config};
use oap_adapter_deepseek::native::{SERVER_NAME, SERVER_VERSION};
use oap_adapter_deepseek::process::ProcessConfig;
use oap_adapter_deepseek::runtime::Runtime;
use oap_adapter_deepseek::session::Session;
use oap_protocol::{
    EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest, Participant,
    SessionStatus,
};

fn event(seq: i64, kind: &str, data: &str) -> String {
    format!(
        "printf '%s\\n' '{{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{{\"sessionId\":\"s\",\"event\":{{\"type\":\"{kind}\",\"seq\":{seq},\"time\":1,\"data\":{data}}}}}}}'\n"
    )
}

fn script(dir: &str) -> String {
    let info = format!(
        r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
        *SERVER_VERSION
    );
    let turn = [
        event(
            1,
            "agent/inbox/spliced",
            r#"{"target":"next-turn","start":0,"inserted":[{"id":"receipt","role":"user","content":[],"source":{"kind":"user"}}]}"#,
        ),
        event(2, "turn/start", r#"{"turn":1}"#),
        event(3, "step/start", r#"{"turn":1,"step":1}"#),
        event(
            4,
            "user/message",
            r#"{"id":"receipt","role":"user","content":[],"source":{"kind":"user"}}"#,
        ),
        event(
            5,
            "assistant/message",
            r#"{"turn":1,"step":1,"message":{"id":"a","role":"assistant","content":[{"type":"text","text":"hi"}],"source":{"kind":"model","provider":"p","model":"m"}},"stream":[],"usage":{"inputTokens":3,"outputTokens":2}}"#,
        ),
        event(6, "step/end", r#"{"turn":1,"step":1}"#),
        event(7, "turn/end", r#"{"turn":1,"reason":{"kind":"completed"}}"#),
        "printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"method\":\"session.status\",\"params\":{\"sessionId\":\"s\",\"status\":\"idle\"}}'\n".into(),
    ]
    .concat();
    format!(
        r#"while read line; do
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
case "$line" in
*'"method":"initialize"'*) printf '{{"jsonrpc":"2.0","id":%s,"result":{info}}}\n' "$id" ;;
*'"method":"session/prompt"'*)
printf '{{"jsonrpc":"2.0","id":%s,"result":{{"messageId":"receipt"}}}}\n' "$id"
n=0; until [ -f "{dir}/release" ] || [ $n -ge 200 ]; do sleep 0.05; n=$((n+1)); done
{turn};;
*'"method":"shutdown"'*) printf '{{"jsonrpc":"2.0","id":%s,"result":{{}}}}\n' "$id"; exit 0 ;;
esac
done"#
    )
}

fn threads() -> usize {
    if let Ok(tasks) = std::fs::read_dir("/proc/self/task") {
        return tasks.count();
    }
    let listing = std::process::Command::new("ps")
        .args(["-M", "-p", &std::process::id().to_string()])
        .output()
        .unwrap();
    String::from_utf8_lossy(&listing.stdout).lines().count() - 1
}

#[derive(Default)]
struct Flag(AtomicBool);

impl Wake for Flag {
    fn wake(self: Arc<Self>) {
        self.0.store(true, Ordering::SeqCst);
    }
}

fn until(what: &str, mut ready: impl FnMut() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(5);
    while !ready() {
        assert!(Instant::now() < deadline, "{what}");
        std::thread::sleep(Duration::from_millis(10));
    }
}

#[test]
fn dropping_submit_while_admission_is_pending_leaves_no_waiter_and_the_run_still_settles() {
    let dir = std::env::temp_dir().join(format!("oap-deepseek-drop-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.to_string_lossy().into_owned();
    let session: Session = Adapter::new(Config {
        process: ProcessConfig {
            path: "/bin/sh".into(),
            args: vec!["-c".into(), script(&path), "sh".into()],
            dir: path,
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
    .unwrap();
    let baseline = threads();
    let flag = Arc::new(Flag::default());
    let waker = Waker::from(flag.clone());
    let mut submitted = oap_adapter::Session::submit(
        &session,
        SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "s".into(),
                messages: vec![Message {
                    role: MessageRole::USER,
                    content: MessageContent::from_text("hello"),
                    ..Message::default()
                }],
                ..MessageSubmitRequest::default()
            },
            envelope_id: "e1".into(),
        },
    );
    let mut context = Context::from_waker(&waker);
    assert!(submitted.as_mut().poll(&mut context).is_pending());
    until("the receipt never reached the submit", || {
        flag.0.load(Ordering::SeqCst)
    });
    assert!(submitted.as_mut().poll(&mut context).is_pending());
    drop(submitted);
    let mut counted = 0;
    let deadline = Instant::now() + Duration::from_secs(2);
    while Instant::now() < deadline {
        counted = threads();
        if counted <= baseline {
            break;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    std::fs::write(dir.join("release"), "").unwrap();
    assert!(
        counted <= baseline,
        "{} threads outlived the dropped submit",
        counted - baseline
    );
    until("the run never settled", || {
        session.state().is_ok_and(|state| {
            state.status == SessionStatus::IDLE && state.transcript_cursor == "4"
        })
    });
    let (_, mut replay) = session
        .resume(ResumeRequest {
            run_id: "run-minted".into(),
            after_sequence: 0,
        })
        .unwrap();
    let kinds: Vec<_> = oap_adapter::testkit::drain(&mut replay, Duration::from_secs(5))
        .into_iter()
        .map(|envelope| envelope.envelope_type)
        .collect();
    assert_eq!(kinds.first(), Some(&EnvelopeType::RUN_STARTED));
    assert_eq!(kinds.last(), Some(&EnvelopeType::RUN_COMPLETED));
    session.close().unwrap();
    let _ = std::fs::remove_dir_all(&dir);
}
