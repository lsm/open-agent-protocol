use std::collections::BTreeSet;
use std::path::PathBuf;

use oap_validation::{codes, Phase, Validator};

fn check(names: &[&str], code: &str) {
    let validator = Validator::new().unwrap();
    for name in names {
        let kind = if code.is_empty() {
            "valid"
        } else {
            "semantic-invalid"
        };
        let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures")
            .join(kind)
            .join(format!("{name}.json"));
        let result = validator.validate(&std::fs::read(path).unwrap(), name);
        assert!(result
            .diagnostics
            .iter()
            .all(|d| d.phase == Phase::Semantic));
        let got: BTreeSet<_> = result.diagnostics.into_iter().map(|d| d.code).collect();
        let want = if code.is_empty() {
            BTreeSet::new()
        } else {
            BTreeSet::from([code.to_owned()])
        };
        assert_eq!(got, want, "{name}");
    }
}

#[test]
fn flow_ids_remain_unique_while_active_and_after_completion() {
    check(
        &[
            "auth-flow-reuses-active-id",
            "auth-flow-reuses-completed-id",
        ],
        codes::AUTH_FLOW_ORDER,
    );
}

#[test]
fn events_cancellation_and_completion_require_a_started_flow() {
    check(
        &[
            "auth-event-before-start",
            "auth-completed-before-start",
            "auth-flow-cancel-before-start",
        ],
        codes::AUTH_FLOW_ORDER,
    );
}

#[test]
fn events_and_terminal_consume_a_contiguous_sequence_for_their_flow() {
    check(
        &[
            "auth-flow-first-sequence-gap",
            "auth-flow-terminal-sequence-gap",
        ],
        codes::SEQUENCE_GAP,
    );
    check(
        &["auth-flow-sequence-regression"],
        codes::SEQUENCE_REGRESSION,
    );
}

#[test]
fn nothing_for_the_flow_follows_completion() {
    check(
        &[
            "auth-flow-event-after-terminal",
            "auth-flow-duplicate-terminal",
            "auth-flow-cancel-after-terminal",
        ],
        codes::EVENT_AFTER_TERMINAL,
    );
}

#[test]
fn cancellation_acknowledgment_does_not_replace_completion() {
    check(
        &[
            "auth-missing-terminal",
            "auth-progress-without-a-terminal",
            "auth-flow-cancel-without-completion",
        ],
        codes::MISSING_AUTH_TERMINAL,
    );
}

#[test]
fn completed_interleaved_and_refused_flows_remain_valid() {
    check(
        &[
            "auth-login-interactive",
            "auth-login-cancelled",
            "auth-flow-immediate-completion",
            "auth-flow-interleaved",
            "auth-flow-refused-start",
        ],
        "",
    );
}
