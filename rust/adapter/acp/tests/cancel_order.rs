use std::future::Future;
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::task::{Context, Poll};
use std::time::Duration;

use futures::{executor::block_on, task::noop_waker, StreamExt};
use oap_adapter_acp::{
    native::PromptParams,
    process::{Process, ProcessConfig},
    Permissions, PromptBridge, TextRun, TextRunContext,
};

static NEXT: AtomicU64 = AtomicU64::new(0);

#[test]
fn native_confirmation_waits_for_the_accepted_write_to_commit_its_intent() {
    let directory = std::env::temp_dir().join(format!(
        "oap-cancel-order-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    ));
    std::fs::create_dir(&directory).unwrap();
    let script = r#"printf '%s' $$ > "$1/pid"; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"native"}}'; read -r request; read -r cancel; printf '%s\n' "$cancel" > "$1/cancel.json"; printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"cancelled"}}'; read -r eof; exit 0"#;
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: "/bin/sh".into(),
            args: vec![
                "-c".into(),
                script.into(),
                "fixture".into(),
                directory.to_string_lossy().into(),
            ],
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let bridge = Arc::new(PromptBridge::new(process, |_| {}));
    block_on(
        bridge.call(
            "session/new".into(),
            Some(
                serde_json::value::to_raw_value(&serde_json::json!({"cwd":"/", "mcpServers":[]}))
                    .unwrap(),
            ),
            None,
        ),
    )
    .unwrap();
    let (entered, clock_entered) = mpsc::channel();
    let (release, wait) = mpsc::channel();
    let wait = Mutex::new(wait);
    let first = AtomicBool::new(true);
    let controller = Permissions::new(bridge.clone(), "user".into(), move || {
        if first.swap(false, Ordering::SeqCst) {
            entered.send(()).unwrap();
            wait.lock()
                .unwrap()
                .recv_timeout(Duration::from_secs(5))
                .unwrap();
        }
        100
    })
    .unwrap();
    let mut sequence = 0;
    let run = Arc::new(Mutex::new(TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native".into(),
            run_id: "run".into(),
            message_id: "session/message".into(),
            capability_revision: None,
        },
        move |kind| {
            sequence += 1;
            format!("{kind}-{sequence}")
        },
    )));
    let mut prompt = bridge.prompt(
        PromptParams {
            session_id: "native".into(),
            ..PromptParams::default()
        },
        None,
    );
    block_on(prompt.admitted()).unwrap();
    let stream = run.lock().unwrap().start(10).unwrap();
    let cancel = controller.clone().cancel(run.clone());
    clock_entered.recv_timeout(Duration::from_secs(3)).unwrap();
    let confirmed = block_on(prompt).unwrap();
    let mut parent = controller.complete(run, confirmed);
    let waker = noop_waker();
    let first_poll = parent.as_mut().poll(&mut Context::from_waker(&waker));
    let pending = first_poll.is_pending();
    release.send(()).unwrap();
    let accepted = block_on(cancel);
    let complete = match first_poll {
        Poll::Ready(result) => result,
        Poll::Pending => block_on(parent),
    };
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    block_on(bridge.close()).unwrap();
    let pid = std::fs::read_to_string(directory.join("pid")).unwrap();
    let reaped = !std::process::Command::new("/bin/kill")
        .args(["-0", pid.trim()])
        .output()
        .unwrap()
        .status
        .success();
    let wire: serde_json::Value =
        serde_json::from_slice(&std::fs::read(directory.join("cancel.json")).unwrap()).unwrap();
    std::fs::remove_dir_all(directory).unwrap();
    println!(
        "cancel-order-proof {}",
        serde_json::json!({"parent_pending":pending,"reaped":reaped,"wire":wire,"events":events})
    );
    assert!(reaped);
    assert!(pending, "native confirmation overtook intent commit");
    assert_eq!(accepted.unwrap().status.as_str(), "cancelling");
    complete.unwrap();
    assert_eq!(
        events
            .iter()
            .map(|event| event.envelope_type.as_str())
            .collect::<Vec<_>>(),
        vec!["run.started", "run.status.updated", "run.cancelled"]
    );
    assert_eq!(
        wire,
        serde_json::json!({"jsonrpc":"2.0","method":"session/cancel","params":{"sessionId":"native"}})
    );
}
