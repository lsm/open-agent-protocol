use std::sync::Arc;

use oap_adapter::{Error, OpenRequest, CONTROL_UNSATISFIABLE};
use oap_adapter_codex_appserver::adapter::{reasoning_level, thread_config, Adapter, Config};
use oap_adapter_codex_appserver::client::{Client, ClientFactory};
use oap_adapter_codex_appserver::process::ProcessConfig;
use oap_adapter_codex_appserver::rpc::{error, Fault};
use oap_protocol::{
    CompactionKind, CompactionPolicy, ReasoningLevel, SupportLevel, FEATURE_COMPACTION_POLICY,
};
use serde_json::json;

fn config() -> Config {
    Config {
        process: ProcessConfig {
            path: "/bin/false".into(),
            ..ProcessConfig::default()
        },
        ..Config::default()
    }
}

fn refused(
    _: Option<std::time::Instant>,
) -> Result<Arc<dyn Client>, oap_adapter_codex_appserver::rpc::Error> {
    Err(error(Fault::Transport("unused".into())))
}

fn harness_text(error: Error) -> String {
    match error {
        Error::Harness(text) => text,
        other => panic!("not a harness error: {other:?}"),
    }
}

fn open(level: &str, policy: Option<CompactionPolicy>) -> OpenRequest {
    OpenRequest {
        reasoning_level: level.into(),
        compaction_policy: policy,
        ..OpenRequest::default()
    }
}

fn policy(kind: CompactionKind, tokens: i64, share_percent: i64) -> Option<CompactionPolicy> {
    Some(CompactionPolicy {
        kind,
        tokens,
        share_percent,
    })
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
    assert_eq!(
        descriptor.capability_revision,
        oap_harness::current("codex-app-server")
            .unwrap()
            .capability_revision
    );
    assert_eq!(descriptor.journal.scope, "session");
    assert_eq!(descriptor.journal.persistence, "process_memory");
    assert_eq!(descriptor.journal.replay, SupportLevel::DEGRADED);
    assert_eq!(descriptor.journal.capacity, 256);
    assert_eq!(descriptor.max_active_runs_per_session, 1);
    assert!(descriptor.interactive_gates);
    assert_eq!(descriptor.cancellation_target, "run");
    assert_eq!(
        descriptor.cancellation_implementation,
        "native_turn_interrupt"
    );
}

#[test]
fn the_journal_keeps_a_configured_capacity() {
    let descriptor = Adapter::new(Config {
        journal_capacity: 3,
        ..config()
    })
    .unwrap()
    .describe();
    assert_eq!(descriptor.journal.capacity, 3);
}

#[test]
fn construction_requires_a_factory_or_an_executable() {
    let text = harness_text(
        Adapter::new(Config::default())
            .err()
            .expect("an adapter with neither a factory nor an executable was built"),
    );
    assert_eq!(
        text,
        "codex app-server adapter: factory or executable is required"
    );
    let factory: Arc<dyn ClientFactory> = Arc::new(refused);
    assert!(Adapter::new(Config {
        factory: Some(factory),
        ..Config::default()
    })
    .is_ok());
}

#[test]
fn the_thread_config_carries_effort_and_a_token_limit() {
    assert!(thread_config(&open("", None)).unwrap().is_empty());
    for (level, effort) in [
        ("off", "none"),
        ("minimal", "minimal"),
        ("high", "high"),
        ("max", "max"),
    ] {
        let config = thread_config(&open(level, None)).unwrap();
        assert_eq!(
            serde_json::to_value(config).unwrap(),
            json!({"model_reasoning_effort": effort})
        );
    }
    let config = thread_config(&open("low", policy(CompactionKind::TOKENS, 9000, 0))).unwrap();
    assert_eq!(
        serde_json::to_value(config).unwrap(),
        json!({"model_reasoning_effort": "low", "model_auto_compact_token_limit": 9000})
    );
    assert!(thread_config(&open("", policy(CompactionKind::AUTO, 0, 0)))
        .unwrap()
        .is_empty());
}

#[test]
fn the_thread_config_refuses_off_and_share_compaction() {
    for kind in [CompactionKind::OFF, CompactionKind::SHARE] {
        match thread_config(&open("high", policy(kind.clone(), 0, 40))) {
            Err(Error::UnsupportedControl(control)) => {
                assert_eq!(control.feature, FEATURE_COMPACTION_POLICY);
                assert_eq!(control.reason, CONTROL_UNSATISFIABLE);
                assert_eq!(control.field, "compaction_policy");
                assert_eq!(
                    control.detail,
                    "Codex takes a token limit only; it has no off, and its share applies only at a turn's end"
                );
            }
            other => panic!("{kind:?} was not refused: {other:?}"),
        }
    }
}

#[test]
fn a_resumed_effort_maps_to_the_oap_level() {
    for (effort, level) in [
        ("none", ReasoningLevel::OFF),
        ("minimal", ReasoningLevel::MINIMAL),
        ("low", ReasoningLevel::LOW),
        ("medium", ReasoningLevel::MEDIUM),
        ("high", ReasoningLevel::HIGH),
        ("xhigh", ReasoningLevel::XHIGH),
        ("max", ReasoningLevel::MAX),
        ("off", ReasoningLevel::default()),
        ("turbo", ReasoningLevel::default()),
        ("", ReasoningLevel::default()),
    ] {
        assert_eq!(reasoning_level(effort), level, "{effort}");
    }
}
