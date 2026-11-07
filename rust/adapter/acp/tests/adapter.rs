use futures::executor::block_on;
use oap_adapter::{Adapter as AdapterContract, Error, OpenRequest};
use oap_adapter_acp::{native::McpServer, pin, process::ProcessConfig, Adapter, Config};
use oap_protocol::{Participant, SupportLevel, ToolDefinition, ToolSourceAttachment};

fn config() -> Config {
    Config {
        process: ProcessConfig {
            path: "/synthetic/acp-not-executed".into(),
            dir: std::env::temp_dir().to_string_lossy().into(),
            ..ProcessConfig::default()
        },
        ..Config::default()
    }
}

fn request() -> OpenRequest {
    OpenRequest {
        participant: Participant {
            id: "tester".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

#[test]
fn invalid_configuration_is_refused() {
    for (configured, text) in [
        (Config::default(), "executable is required"),
        (
            Config {
                process: ProcessConfig {
                    dir: "relative".into(),
                    ..config().process
                },
                ..Config::default()
            },
            "absolute working directory is required",
        ),
        (
            Config {
                mcp_servers: vec![McpServer::default()],
                ..config()
            },
            "needs a name",
        ),
        (
            Config {
                mcp_servers: vec![
                    McpServer {
                        name: "x".into(),
                        ..McpServer::default()
                    };
                    2
                ],
                ..config()
            },
            "\"x\" is configured twice",
        ),
    ] {
        assert!(Adapter::new(configured)
            .err()
            .unwrap()
            .to_string()
            .contains(text));
    }
}

#[test]
fn the_public_probe_reads_the_pin_and_discloses_staged_support_honestly() {
    let adapter = Adapter::new(Config {
        mcp_servers: vec![McpServer {
            name: "configured".into(),
            ..McpServer::default()
        }],
        ..config()
    })
    .unwrap();
    let descriptor = block_on(adapter.probe()).unwrap();
    assert_eq!(
        descriptor.capabilities.endpoint.version,
        pin().endpoint_version
    );
    assert_eq!(
        descriptor.capability_revision,
        format!("{}-rust-result", pin().capability_revision)
    );
    assert_eq!(descriptor.capabilities.sources[0].id, "configured");
    assert_eq!(descriptor.journal.replay, SupportLevel::UNAVAILABLE);
    assert!(!descriptor.interactive_gates);
    assert_eq!(descriptor.max_active_runs_per_session, 1);
    for (key, support) in descriptor.capabilities.features {
        let expected = match key.as_str() {
            "protocol.initialize"
            | "capabilities"
            | "session.state"
            | "session.message.delivery.auto"
            | "run.status" => SupportLevel::EMULATED,
            "session.open" => SupportLevel::NATIVE,
            "models.list" | "session.model.switch" | "session.message.submit" | "run.streaming" => {
                SupportLevel::DEGRADED
            }
            _ => SupportLevel::UNAVAILABLE,
        };
        assert_eq!(support.level, expected, "{key}");
    }
}

#[test]
fn public_open_and_admission_return_typed_control_refusals() {
    let adapter = Adapter::new(config()).unwrap();
    assert!(block_on(adapter.admitter().unwrap().admit(&request())).is_ok());
    for (request, feature) in [
        (
            OpenRequest {
                reopen: true,
                ..request()
            },
            "session.open.reopen",
        ),
        (
            OpenRequest {
                reasoning_level: "high".into(),
                ..request()
            },
            "session.reasoning",
        ),
        (
            OpenRequest {
                tool_sources: vec![ToolSourceAttachment::default()],
                ..request()
            },
            "action.tool_sources.attach",
        ),
        (
            OpenRequest {
                tools: vec![ToolDefinition::default()],
                ..request()
            },
            "action.tools.provide",
        ),
    ] {
        let admitted = block_on(adapter.admitter().unwrap().admit(&request))
            .err()
            .unwrap();
        let opened = block_on(adapter.open(request)).err().unwrap();
        assert_eq!(admitted, opened);
        let Error::UnsupportedControl(control) = opened else {
            panic!("unexpected {opened}")
        };
        assert_eq!(control.feature, feature);
        assert_eq!(control.reason, "unadvertised");
    }
    assert_eq!(
        block_on(adapter.open(OpenRequest::default()))
            .err()
            .unwrap(),
        Error::InvalidParticipant
    );
}
