#![cfg(unix)]

use std::sync::Arc;
use std::task::{Context, Waker};
use std::time::{Duration, Instant};

use oap_adapter::{Error, OpenRequest, SubmitRequest};
use oap_adapter_deepseek::adapter::{Adapter, Config};
use oap_adapter_deepseek::native::{SERVER_NAME, SERVER_VERSION};
use oap_adapter_deepseek::process::ProcessConfig;
use oap_adapter_deepseek::runtime::Runtime;
use oap_adapter_deepseek::session::Session;
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, Participant, SessionStatus,
};

fn script(dir: &str) -> String {
    let info = format!(
        r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
        *SERVER_VERSION
    );
    format!(
        r#"while read line; do
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
case "$line" in
*'"method":"initialize"'*) printf '{{"jsonrpc":"2.0","id":%s,"result":{info}}}\n' "$id" ;;
*'"method":"session/prompt"'*)
printf '%s\n' "$line" >> "{dir}/prompts"
n=0; until [ -f "{dir}/release" ] || [ $n -ge 200 ]; do sleep 0.05; n=$((n+1)); done
printf '{{"jsonrpc":"2.0","id":%s,"error":{{"code":-32000,"message":"busy"}}}}\n' "$id" ;;
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

fn until(what: &str, mut ready: impl FnMut() -> bool) {
    let deadline = Instant::now() + Duration::from_secs(5);
    while !ready() {
        assert!(Instant::now() < deadline, "{what}");
        std::thread::sleep(Duration::from_millis(10));
    }
}

fn request(text: &str) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "s".into(),
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text(text),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    }
}

#[test]
fn a_submit_dropped_before_its_prompt_is_sent_never_sends_it() {
    let dir = std::env::temp_dir().join(format!("oap-deepseek-unsent-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.to_string_lossy().into_owned();
    let session: Arc<Session> = Adapter::new(Config {
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
    .map(Arc::new)
    .unwrap();
    let prompts = || {
        std::fs::read_to_string(dir.join("prompts"))
            .unwrap_or_default()
            .lines()
            .count()
    };
    let baseline = threads();
    let first = {
        let session = session.clone();
        std::thread::spawn(move || session.submit(request("first")))
    };
    until("the first prompt was not sent", || prompts() == 1);
    let mut dropped = oap_adapter::Session::submit(
        &*session,
        SubmitRequest {
            request: request("dropped"),
            envelope_id: "e2".into(),
        },
    );
    let mut context = Context::from_waker(Waker::noop());
    assert!(dropped.as_mut().poll(&mut context).is_pending());
    drop(dropped);
    std::fs::write(dir.join("release"), "").unwrap();
    assert!(matches!(first.join().unwrap(), Err(Error::Harness(_))));
    until("a thread outlived the dropped submit", || {
        threads() <= baseline
    });
    assert_eq!(prompts(), 1, "the dropped submit sent its prompt");
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
    session.close().unwrap();
    let _ = std::fs::remove_dir_all(&dir);
}
