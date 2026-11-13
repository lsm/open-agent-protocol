use oap_adapter::Error;
use oap_adapter_hermes::adapter::{Adapter, Config};
use oap_adapter_hermes::process::ProcessConfig;
use oap_protocol::SupportLevel;

fn config() -> Config {
    Config {
        process: ProcessConfig {
            path: "/bin/false".into(),
            ..ProcessConfig::default()
        },
        ..Config::default()
    }
}

#[test]
fn the_descriptor_equals_go_for_the_catalog_revision() {
    let descriptor = Adapter::new(config()).unwrap().describe();
    let wanted: serde_json::Value =
        serde_json::from_str(include_str!("go_capabilities.json")).unwrap();
    assert_eq!(
        serde_json::to_value(&descriptor.capabilities).unwrap(),
        wanted
    );
    assert_eq!(descriptor.capability_revision, "hermes-v0.23.1-oap-v1");
    assert_eq!(
        descriptor.capability_revision,
        oap_harness::current("hermes").unwrap().capability_revision
    );
    assert_eq!(descriptor.journal.scope, "session");
    assert_eq!(descriptor.journal.persistence, "process_memory");
    assert_eq!(descriptor.journal.replay, SupportLevel::DEGRADED);
    assert_eq!(descriptor.journal.capacity, 256);
    assert_eq!(descriptor.max_active_runs_per_session, 1);
    assert!(descriptor.interactive_gates);
    assert_eq!(descriptor.cancellation_target, "session");
    assert_eq!(descriptor.cancellation_implementation, "session.interrupt");
}

#[test]
fn a_configured_journal_capacity_is_described() {
    let descriptor = Adapter::new(Config {
        journal_capacity: 7,
        ..config()
    })
    .unwrap()
    .describe();
    assert_eq!(descriptor.journal.capacity, 7);
}

#[test]
fn an_adapter_needs_an_executable() {
    let Err(Error::Harness(refused)) = Adapter::new(Config::default()) else {
        panic!("an adapter without an executable was built");
    };
    assert_eq!(refused, "hermes adapter: factory or executable is required");
}
