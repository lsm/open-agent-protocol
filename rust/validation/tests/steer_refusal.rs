use oap_validation::Validator;

const MISMATCH: &str = "session_state_mismatch";
const ILLEGAL: &str = "illegal_run_transition";
const REASON: &str = "/payload/error/details/reason";
const CODE: &str = "/payload/error/code";

fn drawn(path: &str) -> Vec<(String, String, String, String)> {
    let path = format!("{}/../../fixtures/{path}", env!("CARGO_MANIFEST_DIR"));
    let data = std::fs::read(&path).unwrap();
    let mut found: Vec<_> = Validator::new()
        .unwrap()
        .validate(&data, &path)
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id, d.pointer, d.actual))
        .collect();
    found.sort();
    found
}

fn expect(path: &str, want: &[(&str, &str, &str, &str)]) {
    let mut want: Vec<_> = want
        .iter()
        .map(|(a, b, c, d)| (a.to_string(), b.to_string(), c.to_string(), d.to_string()))
        .collect();
    want.sort();
    assert_eq!(drawn(path), want, "{path}");
}

#[test]
fn a_refused_steer_names_the_highest_ranked_target_condition() {
    for (fixture, actual) in [
        (
            "compaction-steer-no-model-turn-wrong-reason",
            "not_steerable",
        ),
        ("steer-no-active-run-wrong-reason", "queued"),
        ("steer-target-wrong-reason", "terminal"),
        ("steer-unknown-target-run-wrong-reason", "queued"),
    ] {
        let path = format!("semantic-invalid/{fixture}.json");
        expect(&path, &[(ILLEGAL, "err1", REASON, actual)]);
    }
}

#[test]
fn a_steer_whose_target_cannot_take_guidance_is_refused_as_an_invalid_target() {
    for fixture in [
        "steer-no-active-run-wrong-refusal",
        "steer-target-terminated-in-flight-wrong-refusal",
        "steer-target-wrong-refusal",
        "steer-unknown-target-run-wrong-refusal",
    ] {
        let path = format!("semantic-invalid/{fixture}.json");
        expect(&path, &[(ILLEGAL, "err1", CODE, "internal_error")]);
    }
}

#[test]
fn a_run_cannot_end_with_guidance_still_pending() {
    let pending = ("pending_steer_at_terminal", "ev2", "/type", "");
    expect(
        "semantic-invalid/steer-pending-at-terminal.json",
        &[pending],
    );
    let unmatched = ("unmatched_steer", "applied", "/payload/submission_id", "");
    expect(
        "semantic-invalid/steer-settled-before-response.json",
        &[unmatched, pending],
    );
}

#[test]
fn a_state_snapshot_lists_exactly_the_steers_pending_at_its_capture() {
    for fixture in [
        "steer-state-omits-admitted-marker",
        "steer-state-omits-pending",
        "steer-state-pending-without-position",
        "steer-state-retains-settled",
        "steer-state-wrong-request-id",
    ] {
        let path = format!("semantic-invalid/{fixture}.json");
        expect(&path, &[(MISMATCH, "stater", "/payload/active_runs", "")]);
    }
}

fn valid(name: &str) -> Vec<serde_json::Value> {
    let path = format!("{}/../../fixtures/valid/{name}", env!("CARGO_MANIFEST_DIR"));
    serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap()
}

fn at(trace: &[serde_json::Value], id: &str) -> usize {
    trace.iter().position(|e| e["id"] == id).unwrap()
}

fn entry(trace: &mut [serde_json::Value]) -> &mut serde_json::Map<String, serde_json::Value> {
    let state = at(trace, "stater");
    trace[state]["payload"]["active_runs"][0]
        .as_object_mut()
        .unwrap()
}

fn codes_of(trace: &[serde_json::Value]) -> Vec<(String, String)> {
    Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "steer-refusal")
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id))
        .collect()
}

fn unmarked(trace: &mut [serde_json::Value]) {
    let listed = entry(trace);
    listed.remove("pending_steers");
    listed.remove("admitted_submit_requests");
}

#[test]
fn a_snapshot_contradicts_a_steer_it_lists_but_never_admitted_or_hides_unmarked() {
    let contradicted = vec![(MISMATCH.to_owned(), "stater".to_owned())];
    let mut ghost = valid("steer-state-pending.json");
    entry(&mut ghost)["pending_steers"]
        .as_array_mut()
        .unwrap()
        .push(serde_json::json!({"submission_id": "steer-ghost", "request_id": "sub9"}));
    assert_eq!(codes_of(&ghost), contradicted);

    let mut hidden = valid("steer-state-pending.json");
    unmarked(&mut hidden);
    assert_eq!(codes_of(&hidden), contradicted);
}

#[test]
fn a_snapshot_need_not_mark_a_steer_settled_before_or_admitted_after_its_capture() {
    let mut settled = valid("steer-state-pending.json");
    let request = settled.remove(at(&settled, "stateq"));
    let response = settled.remove(at(&settled, "stater"));
    let after = at(&settled, "applied") + 1;
    settled.splice(after..after, [request, response]);
    unmarked(&mut settled);
    entry(&mut settled).insert("as_of_sequence".into(), serde_json::json!(2));
    assert_eq!(codes_of(&settled), []);

    let mut straddled = valid("steer-state-pending.json");
    let request = straddled.remove(at(&straddled, "stateq"));
    straddled.insert(at(&straddled, "sub2"), request);
    unmarked(&mut straddled);
    assert_eq!(codes_of(&straddled), []);
}

#[test]
fn a_steer_refused_while_its_target_can_take_guidance_owes_no_target_reason() {
    let mut trace = valid("steer-at-boundary.json");
    trace.remove(at(&trace, "applied"));
    let refusal = at(&trace, "err1");
    trace[refusal]["type"] = serde_json::json!("error.response");
    trace[refusal]["payload"] =
        serde_json::json!({"error": {"code": "internal_error", "message": "busy"}});
    let completed = at(&trace, "ev2");
    trace[completed]["sequence"] = serde_json::json!(2);
    assert_eq!(codes_of(&trace), []);
}
