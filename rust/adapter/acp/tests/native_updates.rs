use oap_adapter_acp::native::{
    decode, AgentMessageChunk, ContentBlock, PermissionRequest, PermissionResponse,
    SessionUpdateParams, ToolCall, ToolCallUpdate, UpdateHeader,
};

#[test]
fn a_null_struct_member_decodes_as_its_zero_value() {
    let chunk = decode::<AgentMessageChunk>(
        br#"{"sessionUpdate":"agent_message_chunk","content":null,"messageId":null}"#,
    )
    .unwrap();
    assert_eq!(chunk.content, ContentBlock::default());
    assert_eq!(chunk.message_id, "");
}

#[test]
fn a_struct_member_refuses_an_array() {
    assert!(decode::<AgentMessageChunk>(br#"{"content":["text"]}"#).is_err());
    assert!(decode::<PermissionRequest>(br#"{"toolCall":[]}"#).is_err());
}

#[test]
fn raw_members_keep_an_explicit_null() {
    let call = decode::<ToolCall>(
        br#"{"sessionUpdate":"tool_call","toolCallId":"t","title":"T","rawInput":null,"rawOutput":{"a":1}}"#,
    )
    .unwrap();
    assert_eq!(call.raw_input.as_deref().map(|raw| raw.get()), Some("null"));
    assert_eq!(
        call.raw_output.as_deref().map(|raw| raw.get()),
        Some(r#"{"a":1}"#)
    );
    assert!(call.content.is_none());
}

#[test]
fn tool_call_update_tells_absent_from_empty() {
    let update = decode::<ToolCallUpdate>(
        br#"{"sessionUpdate":"tool_call_update","toolCallId":"t","title":"","status":null}"#,
    )
    .unwrap();
    assert_eq!(update.title.as_deref(), Some(""));
    assert_eq!(update.status, None);
    assert_eq!(update.kind, None);
}

#[test]
fn an_update_is_kept_raw_until_its_header_names_it() {
    let params = decode::<SessionUpdateParams>(
        br#"{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}"#,
    )
    .unwrap();
    let update = params.update.unwrap();
    let header = decode::<UpdateHeader>(update.get().as_bytes()).unwrap();
    assert_eq!(header.session_update, "agent_message_chunk");
    let chunk = decode::<AgentMessageChunk>(update.get().as_bytes()).unwrap();
    assert_eq!(chunk.content.text, "hi");
}

#[test]
fn a_permission_request_carries_its_tool_call_and_options() {
    let request = decode::<PermissionRequest>(
        br#"{"sessionId":"s","toolCall":{"toolCallId":"t","title":"Run"},"options":[{"optionId":"a","name":"Allow","kind":"allow_once"},null]}"#,
    )
    .unwrap();
    assert_eq!(request.tool_call.tool_call_id, "t");
    assert_eq!(request.options.len(), 2);
    assert_eq!(request.options[0].kind, "allow_once");
    assert_eq!(request.options[1].option_id, "");
}

#[test]
fn a_permission_response_omits_an_empty_option() {
    let cancelled: PermissionResponse =
        serde_json::from_str(r#"{"outcome":{"outcome":"cancelled"}}"#).unwrap();
    assert_eq!(
        serde_json::to_string(&cancelled).unwrap(),
        r#"{"outcome":{"outcome":"cancelled"}}"#
    );
    let selected: PermissionResponse =
        serde_json::from_str(r#"{"outcome":{"outcome":"selected","optionId":"a"}}"#).unwrap();
    assert_eq!(
        serde_json::to_string(&selected).unwrap(),
        r#"{"outcome":{"outcome":"selected","optionId":"a"}}"#
    );
}
