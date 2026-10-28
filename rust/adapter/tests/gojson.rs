use oap_adapter::gojson::{
    go_marshal, go_quote, reject_duplicate_keys, replace_lone_surrogates, WalkError, NESTING_LIMIT,
};

fn duplicate(frame: &str) -> Option<String> {
    match reject_duplicate_keys(frame.as_bytes()) {
        Err(WalkError::Duplicate(key)) => Some(key),
        _ => None,
    }
}

#[test]
fn a_key_written_twice_is_refused_at_any_depth() {
    assert_eq!(duplicate(r#"{"a":1,"a":2}"#).as_deref(), Some("a"));
    assert_eq!(duplicate(r#"{"k":{"n":1,"n":2}}"#).as_deref(), Some("n"));
    assert_eq!(
        duplicate(r#"[{"x":[{"y":1,"y":2}]}]"#).as_deref(),
        Some("y")
    );
    assert_eq!(duplicate(r#"{"\u0061":1,"a":2}"#).as_deref(), Some("a"));
}

#[test]
fn the_same_key_in_sibling_objects_is_not_a_duplicate() {
    assert_eq!(
        reject_duplicate_keys(br#"{"a":{"k":1},"b":{"k":"k"},"c":["k","k"]}"#),
        Ok(())
    );
    assert_eq!(reject_duplicate_keys(br#" "a" "#), Ok(()));
    assert_eq!(reject_duplicate_keys(b"[1, true, null, -2.5e3]"), Ok(()));
}

#[test]
fn a_value_after_the_first_is_trailing() {
    assert_eq!(
        reject_duplicate_keys(br#"{"a":1}{"b":2}"#),
        Err(WalkError::Trailing)
    );
    assert_eq!(reject_duplicate_keys(b"1 2"), Err(WalkError::Trailing));
}

#[test]
fn unbalanced_input_is_a_syntax_error() {
    assert_eq!(reject_duplicate_keys(b"{"), Err(WalkError::Syntax));
    assert_eq!(reject_duplicate_keys(b"]"), Err(WalkError::Syntax));
    assert_eq!(reject_duplicate_keys(b""), Err(WalkError::Syntax));
    assert_eq!(reject_duplicate_keys(br#"{"a"#), Err(WalkError::Syntax));
}

#[test]
fn nesting_stops_at_go_s_depth_limit() {
    let deep = "[".repeat(NESTING_LIMIT) + &"]".repeat(NESTING_LIMIT);
    assert_eq!(reject_duplicate_keys(deep.as_bytes()), Ok(()));
    let deeper = "[".repeat(NESTING_LIMIT + 1) + &"]".repeat(NESTING_LIMIT + 1);
    assert_eq!(
        reject_duplicate_keys(deeper.as_bytes()),
        Err(WalkError::TooDeep)
    );
}

#[test]
fn the_duplicate_message_quotes_the_key_as_go_does() {
    assert_eq!(
        WalkError::Duplicate("a\"b\n".into()).to_string(),
        r#"duplicate object key "a\"b\n""#
    );
    assert_eq!(go_quote("\u{1}\u{7f}é"), r#""\x01\x7fé""#);
}

#[test]
fn lone_surrogates_become_the_replacement_character() {
    assert_eq!(replace_lone_surrogates(r#""\ud800x""#), r#""\ufffdx""#);
    assert_eq!(replace_lone_surrogates(r#""\uDC00""#), r#""\ufffd""#);
    assert_eq!(
        replace_lone_surrogates(r#"["\ud83d\ude00","\ud83d"]"#),
        r#"["\ud83d\ude00","\ufffd"]"#
    );
    assert_eq!(replace_lone_surrogates(r#""\\ud800""#), r#""\\ud800""#);
    assert_eq!(replace_lone_surrogates(r#""é\u0041""#), r#""é\u0041""#);
    let decoded: String = serde_json::from_str(&replace_lone_surrogates(r#""\ud800""#)).unwrap();
    assert_eq!(decoded, "\u{fffd}");
}

#[test]
fn marshal_compacts_and_escapes_html_inside_strings_only() {
    assert_eq!(
        go_marshal("{ \"a\" : [1, 2],\n\t\"b\": \"x <y> & z\" }"),
        r#"{"a":[1,2],"b":"x \u003cy\u003e \u0026 z"}"#
    );
    assert_eq!(
        go_marshal("\"\u{2028}\u{2029} \\\" <\""),
        r#""\u2028\u2029 \" \u003c""#
    );
    assert_eq!(
        go_marshal(&serde_json::to_string("tab\tthere\u{1}").unwrap()),
        r#""tab\tthere\u0001""#
    );
}
