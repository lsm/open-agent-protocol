use std::path::Path;

use oap_validation::{codes, Phase, Validator};

#[test]
fn snapshots_must_reconcile_their_claims_with_the_events_that_follow() {
    let validator = Validator::new().unwrap();
    for name in [
        "queue-state-omits-pending-interaction",
        "queue-state-unpositioned-entry-omits-pending-without-queue",
        "queue-state-pending-without-as-of",
        "queue-state-as-of-never-reached",
        "queue-state-queued-at-forthcoming-start",
        "queue-state-cancelling-reservation-past-its-promotion",
        "queue-state-cancelling-run-left-the-queue-early",
        "queue-state-cancelling-run-drops-a-place-it-never-left",
        "queue-state-executes-a-run-that-never-starts",
        "queue-state-false-settled-claim",
        "queue-state-settled-wrong-sequence",
        "queue-state-settled-claim-then-activity",
        "queue-open-keeps-a-resolved-interaction-pending",
        "queue-open-adds-an-interaction-the-entry-denied",
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
