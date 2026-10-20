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

fn event(kind: &str, id: &str, mut fields: Value) -> Value {
    fields["protocol"] = json!("open-agent-protocol");
    fields["version"] = json!("0.1");
    fields["profile"] = json!("open-agent-protocol.agent-control-core");
    fields["type"] = json!(kind);
    fields["id"] = json!(id);
    fields
}

fn targeting(trace: &mut [Value], run: &str) {
    find(trace, "sub2")["payload"]["target_run_id"] = json!(run);
}

fn position(trace: &[Value], id: &str) -> usize {
    trace.iter().position(|e| e["id"] == id).unwrap()
}

fn untargeted(trace: &mut [Value]) -> &mut [Value] {
    find(trace, "sub2")["payload"]
        .as_object_mut()
        .unwrap()
        .remove("target_run_id");
    trace
}

fn refused_for(trace: &[Value]) -> Vec<String> {
    validate(trace)
        .into_iter()
        .filter(|d| {
            d.code == codes::ILLEGAL_RUN_TRANSITION
                && d.envelope_id == "err1"
                && d.pointer == "/payload/run_id"
        })
        .map(|d| d.actual)
        .collect()
}

#[test]
fn a_steered_admission_names_why_its_target_cannot_take_guidance() {
    let mut terminal = fixture("steer-at-boundary.json");
    targeting(&mut terminal, "r1");
    terminal.remove(position(&terminal, "applied"));
    let mut completed = terminal.remove(position(&terminal, "ev2"));
    completed["sequence"] = json!(2);
    terminal.insert(position(&terminal, "sub2"), completed);
    assert_eq!(refused_for(&terminal), ["terminal"]);
    assert_eq!(refused_for(untargeted(&mut terminal)), ["no_active_run"]);

    let mut cancelling = fixture("steer-at-boundary.json");
    targeting(&mut cancelling, "r1");
    cancelling.remove(position(&cancelling, "applied"));
    let scope = json!({"session_id": "s1", "run_id": "r1"});
    let request = event(
        "run.cancel.request",
        "cq",
        json!({"session_id": "s1", "run_id": "r1", "payload": scope}),
    );
    let accepted = event(
        "run.cancel.response",
        "cr",
        json!({"in_reply_to": "cq", "session_id": "s1", "run_id": "r1",
            "payload": {"session_id": "s1", "run_id": "r1", "accepted": true, "status": "cancelling"}}),
    );
    let at = position(&cancelling, "sub2");
    cancelling.splice(at..at, [request, accepted]);
    let settled = find(&mut cancelling, "ev2");
    settled["type"] = json!("run.cancelled");
    settled["sequence"] = json!(2);
    settled["payload"] = json!({"session_id": "s1", "run_id": "r1", "reason": "cancel confirmed"});
    assert_eq!(refused_for(&cancelling), ["not_steerable"]);
    assert_eq!(refused_for(untargeted(&mut cancelling)), ["not_steerable"]);

    let mut cross = fixture("steer-at-boundary.json");
    targeting(&mut cross, "r1");
    let request = find(&mut cross, "sub2");
    request["session_id"] = json!("s2");
    request["payload"]["session_id"] = json!("s2");
    assert_eq!(refused_for(&cross), ["cross_session"]);

    let mut compaction = fixture("steer-at-boundary.json");
    targeting(&mut compaction, "r1");
    find(&mut compaction, "capr")["payload"]["features"]["session.compact"] =
        json!({"level": "native"});
    *find(&mut compaction, "sub1") = event(
        "session.compact.request",
        "sub1",
        json!({"session_id": "s1", "capability_revision": "cap-v1", "payload": {"session_id": "s1"}}),
    );
    *find(&mut compaction, "adm1") = event(
        "session.compact.response",
        "adm1",
        json!({"in_reply_to": "sub1", "session_id": "s1", "capability_revision": "cap-v1",
            "payload": {"session_id": "s1", "accepted": true, "submission_id": "sub-adm1",
                "requested_delivery": "auto", "effective_delivery": "start",
                "delivery_resolution": "session_idle", "admission": "started",
                "run_id": "r1", "status": "running"}}),
    );
    compaction.remove(position(&compaction, "applied"));
    compaction.remove(position(&compaction, "ev2"));
    assert_eq!(refused_for(&compaction), ["no_model_turn"]);
    assert_eq!(refused_for(untargeted(&mut compaction)), ["no_model_turn"]);

    let mut unstarted = fixture("steer-at-boundary.json");
    unstarted.remove(position(&unstarted, "applied"));
    let started = unstarted.remove(position(&unstarted, "ev1"));
    unstarted.insert(position(&unstarted, "ev2"), started);
    find(&mut unstarted, "ev2")["sequence"] = json!(2);
    assert_eq!(refused_for(&unstarted), ["no_active_run"]);

    let mut queued = fixture("steer-at-boundary.json");
    targeting(&mut queued, "r2");
    find(&mut queued, "capr")["payload"]["features"]["session.message.delivery.queue"] =
        json!({"level": "native"});
    let request = event(
        "session.message.submit.request",
        "sub3",
        json!({"session_id": "s1", "capability_revision": "cap-v1",
            "payload": {"session_id": "s1", "messages": [{"role": "user", "content": "later"}], "delivery": "queue"}}),
    );
    let reserved = event(
        "session.message.submit.response",
        "adm3",
        json!({"in_reply_to": "sub3", "session_id": "s1", "capability_revision": "cap-v1",
            "payload": {"session_id": "s1", "accepted": true, "submission_id": "sub-adm3",
                "requested_delivery": "queue", "effective_delivery": "queue",
                "delivery_resolution": "session_busy", "admission": "queued",
                "run_id": "r2", "status": "queued"}}),
    );
    let at = position(&queued, "sub2");
    queued.splice(at..at, [request, reserved]);
    assert_eq!(refused_for(&queued), ["queued"]);
}

#[test]
fn a_steer_whose_controls_were_refused_is_never_admitted() {
    let mut trace = fixture("steer-at-boundary.json");
    find(&mut trace, "sub2")["payload"]["instructions"] = json!("be brief");
    let diagnostics = validate(&trace);
    let found = found(&diagnostics);
    assert!(
        found.contains(&(codes::UNAVAILABLE_CAPABILITY, "err1", "/payload/admission")),
        "{diagnostics:#?}"
    );
    assert!(
        found.contains(&(codes::UNMATCHED_STEER, "applied", "/payload/submission_id")),
        "{diagnostics:#?}"
    );
}
