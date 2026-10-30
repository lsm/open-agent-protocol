use oap_adapter::gojson::{
    decode, go_float, raw, remarshal_any, strings, structure, structures, zero, Decode,
};
use serde::Deserialize;
use serde_json::value::RawValue;

#[derive(Debug, Default, Deserialize, PartialEq)]
struct Inner {
    #[serde(default, deserialize_with = "zero")]
    name: String,
}

impl Decode for Inner {}

#[derive(Debug, Default, Deserialize)]
struct Outer {
    #[serde(default, deserialize_with = "zero")]
    count: i64,
    #[serde(default, deserialize_with = "strings")]
    tags: Vec<String>,
    #[serde(default, deserialize_with = "structure")]
    inner: Inner,
    #[serde(default, deserialize_with = "structures")]
    items: Vec<Inner>,
    #[serde(default, deserialize_with = "raw")]
    extra: Option<Box<RawValue>>,
}

#[test]
fn a_null_member_decodes_to_the_zero_value() {
    let outer: Outer = serde_json::from_str(
        r#"{"count":null,"tags":null,"inner":null,"items":null,"extra":null}"#,
    )
    .unwrap();
    assert_eq!(outer.count, 0);
    assert!(outer.tags.is_empty() && outer.items.is_empty());
    assert_eq!(outer.inner, Inner::default());
    assert_eq!(outer.extra.as_deref().map(RawValue::get), Some("null"));
}

#[test]
fn a_null_slice_element_decodes_to_the_zero_value() {
    let outer: Outer =
        serde_json::from_str(r#"{"tags":["a",null],"items":[{"name":"x"},null]}"#).unwrap();
    assert_eq!(outer.tags, ["a", ""]);
    assert_eq!(outer.items, [Inner { name: "x".into() }, Inner::default()]);
}

#[test]
fn an_absent_raw_member_stays_none() {
    let outer: Outer = serde_json::from_str("{}").unwrap();
    assert!(outer.extra.is_none());
}

#[test]
fn a_struct_member_that_is_not_an_object_is_refused() {
    for body in [r#"{"inner":[]}"#, r#"{"inner":"x"}"#, r#"{"items":{}}"#] {
        assert!(serde_json::from_str::<Outer>(body).is_err(), "{body}");
    }
}

#[test]
fn decode_reads_a_top_level_null_as_the_zero_value() {
    assert_eq!(decode::<Inner>(b"null").unwrap(), Inner::default());
    assert_eq!(
        decode::<Inner>(br#"{"name":"x"}"#).unwrap(),
        Inner { name: "x".into() }
    );
    assert!(decode::<Inner>(b"[]").is_err());
}

#[test]
fn go_float_formats_numbers_as_encoding_json_does() {
    for (value, want) in [
        (0.0, "0"),
        (1.0, "1"),
        (0.5, "0.5"),
        (-2.25, "-2.25"),
        (0.000001, "0.000001"),
        (0.0000001, "1e-7"),
        (1e20, "100000000000000000000"),
        (1e21, "1e+21"),
        (1.5e300, "1.5e+300"),
        (-3e-10, "-3e-10"),
    ] {
        assert_eq!(go_float(value), want, "{value}");
    }
}

#[test]
fn remarshal_any_sorts_keys_and_reformats_numbers() {
    assert_eq!(
        remarshal_any(r#"{ "b": 1.0, "a": [2e21, {"d": "<", "c": null}], "e": true }"#).unwrap(),
        r#"{"a":[2e+21,{"c":null,"d":"\u003c"}],"b":1,"e":true}"#
    );
}

#[test]
fn remarshal_any_refuses_malformed_json() {
    assert!(remarshal_any("{").is_err());
}

#[test]
fn remarshal_any_accepts_go_s_nesting_limit_and_refuses_one_past_it() {
    let nested = |levels: usize| format!("{}{}", "[".repeat(levels), "]".repeat(levels));
    assert_eq!(remarshal_any(&nested(10_000)).unwrap(), nested(10_000));
    assert!(remarshal_any(&nested(10_001)).is_err());
}

#[test]
fn backspace_and_form_feed_keep_the_short_escapes_go_writes() {
    assert_eq!(
        remarshal_any(r#"{"a":"x\u0008y\u000cz"}"#).unwrap(),
        r#"{"a":"x\by\fz"}"#
    );
}
