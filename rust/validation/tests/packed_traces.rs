use std::collections::BTreeSet;
use std::path::Path;

use oap_validation::Validator;
use serde_json::Value;

#[test]
fn native_strict_packed_validation_judges_the_shared_traces() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let mut judged = 0;
    for fixture in manifest["fixtures"].as_array().unwrap() {
        let Some(packs) = fixture["packs"].as_array() else {
            continue;
        };
        if fixture["kind"] == "load-invalid" || fixture["mode"] == "tolerant" {
            continue;
        }
        let directories: Vec<_> = packs
            .iter()
            .map(|p| root.join(p.as_str().unwrap()))
            .collect();
        let validator = Validator::with_packs(&directories).unwrap();
        let path = fixture["path"].as_str().unwrap();
        let result = validator.validate(&std::fs::read(root.join(path)).unwrap(), path);
        let expected: BTreeSet<_> = fixture["codes"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|c| c.as_str().unwrap())
            .collect();
        let observed: BTreeSet<_> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
        assert_eq!(
            observed, expected,
            "{}: {:?}",
            fixture["id"], result.diagnostics
        );
        assert_eq!(result.valid(), fixture["valid"].as_bool().unwrap());
        judged += 1;
    }
    assert!(judged >= 10, "{judged}");
}

#[test]
fn a_loaded_pack_preserves_core_validity() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let core = Validator::new().unwrap();
    let packed = Validator::with_packs(&[root.join("packs/storage")]).unwrap();
    for path in [
        "valid/core-completed.json",
        "schema-invalid/missing-envelope-id.json",
    ] {
        let data = std::fs::read(root.join(path)).unwrap();
        assert_eq!(
            core.validate(&data, path).valid(),
            packed.validate(&data, path).valid()
        );
    }
}

#[test]
fn a_request_without_a_declared_success_type_only_accepts_a_refusal() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let validator = Validator::with_packs(&[root.join("packs/reply-roles")]).unwrap();
    let refusal =
        std::fs::read(root.join("valid/ext-request-without-success-refused.json")).unwrap();
    assert!(validator.validate(&refusal, "declared-refusal").valid());
    let response =
        std::fs::read(root.join("semantic-invalid/ext-undeclared-success-response.json")).unwrap();
    let result = validator.validate(&response, "undeclared-success");
    let codes: BTreeSet<_> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
    assert_eq!(
        codes,
        BTreeSet::from(["unmatched_response", "missing_response"])
    );
}

#[test]
fn a_stale_descriptor_keeps_its_member_gate_and_cannot_clear_the_refresh_window() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let pack = root.join("packs/descriptor-member");
    let validator = Validator::with_packs(std::slice::from_ref(&pack)).unwrap();
    let trace = std::fs::read(pack.join("fixtures/region-on-stale-descriptor.json")).unwrap();
    let result = validator.validate(&trace, "stale-descriptor-member");
    let observed: BTreeSet<_> = result
        .diagnostics
        .iter()
        .map(|d| (d.code.as_str(), d.index))
        .collect();
    assert_eq!(
        observed,
        BTreeSet::from([
            ("stale_capability_revision", 4),
            ("unavailable_capability", 4),
            ("stale_capability_revision", 5),
        ])
    );
}

#[test]
fn a_packed_run_prelude_does_not_claim_to_be_a_core_started_event() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let validator = Validator::with_packs(&[root.join("packs/storage")]).unwrap();
    let mut core: Vec<Value> =
        serde_json::from_slice(&std::fs::read(root.join("valid/core-completed.json")).unwrap())
            .unwrap();
    core[2]["sequence"] = serde_json::json!(2);
    core[3]["sequence"] = serde_json::json!(3);
    core.insert(
        2,
        serde_json::json!({
            "protocol":"open-agent-protocol", "version":"0.1",
            "profile":"open-agent-protocol.agent-control-core",
            "type":"com.example.storage.objects.changed", "id":"prelude",
            "session_id":"s1", "run_id":"r1", "sequence":1, "capability_revision":"v1",
            "payload":{"session_id":"s1", "key":"object"},
        }),
    );
    let descriptor: Vec<Value> = serde_json::from_slice(
        &std::fs::read(root.join("valid/ext-advertised-key-gated.json")).unwrap(),
    )
    .unwrap();
    let trace: Vec<_> = descriptor.into_iter().take(2).chain(core).collect();
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "packed-prelude");
    assert!(result.valid(), "{:?}", result.diagnostics);
}

#[test]
fn a_packed_event_checks_each_available_string_scope_field() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let validator = Validator::with_packs(&[root.join("packs/reply-roles")]).unwrap();
    let mut trace: Vec<Value> =
        serde_json::from_slice(&std::fs::read(root.join("valid/core-completed.json")).unwrap())
            .unwrap();
    let mut event = trace[2].clone();
    event["type"] = serde_json::json!("com.example.replyroles.notice");
    event["id"] = serde_json::json!("mixed-scope");
    event["sequence"] = serde_json::json!(2);
    event["payload"] = serde_json::json!({"session_id":42, "run_id":"r-other"});
    trace[3]["sequence"] = serde_json::json!(3);
    trace.insert(3, event);
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "mixed-scope");
    let observed: Vec<_> = result
        .diagnostics
        .iter()
        .map(|d| (d.code.as_str(), d.index))
        .collect();
    assert_eq!(observed, [("scope_mismatch", 3)]);
}
