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
        assert!(result
            .diagnostics
            .iter()
            .all(|d| d.phase == Phase::Semantic));
        let got: BTreeSet<_> = result.diagnostics.into_iter().map(|d| d.code).collect();
        let want: BTreeSet<_> = codes.iter().map(|code| (*code).to_owned()).collect();
        assert_eq!(got, want, "{name}");
    }
}

#[test]
fn explicit_default_and_correlated_limits_bound_each_page() {
    check(
        &[
            "transcript-load-over-limit",
            "transcript-load-over-the-default-limit",
            "transcript-page-correlated-limit",
        ],
        &[codes::TRANSCRIPT_OVER_LIMIT],
    );
}

#[test]
fn entries_are_unique_even_when_repeated_apart() {
    check(
        &[
            "transcript-load-duplicate-entry",
            "transcript-load-returns-an-entry-twice-out-of-order",
        ],
        &[codes::DUPLICATE_TRANSCRIPT_ENTRY],
    );
}

#[test]
fn gaps_belong_to_cursor_reads_and_resume_at_the_first_returned_entry() {
    check(
        &[
            "transcript-load-gap-without-cursor",
            "transcript-load-gap-elsewhere",
            "transcript-page-empty-gap-names-entry",
            "transcript-page-gap-omits-first-entry",
            "transcript-page-correlated-cursor",
        ],
        &[codes::TRANSCRIPT_GAP_MISMATCH],
    );
}

#[test]
fn independent_page_defects_are_all_reported() {
    check(
        &["transcript-page-combined-faults"],
        &[
            codes::TRANSCRIPT_OVER_LIMIT,
            codes::DUPLICATE_TRANSCRIPT_ENTRY,
            codes::TRANSCRIPT_GAP_MISMATCH,
        ],
    );
}

#[test]
fn empty_gap_and_reversed_correlated_replies_are_valid() {
    check(
        &[
            "transcript-load-paged",
            "transcript-load-gap",
            "transcript-page-empty-gap",
            "transcript-page-correlated-reversed-replies",
        ],
        &[],
    );
}
