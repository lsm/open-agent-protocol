#![cfg(unix)]

mod support;

use oxap_conformance::Runner;
use oxap_protocol::CapabilityDescriptor;
use support::client;

#[test]
fn a_catalog_selection_is_switched_reconciled_and_refused_when_missing() {
    let mut client = client("normal");
    let mut runner = Runner::new(&mut client, "s");
    runner.initialize();
    let descriptor = runner.capabilities().unwrap();
    let state = runner.open().unwrap();
    runner.list_sessions(&descriptor);
    let selected = runner.select_model(&descriptor, &state.current_model_id, "");
    assert_eq!(selected, "b");
    assert!(runner.switch_model(&selected, &state.current_model_id));
    assert_eq!(runner.checks().len(), 10);
    assert!(runner.checks().iter().all(|check| check.passed));
    runner.into_checks();
    assert!(client
        .transcript()
        .iter()
        .all(|envelope| !envelope.envelope_type.as_str().starts_with("session.list.")));
}

#[test]
fn an_explicit_model_wins_and_unavailable_or_malformed_catalogs_keep_the_current_model() {
    let mut client = client("normal");
    let mut runner = Runner::new(&mut client, "s");
    assert_eq!(
        runner.select_model(&CapabilityDescriptor::default(), "a", "preferred"),
        "preferred"
    );
    for mode in ["no-models", "bad-catalog"] {
        let mut client = support::client(mode);
        let mut runner = Runner::new(&mut client, "s");
        let descriptor = runner.capabilities().unwrap();
        assert_eq!(runner.select_model(&descriptor, "a", ""), "a");
    }
}

#[test]
fn list_is_checked_as_live_or_unavailable_and_the_declared_result_matters() {
    for (mode, passed) in [("normal", true), ("no-list", true), ("empty-list", false)] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        let descriptor = runner.capabilities().unwrap();
        runner.open().unwrap();
        runner.list_sessions(&descriptor);
        let check = runner.checks().last().unwrap();
        assert_eq!(check.passed, passed, "{mode}: {check:?}");
        assert!(check.name.starts_with("session.list is answered"));
    }
}

#[test]
fn incorrect_switch_acknowledgments_events_states_and_missing_model_ids_fail_their_checks() {
    for (mode, name) in [
        (
            "wrong-previous",
            "session.model.switch changes the session default",
        ),
        (
            "wrong-event",
            "session.model.switch publishes canonical state",
        ),
        (
            "wrong-state",
            "session.model.switch changes the session default",
        ),
        (
            "wrong-missing",
            "session.model.switch refuses a missing model with its id",
        ),
    ] {
        let mut client = client(mode);
        let mut runner = Runner::new(&mut client, "s");
        runner.capabilities().unwrap();
        runner.open().unwrap();
        runner.switch_model("b", "a");
        assert!(
            runner
                .checks()
                .iter()
                .any(|check| !check.passed && check.name == name),
            "{mode}: {:?}",
            runner.checks()
        );
    }
}

#[test]
fn switching_to_the_current_model_needs_no_change_event_and_an_empty_model_fails() {
    let mut client = client("normal");
    let mut runner = Runner::new(&mut client, "s");
    runner.capabilities().unwrap();
    runner.open().unwrap();
    assert!(runner.switch_model("a", "a"));
    assert!(runner.checks().iter().all(|check| check.passed));
    assert!(!runner.switch_model("", "a"));
    assert!(!runner.checks().last().unwrap().passed);
}
