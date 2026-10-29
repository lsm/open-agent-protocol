#![cfg(unix)]

use std::io::{BufRead, BufReader, Write};
use std::sync::{Mutex, MutexGuard};
use std::time::Duration;

use oap_adapter::process::{redact, wait_error, Child, ChildConfig};

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn shell(script: &str) -> ChildConfig {
    ChildConfig {
        path: "/bin/sh".into(),
        args: vec!["-c".into(), script.into()],
        env: Some(vec!["PATH=/usr/bin:/bin".into()]),
        ..ChildConfig::default()
    }
}

fn watched(child: &Child) {
    let (reader, reader_done) = crossbeam_channel::bounded::<()>(0);
    drop(reader);
    child.watch(reader_done, Duration::from_secs(2));
}

#[test]
fn a_child_echoes_through_the_shared_stdin_and_exits_when_it_is_released() {
    let _serial = serial();
    let (child, stdout) = Child::spawn(&shell(
        "read line; printf '%s\\n' \"$line\"; cat >/dev/null",
    ))
    .unwrap();
    let mut writer = child.writer();
    writer.write_all(b"hello\n").unwrap();
    writer.flush().unwrap();
    let mut echoed = String::new();
    BufReader::new(stdout).read_line(&mut echoed).unwrap();
    assert_eq!(echoed, "hello\n");
    watched(&child);
    child.release_stdin();
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(wait_error(child.exit_status()), None);
    assert!(writer.write_all(b"late\n").is_err());
}

#[test]
fn a_nonzero_exit_reads_as_go_s_exec_error_and_stderr_is_redacted() {
    let _serial = serial();
    let (child, _stdout) = Child::spawn(&shell("echo 'boom token=abc123' >&2; exit 3")).unwrap();
    watched(&child);
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(
        wait_error(child.exit_status()).as_deref(),
        Some("exit status 3")
    );
    assert_eq!(child.stderr(), "boom token=[REDACTED]");
}

#[test]
fn stderr_past_its_limit_is_marked_truncated() {
    let _serial = serial();
    let config = ChildConfig {
        stderr_limit: 4,
        ..shell("printf 'abcdefgh' >&2")
    };
    let (child, _stdout) = Child::spawn(&config).unwrap();
    watched(&child);
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(child.stderr(), "abcd [truncated]");
}

#[test]
fn an_interrupted_child_reports_the_signal_and_a_killed_one_too() {
    let _serial = serial();
    let (child, _stdout) = Child::spawn(&shell("exec sleep 30")).unwrap();
    watched(&child);
    assert!(!child.await_exit(Duration::from_millis(100)));
    child.interrupt();
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(
        wait_error(child.exit_status()).as_deref(),
        Some("signal: interrupt")
    );
    let (child, _stdout) = Child::spawn(&shell("exec sleep 30")).unwrap();
    watched(&child);
    child.kill();
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(
        wait_error(child.exit_status()).as_deref(),
        Some("signal: killed")
    );
}

#[test]
fn a_child_sees_only_the_environment_it_was_given() {
    let _serial = serial();
    let config = ChildConfig {
        env: Some(vec!["ONLY=1".into()]),
        ..shell("printf '%s' \"$ONLY:$HOME\" >&2")
    };
    let (child, _stdout) = Child::spawn(&config).unwrap();
    watched(&child);
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(child.stderr(), "1:");
}

#[test]
fn redaction_hides_secret_values_in_both_spellings() {
    assert_eq!(
        redact("Authorization: Bearer sk-1 and \"api_key\": \"v\""),
        "Authorization: [REDACTED] and \"api_key\": \"[REDACTED]\""
    );
}

#[test]
fn a_signal_reads_as_go_names_it_on_this_platform() {
    let _serial = serial();
    let (child, _stdout) = Child::spawn(&shell("kill -USR1 $$")).unwrap();
    watched(&child);
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(
        wait_error(child.exit_status()).as_deref(),
        Some("signal: user defined signal 1")
    );
}

#[test]
fn an_aborted_child_reads_as_go_names_sigabrt_on_this_platform() {
    let _serial = serial();
    let (child, _stdout) = Child::spawn(&shell("ulimit -c 0; kill -ABRT $$")).unwrap();
    watched(&child);
    assert!(child.await_exit(Duration::from_secs(5)));
    let want = if cfg!(target_os = "linux") {
        "signal: aborted"
    } else {
        "signal: abort trap"
    };
    assert_eq!(wait_error(child.exit_status()).as_deref(), Some(want));
}

#[test]
fn a_killed_child_is_reaped_while_a_grandchild_still_holds_its_stdout() {
    let _serial = serial();
    let (child, _stdout) = Child::spawn(&shell("sleep 5 & wait")).unwrap();
    let (reader, reader_done) = crossbeam_channel::bounded::<()>(0);
    child.watch(reader_done, Duration::from_millis(100));
    assert!(!child.await_exit(Duration::from_millis(200)));
    child.kill();
    assert!(child.await_exit(Duration::from_secs(5)));
    assert_eq!(
        wait_error(child.exit_status()).as_deref(),
        Some("signal: killed")
    );
    drop(reader);
}

#[test]
fn releasing_stdin_returns_while_a_write_is_blocked_on_a_full_pipe_and_the_write_fails_once_the_child_dies(
) {
    let _serial = serial();
    let (child, _stdout) = Child::spawn(&shell("sleep 30")).unwrap();
    let mut writer = child.writer();
    let (wrote, written) = crossbeam_channel::bounded(1);
    std::thread::spawn(move || {
        let _ = wrote.send(writer.write_all(&vec![b'x'; 8 << 20]));
    });
    assert!(written.recv_timeout(Duration::from_millis(300)).is_err());
    let (released, release_done) = crossbeam_channel::bounded(1);
    let releaser = child.clone();
    std::thread::spawn(move || {
        releaser.release_stdin();
        let _ = released.send(());
    });
    assert!(release_done.recv_timeout(Duration::from_secs(2)).is_ok());
    watched(&child);
    child.kill();
    assert!(written
        .recv_timeout(Duration::from_secs(5))
        .unwrap()
        .is_err());
    assert!(child.await_exit(Duration::from_secs(5)));
    assert!(child.writer().write_all(b"late\n").is_err());
}
