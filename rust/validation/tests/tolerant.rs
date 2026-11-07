use std::path::Path;

use oap_validation::Validator;
use serde_json::{json, Value};

fn fixture(path: &str) -> Vec<u8> {
    std::fs::read(
        Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures")
            .join(path),
    )
    .unwrap()
}

#[test]
fn tolerant_validation_accepts_unknown_envelopes_but_keeps_known_payload_shapes() {
    let strict = Validator::new().unwrap();
    let tolerant = Validator::tolerant().unwrap();
    let trace = fixture("valid/ext-unpacked-type-tolerated.json");
    assert!(!strict.validate(&trace, "unknown-type").valid());
    assert!(tolerant.validate(&trace, "unknown-type").valid());
    let mut trace: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    trace[2]["payload"]["com.example.note"] = json!("future");
    let data = serde_json::to_vec(&trace).unwrap();
    assert!(!strict.validate(&data, "additive-member").valid());
    assert!(tolerant.validate(&data, "additive-member").valid());
    trace[2]["payload"]["status"] = json!("failed");
    let result = tolerant.validate(&serde_json::to_vec(&trace).unwrap(), "known-const");
    assert_eq!(result.diagnostics[0].pointer, "/payload/status");
}

#[test]
fn tolerant_run_statuses_accept_unknown_vocabulary() {
    let tolerant = Validator::tolerant().unwrap();
    let mut trace: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    let mut update = trace[2].clone();
    update["type"] = json!("run.status.updated");
    update["id"] = json!("paused");
    update["sequence"] = json!(2);
    update["payload"]["status"] = json!("paused");
    trace[3]["sequence"] = json!(3);
    trace.as_array_mut().unwrap().insert(3, update);
    assert!(tolerant
        .validate(&serde_json::to_vec(&trace).unwrap(), "foreign-status")
        .valid());
}

#[test]
fn foreign_admission_fields_preserve_the_constraints_between_known_values() {
    let validator = Validator::tolerant().unwrap();
    let base: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    for (key, value) in [
        ("admission", "parked"),
        ("effective_delivery", "later"),
        ("status", "paused"),
    ] {
        let mut trace = base.clone();
        trace[1]["payload"][key] = json!(value);
        let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), key);
        assert!(result.valid(), "{key}: {:?}", result.diagnostics);
    }
    for (key, value, conflicting, contradiction) in [
        ("effective_delivery", "later", "status", "queued"),
        ("admission", "parked", "effective_delivery", "queue"),
    ] {
        let mut trace = base.clone();
        trace[1]["payload"][key] = json!(value);
        trace[1]["payload"][conflicting] = json!(contradiction);
        let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), key);
        assert!(result
            .diagnostics
            .iter()
            .any(|d| d.code == "illegal_run_transition"));
    }
}

#[test]
fn a_foreign_requested_delivery_does_not_claim_a_known_capability_key() {
    let validator = Validator::tolerant().unwrap();
    let mut trace: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    trace[0]["payload"]["delivery"] = json!("later");
    trace[1]["payload"]["requested_delivery"] = json!("later");
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "future-delivery");
    assert!(result.valid(), "{:?}", result.diagnostics);
}

#[test]
fn an_opaque_admission_keeps_its_status_and_terminal_obligation() {
    let validator = Validator::tolerant().unwrap();
    let mut base: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    base[1]["payload"]["admission"] = json!("merged");
    let result = validator.validate(
        &serde_json::to_vec(&base.as_array().unwrap()[..2]).unwrap(),
        "opaque-unfinished",
    );
    let codes: Vec<_> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
    assert_eq!(codes, ["missing_run_terminal"]);
    for (status, valid) in [("waiting_for_input", true), ("queued", false)] {
        let mut trace = base.clone();
        let mut update = trace[2].clone();
        update["type"] = json!("run.status.updated");
        update["id"] = json!("opaque-before-start");
        update["payload"]["status"] = json!(status);
        trace[2]["sequence"] = json!(2);
        trace[3]["sequence"] = json!(3);
        trace.as_array_mut().unwrap().insert(2, update);
        let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), status);
        assert_eq!(result.valid(), valid, "{status}: {:?}", result.diagnostics);
        assert!(!result
            .diagnostics
            .iter()
            .any(|d| d.code == "missing_run_started"));
        if !valid {
            assert!(result
                .diagnostics
                .iter()
                .any(|d| d.code == "illegal_run_transition"));
        }
    }
}

#[test]
fn a_foreign_cancel_status_does_not_force_the_known_cancelling_state() {
    let validator = Validator::tolerant().unwrap();
    for (status, valid) in [("draining", true), ("cancelling", false)] {
        let mut trace: Value =
            serde_json::from_slice(&fixture("valid/core-cancelled.json")).unwrap();
        for envelope in trace.as_array_mut().unwrap() {
            if envelope["type"] == "run.cancel.response" {
                envelope["payload"]["status"] = json!(status);
            } else if envelope["type"] == "run.status.updated" {
                envelope["payload"]["status"] = json!("running");
            }
        }
        let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), status);
        assert_eq!(result.valid(), valid, "{status}: {:?}", result.diagnostics);
    }
}

#[test]
fn tolerant_packed_validation_retains_declared_schemas() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let validator = Validator::tolerant_with_packs(&[root.join("packs/storage")]).unwrap();
    assert!(validator
        .validate(
            &fixture("valid/ext-advertised-key-gated.json"),
            "packed-positive"
        )
        .valid());
    let result = validator.validate(
        &fixture("schema-invalid/ext-packed-type-malformed.json"),
        "packed-negative",
    );
    assert_eq!(result.diagnostics[0].code, "schema_invalid");
    assert_eq!(result.diagnostics[0].pointer, "/payload");
}

#[test]
fn unknown_types_under_core_roots_do_not_claim_a_core_feature() {
    let validator = Validator::tolerant().unwrap();
    for kind in [
        "action.call.future",
        "action.permission.future",
        "user.input.future",
    ] {
        let event = json!({
            "protocol":"open-agent-protocol", "version":"0.1",
            "profile":"open-agent-protocol.agent-control-core",
            "type":kind, "id":"future", "payload":{},
        });
        let result = validator.validate(&serde_json::to_vec(&event).unwrap(), "unknown-root");
        assert!(result.valid(), "{kind}: {:?}", result.diagnostics);
    }
}

#[test]
fn an_unknown_run_event_checks_each_available_string_scope_field() {
    let validator = Validator::tolerant().unwrap();
    let mut trace: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    let mut event = trace[2].clone();
    event["type"] = json!("com.example.future.event");
    event["id"] = json!("mixed-scope");
    event["sequence"] = json!(2);
    event["payload"] = json!({"session_id":42, "run_id":"r-other"});
    trace[3]["sequence"] = json!(3);
    trace.as_array_mut().unwrap().insert(3, event);
    let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), "mixed-scope");
    let observed: Vec<_> = result
        .diagnostics
        .iter()
        .map(|d| (d.code.as_str(), d.index))
        .collect();
    assert_eq!(observed, [("scope_mismatch", 3)]);
}

#[test]
fn schema_only_task_types_keep_generic_tolerant_scope_and_run_checks() {
    let validator = Validator::tolerant().unwrap();
    let mut trace: Value = serde_json::from_slice(&fixture("valid/core-completed.json")).unwrap();
    let mut event = trace[2].clone();
    event["type"] = json!("session.task.updated");
    event["id"] = json!("task-event");
    event["payload"] = json!({"task_id":"task", "status":"running"});
    let result = validator.validate(&serde_json::to_vec(&event).unwrap(), "task-admission");
    assert!(result
        .diagnostics
        .iter()
        .any(|d| d.code == "illegal_run_transition"));
    trace[2]["sequence"] = json!(2);
    trace[3]["sequence"] = json!(3);
    trace.as_array_mut().unwrap().insert(2, event);
    assert!(validator
        .validate(&serde_json::to_vec(&trace).unwrap(), "task-prelude")
        .valid());
    let mut request = trace[0].clone();
    request["type"] = json!("session.task.output.request");
    request["payload"] = json!({"session_id":"s-other", "task_id":"task"});
    let result = validator.validate(&serde_json::to_vec(&request).unwrap(), "task-scope");
    assert!(result
        .diagnostics
        .iter()
        .any(|d| d.code == "scope_mismatch"));
}
