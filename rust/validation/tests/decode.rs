use oap_validation::{codes, duplicate_key, Phase, Validator};

const ONE: &str = r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"c1","payload":{}}"#;

#[test]
fn a_duplicate_key_is_found_at_any_depth_and_named() {
    assert_eq!(duplicate_key(r#"{"a":1,"a":2}"#).as_deref(), Some("a"));
    assert_eq!(
        duplicate_key(r#"{"a":[{"b":1},{"c":1,"c":2}]}"#).as_deref(),
        Some("c")
    );
    assert_eq!(duplicate_key(r#"{"a":{"b":1},"b":{"a":1}}"#), None);
}

#[test]
fn a_jsonl_trace_reports_the_line_that_does_not_parse() {
    let validator = Validator::new().unwrap();
    let result = validator.validate(format!("{ONE}\n\n{{\"broken\"\n").as_bytes(), "t");
    assert_eq!(result.diagnostics.len(), 1);
    let diagnostic = &result.diagnostics[0];
    assert_eq!(
        (diagnostic.code.as_str(), diagnostic.phase, diagnostic.line),
        (codes::MALFORMED_JSON, Phase::Decode, 3)
    );
}

#[test]
fn each_envelope_is_judged_on_its_own_and_reports_its_index() {
    let validator = Validator::new().unwrap();
    let bad = ONE.replace(r#""version":"0.1""#, r#""version":"9""#);
    let result = validator.validate(format!("[{ONE},{bad},{ONE}]").as_bytes(), "t");
    assert_eq!(result.diagnostics.len(), 1);
    assert_eq!(result.diagnostics[0].code, codes::SCHEMA_INVALID);
    assert_eq!(result.diagnostics[0].index, 1);
    assert!(validator.validate(b"  ", "t").valid());
}

const REPLY: &str = r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.reply.request","id":"r1","payload":{"flow_id":"f","prompt_id":"p","answer":"SENSITIVE_TEST_CODE"}}"#;

fn assert_redacted(trace: &str, code: &str, message: &str) {
    let result = Validator::new().unwrap().validate(trace.as_bytes(), "t");
    assert_eq!(result.diagnostics.len(), 1, "{trace}");
    let diagnostic = &result.diagnostics[0];
    assert_eq!(diagnostic.code, code);
    assert_eq!(diagnostic.message, message);
    let spelled = serde_json::to_string(diagnostic).unwrap();
    assert!(!spelled.contains("SENSITIVE_TEST_CODE"), "{spelled}");
}

#[test]
fn an_auth_answer_never_reaches_a_diagnostic_on_any_failure_path() {
    assert_redacted(
        &format!("[{REPLY}]"),
        codes::SCHEMA_INVALID,
        "invalid auth reply (sensitive answer redacted)",
    );
    assert_redacted(
        &REPLY.replace(r#""id":"r1","#, r#""id":"r1","id":"r2","#),
        codes::DUPLICATE_KEY,
        "duplicate key in auth reply (sensitive answer redacted)",
    );
    assert_redacted(
        &format!("{REPLY}\n{}", &REPLY[..REPLY.len() - 2]),
        codes::MALFORMED_JSON,
        "malformed auth reply trace (sensitive answer redacted)",
    );
}

#[test]
fn a_schema_diagnostic_points_at_the_failing_member_of_the_declared_type() {
    let validator = Validator::new().unwrap();
    for (fixture, pointer) in [
        (
            "schema-invalid/session-list-limit-out-of-range.json",
            "/payload/limit",
        ),
        (
            "schema-invalid/session-settings-share-over-hundred.json",
            "/payload/compaction_policy/share_percent",
        ),
        ("schema-invalid/control-resolve-two-arms.json", "/payload"),
        (
            "schema-invalid/auth-provider-without-kinds.json",
            "/payload/providers/0",
        ),
    ] {
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../fixtures")
            .join(fixture);
        let result = validator.validate(&std::fs::read(path).unwrap(), fixture);
        assert_eq!(result.diagnostics.len(), 1, "{fixture}");
        assert_eq!(result.diagnostics[0].pointer, pointer, "{fixture}");
    }
}

#[test]
fn invalid_utf8_inside_a_string_is_replaced_rather_than_refused() {
    let mut trace = ONE.as_bytes().to_vec();
    let at = trace.windows(4).position(|w| w == b"\"c1\"").unwrap();
    trace[at + 2] = 0xff;
    let result = Validator::new().unwrap().validate(&trace, "t");
    assert!(
        result
            .diagnostics
            .iter()
            .all(|d| d.phase == Phase::Semantic),
        "{:?}",
        result.diagnostics
    );
}
