use oxap_adapter_claude::native::{
    decode_control_request, ControlRequest, CONTROL_CAN_USE_TOOL, CONTROL_HOOK_CALLBACK,
    CONTROL_MCP_MESSAGE, HOOK_PRE_TOOL_USE,
};

const TOOL_USE_ID: &str = "toolu_probe01";

fn decode(subtype: &str, frame: &str) -> ControlRequest {
    decode_control_request(subtype, frame.as_bytes()).unwrap()
}

fn refusal(subtype: &str, frame: &str) -> String {
    decode_control_request(subtype, frame.as_bytes())
        .unwrap_err()
        .to_string()
}

#[test]
fn a_can_use_tool_request_carries_the_tool_its_input_and_its_suggestions() {
    match decode(
        CONTROL_CAN_USE_TOOL,
        &format!(
            r#"{{"type":"control_request","request_id":"b6efb009","request":{{"subtype":"can_use_tool","tool_name":"Bash","display_name":"Bash","input":{{"command":"touch /tmp/x"}},"description":"touch /tmp/x","permission_suggestions":[{{"type":"setMode","mode":"acceptEdits","destination":"session"}}],"blocked_path":"/tmp/x","decision_reason":"outside","title":"Run","agent_id":"a1","tool_use_id":"{TOOL_USE_ID}"}}}}"#
        ),
    ) {
        ControlRequest::CanUseTool(ask) => {
            assert_eq!(ask.tool_name, "Bash");
            assert_eq!(ask.tool_use_id, TOOL_USE_ID);
            assert_eq!(ask.blocked_path, "/tmp/x");
            assert_eq!(ask.display_name, "Bash");
            assert_eq!(ask.description, "touch /tmp/x");
            assert_eq!(ask.decision_reason, "outside");
            assert_eq!(ask.title, "Run");
            assert_eq!(ask.agent_id, "a1");
            assert_eq!(
                ask.input.as_deref().map(|raw| raw.get()),
                Some(r#"{"command":"touch /tmp/x"}"#)
            );
            assert_eq!(
                ask.permission_suggestions.as_deref().map(|raw| raw.get()),
                Some(r#"[{"type":"setMode","mode":"acceptEdits","destination":"session"}]"#)
            );
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_can_use_tool_request_needs_its_tool_its_input_and_its_tool_use() {
    match decode(
        CONTROL_CAN_USE_TOOL,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool","tool_name":"Bash","input":null,"tool_use_id":"t"}}"#,
    ) {
        ControlRequest::CanUseTool(ask) => {
            assert_eq!(ask.input.as_deref().map(|raw| raw.get()), Some("null"))
        }
        other => panic!("{other:?}"),
    }
    for frame in [
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{}}}"#,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool","tool_name":"Bash","tool_use_id":"t"}}"#,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool","input":{},"tool_use_id":"t"}}"#,
        r#"{"type":"control_request","request_id":"r","request":null}"#,
        r#"{"type":"control_request","request_id":"r"}"#,
    ] {
        assert_eq!(
            refusal(CONTROL_CAN_USE_TOOL, frame),
            "claude native: invalid frame for a known type: can_use_tool requires tool_name, input, and tool_use_id",
            "{frame}"
        );
    }
}

#[test]
fn a_hook_callback_names_its_callback_and_the_tool_it_gates() {
    match decode(
        CONTROL_HOOK_CALLBACK,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"hook_callback","callback_id":"h1","input":{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_use_id":"t1"},"tool_use_id":"t1"}}"#,
    ) {
        ControlRequest::HookCallback(hook) => {
            assert_eq!(hook.callback_id, "h1");
            assert_eq!(hook.tool_use_id, "t1");
            assert_eq!(hook.input.hook_event_name, HOOK_PRE_TOOL_USE);
            assert_eq!(hook.input.tool_name, "Bash");
            assert_eq!(hook.input.tool_use_id, "t1");
        }
        other => panic!("{other:?}"),
    }
    match decode(
        CONTROL_HOOK_CALLBACK,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"hook_callback","callback_id":"h1","input":null}}"#,
    ) {
        ControlRequest::HookCallback(hook) => assert_eq!(hook.input.tool_name, ""),
        other => panic!("{other:?}"),
    }
    assert_eq!(
        refusal(
            CONTROL_HOOK_CALLBACK,
            r#"{"type":"control_request","request_id":"r","request":{"subtype":"hook_callback"}}"#
        ),
        "claude native: invalid frame for a known type: hook_callback requires callback_id"
    );
}

#[test]
fn an_mcp_message_keeps_the_message_bytes_it_carries() {
    match decode(
        CONTROL_MCP_MESSAGE,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"mcp_message","server_name":"oap","message":{"jsonrpc":"2.0", "id":1,"method":"tools/list"}}}"#,
    ) {
        ControlRequest::McpMessage(message) => {
            assert_eq!(message.server_name, "oap");
            assert_eq!(
                message.message.as_deref().map(|raw| raw.get()),
                Some(r#"{"jsonrpc":"2.0", "id":1,"method":"tools/list"}"#)
            );
        }
        other => panic!("{other:?}"),
    }
    match decode(
        CONTROL_MCP_MESSAGE,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"mcp_message"}}"#,
    ) {
        ControlRequest::McpMessage(message) => {
            assert_eq!(message.server_name, "");
            assert!(message.message.is_none());
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn an_unconfigured_reverse_request_reads_as_unknown_without_its_body() {
    match decode("elicitation", "not json") {
        ControlRequest::Unknown { subtype } => assert_eq!(subtype, "elicitation"),
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_raw_member_keeps_a_lone_surrogate_escape_as_go_s_raw_message_does() {
    match decode(
        CONTROL_CAN_USE_TOOL,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool","tool_name":"Bash\ud800","input":{"command":"echo \ud800"},"permission_suggestions":["\ud800 x"],"tool_use_id":"t"}}"#,
    ) {
        ControlRequest::CanUseTool(ask) => {
            assert_eq!(ask.tool_name, "Bash\u{fffd}");
            assert_eq!(
                ask.input.as_deref().map(|raw| raw.get()),
                Some(r#"{"command":"echo \ud800"}"#)
            );
            assert_eq!(
                ask.permission_suggestions.as_deref().map(|raw| raw.get()),
                Some(r#"["\ud800 x"]"#)
            );
        }
        other => panic!("{other:?}"),
    }
    match decode(
        CONTROL_MCP_MESSAGE,
        r#"{"type":"control_request","request_id":"r","request":{"subtype":"mcp_message","server_name":"o\ud800","message":{"result":"\ud800"}}}"#,
    ) {
        ControlRequest::McpMessage(message) => {
            assert_eq!(message.server_name, "o\u{fffd}");
            assert_eq!(
                message.message.as_deref().map(|raw| raw.get()),
                Some(r#"{"result":"\ud800"}"#)
            );
        }
        other => panic!("{other:?}"),
    }
}
