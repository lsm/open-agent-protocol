use std::path::Path;

use oap_validation::{codes, Phase, Validator};

#[test]
fn admission_anchors_must_be_answered_by_the_claimed_admission() {
    let validator = Validator::new().unwrap();
    for name in [
        "queue-state-anchor-request-refused",
        "queue-state-anchor-request-unaccounted",
        "queue-state-anchor-request-unanswered",
        "queue-state-entry-anchor-admitted-elsewhere",
        "queue-state-entry-leads-a-different-run",
        "compound-open-anchor-names-an-open-without-a-message",
        "anchor-session-names-state-request",
        "anchor-entry-names-state-request",
        "anchor-foreign-request",
        "anchor-foreign-admission",
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
            "{name}: {result:?}"
        );
        assert!(
            result.has_code(codes::SESSION_STATE_MISMATCH),
            "{name}: {result:?}"
        );
    }
}
