use std::path::Path;

use oap_validation::Validator;
use serde_json::{json, Value};

fn trace(state_model: &str, catalog_model: &str) -> Vec<u8> {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/semantic-invalid/models-current-mismatch.json");
    let mut trace: Vec<Value> = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    let base = trace[4].clone();
    let mut request = base.clone();
    request["id"] = json!("state-req");
    request["type"] = json!("session.state.request");
    request["payload"] = json!({"session_id": "s1"});
    let mut response = base;
    response["id"] = json!("state-resp");
    response["in_reply_to"] = json!("state-req");
    response["type"] = json!("session.state.response");
    response["payload"] =
        json!({"session_id": "s1", "status": "idle", "current_model_id": state_model});
    trace.insert(4, response);
    trace.insert(4, request);
    trace[7]["payload"]["current_model_id"] = json!(catalog_model);
    serde_json::to_vec(&trace).unwrap()
}

#[test]
fn a_state_document_marks_the_model_a_catalog_may_report() {
    let validator = Validator::new().unwrap();
    let held = validator.validate(&trace("m2", "m2"), "state-marks-m2");
    assert!(held.diagnostics.is_empty(), "{:?}", held.diagnostics);
    let stale = validator.validate(&trace("m2", "m1"), "state-marks-stale");
    let codes: Vec<_> = stale.diagnostics.iter().map(|d| d.code.as_str()).collect();
    assert_eq!(codes, ["session_state_mismatch"], "{:?}", stale.diagnostics);
}

#[test]
fn a_run_start_after_terminality_marks_no_model() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("tests/data/catalog_current_model/late-start-after-terminal.json");
    let result = Validator::new()
        .unwrap()
        .validate(&std::fs::read(path).unwrap(), "late-start-after-terminal");
    let mut codes: Vec<_> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
    codes.sort();
    assert_eq!(
        codes,
        ["event_after_terminal", "session_state_mismatch"],
        "{:?}",
        result.diagnostics
    );
}
