use std::path::Path;

use oap_validation::{codes, Phase, Validator};

#[test]
fn a_snapshot_listing_cannot_contradict_its_runs_or_session() {
    let validator = Validator::new().unwrap();
    for name in [
        "queue-state-terminal-status",
        "queue-state-queued-entry-without-position",
        "queue-state-reservations-called-idle",
        "queue-state-running-beside-a-pending-interaction",
        "state-omits-active-runs-with-pending-permission",
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
