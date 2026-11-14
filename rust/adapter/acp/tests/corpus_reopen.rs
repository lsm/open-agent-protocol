use std::future::Future;
use std::path::PathBuf;
use std::process::Command;
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::task::noop_waker;
use oxap_adapter::{Adapter as _, Error, OpenRequest, ResumeRequest};
use oxap_adapter_acp::{pin, process::ProcessConfig, Adapter, Config};
use oxap_protocol::Participant;
use serde_json::Value;

fn wait<F: Future>(future: F) -> F::Output {
    let mut future = Box::pin(future);
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match future
            .as_mut()
            .poll(&mut Context::from_waker(&noop_waker()))
        {
            Poll::Ready(value) => return value,
            Poll::Pending => {
                assert!(Instant::now() < deadline, "corpus operation timed out");
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

struct Workspace(PathBuf);

impl Drop for Workspace {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn read(path: PathBuf) -> Value {
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

#[test]
fn the_reopen_case_loads_its_bound_session_with_the_recorded_wire_and_state() {
    let source = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&pin().corpus)
        .join("cases/session-reopen");
    let definition = read(source.join("case.json"));
    let native = std::fs::read_to_string(source.join("native.jsonl")).unwrap();
    let load: Value = serde_json::from_str(native.lines().nth(2).unwrap()).unwrap();
    assert_eq!(load["raw"]["method"], "session/load");
    let native_id = load["raw"]["params"]["sessionId"]
        .as_str()
        .unwrap()
        .to_owned();
    assert_eq!(definition["identity_map"][&native_id], "session");
    let directory = std::env::temp_dir().join(format!("oap-acp-reopen-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&directory);
    std::fs::create_dir_all(&directory).unwrap();
    let _workspace = Workspace(directory.clone());
    std::fs::write(directory.join("native.jsonl"), &native).unwrap();
    std::fs::write(
        directory.join("agent.py"),
        include_str!("support/reopen_agent.py"),
    )
    .unwrap();
    let python = Command::new("python3")
        .args(["-c", "import sys; print(sys.executable)"])
        .output()
        .unwrap();
    let cwd = directory.to_string_lossy().into_owned();
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: String::from_utf8(python.stdout).unwrap().trim().into(),
            args: vec![
                "-u".into(),
                directory.join("agent.py").to_string_lossy().into(),
                cwd.clone(),
                cwd.clone(),
            ],
            dir: cwd,
            env: Some(Vec::new()),
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        ..Config::default()
    })
    .unwrap();
    let descriptor = wait(adapter.probe()).unwrap();
    for (feature, level) in definition["advertised_capabilities"].as_object().unwrap() {
        assert_eq!(
            descriptor.capabilities.features[feature].level.as_str(),
            level.as_str().unwrap(),
            "{feature}"
        );
    }
    let session = wait(adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        reopen: true,
        native_session_id: native_id.clone(),
        ..OpenRequest::default()
    }))
    .unwrap();
    let deadline = Instant::now() + Duration::from_secs(10);
    while !directory.join("replayed").exists() && !directory.join("mismatch").exists() {
        assert!(Instant::now() < deadline, "agent did not finish the replay");
        std::thread::sleep(Duration::from_millis(1));
    }
    let state = wait(session.state()).unwrap();
    let resumed = wait(session.resume(ResumeRequest {
        run_id: "pre-close-run".into(),
        after_sequence: 0,
    }));
    let recovered_native = session.native_session_id();
    wait(session.close()).unwrap();
    let mismatch = std::fs::read_to_string(directory.join("mismatch")).ok();
    assert_eq!(
        mismatch, None,
        "the host sent a request the recording does not hold"
    );
    let mut state = serde_json::to_value(&state).unwrap();
    assert!(state["updated_at_ms"].as_i64().unwrap() > 0);
    state.as_object_mut().unwrap().remove("updated_at_ms");
    assert_eq!(state, read(source.join("expected-oap.json")));
    assert_eq!(recovered_native, Some(native_id));
    assert!(matches!(resumed, Err(Error::RunNotFound)));
}

#[test]
fn the_reopen_agent_fails_a_request_after_the_recording_ends() {
    let native = std::fs::read_to_string(
        PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../..")
            .join(&pin().corpus)
            .join("cases/session-reopen/native.jsonl"),
    )
    .unwrap();
    let directory =
        std::env::temp_dir().join(format!("oap-acp-reopen-late-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&directory);
    std::fs::create_dir_all(&directory).unwrap();
    let _workspace = Workspace(directory.clone());
    std::fs::write(directory.join("native.jsonl"), &native).unwrap();
    std::fs::write(
        directory.join("agent.py"),
        include_str!("support/reopen_agent.py"),
    )
    .unwrap();
    let mut input = String::new();
    for line in native.lines() {
        let frame: Value = serde_json::from_str(line).unwrap();
        if frame["direction"] == "client_request" {
            input.push_str(&serde_json::to_string(&frame["raw"]).unwrap());
            input.push('\n');
        }
    }
    input.push_str("{\"jsonrpc\":\"2.0\",\"id\":99,\"method\":\"session/cancel\"}\n");
    let mut child = Command::new("python3")
        .arg("-u")
        .arg(directory.join("agent.py"))
        .arg(&directory)
        .arg(&directory)
        .stdin(std::process::Stdio::piped())
        .stdout(std::process::Stdio::null())
        .spawn()
        .unwrap();
    std::io::Write::write_all(&mut child.stdin.take().unwrap(), input.as_bytes()).unwrap();
    let status = child.wait().unwrap();
    assert!(directory.join("replayed").exists());
    assert!(directory.join("mismatch").exists());
    assert!(
        !status.success(),
        "the agent accepted a request after the recording"
    );
}
