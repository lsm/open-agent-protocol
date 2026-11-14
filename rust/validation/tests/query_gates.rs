use std::path::Path;

use oxap_validation::{Phase, Validator};
use serde_json::Value;

#[test]
fn optional_query_outcomes_keep_their_declared_refusals_and_opt_ins() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    for validator in [Validator::new().unwrap(), Validator::tolerant().unwrap()] {
        for id in [
            "models-list-refused-advertised",
            "models-list-degraded-without-optin",
            "models-unadvertised-wrong-reason",
            "session-list-unadvertised-wrong-refusal",
            "session-list-degraded-admitted-without-optin",
            "transcript-load-degraded-admitted-without-optin",
        ] {
            let entry = manifest["fixtures"]
                .as_array()
                .unwrap()
                .iter()
                .find(|entry| entry["id"] == id)
                .unwrap();
            let trace = std::fs::read(root.join(entry["path"].as_str().unwrap())).unwrap();
            let result = validator.validate(&trace, id);
            let expected = entry["codes"][0].as_str().unwrap();
            assert_eq!(
                result.diagnostics.len(),
                1,
                "{id}: {:?}",
                result.diagnostics
            );
            let diagnostic = &result.diagnostics[0];
            assert_eq!(diagnostic.phase, Phase::Semantic, "{id}");
            assert_eq!(diagnostic.code, expected, "{id}");
            assert_eq!(diagnostic.index, 3, "{id}");
        }
    }
}
