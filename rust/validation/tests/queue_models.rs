use oap_validation::Validator;
use serde_json::Value;
use std::collections::BTreeSet;
use std::path::Path;

#[test]
fn queue_model_captures_draw_the_declared_public_diagnostics() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let validator = Validator::new().unwrap();
    let mut failures = Vec::new();
    for fixture in manifest["fixtures"].as_array().unwrap() {
        let id = fixture["id"].as_str().unwrap();
        if !id.starts_with("queue-") || !(id.contains("model") || id.contains("genesis")) {
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
