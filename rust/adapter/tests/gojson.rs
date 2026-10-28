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

#[test]
fn the_walk_refuses_every_shape_go_s_decoder_refuses() {
    for raw in [
        "not json",
        "{}x",
        "123abc",
        r#"{"a":1,}"#,
        "[1,]",
        r#"{"a"}"#,
        "nul",
        r#""\u00""#,
        r#"{"a":}"#,
        "1.2.3",
        "[}",
        r#"{"a":1 "b":2}"#,
        "0000",
        "{1:2}",
        "01",
        "-",
        "1.",
        "1e",
        ".5",
        "[1 2]",
        r#"{"a":1]"#,
        "\"tab\there\"",
    ] {
        assert!(
            reject_duplicate_keys(raw.as_bytes()).is_err(),
            "the walk admitted {raw:?}"
        );
    }
}

#[test]
fn the_walk_admits_well_formed_json_go_admits() {
    for raw in [
        "{}",
        "[]",
        "null",
        "true",
        "false",
        "1.5",
        "-0",
        "-0.5e-3",
        "1E+2",
        r#""text""#,
        r#"{"ts":1e999}"#,
        "[1e-999]",
        "1e700",
        r#"{"a":{"b":{"c":1}}}"#,
        r#" { "a" : [ 1 , { "b" : null } ] } "#,
        r#""\uD83D\uDE00 \/ \n""#,
    ] {
        assert_eq!(reject_duplicate_keys(raw.as_bytes()), Ok(()), "{raw:?}");
    }
}

#[test]
fn a_second_value_is_trailing_only_when_it_is_itself_json() {
    assert_eq!(reject_duplicate_keys(b"0 0"), Err(WalkError::Trailing));
    assert_eq!(reject_duplicate_keys(b"{} {}"), Err(WalkError::Trailing));
    assert_eq!(reject_duplicate_keys(b"[][]"), Err(WalkError::Trailing));
    assert_eq!(reject_duplicate_keys(b"{} x"), Err(WalkError::Syntax));
}

#[test]
fn every_recorded_harness_frame_walks_as_serde_parses_it() {
    let root = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/adapters");
    let mut checked = 0;
    let mut pending = vec![root];
    while let Some(dir) = pending.pop() {
        for entry in std::fs::read_dir(&dir).unwrap() {
            let path = entry.unwrap().path();
            if path.is_dir() {
                pending.push(path);
                continue;
            }
            if path
                .extension()
                .is_none_or(|ext| ext != "jsonl" && ext != "json")
            {
                continue;
            }
            let text = std::fs::read_to_string(&path).unwrap();
            let lines: Vec<&str> = if path.extension().is_some_and(|ext| ext == "jsonl") {
                text.lines().collect()
            } else {
                vec![text.as_str()]
            };
            for line in lines {
                let parsed = serde_json::from_str::<serde::de::IgnoredAny>(line).is_ok();
                let walked = reject_duplicate_keys(line.as_bytes());
                if parsed {
                    assert_eq!(walked, Ok(()), "{}: {line}", path.display());
                }
                checked += 1;
            }
        }
    }
    assert!(checked > 100, "only {checked} recorded frames were found");
}

#[test]
fn go_quote_follows_strconv_printability() {
    for (code, want) in [
        (0x20, "\" \""),
        (0x7e, "\"~\""),
        (0x7f, r#""\x7f""#),
        (0xa0, r#""\u00a0""#),
        (0xa1, "\"\u{a1}\""),
        (0xad, r#""\u00ad""#),
        (0x378, r#""\u0378""#),
        (0x37a, "\"\u{37a}\""),
        (0x200b, r#""\u200b""#),
        (0x2028, r#""\u2028""#),
        (0xe000, r#""\ue000""#),
        (0xfeff, r#""\ufeff""#),
        (0xfffd, "\"\u{fffd}\""),
        (0x1f600, "\"\u{1f600}\""),
        (0xe0001, r#""\U000e0001""#),
        (0x10ffff, r#""\U0010ffff""#),
        (0x59f0f, r#""\U00059f0f""#),
        (0x1da6d, "\"\u{1da6d}\""),
        (0x20862, "\"\u{20862}\""),
    ] {
        let text = char::from_u32(code).unwrap().to_string();
        assert_eq!(go_quote(&text), want, "U+{code:04X}");
    }
    assert_eq!(go_quote("\u{7}\u{8}\u{b}\u{c}\u{0}"), r#""\a\b\v\f\x00""#);
}

#[test]
fn a_bad_escape_before_a_multibyte_character_is_a_syntax_error_and_not_a_panic() {
    let frame = r#"["\ud800\é"]"#.to_owned();
    assert_eq!(
        reject_duplicate_keys(frame.as_bytes()),
        Err(WalkError::Syntax)
    );
    assert_eq!(replace_lone_surrogates(&frame), r#"["\ufffd\é"]"#);
}

#[test]
fn invalid_utf8_is_refused_where_go_s_decoder_would_substitute_it() {
    assert_eq!(
        reject_duplicate_keys(b"{\"\xff\":1}"),
        Err(WalkError::Syntax)
    );
}
