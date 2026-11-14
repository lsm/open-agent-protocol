use oxap_adapter_claude::native::{
    decode_observation, InitFrame, Observation, StatusFrame, SystemNotice, SYSTEM_INIT,
    SYSTEM_SESSION_STATE, SYSTEM_STATUS, TYPE_SYSTEM,
};

const SESSION_ID: &str = "3b926aac-d113-4b86-9dc1-0c36b2013f93";

fn system(subtype: &str, frame: &str) -> Observation {
    decode_observation(TYPE_SYSTEM, subtype, frame.as_bytes()).unwrap()
}

fn refusal(subtype: &str, frame: &str) -> String {
    decode_observation(TYPE_SYSTEM, subtype, frame.as_bytes())
        .unwrap_err()
        .to_string()
}

fn init(frame: &str) -> InitFrame {
    match system(SYSTEM_INIT, frame) {
        Observation::Init(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

fn status(frame: &str) -> StatusFrame {
    match system(SYSTEM_STATUS, frame) {
        Observation::Status(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

fn notice(subtype: &str, frame: &str) -> SystemNotice {
    match system(subtype, frame) {
        Observation::SystemNotice(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

#[test]
fn an_init_frame_carries_the_session_the_cli_opened() {
    let frame = init(&format!(
        r#"{{"type":"system","subtype":"init","cwd":"/w","session_id":"{SESSION_ID}","tools":["Task","Bash"],"mcp_servers":[{{"name":"oap","status":"connected"}}],"model":"claude-sonnet-4-5","permissionMode":"default","slash_commands":[],"apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.999","capabilities":["interrupt_receipt_v1","interrupt_cancel_queued_v1","msg_lifecycle_v1","mcp_read_resource_v1","mcp_tool_ui_meta_v1"],"uuid":"i1"}}"#
    ));
    assert_eq!(frame.session_id, SESSION_ID);
    assert_eq!(frame.model, "claude-sonnet-4-5");
    assert_eq!(
        frame.tools,
        Some(vec!["Task".to_owned(), "Bash".to_owned()])
    );
    assert_eq!(frame.mcp_servers.len(), 1);
    assert_eq!(
        (
            frame.mcp_servers[0].name.as_str(),
            frame.mcp_servers[0].status.as_str()
        ),
        ("oap", "connected")
    );
    assert_eq!(frame.permission_mode, "default");
    assert_eq!(frame.capabilities.len(), 5);
    assert_eq!(frame.claude_code_version, "2.1.999");
    assert_eq!(frame.api_key_source, "ANTHROPIC_API_KEY");
    assert_eq!(frame.cwd, "/w");
    assert_eq!(frame.uuid, "i1");
}

#[test]
fn an_init_frame_needs_a_session_a_model_and_a_tool_list_that_may_be_empty() {
    let empty = init(
        r#"{"type":"system","subtype":"init","session_id":"s","model":"m","tools":[null,"Bash"],"mcp_servers":null}"#,
    );
    assert_eq!(empty.tools, Some(vec![String::new(), "Bash".to_owned()]));
    assert!(empty.mcp_servers.is_empty());
    assert_eq!(
        init(r#"{"type":"system","subtype":"init","session_id":"s","model":"m","tools":[]}"#).tools,
        Some(Vec::new())
    );
    for frame in [
        r#"{"type":"system","subtype":"init","session_id":"s"}"#,
        r#"{"type":"system","subtype":"init","session_id":"s","model":"m"}"#,
        r#"{"type":"system","subtype":"init","session_id":"s","model":"m","tools":null}"#,
        r#"{"type":"system","subtype":"init","model":"m","tools":[]}"#,
        r#"{"type":"system","subtype":"init","session_id":"s","tools":[]}"#,
    ] {
        assert_eq!(
            refusal(SYSTEM_INIT, frame),
            "claude native: invalid frame for a known type: init frame requires session_id, model, and tools",
            "{frame}"
        );
    }
}

#[test]
fn a_status_frame_reports_compaction_and_its_outcome() {
    let compacting = status(&format!(
        r#"{{"type":"system","subtype":"status","status":"compacting","session_id":"{SESSION_ID}","uuid":"s1"}}"#
    ));
    assert!(compacting.compacting());
    assert_eq!(compacting.status.as_deref(), Some("compacting"));
    let done = status(
        r#"{"type":"system","subtype":"status","status":null,"compact_result":"failed","compact_error":"too long","session_id":"s","uuid":"s2"}"#,
    );
    assert!(!done.compacting());
    assert_eq!(done.status, None);
    assert_eq!(done.compact_result.as_deref(), Some("failed"));
    assert_eq!(done.compact_error.as_deref(), Some("too long"));
    assert!(!status(r#"{"type":"system","subtype":"status","status":"idle"}"#).compacting());
    assert!(!status(r#"{"type":"system","subtype":"status"}"#).compacting());
}

#[test]
fn a_session_state_frame_needs_its_state() {
    match system(
        SYSTEM_SESSION_STATE,
        &format!(
            r#"{{"type":"system","subtype":"session_state_changed","state":"idle","session_id":"{SESSION_ID}","uuid":"x1"}}"#
        ),
    ) {
        Observation::SessionState(frame) => {
            assert_eq!(frame.state, "idle");
            assert_eq!(frame.session_id, SESSION_ID);
            assert_eq!(frame.uuid, "x1");
        }
        other => panic!("{other:?}"),
    }
    for frame in [
        r#"{"type":"system","subtype":"session_state_changed","session_id":"s"}"#,
        r#"{"type":"system","subtype":"session_state_changed","state":null}"#,
    ] {
        assert_eq!(
            refusal(SYSTEM_SESSION_STATE, frame),
            "claude native: invalid frame for a known type: session_state_changed requires state",
            "{frame}"
        );
    }
}

#[test]
fn an_unrecognised_system_subtype_is_a_notice_that_keeps_its_bytes() {
    let frame = r#"{"type":"system","subtype":"future_subtype","data":{"anything":true},"session_id":"s","uuid":"n1"}"#;
    let notice = notice("future_subtype", frame);
    assert_eq!(notice.subtype, "future_subtype");
    assert_eq!(notice.session_id, "s");
    assert_eq!(notice.uuid, "n1");
    assert_eq!(notice.raw, frame.as_bytes());
    let named = notice(
        "compact_boundary",
        r#"{"type":"system","subtype":"informational","uuid":"n2"}"#,
    );
    assert_eq!(named.subtype, "informational");
    assert!(decode_observation(TYPE_SYSTEM, "future_subtype", b"[1]").is_err());
}
