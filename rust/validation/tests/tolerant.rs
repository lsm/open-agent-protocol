use std::path::Path;

use oap_validation::Validator;
use serde_json::{json, Value};

fn fixture(path: &str) -> Vec<u8> {
    std::fs::read(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures")
            .join(path),
    )
    .unwrap()
}

#[test]
fn tolerant_validation_accepts_unknown_envelopes_but_keeps_known_payload_shapes() {
    let strict = Validator::new().unwrap();
    let tolerant = Validator::tolerant().unwrap();
    let trace = fixture("valid/ext-unpacked-type-tolerated.json");
    assert!(!strict.validate(&trace, "unknown-type").valid());
    assert!(tolerant.validate(&trace, "unknown-type").valid());
    let mut trace: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    trace[2]["payload"]["com.example.note"] = json!("future");
    let data = serde_json::to_vec(&trace).unwrap();
    assert!(!strict.validate(&data, "additive-member").valid());
    assert!(tolerant.validate(&data, "additive-member").valid());
    trace[2]["payload"]["status"] = json!("failed");
    assert!(!tolerant
        .validate(&serde_json::to_vec(&trace).unwrap(), "known-const")
        .valid());
}

#[test]
fn tolerant_run_statuses_accept_unknown_vocabulary() {
    let tolerant = Validator::tolerant().unwrap();
    let mut trace: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    let mut update = trace[2].clone();
    update["type"] = json!("run.status.updated");
    update["id"] = json!("paused");
    update["sequence"] = json!(2);
    update["payload"]["status"] = json!("paused");
    trace[3]["sequence"] = json!(3);
    trace.as_array_mut().unwrap().insert(3, update);
    assert!(tolerant
        .validate(&serde_json::to_vec(&trace).unwrap(), "foreign-status")
        .valid());
}

#[test]
fn tolerant_packed_validation_retains_declared_schemas() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let validator = Validator::tolerant_with_packs(&[root.join("packs/storage")]).unwrap();
    assert!(validator
        .validate(
            &fixture("valid/ext-advertised-key-gated.json"),
            "packed-positive"
        )
        .valid());
    let result = validator.validate(
        &fixture("schema-invalid/ext-packed-type-malformed.json"),
        "packed-negative",
    );
    assert_eq!(result.diagnostics[0].code, "schema_invalid");
}

#[test]
fn unknown_types_under_core_roots_do_not_claim_a_core_feature() {
    let validator = Validator::tolerant().unwrap();
    for kind in [
        "action.call.future",
        "action.permission.future",
        "user.input.future",
    ] {
        let event = json!({
            "protocol":"open-agent-protocol", "version":"0.1",
            "profile":"open-agent-protocol.agent-control-core",
            "type":kind, "id":"future", "payload":{},
        });
        let result = validator.validate(&serde_json::to_vec(&event).unwrap(), "unknown-root");
        assert!(result.valid(), "{kind}: {:?}", result.diagnostics);
    }
}
