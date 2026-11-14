use oxap_validation::Validator;
use serde_json::Value;
use std::collections::BTreeSet;
use std::path::Path;

#[test]
fn compaction_and_live_settings_draw_the_declared_public_diagnostics() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let validator = Validator::new().unwrap();
    let mut failures = Vec::new();
    for fixture in manifest["fixtures"].as_array().unwrap() {
        let id = fixture["id"].as_str().unwrap();
        if !(id.starts_with("compaction-") || id.starts_with("session-settings-live-")) {
            continue;
        }
        let path = root.join(fixture["path"].as_str().unwrap());
        let result = validator.validate(&std::fs::read(&path).unwrap(), path.to_str().unwrap());
        let actual: BTreeSet<_> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
        let expected: BTreeSet<_> = fixture["codes"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|c| c.as_str().unwrap())
            .collect();
        if actual != expected {
            failures.push(format!("{id}: expected {expected:?}, got {actual:?}"));
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}

#[test]
fn diagnostics_expose_requested_and_reported_values_and_their_correlation() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/semantic-invalid");
    let validator = Validator::new().unwrap();
    let cases = [
        (
            "session-settings-live-unadvertised-admitted",
            "a typed refusal naming session.reasoning",
            "an admitted update",
            "up",
        ),
        (
            "session-settings-live-policy-unadvertised-admitted",
            "a typed refusal naming session.compaction.policy",
            "an admitted update",
            "up",
        ),
        (
            "session-settings-live-degraded-admitted-without-optin",
            "a typed refusal naming session.reasoning",
            "an admitted update",
            "up",
        ),
        ("session-settings-live-level-rounded", "max", "high", "up"),
        (
            "session-settings-live-policy-rounded",
            "tokens 150000",
            "tokens 128000",
            "up",
        ),
        (
            "session-settings-live-run-before-state",
            "session.state.updated before the next run starts",
            "run.started",
            "up",
        ),
        (
            "session-settings-live-never-reflected",
            "a matching session.state.updated",
            "none",
            "up",
        ),
        (
            "compaction-run-stops-for-another-reason",
            "compacted",
            "max_tokens",
            "r1",
        ),
    ];
    for (id, expected, actual, related) in cases {
        let data = std::fs::read(root.join(format!("{id}.json"))).unwrap();
        let result = validator.validate(&data, id);
        assert_eq!(result.diagnostics.len(), 1, "{id}");
        let diagnostic = &result.diagnostics[0];
        assert_eq!(diagnostic.expected, expected, "{id}");
        assert_eq!(diagnostic.actual, actual, "{id}");
        assert_eq!(diagnostic.related_ids, [related], "{id}");
    }
    let mut trace: Value = serde_json::from_slice(
        &std::fs::read(root.join("session-settings-live-level-rounded.json")).unwrap(),
    )
    .unwrap();
    trace[5]["payload"]["session_id"] = Value::String("s2".to_owned());
    trace[5]["session_id"] = Value::String("s2".to_owned());
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "scope");
    let diagnostic = result
        .diagnostics
        .iter()
        .find(|d| d.code == "scope_mismatch")
        .unwrap();
    assert_eq!(diagnostic.expected, "s1");
    assert_eq!(diagnostic.actual, "s2");
    assert_eq!(diagnostic.related_ids, ["up"]);
}

#[test]
fn compaction_gate_diagnostics_name_the_compaction() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/semantic-invalid/compaction-focus-degraded-without-optin.json");
    let result = Validator::new()
        .unwrap()
        .validate(&std::fs::read(path).unwrap(), "focus");
    assert_eq!(
        result.diagnostics[0].message,
        "a compaction focus was admitted without the caller's opt-in"
    );
}
