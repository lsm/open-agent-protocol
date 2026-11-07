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
