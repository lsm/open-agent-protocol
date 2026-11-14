#![cfg(unix)]

use std::path::PathBuf;
use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, Instant};

use oxap_adapter::jsonrpc::Inbound;
use oxap_adapter_hermes::process::{Process, ProcessConfig};

const READY: &str = r#"{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{"name":"default"},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}"#;

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn gateway(body: &str) -> ProcessConfig {
    ProcessConfig {
        path: "/bin/sh".into(),
        args: vec!["-c".into(), format!("set -eu\n{body}")],
        env: Some(vec!["PATH=/usr/bin:/bin".into()]),
        exit_timeout: Some(Duration::from_secs(1)),
        ..ProcessConfig::default()
    }
}

fn ready_then(rest: &str) -> String {
    format!("printf '%s\\n' '{READY}'\n{rest}")
}

fn refused(config: &ProcessConfig) -> String {
    match Process::start(config, None) {
        Ok(_) => panic!("the handshake was accepted"),
        Err(error) => error.to_string(),
    }
}

fn scratch(name: &str) -> PathBuf {
    let dir =
        std::env::temp_dir().join(format!("oap-hermes-process-{name}-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

#[test]
fn a_gateway_that_announces_ready_is_started_and_closes_on_stdin_eof() {
    let _serial = serial();
    let mut config = gateway(&ready_then("while IFS= read -r line; do :; done\nexit 0\n"));
    config.env = Some(Vec::new());
    config.exit_timeout = Some(Duration::from_secs(5));
    let process = Process::start(&config, None).unwrap();
    assert_eq!(
        process.ready.replay_epoch,
        "0123456789abcdef0123456789abcdef"
    );
    assert!(process.ready.change_events);
    assert_eq!(
        process.ready.skin.as_deref().map(|raw| raw.get()),
        Some(r#"{"name":"default"}"#)
    );
    let started = Instant::now();
    assert_eq!(process.close(), Ok(()));
    assert!(started.elapsed() < Duration::from_secs(3));
    assert_eq!(process.close(), Ok(()));
}

#[test]
fn a_gateway_that_exits_with_a_status_reports_it_on_every_close() {
    let _serial = serial();
    let process = Process::start(
        &gateway(&ready_then("while IFS= read -r line; do :; done\nexit 3\n")),
        None,
    )
    .unwrap();
    assert_eq!(process.close(), Err("exit status 3".into()));
    assert_eq!(process.close(), Err("exit status 3".into()));
    assert_eq!(process.wait_error().as_deref(), Some("exit status 3"));
}

#[test]
fn an_empty_environment_allowlist_passes_the_child_nothing() {
    let _serial = serial();
    let probe = scratch("env").join("env");
    let mut config = gateway(&ready_then("while IFS= read -r line; do :; done\n"));
    config.args = vec![
        "-c".into(),
        format!(
            "printf '%s' \"${{HOME-unset}}\" > \"$0\"\nprintf '%s\\n' '{READY}'\nwhile IFS= read -r line; do :; done\n"
        ),
        probe.to_string_lossy().into_owned(),
    ];
    config.env = Some(Vec::new());
    let process = Process::start(&config, None).unwrap();
    assert_eq!(process.close(), Ok(()));
    assert_eq!(std::fs::read_to_string(&probe).unwrap(), "unset");
}

#[test]
fn anything_but_gateway_ready_first_fails_the_handshake_with_go_s_text() {
    let _serial = serial();
    for (first, want) in [
        (
            r#"{"jsonrpc":"2.0","method":"event","params":{"type":"skin.changed","session_id":"","payload":{}}}"#,
            r#"first observation was "skin.changed", not gateway.ready"#,
        ),
        (
            r#"{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"s","seq":1}}"#,
            r#"first observation was "message.start", not gateway.ready"#,
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"result":{}}"#,
            "response preceded the ready event",
        ),
        (
            r#"{"jsonrpc":"2.0","id":"r1","method":"sudo","params":{"session_id":"s"}}"#,
            "reverse request preceded the ready event",
        ),
        (
            r#"{"jsonrpc":"2.0","method":"event","params":{"type":"future/unknown"}}"#,
            r#"hermes native: invalid pinned message: unknown event type "future/unknown""#,
        ),
        (
            r#"{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"short"}}}"#,
            "hermes native: invalid pinned message: invalid gateway.ready payload",
        ),
    ] {
        let config = gateway(&format!(
            "printf '%s\\n' '{first}'\nprintf '%s\\n' '{READY}'\nsleep 10\n"
        ));
        assert_eq!(
            refused(&config),
            format!("hermes rpc: gateway ready handshake failed: {want}; stderr: "),
            "{first}"
        );
    }
}

#[test]
fn a_gateway_that_ignores_stdin_eof_is_interrupted_within_the_bound() {
    let _serial = serial();
    let mut config = gateway(&ready_then(
        "trap 'exit 7' INT\nwhile true; do sleep 0.1; done\n",
    ));
    config.exit_timeout = Some(Duration::from_millis(300));
    let process = Process::start(&config, None).unwrap();
    let started = Instant::now();
    assert_eq!(
        process.close(),
        Err("hermes rpc: gateway did not exit on stdin EOF".into())
    );
    assert!(started.elapsed() < Duration::from_secs(5));
    assert_eq!(
        process.close(),
        Err("hermes rpc: gateway did not exit on stdin EOF".into())
    );
    assert_eq!(
        process.exited().try_recv(),
        Err(crossbeam_channel::TryRecvError::Disconnected)
    );
    assert_eq!(process.wait_error().as_deref(), Some("exit status 7"));
}

#[test]
fn a_gateway_that_ignores_the_interrupt_too_is_killed() {
    let _serial = serial();
    let mut config = gateway(&ready_then("trap '' INT\nwhile true; do sleep 0.1; done\n"));
    config.exit_timeout = Some(Duration::from_millis(300));
    let process = Process::start(&config, None).unwrap();
    let started = Instant::now();
    assert_eq!(
        process.close(),
        Err("hermes rpc: gateway did not exit on stdin EOF".into())
    );
    assert!(started.elapsed() < Duration::from_secs(5));
    assert_eq!(process.wait_error().as_deref(), Some("signal: killed"));
}

#[test]
fn a_refused_handshake_reports_bounded_redacted_stderr() {
    let _serial = serial();
    let mut config = gateway(
        "printf '%s\\n' 'authorization: Bearer secret' >&2\nprintf '%0500d' 0 >&2\nprintf '%s\\n' 'garbage-not-json'\nsleep 10\n",
    );
    config.stderr_limit = 64;
    assert_eq!(
        refused(&config),
        format!(
            "hermes rpc: gateway ready handshake failed: hermes rpc: invalid JSON-RPC message: frame must be exactly one JSON object; stderr: authorization: [REDACTED]\n{} [truncated]",
            "0".repeat(35)
        )
    );
}

#[test]
fn stderr_written_just_after_a_refused_handshake_is_still_reported() {
    let _serial = serial();
    assert_eq!(
        refused(&gateway(
            "(sleep 0.1; printf 'late' >&2) &\nprintf '%s\\n' 'garbage-not-json'\nsleep 10\n",
        )),
        "hermes rpc: gateway ready handshake failed: hermes rpc: invalid JSON-RPC message: frame must be exactly one JSON object; stderr: late"
    );
}

#[test]
fn a_response_to_no_pending_call_closes_the_client() {
    let _serial = serial();
    let process = Process::start(
        &gateway(&ready_then(
            "printf '%s\\n' '{\"jsonrpc\":\"2.0\",\"id\":99,\"result\":{}}'\nwhile IFS= read -r line; do :; done\n",
        )),
        None,
    )
    .unwrap();
    let inbound = process.client.inbound();
    if let Ok(Inbound::Barrier(ack)) = inbound.recv_timeout(Duration::from_secs(5)) {
        drop(ack);
    }
    assert!(process
        .client
        .done()
        .recv_timeout(Duration::from_secs(5))
        .is_err());
    assert_eq!(
        process.client.err().as_deref(),
        Some("hermes rpc: response id is not pending: 99")
    );
}

#[test]
fn a_gateway_that_exits_before_ready_fails_the_handshake_at_eof() {
    let _serial = serial();
    assert_eq!(
        refused(&gateway("printf '%s\\n' 'boom' >&2\nexit 4\n")),
        "hermes rpc: gateway ready handshake failed: EOF; stderr: boom"
    );
}

#[test]
fn a_handshake_past_its_deadline_or_cancelled_fails_as_go_s_context_does() {
    let _serial = serial();
    let silent = gateway("sleep 10\n");
    let deadline = Some(Instant::now() + Duration::from_millis(200));
    let Err(expired) = Process::start(&silent, deadline) else {
        panic!("the handshake was accepted");
    };
    assert_eq!(
        expired.to_string(),
        "hermes rpc: gateway ready handshake failed: context deadline exceeded; stderr: "
    );
    let (cancel, cancelled) = crossbeam_channel::bounded::<()>(0);
    drop(cancel);
    let Err(cancelled) = Process::start_with(&silent, None, &cancelled) else {
        panic!("the handshake was accepted");
    };
    assert_eq!(
        cancelled.to_string(),
        "hermes rpc: gateway ready handshake failed: context canceled; stderr: "
    );
}

#[test]
fn a_gateway_needs_an_executable_path() {
    assert_eq!(
        refused(&ProcessConfig::default()),
        "hermes rpc: gateway ready handshake failed: executable path is required"
    );
}

#[test]
fn a_call_answered_just_before_the_gateway_exits_still_returns_its_result() {
    let _serial = serial();
    let config = gateway(&ready_then(
        "IFS= read -r line\nprintf '%s' '{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true,\"blob\":\"'\nhead -c 1048576 /dev/zero | tr '\\000' x\nprintf '%s\\n' '\"}}'\nexit 0\n",
    ));
    for iteration in 0..10 {
        let process = Process::start(&config, None).unwrap();
        let inbound = process.client.inbound();
        let done = process.client.done();
        let drain = std::thread::spawn(move || loop {
            crossbeam_channel::select! {
                recv(inbound) -> message => match message {
                    Ok(Inbound::Barrier(ack)) => drop(ack),
                    Ok(_) => {}
                    Err(_) => return,
                },
                recv(done) -> _ => return,
            }
        });
        let raw = process
            .client
            .call(
                "probe",
                None,
                Some(Instant::now() + Duration::from_secs(10)),
            )
            .unwrap_or_else(|error| panic!("iteration {iteration}: {error}"));
        let result: serde_json::Value = serde_json::from_str(raw.get()).unwrap();
        assert_eq!(result["ok"], true, "iteration {iteration}");
        assert_eq!(
            result["blob"].as_str().map(str::len),
            Some(1 << 20),
            "iteration {iteration}"
        );
        let _ = process.close();
        drop(process);
        drain.join().unwrap();
    }
}

#[test]
fn a_pending_call_fails_at_close_while_a_descendant_still_holds_stdout() {
    let _serial = serial();
    let process = Process::start(
        &gateway(&ready_then(
            "sleep 15 &\nwhile IFS= read -r line; do :; done\nexit 0\n",
        )),
        None,
    )
    .unwrap();
    let client = process.client.clone();
    let (answered, answer) = crossbeam_channel::bounded(1);
    std::thread::spawn(move || {
        let _ = answered.send(client.call("probe", None, None).map(drop));
    });
    std::thread::sleep(Duration::from_millis(300));
    let _ = process.close();
    let outcome = answer
        .recv_timeout(Duration::from_secs(3))
        .expect("the pending call still waits after close");
    assert!(outcome.is_err());
}
