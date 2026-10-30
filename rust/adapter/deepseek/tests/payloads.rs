use oap_adapter_deepseek::native::{
    decode_strict, InboxSpliced, TodoWrite, ToolError, ToolResult, TurnEnd, UserMessage,
};

#[test]
fn a_user_source_is_direct_only_when_it_names_nothing_but_its_kind() {
    let direct: UserMessage = decode_strict(br#"{"source":{"kind":"user"}}"#).unwrap();
    assert!(direct.source.direct_user());
    let attributed: UserMessage =
        decode_strict(br#"{"source":{"kind":"user","via":"inbox"}}"#).unwrap();
    assert!(!attributed.source.direct_user());
    assert!(!UserMessage::default().source.direct_user());
}

#[test]
fn a_closed_source_kind_refuses_members_it_does_not_name_and_keeps_those_it_does() {
    let refused =
        decode_strict::<UserMessage>(br#"{"source":{"kind":"tool","callId":"c","model":"m"}}"#)
            .unwrap_err();
    assert!(
        refused.starts_with("json: unknown field \"model\""),
        "{refused}"
    );
    let model: UserMessage = decode_strict(
        br#"{"source":{"kind":"model","provider":"deepseek","model":null,"replayState":{"r":1}}}"#,
    )
    .unwrap();
    assert_eq!(model.source.provider, "deepseek");
    assert_eq!(model.source.model, "");
    assert_eq!(
        model.source.replay_state.as_deref().map(|raw| raw.get()),
        Some(r#"{"r":1}"#)
    );
}

#[test]
fn a_source_without_a_kind_or_not_an_object_is_refused_with_go_text() {
    let missing = decode_strict::<UserMessage>(br#"{"source":{}}"#).unwrap_err();
    assert!(missing.starts_with("missing value"), "{missing}");
    let mistyped = decode_strict::<UserMessage>(br#"{"source":"user"}"#).unwrap_err();
    assert!(
        mistyped.starts_with(
            "json: cannot unmarshal string into Go value of type map[string]json.RawMessage"
        ),
        "{mistyped}"
    );
}

#[test]
fn a_tool_error_reason_is_text_only_when_absent_or_a_string() {
    let reason = |data: &[u8]| decode_strict::<ToolError>(data).unwrap().reason_text();
    assert_eq!(reason(br#"{"name":"n","code":"c"}"#), Some(String::new()));
    assert_eq!(reason(br#"{"reason": "why"}"#), Some("why".into()));
    assert_eq!(reason(br#"{"reason":null}"#), None);
    assert_eq!(reason(br#"{"reason":{"x":1}}"#), None);
    let result: ToolResult = decode_strict(br#"{"error":null,"meta":{"m":true}}"#).unwrap();
    assert!(result.error.is_none());
    assert!(result.meta.is_some());
}

#[test]
fn null_and_empty_lists_stay_distinct_and_null_elements_read_as_zero_values() {
    assert!(decode_strict::<TodoWrite>(br#"{"todos":null}"#)
        .unwrap()
        .todos
        .is_none());
    assert_eq!(
        decode_strict::<TodoWrite>(br#"{"todos":[]}"#)
            .unwrap()
            .todos
            .map(|todos| todos.len()),
        Some(0)
    );
    let spliced: InboxSpliced =
        decode_strict(br#"{"target":"t","inserted":[null,{"id":"u"}],"removedCount":0}"#).unwrap();
    assert_eq!(spliced.inserted.len(), 2);
    assert_eq!(spliced.inserted[1].id, "u");
    assert_eq!(spliced.removed_count, Some(0));
    let ended: TurnEnd = decode_strict(br#"{"turn":2,"reason":"done"}"#).unwrap();
    assert_eq!(
        ended.reason.as_deref().map(|raw| raw.get()),
        Some("\"done\"")
    );
}
