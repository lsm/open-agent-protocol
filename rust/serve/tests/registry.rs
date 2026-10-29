use std::sync::Arc;

use oap_adapter::memory::{Config, Memory};
use oap_protocol::{ToolSourceAttachment, TOOL_SOURCE_PROCESS, TOOL_SOURCE_REMOTE};
use oap_serve::{Registry, RegistryError};

fn memory() -> Arc<Memory> {
    Arc::new(Memory::new(Config::default()))
}

#[test]
fn the_default_registry_serves_memory_alone() {
    let registry = Registry::default_registry();
    assert_eq!(registry.names(), ["memory"]);
    assert!(registry.lookup("memory").is_some());
    assert!(registry.lookup("claude").is_none());
}

#[test]
fn a_name_is_registered_once_and_never_empty() {
    let mut registry = Registry::new();
    registry.register("memory", memory()).unwrap();
    assert!(matches!(
        registry.register("memory", memory()),
        Err(RegistryError::AdapterExists(name)) if name == "memory"
    ));
    assert!(matches!(
        registry.register("", memory()),
        Err(RegistryError::NoAdapterName)
    ));
    registry.register("b", memory()).unwrap();
    assert_eq!(registry.names(), ["b", "memory"]);
}

#[test]
fn a_working_directory_is_set_and_cleared_per_adapter() {
    let mut registry = Registry::default_registry();
    assert_eq!(registry.working_directory("memory"), "");
    registry.set_working_directory("memory", "/work");
    assert_eq!(registry.working_directory("memory"), "/work");
    registry.set_working_directory("memory", "");
    assert_eq!(registry.working_directory("memory"), "");
}

fn source(kind: &str, command: &str, environment: &[&str]) -> ToolSourceAttachment {
    ToolSourceAttachment {
        kind: kind.into(),
        command: command.into(),
        environment: environment.iter().map(|name| (*name).into()).collect(),
        ..ToolSourceAttachment::default()
    }
}

#[test]
fn register_tool_source_judges_the_entry_the_loader_would_have_judged() {
    for (name, refused, want) in [
        (
            "no kind",
            source("", "/usr/local/bin/mcp-filesystem", &[]),
            "kind is required",
        ),
        (
            "a kind outside the protocol",
            source("bogus", "", &[]),
            "kind \"bogus\" is not a tool source kind",
        ),
        (
            "one variable named twice",
            source(TOOL_SOURCE_REMOTE, "", &["TOKEN", "TOKEN=literal"]),
            "environment names \"TOKEN\" twice",
        ),
        (
            "a process source with no command",
            source(TOOL_SOURCE_PROCESS, "", &[]),
            "a process source needs a command",
        ),
    ] {
        let mut registry = Registry::new();
        let err = registry
            .register_tool_source("filesystem", refused)
            .unwrap_err();
        assert!(err.to_string().contains(want), "{name}: {err}");
        assert!(registry.tool_source("filesystem").is_none(), "{name}");
    }

    let mut registry = Registry::new();
    assert!(matches!(
        registry.register_tool_source("", source(TOOL_SOURCE_REMOTE, "", &[])),
        Err(RegistryError::NoToolSourceId)
    ));
    registry
        .register_tool_source(
            "remote-tools",
            ToolSourceAttachment {
                endpoint: "https://tools.example".into(),
                ..source(TOOL_SOURCE_REMOTE, "", &[])
            },
        )
        .unwrap();
    registry
        .register_tool_source(
            "filesystem",
            source(TOOL_SOURCE_PROCESS, "/usr/local/bin/mcp-filesystem", &[]),
        )
        .unwrap();
    assert_eq!(registry.tool_source_names(), ["filesystem", "remote-tools"]);
    assert_eq!(registry.tool_source("filesystem").unwrap().id, "filesystem");
    assert!(registry
        .register_tool_source("filesystem", source(TOOL_SOURCE_PROCESS, "/bin/other", &[]))
        .unwrap_err()
        .to_string()
        .contains("already registered"));
}
