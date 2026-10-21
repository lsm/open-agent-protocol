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
    assert!(result
        .diagnostics
        .iter()
        .all(|d| d.phase == Phase::Semantic));
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

fn reopen_load_is_rejected(trace: &Value) -> bool {
    Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "reopen")
        .diagnostics
        .iter()
        .any(|d| {
            d.message
                .starts_with("a reopen loads a session with no run under way")
        })
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

#[test]
fn an_idle_reopen_requires_recovery_but_an_ordinary_open_does_not() {
    let mut trace = fixture("valid/session-reopen-recovered.json");
    assert!(judge(&trace).is_empty());
    trace[3]["payload"]["recovery"]["recovered"] = Value::Bool(false);
    assert_eq!(
        judge(&trace),
        BTreeSet::from([codes::SESSION_STATE_MISMATCH.to_owned()])
    );
    trace[2]["payload"]["reopen"] = Value::Bool(false);
    assert!(judge(&trace).is_empty());
}

#[test]
fn restored_queue_entries_are_allowed_but_a_running_entry_or_legacy_active_run_is_not() {
    assert!(judge(&fixture(
        "valid/session-reopen-lists-restored-queued-runs.json"
    ))
    .is_empty());
    let mut trace = fixture("semantic-invalid/session-reopen-recovered-lists-a-running-run.json");
    trace[3]["payload"]
        .as_object_mut()
        .unwrap()
        .remove("active_run_id");
    assert_eq!(
        judge(&trace),
        BTreeSet::from([codes::SESSION_STATE_MISMATCH.to_owned()])
    );
    assert!(reopen_load_is_rejected(&trace));
    trace[3]["payload"]["active_run_id"] = Value::String("r1".to_owned());
    trace[3]["payload"]
        .as_object_mut()
        .unwrap()
        .remove("active_runs");
    assert_eq!(
        judge(&trace),
        BTreeSet::from([codes::SESSION_STATE_MISMATCH.to_owned()])
    );
    assert!(reopen_load_is_rejected(&trace));
}

#[test]
fn a_reopen_message_exemption_preserves_other_snapshot_checks() {
    let mut trace = fixture("valid/compound-open-admits-its-message.json");
    trace.as_array_mut().unwrap().truncate(4);
    trace[1]["payload"]["features"]["session.open.reopen"] = serde_json::json!({"level": "native"});
    trace[2]["payload"]["reopen"] = Value::Bool(true);
    trace[3]["payload"]["recovery"] = serde_json::json!({"recovered": true});
    assert!(!reopen_load_is_rejected(&trace));
    assert_eq!(
        judge(&trace),
        BTreeSet::from([
            codes::MISSING_RUN_TERMINAL.to_owned(),
            codes::SESSION_STATE_MISMATCH.to_owned(),
        ])
    );
}
