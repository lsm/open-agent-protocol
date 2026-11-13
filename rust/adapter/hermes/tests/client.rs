#![cfg(unix)]

use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard};
use std::time::Duration;

use crossbeam_channel::RecvTimeoutError;
use oap_adapter::jsonrpc::Inbound;
use oap_adapter_hermes::client::{ClientFactory, ProcessFactory, NATIVE_PROTOCOL};
use oap_adapter_hermes::process::ProcessConfig;

const READY: &str = r#"{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}"#;
const EVENT: &str = r#"{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"sess0001","seq":1}}"#;
const DRAIN: &str = "while IFS= read -r line; do :; done\nprintf closed > \"$0.closed\"\n";

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn probe(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("oap-hermes-client-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join(name);
    let _ = std::fs::remove_file(&path);
    let _ = std::fs::remove_file(closed(&path));
    path
}

fn closed(probe: &Path) -> PathBuf {
    probe.with_extension("closed")
}

fn factory(probe: &Path, model: &str, reply: &str, rest: &str) -> ProcessFactory {
    let script = format!(
        "set -eu\nprintf '%s\\n' '{READY}'\nIFS= read -r line\nprintf '%s' \"$line\" > \"$0\"\nprintf '%s\\n' '{reply}'\n{rest}"
    );
    ProcessFactory {
        config: ProcessConfig {
            path: "/bin/sh".into(),
            args: vec!["-c".into(), script, probe.to_string_lossy().into_owned()],
            env: Some(vec!["PATH=/usr/bin:/bin".into()]),
            exit_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        model: model.into(),
        ..ProcessFactory::default()
    }
}

fn sent(probe: &Path) -> String {
    std::fs::read_to_string(probe).unwrap()
}

#[test]
fn a_created_session_carries_both_ids_and_relays_what_follows() {
    let _serial = serial();
    let probe = probe("ok");
    let reply = r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0001","stored_session_id":"st1","extra":1}}"#;
    let (client, id) = factory(
        &probe,
        "m",
        reply,
        &format!("printf '%s\\n' '{EVENT}'\nwhile IFS= read -r line; do :; done\nexit 3\n"),
    )
    .start_with_reasoning("high", None)
    .unwrap();
    assert_eq!(
        sent(&probe),
        r#"{"id":1,"jsonrpc":"2.0","method":"session.create","params":{"model":"m","reasoning_effort":"high"}}"#
    );
    assert_eq!(id, "sess0001");
    assert_eq!(client.stored_session_id(), "st1");
    let inbound = client.inbound();
    match inbound.recv_timeout(Duration::from_secs(5)) {
        Ok(Inbound::Notification(notification)) => assert_eq!(notification.method, "event"),
        _ => panic!("the event after session.create was not relayed"),
    }
    assert_eq!(client.close(), Err("exit status 3".into()));
    assert_eq!(
        client.done().recv_timeout(Duration::from_secs(5)),
        Err(RecvTimeoutError::Disconnected)
    );
    assert_eq!(client.err().as_deref(), Some("exit status 3"));
    assert!(matches!(
        inbound.recv_timeout(Duration::from_secs(5)),
        Err(RecvTimeoutError::Disconnected)
    ));
}

#[test]
fn a_bare_start_sends_empty_params_and_reports_eof_after_a_clean_exit() {
    let _serial = serial();
    let probe = probe("bare");
    let reply =
        r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"sess0001","stored_session_id":"st1"}}"#;
    let (client, id) = factory(&probe, "", reply, DRAIN).start(None).unwrap();
    assert_eq!(
        sent(&probe),
        r#"{"id":1,"jsonrpc":"2.0","method":"session.create","params":{}}"#
    );
    assert_eq!(id, "sess0001");
    assert_eq!(client.close(), Ok(()));
    assert_eq!(client.err().as_deref(), Some("EOF"));
}

#[test]
fn a_create_that_names_no_session_is_refused_and_the_gateway_closed() {
    let _serial = serial();
    for (name, reply) in [
        (
            "nostored",
            r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":"s"}}"#,
        ),
        (
            "noid",
            r#"{"jsonrpc":"2.0","id":1,"result":{"stored_session_id":"st"}}"#,
        ),
        ("null", r#"{"jsonrpc":"2.0","id":1,"result":null}"#),
    ] {
        let probe = probe(name);
        let Err(refused) = factory(&probe, "m", reply, DRAIN).start(None) else {
            panic!("{name}: the create was accepted");
        };
        assert_eq!(refused.to_string(), NATIVE_PROTOCOL, "{name}");
        assert_eq!(
            sent(&probe),
            r#"{"id":1,"jsonrpc":"2.0","method":"session.create","params":{"model":"m"}}"#
        );
        assert!(closed(&probe).exists(), "{name}: the gateway was left open");
    }
}

#[test]
fn a_create_the_gateway_fails_reports_go_s_text_and_closes_the_gateway() {
    let _serial = serial();
    for (name, reply, want) in [
        (
            "remote",
            r#"{"jsonrpc":"2.0","id":1,"error":{"code":4001,"message":"nope","data":{"a":1}}}"#,
            "hermes rpc error 4001 for request 1: nope",
        ),
        (
            "string",
            r#"{"jsonrpc":"2.0","id":1,"result":"x"}"#,
            "decode session.create response: ",
        ),
        (
            "badtype",
            r#"{"jsonrpc":"2.0","id":1,"result":{"session_id":7,"stored_session_id":"st"}}"#,
            "decode session.create response: ",
        ),
    ] {
        let probe = probe(name);
        let Err(refused) = factory(&probe, "m", reply, DRAIN).start(None) else {
            panic!("{name}: the create was accepted");
        };
        let refused = refused.to_string();
        assert!(refused.starts_with(want), "{name}: {refused}");
        assert_ne!(refused, NATIVE_PROTOCOL, "{name}");
        assert!(closed(&probe).exists(), "{name}: the gateway was left open");
    }
}

#[test]
fn a_gateway_that_exits_instead_of_answering_fails_the_create_at_eof() {
    let _serial = serial();
    let probe = probe("exit");
    let mut factory = factory(&probe, "m", "", "exit 3\n");
    factory.config.args[1] = factory.config.args[1].replace("printf '%s\\n' ''\n", "");
    let Err(refused) = factory.start(None) else {
        panic!("the create was accepted");
    };
    assert_eq!(refused.to_string(), "EOF");
}
