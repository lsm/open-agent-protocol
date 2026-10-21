use std::collections::BTreeSet;
use std::path::PathBuf;

use oap_validation::{codes, Phase, Validator};
use serde_json::Value;

fn fixture(name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures")
        .join(name);
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn judge(trace: &Value) -> BTreeSet<String> {
    let result = Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "session-list");
    assert!(result
        .diagnostics
        .iter()
        .all(|d| d.phase == Phase::Semantic));
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

#[test]
fn a_page_respects_its_explicit_limit() {
    let mut trace = fixture("semantic-invalid/session-list-over-limit.json");
    assert_eq!(
        judge(&trace),
        BTreeSet::from([codes::SESSION_LIST_OVER_LIMIT.to_owned()])
    );
    trace[2]["payload"]["limit"] = 2.into();
    assert!(judge(&trace).is_empty());
}

#[test]
fn an_omitted_limit_allows_fifty_entries_but_not_fifty_one() {
    let mut trace = fixture("semantic-invalid/session-list-over-the-default-limit.json");
    assert_eq!(
        judge(&trace),
        BTreeSet::from([codes::SESSION_LIST_OVER_LIMIT.to_owned()])
    );
    trace[3]["payload"]["sessions"]
        .as_array_mut()
        .unwrap()
        .pop();
    assert!(judge(&trace).is_empty());
}

#[test]
fn pages_are_newest_first_with_session_id_breaking_ties() {
    for name in ["session-list-out-of-order", "session-list-tie-out-of-order"] {
        assert_eq!(
            judge(&fixture(&format!("semantic-invalid/{name}.json"))),
            BTreeSet::from([codes::SESSION_LIST_ORDER.to_owned()]),
            "{name}"
        );
    }
    assert!(judge(&fixture("valid/session-list-paged.json")).is_empty());
    assert!(judge(&fixture("valid/session-list-ties-by-session-id.json")).is_empty());
}

#[test]
fn each_session_occurs_once_per_page_even_when_duplicates_are_separated() {
    for name in [
        "session-list-duplicate-entry",
        "session-list-lists-one-session-twice-apart",
    ] {
        assert_eq!(
            judge(&fixture(&format!("semantic-invalid/{name}.json"))),
            BTreeSet::from([codes::DUPLICATE_SESSION_ENTRY.to_owned()]),
            "{name}"
        );
    }
}

#[test]
fn limits_follow_the_correlated_request_when_responses_arrive_out_of_order() {
    let trace = fixture("valid/session-list-paged.json");
    let mut small = trace[2].clone();
    small["payload"]["limit"] = 1.into();
    let mut large = trace[2].clone();
    large["id"] = "large".into();
    let mut large_response = trace[3].clone();
    large_response["id"] = "large-response".into();
    large_response["in_reply_to"] = "large".into();
    let mut small_response = trace[3].clone();
    small_response["payload"]["sessions"]
        .as_array_mut()
        .unwrap()
        .pop();
    let mut trace = serde_json::json!([
        trace[0],
        trace[1],
        small,
        large,
        large_response,
        small_response
    ]);
    assert!(judge(&trace).is_empty());
    trace[5]["payload"]["sessions"] = trace[4]["payload"]["sessions"].clone();
    assert_eq!(
        judge(&trace),
        BTreeSet::from([codes::SESSION_LIST_OVER_LIMIT.to_owned()])
    );
}

#[test]
fn separate_pages_may_list_the_same_session_again() {
    let mut trace = fixture("valid/session-list-paged.json");
    trace[5]["payload"]["sessions"][0]["session_id"] =
        trace[3]["payload"]["sessions"][0]["session_id"].clone();
    assert!(judge(&trace).is_empty());
}
