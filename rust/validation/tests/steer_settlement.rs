use oap_validation::{codes, Diagnostic, Validator};
use serde_json::{json, Value};

fn fixture(name: &str) -> Vec<Value> {
    let path = format!("{}/../../fixtures/valid/{name}", env!("CARGO_MANIFEST_DIR"));
    serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap()
}

fn validate(trace: &[Value]) -> Vec<Diagnostic> {
    Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "steer-settlement")
        .diagnostics
}

fn find<'a>(trace: &'a mut [Value], id: &str) -> &'a mut Value {
    trace.iter_mut().find(|e| e["id"] == id).unwrap()
}

fn found(diagnostics: &[Diagnostic]) -> Vec<(&str, &str, &str)> {
    diagnostics
        .iter()
        .map(|d| (d.code.as_str(), d.envelope_id.as_str(), d.pointer.as_str()))
        .collect()
}

#[test]
fn a_settlement_of_an_admitted_or_recovered_steer_is_valid() {
    for name in [
        "steer-at-boundary.json",
        "steer-immediate.json",
        "steer-dropped-at-terminal.json",
        "steer-recovered-pending.json",
    ] {
        let diagnostics = validate(&fixture(name));
        assert!(diagnostics.is_empty(), "{name}: {diagnostics:#?}");
    }
}

#[test]
fn a_settlement_naming_no_admitted_steer_is_unmatched() {
    let mut trace = fixture("steer-at-boundary.json");
    find(&mut trace, "applied")["payload"]["submission_id"] = json!("steer-unknown");
    let diagnostics = validate(&trace);
    assert!(
        found(&diagnostics).contains(&(
            codes::UNMATCHED_STEER,
            "applied",
            "/payload/submission_id"
        )),
        "{diagnostics:#?}"
    );
}

#[test]
fn a_settlement_naming_another_request_is_unmatched() {
    let mut trace = fixture("steer-at-boundary.json");
    find(&mut trace, "applied")["payload"]["request_id"] = json!("sub1");
    assert_eq!(
        found(&validate(&trace)),
        [(codes::UNMATCHED_STEER, "applied", "/payload/request_id")]
    );
}

#[test]
fn an_applied_settlement_reporting_other_messages_is_unmatched() {
    let mut trace = fixture("steer-at-boundary.json");
    find(&mut trace, "applied")["payload"]["message_ids"] = json!(["other"]);
    assert_eq!(
        found(&validate(&trace)),
        [(codes::UNMATCHED_STEER, "applied", "/payload/message_ids")]
    );
    find(&mut trace, "err1")["payload"]
        .as_object_mut()
        .unwrap()
        .remove("message_ids");
    assert!(validate(&trace).is_empty());
    find(&mut trace, "err1")["payload"]["message_ids"] = json!([]);
    assert_eq!(
        found(&validate(&trace)),
        [(codes::UNMATCHED_STEER, "applied", "/payload/message_ids")]
    );
}

#[test]
fn a_steer_settled_twice_is_a_duplicate() {
    let mut trace = fixture("steer-at-boundary.json");
    let mut again = find(&mut trace, "applied").clone();
    again["id"] = json!("again");
    again["sequence"] = json!(3);
    trace.insert(trace.len() - 1, again);
    find(&mut trace, "ev2")["sequence"] = json!(4);
    assert_eq!(
        found(&validate(&trace)),
        [(codes::DUPLICATE_STEER, "again", "/payload/submission_id")]
    );
}

#[test]
fn a_settlement_must_cite_a_descriptor_advertising_steer() {
    let mut trace = fixture("steer-at-boundary.json");
    let features = &mut find(&mut trace, "capr")["payload"]["features"];
    features
        .as_object_mut()
        .unwrap()
        .remove("session.message.delivery.steer");
    let diagnostics = validate(&trace);
    assert!(
        found(&diagnostics).contains(&(codes::UNAVAILABLE_CAPABILITY, "applied", "/type")),
        "{diagnostics:#?}"
    );
}

#[test]
fn a_steer_whose_admission_names_another_run_is_never_admitted() {
    let mut trace = fixture("steer-at-boundary.json");
    find(&mut trace, "err1")["payload"]["run_id"] = json!("r2");
    let diagnostics = validate(&trace);
    let found = found(&diagnostics);
    assert!(
        found.contains(&(codes::SCOPE_MISMATCH, "err1", "/payload/run_id")),
        "{diagnostics:#?}"
    );
    assert!(
        found.contains(&(codes::UNMATCHED_STEER, "applied", "/payload/submission_id")),
        "{diagnostics:#?}"
    );
}

#[test]
fn a_recovered_run_settles_only_the_steers_its_snapshot_listed() {
    let mut trace = fixture("steer-recovered-pending.json");
    for id in ["openr", "stater"] {
        find(&mut trace, id)["payload"]["active_runs"][0]
            .as_object_mut()
            .unwrap()
            .remove("pending_steers");
    }
    assert_eq!(
        found(&validate(&trace)),
        [(codes::UNMATCHED_STEER, "applied", "/payload/submission_id")]
    );
}
