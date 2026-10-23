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

#[test]
fn unknown_provided_pattern_limits_do_not_invent_an_attachment_refusal() {
    assert!(judge(&fixture(
        "valid",
        "attach-preserves-unknown-provided-pattern-boundary"
    ))
    .is_empty());
}

#[test]
fn one_compound_open_judges_a_missing_descriptor_once() {
    let mut trace = fixture(
        "semantic-invalid",
        "attach-capability-before-provided-owner",
    );
    trace
        .as_array_mut()
        .unwrap()
        .retain(|e| !e["type"].as_str().unwrap().starts_with("capabilities."));
    let response = trace.as_array_mut().unwrap().last_mut().unwrap();
    response["type"] = "error.response".into();
    response["payload"] =
        serde_json::json!({"error":{"code":"internal","message":"synthetic refusal"}});
    assert_eq!(judge(&trace), [("unavailable_capability".into(), 2)]);
}

#[test]
fn provide_defect_beats_attach_limit() {
    let mut trace = fixture("valid", "open-provide-two-tools");
    trace.as_array_mut().unwrap().truncate(6);
    trace[3]["payload"] = serde_json::json!({"endpoint":{"id":"agent","name":"Control-tools fixture endpoint"},"features":{"action.tools.list":{"level":"native"},"action.tools.provide":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"tools":{"level":"native"},"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"max_sources":1}}},"sources":[{"id":"native","kind":"native"}],"tools":[{"execution_owner":"agent","input_schema":{"type":"object"},"name":"grep","source":"native"}]});
    trace[4]["payload"] = serde_json::json!({"session_id":"s1","tools":[{"execution_owner":"control","input_schema":{"type":"object"},"name":"grep","source":"native"}],"tool_sources":[{"id":"files","kind":"process"},{"id":"docs","kind":"process"}]});
    trace[5]["payload"] = serde_json::json!({"error":{"code":"unsupported_feature","message":"synthetic review reproduction","details":{"feature":"action.tool_sources.attach","reason":"unsatisfiable","source":"docs"}}});
    trace[5]["type"] = "error.response".into();
    assert_eq!(judge(&trace), [("duplicate_tool_name".into(), 5)]);
}

#[test]
fn attach_defect_beats_provide_limit() {
    let mut trace = fixture("valid", "open-provide-two-tools");
    trace.as_array_mut().unwrap().truncate(6);
    trace[3]["payload"] = serde_json::json!({"endpoint":{"id":"agent","name":"Control-tools fixture endpoint"},"features":{"action.tools.list":{"level":"native"},"action.tools.provide":{"level":"native","limits":{"max_tools":1}},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"tools":{"level":"native"},"action.tool_sources.attach":{"level":"native","modes":["session_open"]}},"sources":[{"id":"native","kind":"native"}],"tools":[{"execution_owner":"agent","input_schema":{"type":"object"},"name":"grep","source":"native"}]});
    trace[4]["payload"] = serde_json::json!({"session_id":"s1","tools":[{"execution_owner":"control","input_schema":{"type":"object"},"name":"echo","source":"native"},{"execution_owner":"control","input_schema":{"type":"object"},"name":"upper","source":"native"}],"tool_sources":[{"id":"native","kind":"process"}]});
    trace[5]["payload"] = serde_json::json!({"error":{"code":"unsupported_feature","message":"synthetic review reproduction","details":{"feature":"action.tools.provide","reason":"unsatisfiable","tool":"upper"}}});
    trace[5]["type"] = "error.response".into();
    assert_eq!(judge(&trace), [("duplicate_tool_source".into(), 5)]);
}

#[test]
fn provide_honour_survives_subscribe_defect() {
    let mut trace = fixture("valid", "open-provide-two-tools");
    trace.as_array_mut().unwrap().truncate(6);
    trace[3]["payload"] = serde_json::json!({"endpoint":{"id":"agent","name":"Control-tools fixture endpoint"},"features":{"action.tools.list":{"level":"native"},"action.tools.provide":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"tools":{"level":"native"}},"sources":[{"id":"native","kind":"native"}],"tools":[{"execution_owner":"agent","input_schema":{"type":"object"},"name":"grep","source":"native"}]});
    trace[4]["payload"] = serde_json::json!({"session_id":"s1","tools":[{"execution_owner":"control","input_schema":{"type":"object"},"name":"echo","source":"native"},{"execution_owner":"control","input_schema":{"type":"object"},"name":"upper","source":"native"}],"subscribe":true});
    trace[5]["payload"] = serde_json::json!({"error":{"code":"internal","message":"synthetic review reproduction"}});
    trace[5]["type"] = "error.response".into();
    assert_eq!(judge(&trace), [("unavailable_capability".into(), 5), ("undisclosed_provide_limit".into(), 5)]);
}

#[test]
fn attach_honour_survives_subscribe_defect() {
    let mut trace = fixture("valid", "open-provide-two-tools");
    trace.as_array_mut().unwrap().truncate(6);
    trace[3]["payload"] = serde_json::json!({"endpoint":{"id":"agent","name":"Control-tools fixture endpoint"},"features":{"action.tools.list":{"level":"native"},"action.tools.provide":{"level":"native"},"session.message.delivery.auto":{"level":"native"},"session.message.submit":{"level":"native"},"session.open":{"level":"native"},"session.state":{"level":"native"},"tools":{"level":"native"},"action.tool_sources.attach":{"level":"native","modes":["session_open"]}},"sources":[{"id":"native","kind":"native"}],"tools":[{"execution_owner":"agent","input_schema":{"type":"object"},"name":"grep","source":"native"}]});
    trace[4]["payload"] = serde_json::json!({"session_id":"s1","subscribe":true,"tool_sources":[{"id":"files","kind":"process"}]});
    trace[5]["payload"] = serde_json::json!({"error":{"code":"internal","message":"synthetic review reproduction"}});
    trace[5]["type"] = "error.response".into();
    assert_eq!(judge(&trace), [("unavailable_capability".into(), 5), ("undisclosed_attach_limit".into(), 5)]);
}
