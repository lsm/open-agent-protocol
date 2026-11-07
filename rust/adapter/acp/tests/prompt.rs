use std::future::Future;
use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::task::Context;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use futures::{executor::block_on, task::noop_waker};
use oap_adapter::jsonrpc::Inbound;
use oap_adapter_acp::native::{ContentBlock, PromptParams};
use oap_adapter_acp::process::{Process, ProcessConfig};
use oap_adapter_acp::PromptBridge;

static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture(PathBuf);

impl Fixture {
    fn new() -> Self {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let serial = NEXT.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "oap-acp-prompt-{}-{stamp}-{serial}",
            std::process::id()
        ));
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn bridge(
        &self,
        mode: &str,
        callback: impl FnMut(Inbound) + Send + 'static,
    ) -> Arc<PromptBridge> {
        let script = r#"printf '%s' $$ > "$1"; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"native"}}'; if [ "$2" = blocked ]; then exec /bin/sleep 60; fi; read -r request; printf ready > "$1.ready"; if [ "$2" = quiet ]; then exec /bin/sleep 60; fi; printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"index":0}}' '{"jsonrpc":"2.0","id":"reverse","method":"fixture/request","params":{}}'; read -r reply; printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"index":1}}' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'; read -r waiting; exit 0"#;
        let process = block_on(Process::start_async(
            ProcessConfig {
                path: "/bin/sh".into(),
                args: vec![
                    "-c".into(),
                    script.into(),
                    "fixture".into(),
                    self.0.join("pid").to_string_lossy().into(),
                    mode.into(),
                ],
                env: Some(Vec::new()),
                queue_capacity: 1,
                shutdown_timeout: Some(Duration::from_millis(150)),
                ..ProcessConfig::default()
            },
            None,
        ))
        .unwrap();
        let bridge = Arc::new(PromptBridge::new(process, callback));
        block_on(
            bridge.call(
                "session/new".into(),
                Some(
                    serde_json::value::to_raw_value(
                        &serde_json::json!({"cwd":"/", "mcpServers":[]}),
                    )
                    .unwrap(),
                ),
                None,
            ),
        )
        .unwrap();
        bridge
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
            "native child {pid} survived"
        );
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn prompt() -> PromptParams {
    PromptParams {
        session_id: "native".into(),
        prompt: vec![ContentBlock {
            kind: "text".into(),
            text: "synthetic".into(),
            ..ContentBlock::default()
        }],
    }
}

#[test]
fn prompt_completion_follows_ordered_notifications_and_reverse_replies() {
    let fixture = Fixture::new();
    let events = Arc::new(Mutex::new(Vec::new()));
    let seen = events.clone();
    let (entered, ready) = std::sync::mpsc::channel();
    let (release, waiting) = std::sync::mpsc::channel();
    let bridge = fixture.bridge("flow", move |message| match message {
        Inbound::Notification(notification) => {
            let value: serde_json::Value =
                serde_json::from_str(notification.params.unwrap().get()).unwrap();
            if value["index"] == 0 {
                entered.send(()).unwrap();
                waiting.recv().unwrap();
            }
            seen.lock().unwrap().push(value["index"].as_i64().unwrap());
        }
        Inbound::Request(request) => request
            .respond(&serde_json::json!({"accepted":true}))
            .unwrap(),
        Inbound::Barrier(_) => panic!("bridge exposed an internal barrier"),
    });
    let mut call = Box::pin(bridge.prompt(prompt(), None));
    block_on(call.admitted()).unwrap();
    ready.recv_timeout(Duration::from_secs(3)).unwrap();
    let waker = noop_waker();
    assert!(call
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    release.send(()).unwrap();
    assert_eq!(block_on(call).unwrap().stop_reason, "end_turn");
    assert_eq!(*events.lock().unwrap(), vec![0, 1]);
    block_on(bridge.close()).unwrap();
    assert_eq!(Arc::strong_count(&events), 1);
    fixture.reaped();
}

#[test]
fn dropping_a_quiet_prompt_reaps_before_return_and_close_joins_the_dispatcher() {
    let fixture = Fixture::new();
    let owner = Arc::new(());
    let observed = owner.clone();
    let bridge = fixture.bridge("quiet", move |_| {
        let _ = &observed;
    });
    let mut call = Box::pin(bridge.prompt(prompt(), None));
    block_on(call.admitted()).unwrap();
    let waker = noop_waker();
    assert!(call
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    let began = Instant::now();
    drop(call);
    assert!(began.elapsed() < Duration::from_secs(2));
    fixture.reaped();
    let _ = block_on(bridge.close());
    assert_eq!(Arc::strong_count(&owner), 1);
}

#[test]
fn dropping_a_prompt_cancels_a_pending_write_admission() {
    let fixture = Fixture::new();
    let bridge = fixture.bridge("blocked", |_| {});
    let mut params = prompt();
    params.prompt[0].text = "x".repeat(2 * 1024 * 1024);
    let mut call = bridge.prompt(params, None);
    let waker = noop_waker();
    {
        let mut admitted = Box::pin(call.admitted());
        assert!(admitted
            .as_mut()
            .poll(&mut Context::from_waker(&waker))
            .is_pending());
    }
    drop(call);
    fixture.reaped();
    let _ = block_on(bridge.close());
}

#[test]
fn close_terminates_a_quiet_prompt_and_joins_its_inbound_handler() {
    let fixture = Fixture::new();
    let owner = Arc::new(());
    let observed = owner.clone();
    let bridge = fixture.bridge("quiet", move |_| {
        let _ = &observed;
    });
    let mut call = bridge.prompt(prompt(), None);
    block_on(call.admitted()).unwrap();
    let error = block_on(bridge.close()).unwrap_err();
    assert!(error.to_string().contains("shutdown timed out"));
    assert!(block_on(call).is_err());
    assert_eq!(Arc::strong_count(&owner), 1);
    fixture.reaped();
}

#[test]
fn a_panicking_inbound_handler_fails_the_call_and_is_joined() {
    let fixture = Fixture::new();
    let bridge = fixture.bridge("flow", |_| panic!("synthetic callback failure"));
    let mut call = bridge.prompt(prompt(), None);
    block_on(call.admitted()).unwrap();
    assert!(block_on(call).is_err());
    assert!(block_on(bridge.close()).is_err());
    fixture.reaped();
}

#[test]
fn dropping_quiet_bridge_shutdown_reaps_and_joins_before_return() {
    let fixture = Fixture::new();
    let owner = Arc::new(());
    let observed = owner.clone();
    let bridge = fixture.bridge("quiet", move |_| {
        let _ = &observed;
    });
    let mut call = bridge.prompt(prompt(), None);
    block_on(call.admitted()).unwrap();
    drop(bridge.clone().close());
    fixture.reaped();
    assert_eq!(Arc::strong_count(&owner), 1);
    assert!(block_on(call).is_err());
}
