use oap_adapter::{Error, OpenRequest};
use oap_adapter_deepseek::{
    adapter::{compaction_patch, Adapter, Config},
    process::ProcessConfig,
};
use oap_protocol::{CompactionKind, CompactionPolicy, ReasoningLevel, SupportLevel};

fn config() -> Config {
    Config {
        process: ProcessConfig {
            path: "/bin/false".into(),
            args: vec!["serve".into()],
            dir: "/workspace".into(),
            ..ProcessConfig::default()
        },
        provider: "p".into(),
        model: "m".into(),
        ..Config::default()
    }
}

fn refusal(error: Error) -> (String, &'static str, String, String) {
    match error {
        Error::UnsupportedControl(control) => (
            control.feature,
            control.reason,
            control.field,
            control.detail,
        ),
        other => panic!("not a control refusal: {other:?}"),
    }
}

#[test]
fn the_descriptor_equals_go_for_the_catalog_revision() {
    let adapter = Adapter::new(config()).unwrap();
    let descriptor = adapter.describe();
    let wanted: serde_json::Value =
        serde_json::from_str(include_str!("go_capabilities.json")).unwrap();
    assert_eq!(
        serde_json::to_value(&descriptor.capabilities).unwrap(),
        wanted
    );
    assert_eq!(
        descriptor.capability_revision,
        oap_harness::current("deepseek-harness")
            .unwrap()
            .capability_revision
    );
    assert_eq!(descriptor.journal.scope, "session");
    assert_eq!(descriptor.journal.persistence, "process_memory");
    assert_eq!(descriptor.journal.replay, SupportLevel::DEGRADED);
    assert_eq!(descriptor.journal.capacity, 256);
    assert_eq!(descriptor.max_active_runs_per_session, 1);
    assert!(!descriptor.interactive_gates);
    assert_eq!(descriptor.cancellation_target, "none");
    assert_eq!(descriptor.cancellation_implementation, "unavailable");
}

#[test]
fn construction_requires_an_executable_an_absolute_directory_and_valid_initialize_params() {
    for (mutate, text) in [
        (
            (|c: &mut Config| c.process.path.clear()) as fn(&mut Config),
            "deepseek adapter: factory or executable is required",
        ),
        (
            |c: &mut Config| c.process.dir = "relative".into(),
            "deepseek adapter: absolute working directory is required",
        ),
        (
            |c: &mut Config| c.model.clear(),
            "initialize cwd, provider, and model are required",
        ),
        (
            |c: &mut Config| c.max_tokens = Some(0),
            "maxTokens must be a positive safe integer",
        ),
    ] {
        let mut configured = config();
        mutate(&mut configured);
        let error = Adapter::new(configured).err().unwrap().to_string();
        assert!(error.contains(text), "{error}");
    }
}

#[test]
fn a_launch_carries_the_initialize_params_reasoning_and_patch() {
    let adapter = Adapter::new(Config {
        max_tokens: Some(9),
        ..config()
    })
    .unwrap();
    let plain = adapter.launch(&OpenRequest::default()).unwrap();
    assert_eq!(plain.patch, None);
    assert_eq!(plain.process.args, ["serve"]);
    assert_eq!(plain.process.initialize.cwd, "/workspace");
    assert_eq!(plain.process.initialize.provider, "p");
    assert_eq!(plain.process.initialize.model, "m");
    assert_eq!(plain.process.initialize.max_tokens, Some(9));
    assert_eq!(plain.process.initialize.reasoning_effort, "");
    let configured = adapter
        .launch(&OpenRequest {
            reasoning_level: ReasoningLevel::MAX,
            compaction_policy: Some(CompactionPolicy {
                kind: CompactionKind::SHARE,
                share_percent: 75,
                ..CompactionPolicy::default()
            }),
            ..OpenRequest::default()
        })
        .unwrap();
    assert_eq!(configured.process.initialize.reasoning_effort, "max");
    assert_eq!(
        configured.patch.as_deref(),
        Some("- id: compaction-basic\n  config:\n    thresholdRatio: 0.75\n")
    );
    assert_eq!(
        refusal(
            adapter
                .launch(&OpenRequest {
                    reasoning_level: ReasoningLevel::MEDIUM,
                    ..OpenRequest::default()
                })
                .err()
                .unwrap()
        ),
        (
            "session.reasoning".into(),
            "unsatisfiable",
            "reasoning_level".into(),
            "the DeepSeek route takes off, low, high and max".into()
        )
    );
}

#[test]
fn compaction_patches_follow_go() {
    let policy = |kind, share_percent| {
        Some(CompactionPolicy {
            kind,
            share_percent,
            ..CompactionPolicy::default()
        })
    };
    assert_eq!(compaction_patch(None).unwrap(), None);
    assert_eq!(
        compaction_patch(policy(CompactionKind::AUTO, 0).as_ref()).unwrap(),
        None
    );
    assert_eq!(
        compaction_patch(policy(CompactionKind::OFF, 0).as_ref())
            .unwrap()
            .as_deref(),
        Some("- id: compaction-basic\n  disabled: true\n")
    );
    assert_eq!(
        compaction_patch(policy(CompactionKind::SHARE, 50).as_ref())
            .unwrap()
            .as_deref(),
        Some("- id: compaction-basic\n  config:\n    thresholdRatio: 0.5\n")
    );
    assert_eq!(
        compaction_patch(policy(CompactionKind::SHARE, 33).as_ref())
            .unwrap()
            .as_deref(),
        Some("- id: compaction-basic\n  config:\n    thresholdRatio: 0.33\n")
    );
    assert_eq!(
        refusal(
            compaction_patch(policy(CompactionKind::TOKENS, 0).as_ref())
                .err()
                .unwrap()
        ),
        (
            "session.compaction.policy".into(),
            "unsatisfiable",
            "compaction_policy".into(),
            "compaction-basic sets its threshold from the window, so a token count is refused"
                .into()
        )
    );
}
