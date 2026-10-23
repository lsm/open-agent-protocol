use std::path::PathBuf;

use oap_validation::{Phase, Validator};
use serde_json::Value;

fn fixture(name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures")
        .join(name);
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn judge(trace: &Value) -> Vec<(String, usize)> {
    let result = Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "tools-list");
    assert!(result
        .diagnostics
        .iter()
        .all(|d| d.phase == Phase::Semantic));
    result
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.index))
        .collect()
}

#[test]
fn unadvertised_catalogs_require_the_typed_feature_and_reason() {
    for name in [
        "tools-list-unadvertised-served",
        "tools-list-ungated-wrong-refusal",
        "tools-list-ungated-wrong-feature",
        "tools-list-ungated-wrong-reason",
    ] {
        assert_eq!(
            judge(&fixture(&format!("semantic-invalid/{name}.json"))),
            [("unavailable_capability".into(), 3)],
            "{name}"
        );
    }
}

#[test]
fn degraded_catalogs_require_their_own_optin_and_advertised_catalogs_are_honoured() {
    assert_eq!(
        judge(&fixture(
            "semantic-invalid/tools-list-degraded-without-optin.json"
        )),
        [("degraded_without_optin".into(), 3)]
    );
    assert_eq!(
        judge(&fixture(
            "semantic-invalid/tools-list-refused-advertised.json"
        )),
        [("unhonoured_capability".into(), 3)]
    );
}

#[test]
fn served_catalogs_have_unique_names_and_sources_and_resolve_each_tool() {
    for (name, code) in [
        ("tools-duplicate-name", "duplicate_tool_name"),
        ("tools-duplicate-source-id", "duplicate_tool_source"),
        ("tools-unmatched-source", "unmatched_tool_source"),
        ("tools-unattributed-tool", "unmatched_tool_source"),
    ] {
        assert_eq!(
            judge(&fixture(&format!("semantic-invalid/{name}.json"))),
            [(code.into(), 3)],
            "{name}"
        );
    }
}

#[test]
fn gates_and_response_local_faults_are_independent() {
    assert_eq!(
        judge(&fixture(
            "semantic-invalid/tools-list-gated-dangling-source.json"
        )),
        [
            ("unavailable_capability".into(), 3),
            ("unmatched_tool_source".into(), 3)
        ]
    );
}

#[test]
fn overlapping_requests_keep_their_own_optin() {
    assert_eq!(
        judge(&fixture(
            "semantic-invalid/tools-list-overlapping-optins.json"
        )),
        [("degraded_without_optin".into(), 5)]
    );
    assert!(judge(&fixture("valid/tools-list-overlapping-optins-refused.json")).is_empty());
}

#[test]
fn conforming_refusals_and_optins_stay_valid_including_after_refresh() {
    for name in [
        "tools-list-ungated-refused",
        "tools-list-degraded-refused",
        "tools-list-degraded-optin",
        "tools-list-refusal-keeps-requested-gate",
    ] {
        assert!(
            judge(&fixture(&format!("valid/{name}.json"))).is_empty(),
            "{name}"
        );
    }
}
