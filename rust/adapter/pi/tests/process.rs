#![cfg(unix)]

use std::sync::{Mutex, MutexGuard};

use oap_adapter_pi::client::Client;
use oap_adapter_pi::native::Command;
use oap_adapter_pi::process::{Process, ProcessConfig};
use oap_adapter_pi::rpc::Error;

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

const STATE: &str = r#"{"thinkingLevel":"off","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"all","sessionId":"s1","autoCompactionEnabled":true,"messageCount":0,"pendingMessageCount":0}"#;

fn fake_pi(script: &str) -> ProcessConfig {
    ProcessConfig {
        path: "/bin/sh".into(),
        args: vec!["-c".into(), script.into(), "pi".into()],
        env: Some(vec!["PATH=/usr/bin:/bin".into()]),
        ..ProcessConfig::default()
    }
}

fn reply(id: &str, tail: &str) -> String {
    format!(
        "read line; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"{id}\",\"command\":\"get_state\",{tail}}}'"
    )
}

fn get_state() -> Command {
    Command {
        kind: "get_state".into(),
        ..Command::default()
    }
}

#[test]
fn the_launcher_runs_pi_in_rpc_mode_takes_its_state_from_get_state_and_closes_on_stdin_eof() {
    let _serial = serial();
    let script = format!(
        "[ \"$*\" = '--mode rpc' ] || exit 9; {}; {}; cat >/dev/null",
        reply("req_1", &format!("\"success\":true,\"data\":{STATE}")),
        reply("req_2", "\"success\":false,\"error\":\"nope\""),
    );
    let (process, state) = Process::start(&fake_pi(&script)).unwrap();
    assert_eq!(state.session_id, "s1");
    let inbound = process.inbound();
    std::thread::spawn(move || while inbound.recv().is_ok() {});
    let refused = process.call(&get_state(), true).unwrap_err();
    assert_eq!(
        refused.to_string(),
        "pi rpc error for req_2 (get_state): nope"
    );
    assert!(matches!(refused, Error::Remote { .. }));
    assert_eq!(process.close(), Ok(()));
    assert!(process.call(&get_state(), true).is_err());
}

#[test]
fn the_handshake_state_ignores_members_the_pin_does_not_name() {
    let _serial = serial();
    let state = STATE.replacen('{', r#"{"newField":{"x":1},"#, 1);
    let script = format!(
        "{}; cat >/dev/null",
        reply("req_1", &format!("\"success\":true,\"data\":{state}"))
    );
    let (process, state) = Process::start(&fake_pi(&script)).unwrap();
    assert_eq!(state.session_id, "s1");
    assert_eq!(process.close(), Ok(()));
}

#[test]
fn a_pi_that_exits_before_its_state_fails_the_handshake_with_its_stderr() {
    let _serial = serial();
    let failure = Process::start(&fake_pi("echo 'token=abc broke' >&2; exit 3"))
        .err()
        .unwrap()
        .to_string();
    assert!(
        failure.starts_with("pi rpc: startup handshake failed: "),
        "{failure}"
    );
    assert!(
        failure.ends_with("stderr: token=[REDACTED] broke"),
        "{failure}"
    );
}

#[test]
fn an_invalid_state_fails_the_handshake() {
    let _serial = serial();
    let script = format!(
        "{}; cat >/dev/null",
        reply("req_1", "\"success\":true,\"data\":{\"sessionId\":\"\"}")
    );
    let failure = Process::start(&fake_pi(&script)).err().unwrap().to_string();
    assert!(
        failure.starts_with("pi rpc: startup handshake failed: invalid get_state response"),
        "{failure}"
    );
}
