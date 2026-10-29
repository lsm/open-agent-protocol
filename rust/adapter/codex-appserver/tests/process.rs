use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, Instant};

use oap_adapter_codex_appserver::process::{
    ClientInfo, InitializeCapabilities, Process, ProcessConfig,
};

static SPAWNING: Mutex<()> = Mutex::new(());

fn serial() -> MutexGuard<'static, ()> {
    SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn server(script: &str) -> ProcessConfig {
    ProcessConfig {
        path: "/bin/sh".into(),
        args: vec!["-c".into(), script.into(), "codex".into()],
        env: Some(vec!["PATH=/usr/bin:/bin".into()]),
        shutdown_timeout: Some(Duration::from_secs(2)),
        client_info: ClientInfo {
            name: "open-agent-protocol".into(),
            version: "0.1".into(),
            ..ClientInfo::default()
        },
        ..ProcessConfig::default()
    }
}

fn deadline() -> Option<Instant> {
    Some(Instant::now() + Duration::from_secs(5))
}

#[test]
fn the_handshake_speaks_the_unversioned_wire_and_announces_initialized() {
    let _serial = serial();
    let script = r#"read request; printf '%s\n' "$request" >&2; printf '%s\n' '{"id":0,"result":{"userAgent":"fixture","codexHome":"/h","platformFamily":"unix","platformOs":"macos"}}'; read notice; printf '%s\n' "$notice" >&2; printf '%s' "$*" >&2; cat >/dev/null"#;
    let process = Process::start(&server(script), deadline()).unwrap();
    assert_eq!(process.initialize.user_agent, "fixture");
    assert_eq!(process.close(), Ok(()));
    assert_eq!(
        process.stderr(),
        "{\"id\":0,\"method\":\"initialize\",\"params\":{\"clientInfo\":{\"name\":\"open-agent-protocol\",\"version\":\"0.1\"}}}\n{\"method\":\"initialized\"}\napp-server --listen stdio://"
    );
}

#[test]
fn a_server_that_exits_before_answering_fails_the_handshake_with_its_stderr() {
    let _serial = serial();
    let failure = Process::start(&server("echo 'boom token=abc' >&2; exit 3"), deadline())
        .err()
        .unwrap()
        .to_string();
    assert!(
        failure.starts_with("codex app-server rpc: initialization handshake failed: "),
        "{failure}"
    );
    assert!(
        failure.ends_with("stderr: boom token=[REDACTED]"),
        "{failure}"
    );
}

#[test]
fn client_info_is_required() {
    let config = ProcessConfig {
        client_info: ClientInfo::default(),
        ..server("exit 0")
    };
    let failure = Process::start(&config, deadline())
        .err()
        .unwrap()
        .to_string();
    assert_eq!(
        failure,
        "codex app-server rpc: initialization handshake failed: executable path and client info are required"
    );
}

#[test]
fn a_null_initialize_result_completes_the_handshake_with_an_empty_response() {
    let _serial = serial();
    let script =
        r#"read request; printf '%s\n' '{"id":0,"result":null}'; read notice; cat >/dev/null"#;
    let process = Process::start(&server(script), deadline()).unwrap();
    assert_eq!(process.initialize.user_agent, "");
    assert_eq!(process.initialize.codex_home, "");
    assert_eq!(process.close(), Ok(()));
}

#[test]
fn close_bounds_a_forced_shutdown_when_a_descendant_holds_the_pipes() {
    let _serial = serial();
    let script =
        r#"read request; printf '%s\n' '{"id":0,"result":{}}'; read notice; sleep 5 & exit 0"#;
    let config = ProcessConfig {
        shutdown_timeout: Some(Duration::from_secs(1)),
        ..server(script)
    };
    let process = Process::start(&config, deadline()).unwrap();
    let (sender, receiver) = std::sync::mpsc::channel();
    let started = Instant::now();
    std::thread::spawn(move || {
        let _ = sender.send(process.close());
    });
    let outcome = receiver
        .recv_timeout(Duration::from_secs(4))
        .expect("close blocked while a descendant held the pipes open");
    assert_eq!(
        outcome,
        Err("codex app-server rpc: shutdown timed out".to_string())
    );
    assert!(
        started.elapsed() < Duration::from_millis(1700),
        "{:?}",
        started.elapsed()
    );
}

#[test]
fn configured_capabilities_ride_in_the_initialize_params() {
    let _serial = serial();
    let script = r#"read request; printf '%s\n' "$request" >&2; printf '%s\n' '{"id":0,"result":{}}'; read notice; cat >/dev/null"#;
    let config = ProcessConfig {
        capabilities: Some(InitializeCapabilities {
            experimental_api: true,
            opt_out_notification_methods: vec!["item/started".into()],
            extensions: [(
                "z".to_string(),
                serde_json::value::RawValue::from_string("{\"on\":true}".into()).unwrap(),
            )]
            .into(),
            ..InitializeCapabilities::default()
        }),
        ..server(script)
    };
    let process = Process::start(&config, deadline()).unwrap();
    assert_eq!(process.close(), Ok(()));
    assert_eq!(
        process.stderr(),
        "{\"id\":0,\"method\":\"initialize\",\"params\":{\"clientInfo\":{\"name\":\"open-agent-protocol\",\"version\":\"0.1\"},\"capabilities\":{\"experimentalApi\":true,\"optOutNotificationMethods\":[\"item/started\"],\"extensions\":{\"z\":{\"on\":true}}}}}"
    );
}
