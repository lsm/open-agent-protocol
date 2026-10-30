#![cfg(unix)]

use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, Instant};

use oap_adapter_deepseek::native::{InitializeParams, SERVER_NAME, SERVER_VERSION};
use oap_adapter_deepseek::process::{Process, ProcessConfig};

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn server(script: &str) -> ProcessConfig {
    ProcessConfig {
        path: "/bin/sh".into(),
        args: vec!["-c".into(), script.into()],
        env: Some(vec!["PATH=/usr/bin:/bin".into()]),
        shutdown_timeout: Some(Duration::from_secs(2)),
        initialize: InitializeParams {
            cwd: "/w".into(),
            provider: "deepseek".into(),
            model: "m".into(),
            ..InitializeParams::default()
        },
        ..ProcessConfig::default()
    }
}

fn answer(id: u32, result: &str) -> String {
    format!(
        r#"read line; printf '%s\n' "$line" >&2; printf '%s\n' '{{"jsonrpc":"2.0","id":{id},"result":{result}}}'"#
    )
}

fn server_info() -> String {
    format!(
        r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
        *SERVER_VERSION
    )
}

fn deadline() -> Option<Instant> {
    Some(Instant::now() + Duration::from_secs(5))
}

#[test]
fn the_handshake_initializes_and_close_asks_for_shutdown_before_stdin_eof() {
    let _serial = serial();
    let script = format!(
        "{}; {}; cat >/dev/null",
        answer(1, &server_info()),
        answer(2, "{}")
    );
    let process = Process::start(server(&script), deadline()).unwrap();
    assert_eq!(process.initialize.server_info.name, SERVER_NAME);
    assert_eq!(process.close(), Ok(()));
    assert_eq!(
        process.stderr(),
        "{\"id\":1,\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"params\":{\"cwd\":\"/w\",\"provider\":\"deepseek\",\"model\":\"m\"}}\n{\"id\":2,\"jsonrpc\":\"2.0\",\"method\":\"shutdown\"}"
    );
}

#[test]
fn a_null_or_foreign_server_fails_the_handshake_with_gos_text() {
    let _serial = serial();
    let failure = |result: &str| {
        Process::start(
            server(&format!("{}; cat >/dev/null", answer(1, result))),
            deadline(),
        )
        .err()
        .unwrap()
        .to_string()
    };
    assert!(failure("null").starts_with(
        "deepseek rpc: initialization handshake failed: deepseek native: invalid pinned message: unexpected serverInfo \"\"/\"\"; stderr: "
    ));
    assert!(failure("5").starts_with(
        "deepseek rpc: initialization handshake failed: decode initialize response: json: cannot unmarshal number into Go value of type native.InitializeResult; stderr: "
    ));
}

#[test]
fn invalid_initialize_params_are_refused_before_any_spawn() {
    let config = ProcessConfig {
        initialize: InitializeParams::default(),
        ..server("exit 9")
    };
    assert_eq!(
        Process::start(config, deadline()).err().unwrap().to_string(),
        "deepseek rpc: initialization handshake failed: deepseek native: invalid pinned message: initialize cwd, provider, and model are required"
    );
}

#[test]
fn a_notification_before_the_initialize_response_fails_the_handshake() {
    let _serial = serial();
    let script = r#"read line; printf '%s\n' '{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":"s","status":"idle"}}'; cat >/dev/null"#;
    let failure = Process::start(server(script), deadline())
        .err()
        .unwrap()
        .to_string();
    assert!(
        failure.starts_with("deepseek rpc: initialization handshake failed: native observation preceded initialize response; stderr: "),
        "{failure}"
    );
}

#[test]
fn a_server_that_never_answers_fails_at_the_deadline() {
    let _serial = serial();
    let started = Instant::now();
    let failure = Process::start(
        server("exec sleep 30"),
        Some(Instant::now() + Duration::from_millis(300)),
    )
    .err()
    .unwrap()
    .to_string();
    assert!(started.elapsed() < Duration::from_secs(5));
    assert!(
        failure.starts_with(
            "deepseek rpc: initialization handshake failed: context deadline exceeded; stderr: "
        ),
        "{failure}"
    );
}

#[test]
fn close_without_a_shutdown_answer_kills_at_the_timeout() {
    let _serial = serial();
    let script = format!("{}; exec sleep 30", answer(1, &server_info()));
    let config = ProcessConfig {
        shutdown_timeout: Some(Duration::from_millis(300)),
        ..server(&script)
    };
    let process = Process::start(config, deadline()).unwrap();
    let started = Instant::now();
    assert_eq!(
        process.close(),
        Err("deepseek rpc: shutdown timed out".into())
    );
    assert!(started.elapsed() < Duration::from_secs(3));
}
