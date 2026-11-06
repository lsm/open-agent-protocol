use std::future::Future;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::task::Context;
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use futures::{executor::block_on, task::noop_waker};
use oap_adapter_acp::process::{Process, ProcessConfig};

struct Fixture(PathBuf);

impl Fixture {
    fn new() -> Self {
        let suffix = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos();
        let path =
            std::env::temp_dir().join(format!("oap-acp-operation-{}-{suffix}", std::process::id()));
        std::fs::create_dir(&path).unwrap();
        Self(path)
    }

    fn config(&self, method: &str) -> ProcessConfig {
        let script = if method == "initialize" {
            r#"printf '%s' $$ > "$1"; read -r request; printf ready > "$2"; read -r waiting"#
        } else {
            r#"printf '%s' $$ > "$1"; read -r request; printf '%s\n' '{"id":1,"jsonrpc":"2.0","result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf ready > "$2"; read -r waiting"#
        };
        ProcessConfig {
            path: "/bin/sh".into(),
            args: vec![
                "-c".into(),
                script.into(),
                "fixture".into(),
                self.0.join("pid").to_string_lossy().into(),
                self.0.join("ready").to_string_lossy().into(),
            ],
            env: Some(Vec::new()),
            shutdown_timeout: Some(Duration::from_millis(200)),
            ..ProcessConfig::default()
        }
    }

    fn assert_reaped(&self) {
        let pid = std::fs::read_to_string(self.0.join("pid")).unwrap();
        let output = Command::new("/bin/kill")
            .args(["-0", &pid])
            .output()
            .unwrap();
        assert!(
            !output.status.success(),
            "child {pid} still exists after cancellation"
        );
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn wait_for(path: &Path) {
    let deadline = Instant::now() + Duration::from_secs(3);
    while !path.exists() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(1));
    }
    assert!(path.exists(), "synthetic agent never observed the request");
}

fn cancel_pending<T>(operation: impl Future<Output = T>, ready: &Path) {
    let mut operation = Box::pin(operation);
    wait_for(ready);
    let waker = noop_waker();
    let began = Instant::now();
    assert!(operation
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    assert!(began.elapsed() < Duration::from_millis(100));
    let began = Instant::now();
    drop(operation);
    assert!(began.elapsed() < Duration::from_secs(2));
}

#[test]
fn dropping_a_quiet_initialize_future_reaps_the_native_child() {
    let fixture = Fixture::new();
    cancel_pending(
        Process::start_async(fixture.config("initialize"), None),
        &fixture.0.join("ready"),
    );
    fixture.assert_reaped();
}

#[test]
fn dropping_a_quiet_call_future_reaps_the_native_child() {
    let fixture = Fixture::new();
    let process = block_on(Process::start_async(
        fixture.config("session/new"),
        Some(Instant::now() + Duration::from_secs(3)),
    ))
    .unwrap();
    cancel_pending(
        process.call_async(
            "session/new".into(),
            Some(
                serde_json::value::to_raw_value(&oap_adapter_acp::native::SessionNewParams {
                    cwd: "/".into(),
                    mcp_servers: Vec::new(),
                })
                .unwrap(),
            ),
            None,
        ),
        &fixture.0.join("ready"),
    );
    fixture.assert_reaped();
}

#[test]
fn async_configuration_errors_return_through_the_future() {
    let error = block_on(Process::start_async(ProcessConfig::default(), None))
        .err()
        .unwrap();
    assert!(error.to_string().contains("executable path is required"));
}
