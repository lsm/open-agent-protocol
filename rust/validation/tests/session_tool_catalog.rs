use oxap_validation::Validator;
use serde_json::Value;
use std::collections::BTreeSet;
use std::path::Path;

#[test]
fn session_tool_catalogs_draw_the_declared_public_diagnostics() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let validator = Validator::new().unwrap();
    let mut failures = Vec::new();
    for fixture in manifest["fixtures"].as_array().unwrap() {
        let id = fixture["id"].as_str().unwrap();
        if !(id.starts_with("tools-catalog-") || id.starts_with("tools-refresh-"))
            || fixture["mode"] == "tolerant"
            || id == "tools-catalog-follows-contradicting-open"
        {
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
fn equivalent_schema_numbers_preserve_their_json_type() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/semantic-invalid/tools-catalog-alters-provided-schema.json");
    let source: Vec<Value> = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    let validator = Validator::new().unwrap();
    for (constant, valid) in [
        (serde_json::json!(1.0), true),
        (serde_json::json!("1e0"), false),
    ] {
        let mut trace = source.clone();
        let supplied = trace
            .iter_mut()
            .find(|e| e["type"] == "session.open.request")
            .unwrap();
        let name = supplied["payload"]["tools"][0]["name"].clone();
        supplied["payload"]["tools"][0]["input_schema"] = serde_json::json!({"const": 1});
        let catalog = trace
            .iter_mut()
            .find(|e| e["type"] == "action.tools.list.response")
            .unwrap();
        let listed = catalog["payload"]["tools"]
            .as_array_mut()
            .unwrap()
            .iter_mut()
            .find(|tool| tool["name"] == name)
            .unwrap();
        listed["input_schema"] = serde_json::json!({"const": constant});
        let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "schema-types");
        let codes: Vec<_> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
        assert_eq!(
            codes,
            if valid {
                vec![]
            } else {
                vec!["catalog_mismatch"]
            }
        );
    }
}
