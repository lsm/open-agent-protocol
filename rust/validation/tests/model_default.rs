use oap_validation::Validator;

fn unapplied(fixture: &str) -> Vec<(String, String, String, String)> {
    let path = format!(
        "{}/../../fixtures/semantic-invalid/{fixture}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .filter(|d| d.code == "unapplied_control")
        .map(|d| (d.envelope_id, d.pointer, d.expected, d.actual))
        .collect()
}

#[test]
fn a_per_run_model_selection_leaves_the_session_default_alone() {
    for (fixture, envelope) in [
        ("controls-per-run-changes-default", "state-after-resp"),
        ("controls-per-run-overwrites-default", "state-after-resp"),
        (
            "controls-per-run-overwrites-default-at-terminal",
            "state-after-resp",
        ),
        (
            "controls-per-run-overwrites-default-after-refresh",
            "state-after-resp",
        ),
        ("models-open-response-guards-the-default", "state-resp"),
    ] {
        let found = unapplied(fixture);
        assert_eq!(found.len(), 1, "{fixture}: {found:?}");
        let (id, pointer, expected, actual) = &found[0];
        assert_eq!(
            (id.as_str(), pointer.as_str()),
            (envelope, "/payload/current_model_id"),
            "{fixture}"
        );
        assert_ne!(expected, actual, "{fixture}");
    }
}

fn unapplied_in(trace: &str) -> Vec<(String, String, String)> {
    let path = format!(
        "{}/tests/data/model_default/{trace}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .filter(|d| d.code == "unapplied_control")
        .map(|d| (d.envelope_id, d.expected, d.actual))
        .collect()
}

#[test]
fn a_switch_moves_the_default_a_per_run_selection_keeps() {
    assert_eq!(unapplied_in("switch-after"), []);
    assert_eq!(unapplied_in("switch-before"), []);
}

#[test]
fn a_queued_per_run_selection_guards_the_default_from_its_promotion() {
    let moved = ("st2".to_owned(), "model-b".to_owned(), "model-c".to_owned());
    assert_eq!(unapplied_in("promoted-moves-default"), [moved]);
    assert_eq!(unapplied_in("default-moves-while-queued"), []);
}

#[test]
fn a_switch_response_that_answers_no_matching_request_leaves_the_guard() {
    for trace in [
        "switch-uncorrelated",
        "switch-other-model",
        "switch-other-session",
    ] {
        let moved: Vec<String> = unapplied_in(trace)
            .into_iter()
            .filter(|(_, expected, _)| expected == "default-model")
            .map(|(envelope, _, _)| envelope)
            .collect();
        assert_eq!(moved, ["sw-updated", "state-after-resp"], "{trace}");
    }
}
