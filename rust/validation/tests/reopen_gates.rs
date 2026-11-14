use std::path::Path;

use oxap_validation::{Phase, Validator};

#[test]
fn reopen_admissions_and_refusals_require_the_advertised_gate() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/semantic-invalid");
    for validator in [Validator::new().unwrap(), Validator::tolerant().unwrap()] {
        for (name, code) in [
            ("session-reopen-unadvertised", "unavailable_capability"),
            (
                "session-reopen-unadvertised-wrong-refusal",
                "unavailable_capability",
            ),
            (
                "session-reopen-degraded-admitted-without-optin",
                "degraded_without_optin",
            ),
        ] {
            let result = validator.validate(
                &std::fs::read(root.join(format!("{name}.json"))).unwrap(),
                name,
            );
            assert_eq!(
                result.diagnostics.len(),
                1,
                "{name}: {:?}",
                result.diagnostics
            );
            assert_eq!(result.diagnostics[0].phase, Phase::Semantic, "{name}");
            assert_eq!(result.diagnostics[0].code, code, "{name}");
            assert_eq!(result.diagnostics[0].index, 3, "{name}");
        }
    }
}
