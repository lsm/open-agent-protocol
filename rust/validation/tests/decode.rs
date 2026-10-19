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
