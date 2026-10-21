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
        assert!(
            result
                .diagnostics
                .iter()
                .all(|d| d.phase == Phase::Semantic),
            "{name}: {:?}",
            result.diagnostics
        );
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
fn an_open_requires_affirmative_session_open_disclosure() {
    check(
        &[
            "session-settings-unadvertised-admitted",
            "session-settings-live-only-admitted-at-open",
            "open-settings-reasoning-missing-mode",
            "open-settings-gates-before-fidelity",
        ],
        codes::UNAVAILABLE_CAPABILITY,
    );
}

#[test]
fn degraded_settings_require_their_own_opt_in() {
    check(
        &[
            "session-settings-degraded-admitted-without-optin",
            "open-settings-policy-degraded-wrong-optin",
        ],
        codes::DEGRADED_WITHOUT_OPTIN,
    );
}

#[test]
fn refusals_name_the_required_feature_and_reason_with_one_owner() {
    check(
        &[
            "session-settings-unadvertised-wrong-refusal",
            "open-settings-refusal-missing-feature",
            "open-settings-refusal-wrong-reason",
            "open-settings-mixed-defects-wrong-refusal",
        ],
        codes::UNAVAILABLE_CAPABILITY,
    );
}

#[test]
fn reported_values_match_the_correlated_open_without_rounding() {
    check(
        &[
            "session-settings-level-rounded",
            "session-settings-policy-rounded",
            "open-settings-share-rounded",
            "open-settings-tokens-rounded",
            "open-settings-reversed-replies-rounded",
        ],
        codes::SESSION_STATE_MISMATCH,
    );
}

#[test]
fn matching_unknown_opted_in_and_discharged_opens_are_valid() {
    check(
        &[
            "session-settings-at-open",
            "session-settings-share-at-open",
            "session-settings-unadvertised-refused",
            "session-settings-live-only-refused-at-open",
            "session-settings-degraded-with-optin",
            "session-settings-degraded-refused-without-optin",
            "session-settings-state-omits-unknown",
            "open-settings-nonfirst-defect-refused",
            "open-settings-open-level-refusal",
            "open-settings-setting-refusal-with-subscribe",
            "open-settings-setting-refusal-discharges-provide",
        ],
        "",
    );
}
