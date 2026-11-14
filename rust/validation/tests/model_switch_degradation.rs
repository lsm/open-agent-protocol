use std::path::Path;

use oxap_validation::Validator;
use serde_json::{json, Value};

#[test]
fn only_the_switch_feature_opts_into_degraded_acceptance() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/semantic-invalid/model-switch-degraded-no-optin.json");
    let original: Vec<Value> = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    let validator = Validator::new().unwrap();
    for (level, opt_in, expected) in [
        ("degraded", json!([]), vec!["degraded_without_optin"]),
        (
            "degraded",
            json!(["models.list"]),
            vec!["degraded_without_optin"],
        ),
        ("degraded", json!(["session.model.switch"]), vec![]),
        ("native", json!([]), vec![]),
        ("emulated", json!([]), vec![]),
    ] {
        let mut trace = original.clone();
        trace[1]["payload"]["features"]["session.model.switch"]["level"] = json!(level);
        trace[4]["payload"]["allow_degraded_features"] = opt_in;
        let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "switch-opt-in");
        let codes: Vec<_> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
        assert_eq!(codes, expected, "{:?}", result.diagnostics);
    }
}
