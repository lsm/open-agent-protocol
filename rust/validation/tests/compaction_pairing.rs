use std::collections::BTreeSet;
use std::path::PathBuf;

use oap_validation::{codes, Phase, Validator};

fn check(names: &[&str], codes: &[&str]) {
    let validator = Validator::new().unwrap();
    for name in names {
        let kind = if codes.is_empty() {
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
        let want: BTreeSet<_> = codes.iter().map(|code| (*code).to_owned()).collect();
        assert_eq!(got, want, "{name}");
    }
}

#[test]
fn a_second_start_preserves_the_open_pair() {
    check(
        &[
            "compaction-unpaired",
            "compaction-started-again-before-the-first-ended",
            "compaction-pair-second-start-preserves-first",
        ],
        &[codes::COMPACTION_UNPAIRED],
    );
}

#[test]
fn an_end_needs_its_matching_start_in_the_same_run() {
    check(
        &[
            "compaction-ended-without-start",
            "compaction-ended-names-a-compaction-that-never-started",
            "compaction-pair-second-end",
            "compaction-pair-end-in-another-run",
        ],
        &[codes::COMPACTION_ENDED_WITHOUT_START],
    );
}

#[test]
fn all_run_terminals_require_the_open_pair_to_end() {
    check(
        &[
            "compaction-open-at-terminal",
            "compaction-open-when-the-run-fails",
            "compaction-pair-open-at-cancelled",
        ],
        &[codes::COMPACTION_OPEN_AT_TERMINAL],
    );
}

#[test]
fn a_failed_end_requires_an_error_even_when_its_pair_is_wrong() {
    check(
        &[
            "compaction-failed-without-error",
            "compaction-failed-without-error-in-a-requested-run",
        ],
        &[codes::COMPACTION_FAILED_WITHOUT_ERROR],
    );
    check(
        &["compaction-pair-wrong-failed-end-preserves-first"],
        &[
            codes::COMPACTION_FAILED_WITHOUT_ERROR,
            codes::COMPACTION_ENDED_WITHOUT_START,
        ],
    );
}

#[test]
fn completed_failed_cancelled_sequential_and_interleaved_pairs_are_valid() {
    check(
        &[
            "compaction-requested-run",
            "compaction-threshold-inside-run",
            "compaction-pair-two-sequential",
            "compaction-pair-failed-end-with-error",
            "compaction-pair-cancelled-end",
            "compaction-pair-interleaved-runs",
        ],
        &[],
    );
}
