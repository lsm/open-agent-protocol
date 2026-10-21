use std::path::Path;

use oap_validation::{codes, Phase, Validator};

#[test]
fn control_tool_settlement_binding_and_disclosed_limits_match_the_shared_fixtures() {
    let validator = Validator::new().unwrap();
    for (name, code) in [
        (
            "control-call-requested-without-interaction",
            codes::ILLEGAL_TOOL_TRANSITION,
        ),
        (
            "control-call-resolve-names-a-call-it-is-not-bound-to",
            codes::SCOPE_MISMATCH,
        ),
        (
            "control-call-unknown-interaction-accepted",
            codes::UNMATCHED_INTERACTION,
        ),
        (
            "control-tool-result-mismatch",
            codes::RESOLUTION_PAYLOAD_MISMATCH,
        ),
        (
            "provide-name-pattern-not-re2",
            codes::UNDISCLOSED_PROVIDE_LIMIT,
        ),
    ] {
        let path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/semantic-invalid")
            .join(format!("{name}.json"));
        let result = validator.validate(&std::fs::read(path).unwrap(), name);
        assert!(
            result
                .diagnostics
                .iter()
                .all(|d| d.phase == Phase::Semantic),
            "{result:?}"
        );
        assert!(result.has_code(code), "{name}: {result:?}");
    }
}

#[test]
fn a_completion_carries_the_accepted_json_value_not_its_serialization() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/valid/control-tool-roundtrip.json");
    let mut trace: Vec<serde_json::Value> =
        serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    for e in &mut trace {
        if e["id"] == "res-req" {
            e["payload"]["result"] = serde_json::json!({"count": 1, "ok": true});
        }
        if e["id"] == "ev-completed" {
            e["payload"]["result"] = serde_json::json!({"ok": true, "count": 1.0});
        }
    }
    let validator = Validator::new().unwrap();
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "canonical-result");
    assert!(result.valid(), "{result:?}");
    trace
        .iter_mut()
        .find(|e| e["id"] == "ev-completed")
        .unwrap()["payload"]["result"]["count"] = serde_json::json!(2);
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "changed-result");
    assert!(
        result
            .diagnostics
            .iter()
            .all(|d| d.phase == Phase::Semantic),
        "{result:?}"
    );
    assert!(
        result.has_code(codes::RESOLUTION_PAYLOAD_MISMATCH),
        "{result:?}"
    );
}

#[test]
fn a_compound_open_can_name_a_subscribe_defect_without_a_spurious_provide_diagnostic() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/valid/open-provide-unadvertised-rejected.json");
    let mut trace: Vec<serde_json::Value> =
        serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    trace
        .iter_mut()
        .find(|e| e["type"] == "session.open.request")
        .unwrap()["payload"]["subscribe"] = serde_json::json!(true);
    let error = trace
        .iter()
        .position(|e| e["type"] == "error.response")
        .unwrap();
    trace[error]["payload"]["error"]["details"]["feature"] =
        serde_json::json!("session.open.subscribe");
    let validator = Validator::new().unwrap();
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "compound-refusal");
    assert!(result.valid(), "{result:?}");
    trace[error]["payload"]["error"]["details"]["feature"] = serde_json::json!("unrelated.feature");
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "unrelated-refusal");
    assert!(
        result
            .diagnostics
            .iter()
            .all(|d| d.phase == Phase::Semantic),
        "{result:?}"
    );
    assert!(result.has_code(codes::UNAVAILABLE_CAPABILITY), "{result:?}");
}
