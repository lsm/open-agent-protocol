#![cfg(unix)]

use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard};
use std::time::Duration;

use oap_adapter::{Error, OpenRequest};
use oap_adapter_hermes::adapter::{Adapter, Config};
use oap_adapter_hermes::client::{ClientFactory, ProcessFactory, PENDING_AUTO_CONTINUE};
use oap_adapter_hermes::process::ProcessConfig;
use oap_protocol::Participant;

const READY: &str = r#"{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}"#;
const DRAIN: &str = "while IFS= read -r line; do :; done\n";
const RESUMED: &str = r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0002","resumed":"st","message_count":3,"info":{"model":"m2"},"running":false,"status":"idle","auto_continue":null,"extra":1}}"#;

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn probe(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("oap-hermes-reopen-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join(name);
    let _ = std::fs::remove_file(&path);
    path
}

fn gateway(probe: &Path, reply: &str) -> ProcessConfig {
    let rest = if reply.is_empty() {
        "exit 3\n".to_owned()
    } else {
        format!("printf '%s\\n' '{reply}'\n{DRAIN}")
    };
    ProcessConfig {
        path: "/bin/sh".into(),
        args: vec![
            "-c".into(),
            format!("printf '%s\\n' '{READY}'\nIFS= read -r line\nprintf '%s' \"$line\" > \"$0\"\n{rest}"),
            probe.to_string_lossy().into_owned(),
        ],
        env: Some(vec!["PATH=/usr/bin:/bin".into()]),
        exit_timeout: Some(Duration::from_secs(1)),
        ..ProcessConfig::default()
    }
}

fn sent(probe: &Path) -> String {
    std::fs::read_to_string(probe).unwrap()
}

fn adapter(config: ProcessConfig) -> Adapter {
    Adapter::new(Config {
        process: config,
        model: "hermes-test".into(),
        runtime: oap_adapter::runtime::Runtime::new(|| 42, |kind| format!("{kind}-minted")),
        ..Config::default()
    })
    .unwrap()
}

fn reopen(stored: &str) -> OpenRequest {
    OpenRequest {
        session_id: "s".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        reopen: true,
        native_session_id: stored.into(),
        ..OpenRequest::default()
    }
}

fn detail(error: Error) -> String {
    match error {
        Error::UnsupportedControl(control) => control.detail,
        other => panic!("not a control refusal: {other:?}"),
    }
}

#[test]
fn a_reopen_resumes_the_stored_session_and_binds_to_it() {
    let _serial = serial();
    let probe = probe("ok");
    let factory = ProcessFactory {
        config: gateway(&probe, RESUMED),
        model: "hermes-test".into(),
        ..ProcessFactory::default()
    };
    let (client, resumed) = factory.reopen("st", None).unwrap();
    assert_eq!(
        sent(&probe),
        r#"{"id":1,"jsonrpc":"2.0","method":"session.resume","params":{"session_id":"st"}}"#
    );
    assert_eq!(resumed.session_id, "sess0002");
    assert_eq!(client.stored_session_id(), "st");
    assert_eq!(
        resumed.info.as_deref().map(|raw| raw.get()),
        Some(r#"{"model":"m2"}"#)
    );
    assert_eq!(client.close(), Ok(()));
    let session = adapter(gateway(&probe, RESUMED))
        .open(reopen("st"))
        .unwrap();
    assert_eq!(
        serde_json::to_string(&session.state().unwrap()).unwrap(),
        r#"{"session_id":"s","status":"idle","current_model_id":"m2","updated_at_ms":42,"recovery":{"recovered":true,"reason":"Hermes restored the bound conversation and reports the model it last ran under; OAP runs and cursors remain process-local"}}"#
    );
    assert_eq!(session.native_session_id(), "st");
    assert_eq!(session.close(), Ok(()));
}

#[test]
fn a_resume_hermes_cannot_take_safely_is_refused_with_go_s_text() {
    let _serial = serial();
    let running = "Hermes reattached the stored session while it was ";
    for (name, stored, reply, want) in [
        (
            "notfound",
            "st<1>",
            r#"{"jsonrpc":"2.0","id":1,"error":{"code":4007,"message":"session not found"}}"#,
            "Hermes has no stored session \"st<1>\"".to_owned(),
        ),
        (
            "remote",
            "st",
            r#"{"jsonrpc":"2.0","id":1,"error":{"code":4001,"message":"nope"}}"#,
            "hermes rpc error 4001 for request 1: nope".to_owned(),
        ),
        (
            "noid",
            "st",
            r#"{"jsonrpc":"2.0","id":1,"result":{"resumed":"st"}}"#,
            "Hermes omitted the reloaded session id".to_owned(),
        ),
        (
            "auto",
            "st",
            r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0002","auto_continue":{"turn":1}}}"#,
            PENDING_AUTO_CONTINUE.to_owned(),
        ),
        (
            "autostr",
            "st",
            r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0002","auto_continue":""}}"#,
            PENDING_AUTO_CONTINUE.to_owned(),
        ),
        (
            "autofalse",
            "st",
            r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0002","auto_continue":false}}"#,
            PENDING_AUTO_CONTINUE.to_owned(),
        ),
        (
            "running",
            "st",
            r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0002","running":true,"status":"busy"}}"#,
            format!("{running}running"),
        ),
        (
            "busy",
            "st",
            r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0002","status":"busy"}}"#,
            format!("{running}busy"),
        ),
        ("exit", "st", "", "EOF".to_owned()),
    ] {
        let probe = probe(name);
        let factory = ProcessFactory {
            config: gateway(&probe, reply),
            model: "hermes-test".into(),
            ..ProcessFactory::default()
        };
        let Err(refused) = factory.reopen(stored, None) else {
            panic!("{name}: the resume was accepted");
        };
        assert_eq!(refused, want, "{name}");
        assert_eq!(
            sent(&probe),
            format!(
                r#"{{"id":1,"jsonrpc":"2.0","method":"session.resume","params":{{"session_id":"{}"}}}}"#,
                stored.replace('<', "\\u003c").replace('>', "\\u003e")
            ),
            "{name}"
        );
    }
    let probe = probe("string");
    let factory = ProcessFactory {
        config: gateway(&probe, r#"{"jsonrpc":"2.0","id":1,"result":"x"}"#),
        model: String::new(),
        ..ProcessFactory::default()
    };
    let Err(refused) = factory.reopen("st", None) else {
        panic!("the resume was accepted");
    };
    assert!(
        refused.starts_with("decode session.resume response: "),
        "{refused}"
    );
}

#[test]
fn an_open_reports_a_failed_reload_as_an_unsatisfiable_reopen() {
    let _serial = serial();
    let probe = probe("nf2");
    let refused = adapter(gateway(
        &probe,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":4007,"message":"session not found"}}"#,
    ))
    .open(reopen("st"))
    .err()
    .unwrap();
    assert_eq!(
        refused.to_string(),
        "adapter: unsupported input: session.open.reopen (unsatisfiable): Hermes could not reload its binding: Hermes has no stored session \"st\""
    );
    let mut launch = gateway(&probe, "");
    launch.args[1] = "exit 4".into();
    assert_eq!(
        detail(adapter(launch).open(reopen("st")).err().unwrap()),
        "Hermes could not reload its binding: hermes rpc: gateway ready handshake failed: EOF; stderr: "
    );
}
