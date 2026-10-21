use std::collections::BTreeSet;
use std::path::PathBuf;

use oap_validation::{codes, Phase, Validator};
use serde_json::Value;

fn fixture(name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures")
        .join(name);
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn judge(trace: &Value) -> BTreeSet<String> {
    let result = Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "reopen");
    assert!(result.diagnostics.iter().all(|d| d.phase == Phase::Semantic));
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

#[test]
fn a_reopen_cannot_restore_queued_runs_without_recovery_or_restore_a_running_run() {
    for name in [
        "semantic-invalid/session-reopen-unrecovered-lists-queued-runs.json",
        "semantic-invalid/session-reopen-recovered-lists-a-running-run.json",
    ] {
        assert_eq!(
            judge(&fixture(name)),
            BTreeSet::from([codes::SESSION_STATE_MISMATCH.to_owned()]),
            "{name}"
        );
    }
}
