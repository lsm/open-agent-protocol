use std::path::Path;

use oap_validation::{codes, Phase, Validator};

#[test]
fn admission_anchors_must_be_answered_by_the_claimed_admission() {
    let validator = Validator::new().unwrap();
    for name in [
        "queue-state-anchor-request-refused",
        "queue-state-anchor-request-unaccounted",
        "queue-state-anchor-request-unanswered",
        "queue-state-entry-anchor-admitted-elsewhere",
        "queue-state-entry-leads-a-different-run",
        "compound-open-anchor-names-an-open-without-a-message",
        "anchor-session-names-state-request",
        "anchor-entry-names-state-request",
        "anchor-foreign-request",
        "anchor-foreign-admission",
        "anchor-foreign-entry-refused",
        "anchor-foreign-entry-wrong-run",
    ] {
        let path = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures/semantic-invalid")
            .join(format!("{name}.json"));
        let result = validator.validate(&std::fs::read(path).unwrap(), name);
        assert!(
            result
                .diagnostics
                .iter()
                .all(|d| d.phase == Phase::Semantic),
            "{name}: {result:?}"
        );
        assert!(
            result.has_code(codes::SESSION_STATE_MISMATCH),
            "{name}: {result:?}"
        );
    }
}

#[test]
fn a_message_carrying_open_can_anchor_the_session_its_response_allocates() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/valid/anchor-message-open-assigns-session.json");
    let result = Validator::new()
        .unwrap()
        .validate(&std::fs::read(path).unwrap(), "allocated-session");
    assert!(result.valid(), "{result:?}");
}

#[test]
fn an_answered_open_anchor_reports_an_unestablished_admission() {
    let path = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/valid/compound-open-admits-its-message.json");
    let mut trace: serde_json::Value =
        serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    trace.as_array_mut().unwrap().truncate(4);
    trace[1]["payload"]["features"]["session.open.reopen"] = serde_json::json!({"level": "native"});
    trace[2]["payload"]["reopen"] = serde_json::json!(true);
    trace[3]["payload"]["recovery"] = serde_json::json!({"recovered": true});
    let result = Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(&trace).unwrap(), "answered-open");
    let findings: Vec<_> = result
        .diagnostics
        .iter()
        .filter(|d| d.code == codes::SESSION_STATE_MISMATCH)
        .collect();
    assert_eq!(findings.len(), 1);
    assert_eq!(
        findings[0].message,
        "the answered request did not establish the claimed admission"
    );
}
