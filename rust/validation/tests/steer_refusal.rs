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
