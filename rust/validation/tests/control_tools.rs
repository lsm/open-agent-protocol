use std::path::Path;

use oap_validation::{codes, Phase, Validator};

#[test]
fn control_tool_settlement_binding_and_disclosed_limits_match_the_shared_fixtures() {
    let validator = Validator::new().unwrap();
    for (name, code) in [
        ("control-call-requested-without-interaction", codes::ILLEGAL_TOOL_TRANSITION),
        ("control-call-resolve-names-a-call-it-is-not-bound-to", codes::SCOPE_MISMATCH),
        ("control-call-unknown-interaction-accepted", codes::UNMATCHED_INTERACTION),
        ("control-tool-result-mismatch", codes::RESOLUTION_PAYLOAD_MISMATCH),
        ("provide-name-pattern-not-re2", codes::UNDISCLOSED_PROVIDE_LIMIT),
    ] {
        let path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/semantic-invalid")
            .join(format!("{name}.json"));
        let result = validator.validate(&std::fs::read(path).unwrap(), name);
        assert!(result.diagnostics.iter().all(|d| d.phase == Phase::Semantic), "{result:?}");
        assert!(result.has_code(code), "{name}: {result:?}");
    }
}
