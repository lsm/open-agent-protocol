use std::time::{Duration, Instant};

use oxap_adapter_acp::process::{Implementation, Process, ProcessConfig};

fn server(script: &str) -> ProcessConfig {
    ProcessConfig {
        path: "/bin/sh".into(),
        args: vec!["-c".into(), script.into()],
        env: Some(vec!["PATH=/usr/bin:/bin".into()]),
        shutdown_timeout: Some(Duration::from_millis(200)),
        ..ProcessConfig::default()
    }
}

fn start(config: &ProcessConfig) -> Result<Process, String> {
    Process::start(config, Some(Instant::now() + Duration::from_secs(3)))
        .map_err(|error| error.to_string())
}

fn answer(result: &str) -> String {
    format!(
        r#"read -r request; printf '%s\n' "$request" >&2; printf '%s\n' '{{"jsonrpc":"2.0","id":1,"result":{result}}}'; cat >/dev/null"#
    )
}

#[test]
fn initialize_has_version_one_empty_capabilities_and_no_extra_notification() {
    let process = start(&server(&answer(r#"{"protocolVersion":1,"agentCapabilities":{},"authMethods":[null,{"id":"login"}],"agentInfo":{"name":"fake","version":"1","title":null}}"#))).unwrap();
    assert_eq!(process.initialize.protocol_version, 1);
    assert_eq!(process.initialize.auth_methods.len(), 2);
    assert_eq!(process.initialize.agent_info.as_ref().unwrap().name, "fake");
    assert_eq!(process.close(), Ok(()));
    assert_eq!(process.close(), Ok(()));
    assert_eq!(
        process.stderr(),
        r#"{"id":1,"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":1,"clientCapabilities":{}}}"#
    );
}

#[test]
fn configured_capabilities_and_info_keep_go_wire_escaping() {
    let config = ProcessConfig {
        client_capabilities: [(
            "terminal".into(),
            serde_json::value::RawValue::from_string("true".into()).unwrap(),
        )]
        .into(),
        client_info: Some(Implementation {
            name: "a<&>".into(),
            title: "fixture".into(),
            version: "0.1".into(),
        }),
        ..server(&answer(
            r#"{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}"#,
        ))
    };
    let process = start(&config).unwrap();
    assert_eq!(
        process.initialize.agent_capabilities.as_ref().unwrap()["loadSession"].get(),
        "true"
    );
    process.close().unwrap();
    let wire = process.stderr();
    assert!(wire.contains(r#""name":"a\u003c\u0026\u003e""#), "{wire}");
    assert!(
        wire.contains(r#""clientCapabilities":{"terminal":true}"#),
        "{wire}"
    );
}

#[test]
fn invalid_configuration_is_refused_before_spawning() {
    for (config, expected) in [
        (ProcessConfig::default(), "executable path is required"),
        (
            ProcessConfig {
                protocol_version: 2,
                ..server("exit 99")
            },
            "unsupported requested protocol version 2",
        ),
        (
            ProcessConfig {
                client_info: Some(Implementation::default()),
                ..server("exit 99")
            },
            "client info name and version are required",
        ),
    ] {
        assert_eq!(
            start(&config).err().unwrap(),
            format!("acp rpc: initialization handshake failed: {expected}")
        );
    }
}

#[test]
fn invalid_handshakes_are_refused_and_children_are_reaped() {
    for (result, expected) in [
        ("null", "unsupported protocol version 0"),
        (
            r#"{"protocolVersion":2,"agentCapabilities":{}}"#,
            "unsupported protocol version 2",
        ),
        (r#"{"protocolVersion":1}"#, "agentCapabilities is required"),
        (
            r#"{"protocolVersion":1,"agentCapabilities":null}"#,
            "agentCapabilities is required",
        ),
        (
            r#"{"protocolVersion":1,"agentCapabilities":[]}"#,
            "decode initialize response",
        ),
        (
            r#"{"protocolVersion":1,"agentCapabilities":{},"agentInfo":[]}"#,
            "decode initialize response",
        ),
    ] {
        let failure = start(&server(&answer(result))).err().unwrap();
        assert!(failure.contains(expected), "{result}: {failure}");
    }
}

#[test]
fn early_exit_redacts_stderr_and_deadlines_abort_a_quiet_child() {
    let failure = start(&server(
        "read -r request; echo 'boom token=synthetic' >&2; exit 3",
    ))
    .err()
    .unwrap();
    assert!(
        failure.contains("stderr: boom token=[REDACTED]"),
        "{failure}"
    );
    let failure = Process::start(
        &server("read -r request; read -r waiting"),
        Some(Instant::now() + Duration::from_millis(100)),
    )
    .err()
    .unwrap()
    .to_string();
    assert!(failure.contains("context deadline exceeded"), "{failure}");
}

#[test]
fn close_preserves_exit_status_and_bounds_an_uncooperative_child() {
    let script = answer(r#"{"protocolVersion":1,"agentCapabilities":{}}"#);
    let process = start(&server(&(script.clone() + "; exit 7"))).unwrap();
    assert_eq!(process.close(), Err("exit status 7".into()));
    let process = start(&server(&(script + "; exec cat /dev/zero >/dev/null"))).unwrap();
    let began = Instant::now();
    assert_eq!(process.close(), Err("acp rpc: shutdown timed out".into()));
    assert!(began.elapsed() < Duration::from_secs(2));
}
