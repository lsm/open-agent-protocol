use oxap_adapter_claude::native::{
    decode_interrupt_result, deny_tool, hook_continue, initialize_request, interrupt_request,
    mcp_response, permission_allow, permission_deny,
};
use serde_json::value::RawValue;

fn raw(text: &str) -> Box<RawValue> {
    RawValue::from_string(text.to_owned()).unwrap()
}

#[test]
fn a_hook_continue_is_an_empty_object() {
    assert_eq!(hook_continue().unwrap(), "{}");
}

#[test]
fn a_tool_denial_names_the_pre_tool_use_hook_and_escapes_its_reason_as_go_does() {
    assert_eq!(
        deny_tool("Tool \"x\" <is> & refused").unwrap(),
        r#"{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Tool \"x\" \u003cis\u003e \u0026 refused"}}"#
    );
}

#[test]
fn an_initialize_request_registers_the_tool_selection_hook_and_names_servers_only_when_given() {
    assert_eq!(
        initialize_request(&[]).unwrap(),
        r#"{"subtype":"initialize","hooks":{"PreToolUse":[{"matcher":null,"hookCallbackIds":["oap_tool_selection"]}]}}"#
    );
    assert_eq!(
        initialize_request(&["oap".to_owned(), "b".to_owned()]).unwrap(),
        r#"{"subtype":"initialize","hooks":{"PreToolUse":[{"matcher":null,"hookCallbackIds":["oap_tool_selection"]}]},"sdkMcpServers":["oap","b"]}"#
    );
}

#[test]
fn an_interrupt_request_carries_only_its_subtype() {
    assert_eq!(interrupt_request().unwrap(), r#"{"subtype":"interrupt"}"#);
}

#[test]
fn a_permission_allow_carries_the_input_compacted_and_escaped_as_go_marshals_a_raw_message() {
    assert_eq!(
        permission_allow(Some(&raw(r#"{ "command": "a<b",  "x": "\ud800" }"#))).unwrap(),
        r#"{"behavior":"allow","updatedInput":{"command":"a\u003cb","x":"\ud800"}}"#
    );
    assert_eq!(
        permission_allow(None).unwrap(),
        r#"{"behavior":"allow","updatedInput":null}"#
    );
}

#[test]
fn a_permission_deny_writes_interrupt_only_when_it_interrupts() {
    assert_eq!(
        permission_deny("Denied by the operator", false).unwrap(),
        r#"{"behavior":"deny","message":"Denied by the operator"}"#
    );
    assert_eq!(
        permission_deny("a>b", true).unwrap(),
        r#"{"behavior":"deny","message":"a\u003eb","interrupt":true}"#
    );
}

#[test]
fn an_mcp_response_carries_the_message_as_go_marshals_a_raw_message() {
    assert_eq!(
        mcp_response(Some(&raw(r#"{"jsonrpc": "2.0", "id": 1}"#))).unwrap(),
        r#"{"mcp_response":{"jsonrpc":"2.0","id":1}}"#
    );
    assert_eq!(mcp_response(None).unwrap(), r#"{"mcp_response":null}"#);
}

#[test]
fn an_interrupt_result_reads_null_members_and_elements_as_their_zero_values() {
    let result =
        decode_interrupt_result(br#"{"still_queued":["a",null],"cancelled":null}"#).unwrap();
    assert_eq!(result.still_queued, ["a", ""]);
    assert!(result.cancelled.is_empty());
    let result = decode_interrupt_result(br#"{"cancelled":["c"]}"#).unwrap();
    assert!(result.still_queued.is_empty());
    assert_eq!(result.cancelled, ["c"]);
    let result = decode_interrupt_result(b"null").unwrap();
    assert!(result.still_queued.is_empty() && result.cancelled.is_empty());
    for refused in [&b"[]"[..], b"\"x\"", b"{\"still_queued\":\"a\"}"] {
        assert!(
            decode_interrupt_result(refused).is_err(),
            "{}",
            String::from_utf8_lossy(refused)
        );
    }
}
