use std::path::PathBuf;

use oap_serve::Registry;

fn written(name: &str, body: &str) -> PathBuf {
    let dir =
        std::env::temp_dir().join(format!("oap-registry-config-{}-{name}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join("registry.json");
    std::fs::write(&path, body).unwrap();
    path
}

fn loaded(name: &str, body: &str) -> Result<Registry, String> {
    Registry::load(&written(name, body), |name| {
        (name == "SET").then(|| "value".to_owned())
    })
    .map_err(|err| err.to_string())
}

#[test]
fn a_memory_entry_is_built_under_its_name_with_its_directory_and_tool_sources() {
    let registry = loaded(
        "memory",
        r#"{"adapters":{"m":{"type":"memory","journal_capacity":4,"working_directory":"/w","environment":["A=1","SET","UNSET"]}},
            "tool_sources":{"fs":{"kind":"process","command":"tool","environment":["SET","B=2"]}}}"#,
    )
    .unwrap();
    assert_eq!(registry.names(), ["m"]);
    assert_eq!(registry.working_directory("m"), "/w");
    let source = registry.tool_source("fs").unwrap();
    assert_eq!(source.command, "tool");
    assert_eq!(source.environment, ["SET=value", "B=2"]);
}

#[test]
fn a_config_the_rust_hub_cannot_serve_as_written_is_refused_naming_why() {
    let refused = |name: &str, body: &str| loaded(name, body).err().unwrap_or_default();
    assert!(refused("top", r#"{"adapters":{},"extra":1}"#).contains("unknown field \"extra\""));
    assert!(refused(
        "member",
        r#"{"adapters":{"m":{"type":"memory","colour":1}}}"#
    )
    .contains("adapter \"m\": unknown field \"colour\""));
    assert!(refused("dup", r#"{"adapters":{},"adapters":{}}"#).contains("DuplicateField"));
    assert!(refused(
        "negative",
        r#"{"adapters":{"m":{"journal_capacity":-1,"type":"memory"}}}"#
    )
    .contains("journal_capacity -1 is negative"));
    assert!(
        refused("harness", r#"{"adapters":{"c":{"type":"claude"}}}"#)
            .contains("type \"claude\" is not built in the Rust tree yet")
    );
    assert!(refused("unknown", r#"{"adapters":{"x":{}}}"#).contains("unknown type \"x\""));
    assert!(refused(
        "any",
        r#"{"adapters":{"m":{"type":"memory","any_directory":true}}}"#
    )
    .contains("any_directory"));
    assert!(refused(
        "unset",
        r#"{"tool_sources":{"fs":{"kind":"process","command":"t","environment":["UNSET"]}}}"#
    )
    .contains("environment variable \"UNSET\" is not set"));
    assert!(refused(
        "nameless",
        r#"{"adapters":{"m":{"type":"memory","environment":["=1"]}}}"#
    )
    .contains("environment entry has no variable name"));
}

#[test]
fn a_null_reads_as_absent_wherever_goap_reads_it_so() {
    assert!(loaded("document", "null").unwrap().names().is_empty());
    assert!(
        loaded("members", r#"{"adapters":null,"tool_sources":null}"#)
            .unwrap()
            .names()
            .is_empty()
    );
    let registry = loaded(
        "entry",
        r#"{"adapters":{"memory":null,"m":{"type":"memory","working_directory":null}}}"#,
    )
    .unwrap();
    assert_eq!(registry.names(), ["m", "memory"]);
    assert!(loaded(
        "unknown",
        r#"{"adapters":{"m":{"type":"memory","colour":null}}}"#
    )
    .err()
    .unwrap_or_default()
    .contains("unknown field \"colour\""));
}
