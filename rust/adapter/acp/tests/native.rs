use oap_adapter_acp::native::{
    decode, ConfigOption, ConfigValue, ContentBlock, McpServer, PromptParams, PromptResult,
    SessionNewParams, SessionNewResult,
};

fn values(options: &str) -> Vec<ConfigValue> {
    let option: ConfigOption =
        serde_json::from_str(&format!(r#"{{"id":"x","name":"X","options":{options}}}"#)).unwrap();
    option.values()
}

fn value(value: &str, name: &str) -> ConfigValue {
    ConfigValue {
        value: value.into(),
        name: name.into(),
    }
}

#[test]
fn config_values_flatten_groups_after_their_own_value() {
    assert_eq!(
        values(
            r#"[{"value":"low","name":"Low"},{"name":"Group","options":[{"value":"high","name":"High"}]},{"value":"mid","name":"Mid","options":[{"value":"max","name":"Max"}]}]"#
        ),
        vec![
            value("low", "Low"),
            value("high", "High"),
            value("mid", "Mid"),
            value("max", "Max")
        ]
    );
}

#[test]
fn config_values_are_empty_when_options_do_not_decode() {
    assert!(values(r#"{"value":"low"}"#).is_empty());
    assert!(values(r#"[{"value":1}]"#).is_empty());
    assert!(values("null").is_empty());
    let absent: ConfigOption = serde_json::from_str(r#"{"id":"x","name":"X"}"#).unwrap();
    assert!(absent.values().is_empty());
}

#[test]
fn config_values_skip_null_entries() {
    assert_eq!(
        values(r#"[null,{"value":"a","name":"A"}]"#),
        vec![value("a", "A")]
    );
}

#[test]
fn session_new_result_must_be_an_object() {
    assert!(decode::<SessionNewResult>(b"null").is_err());
    assert!(decode::<SessionNewResult>(b"[\"s\"]").is_err());
    let result = decode::<SessionNewResult>(br#"{"sessionId":"s","extra":1}"#).unwrap();
    assert_eq!(result.session_id, "s");
    assert!(result.config_options.is_empty());
}

#[test]
fn a_null_string_element_decodes_as_empty() {
    let params =
        decode::<SessionNewParams>(br#"{"mcpServers":[{"name":"m","args":["a",null]}]}"#).unwrap();
    assert_eq!(params.mcp_servers[0].args, ["a", ""]);
}

#[test]
fn null_members_decode_as_their_zero_value() {
    let result = decode::<SessionNewResult>(br#"{"sessionId":null,"configOptions":null}"#).unwrap();
    assert_eq!(result.session_id, "");
    assert!(result.config_options.is_empty());
    let prompt = decode::<PromptParams>(br#"{"sessionId":"s","prompt":[null]}"#).unwrap();
    assert_eq!(prompt.prompt, vec![ContentBlock::default()]);
}

#[test]
fn a_struct_member_refuses_an_array() {
    assert!(decode::<PromptParams>(br#"{"prompt":[["text"]]}"#).is_err());
    assert!(decode::<PromptParams>(br#"{"prompt":{"type":"text"}}"#).is_err());
}

#[test]
fn raw_members_keep_an_explicit_null() {
    let result = decode::<PromptResult>(br#"{"stopReason":"end_turn","usage":null}"#).unwrap();
    assert_eq!(result.usage.as_deref().map(|raw| raw.get()), Some("null"));
    let absent = decode::<PromptResult>(br#"{"stopReason":"end_turn"}"#).unwrap();
    assert!(absent.usage.is_none());
}

#[test]
fn prompt_result_keeps_usage_raw() {
    let result =
        decode::<PromptResult>(br#"{"stopReason":"end_turn","usage":{"inputTokens":3}}"#).unwrap();
    assert_eq!(result.stop_reason, "end_turn");
    assert_eq!(result.usage.unwrap().get(), r#"{"inputTokens":3}"#);
}

#[test]
fn requests_omit_the_members_go_omits() {
    let params = SessionNewParams {
        cwd: "/w".into(),
        mcp_servers: vec![McpServer {
            name: "m".into(),
            command: "c".into(),
            args: vec![],
            env: vec![],
        }],
    };
    assert_eq!(
        serde_json::to_string(&params).unwrap(),
        r#"{"cwd":"/w","mcpServers":[{"name":"m","command":"c"}]}"#
    );
    let empty = SessionNewParams {
        cwd: "/w".into(),
        mcp_servers: vec![],
    };
    assert_eq!(
        serde_json::to_string(&empty).unwrap(),
        r#"{"cwd":"/w","mcpServers":[]}"#
    );
    let prompt = PromptParams {
        session_id: "s".into(),
        prompt: vec![ContentBlock {
            kind: "text".into(),
            text: "hi".into(),
            ..ContentBlock::default()
        }],
    };
    assert_eq!(
        serde_json::to_string(&prompt).unwrap(),
        r#"{"sessionId":"s","prompt":[{"type":"text","text":"hi"}]}"#
    );
}
