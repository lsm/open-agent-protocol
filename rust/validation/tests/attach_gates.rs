use std::path::PathBuf;

use oap_validation::{Phase, Validator};
use serde_json::Value;

fn fixture(kind: &str, name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures")
        .join(kind)
        .join(format!("{name}.json"));
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn judge(trace: &Value) -> Vec<(String, usize)> {
    let result = Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "attach-gates");
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
fn advertised_attachment_requires_session_open_mode_on_the_descriptor() {
    for name in [
        "attach-advertised-without-modes",
        "open-attach-without-session-open-mode-refused",
    ] {
        assert_eq!(
            judge(&fixture("semantic-invalid", name)),
            [("undisclosed_attach_modes".into(), 1)],
            "{name}"
        );
    }
    assert_eq!(
        judge(&fixture(
            "semantic-invalid",
            "open-attach-without-session-open-mode"
        )),
        [
            ("undisclosed_attach_modes".into(), 1),
            ("unavailable_capability".into(), 3)
        ]
    );
}

#[test]
fn capabilities_degradation_and_remote_modes_are_judged_on_the_reply() {
    for name in [
        "open-attach-unadvertised",
        "open-unadvertised-wrong-reason",
        "open-attach-remote-unadvertised",
        "open-attach-remote-wrong-refusal",
    ] {
        assert_eq!(
            judge(&fixture("semantic-invalid", name)),
            [("unavailable_capability".into(), 3)],
            "{name}"
        );
    }
    assert_eq!(
        judge(&fixture(
            "semantic-invalid",
            "open-degraded-attach-without-optin"
        )),
        [("degraded_without_optin".into(), 3)]
    );
}

#[test]
fn ids_cannot_collide_with_declared_sources_or_other_attachments() {
    for name in [
        "open-attach-colliding-source-admitted",
        "tool-source-collision-wrong-refusal",
        "attach-request-source-id-repeated",
    ] {
        assert_eq!(
            judge(&fixture("semantic-invalid", name)),
            [("duplicate_tool_source".into(), 3)],
            "{name}"
        );
    }
}

#[test]
fn refusals_obey_disclosed_limits_and_name_the_source_to_drop() {
    for name in [
        "tool-source-over-limit-unnamed-source",
        "tool-source-over-limit-untyped-refusal",
        "attach-transport-refusal-names-source",
    ] {
        assert_eq!(
            judge(&fixture("semantic-invalid", name)),
            [("unavailable_capability".into(), 3)],
            "{name}"
        );
    }
    for name in [
        "tool-source-attach-refused-within-limits",
        "tool-source-attach-refused-at-its-limit",
    ] {
        assert_eq!(
            judge(&fixture("semantic-invalid", name)),
            [("undisclosed_attach_limit".into(), 3)],
            "{name}"
        );
    }
    assert!(judge(&fixture("valid", "attach-above-disclosed-limit-admitted")).is_empty());
}

#[test]
fn compound_opens_have_one_source_gate_or_honour_owner() {
    assert!(judge(&fixture(
        "valid",
        "attach-refusal-discharges-provided-tools"
    ))
    .is_empty());
    assert_eq!(
        judge(&fixture(
            "semantic-invalid",
            "attach-capability-before-provided-owner"
        )),
        [("unavailable_capability".into(), 5)]
    );
    assert_eq!(
        judge(&fixture(
            "semantic-invalid",
            "attach-and-provide-have-one-honour-owner"
        )),
        [("undisclosed_attach_limit".into(), 5)]
    );
}

#[test]
fn conforming_refusals_optins_and_layered_advertisements_are_valid() {
    for name in [
        "open-attach-process-source",
        "open-attach-layered-capability",
        "open-attach-unadvertised-rejected",
        "open-attach-remote-unadvertised-rejected",
        "tool-source-remote-unadvertised-attach",
        "tool-source-collision-refused",
        "tool-source-collision-unadvertised-attach",
        "tool-source-attach-refused-over-limit",
        "open-degraded-attach-optin",
        "open-degraded-attach-refused",
    ] {
        assert!(judge(&fixture("valid", name)).is_empty(), "{name}");
    }
}
