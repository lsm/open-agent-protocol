use oxap_adapter_claude::native::{
    decode_observation, Observation, COMMAND_CANCELLED, COMMAND_COMPLETED, COMMAND_QUEUED,
    COMMAND_STARTED, TYPE_COMMAND_LIFECYCLE, TYPE_CONVERSATION, TYPE_KEEP_ALIVE,
    TYPE_TOOL_PROGRESS,
};

const SESSION_ID: &str = "3b926aac-d113-4b86-9dc1-0c36b2013f93";
const COMMAND_UUID: &str = "aaaa1111-0000-4000-8000-000000000001";
const TOOL_USE_ID: &str = "toolu_probe01";

fn decode(kind: &str, frame: &str) -> Observation {
    decode_observation(kind, "", frame.as_bytes()).unwrap()
}

fn refusal(kind: &str, frame: &str) -> String {
    decode_observation(kind, "", frame.as_bytes())
        .unwrap_err()
        .to_string()
}

#[test]
fn a_command_lifecycle_frame_carries_its_command_and_state() {
    for state in [
        COMMAND_QUEUED,
        COMMAND_STARTED,
        COMMAND_COMPLETED,
        COMMAND_CANCELLED,
    ] {
        match decode(
            TYPE_COMMAND_LIFECYCLE,
            &format!(
                r#"{{"type":"command_lifecycle","command_uuid":"{COMMAND_UUID}","state":"{state}","session_id":"{SESSION_ID}","uuid":"c1"}}"#
            ),
        ) {
            Observation::CommandLifecycle(frame) => {
                assert_eq!(frame.command_uuid, COMMAND_UUID);
                assert_eq!(frame.state, state);
                assert_eq!(frame.session_id, SESSION_ID);
                assert_eq!(frame.uuid, "c1");
            }
            other => panic!("{state}: {other:?}"),
        }
    }
    for frame in [
        r#"{"type":"command_lifecycle","state":"queued","session_id":"s"}"#,
        r#"{"type":"command_lifecycle","command_uuid":"u","session_id":"s"}"#,
        r#"{"type":"command_lifecycle","command_uuid":"u","state":"queued"}"#,
        r#"{"type":"command_lifecycle","command_uuid":null,"state":"queued","session_id":"s"}"#,
    ] {
        assert_eq!(
            refusal(TYPE_COMMAND_LIFECYCLE, frame),
            "claude native: invalid frame for a known type: command_lifecycle requires command_uuid, state, and session_id",
            "{frame}"
        );
    }
}

#[test]
fn a_tool_progress_frame_carries_its_tool_and_elapsed_time() {
    match decode(
        TYPE_TOOL_PROGRESS,
        &format!(
            r#"{{"type":"tool_progress","tool_use_id":"{TOOL_USE_ID}","tool_name":"Bash","parent_tool_use_id":"toolu_parent","elapsed_time_seconds":2.5,"task_id":"task1","session_id":"{SESSION_ID}","uuid":"p1"}}"#
        ),
    ) {
        Observation::ToolProgress(frame) => {
            assert_eq!(frame.tool_use_id, TOOL_USE_ID);
            assert_eq!(frame.tool_name, "Bash");
            assert_eq!(frame.parent_tool_use_id.as_deref(), Some("toolu_parent"));
            assert_eq!(frame.elapsed_time_seconds, 2.5);
            assert_eq!(frame.task_id, "task1");
            assert_eq!(frame.uuid, "p1");
        }
        other => panic!("{other:?}"),
    }
    match decode(
        TYPE_TOOL_PROGRESS,
        r#"{"type":"tool_progress","tool_use_id":"t","tool_name":"Bash","session_id":"s","parent_tool_use_id":null,"elapsed_time_seconds":null}"#,
    ) {
        Observation::ToolProgress(frame) => {
            assert_eq!(frame.parent_tool_use_id, None);
            assert_eq!(frame.elapsed_time_seconds, 0.0);
        }
        other => panic!("{other:?}"),
    }
    for frame in [
        r#"{"type":"tool_progress","tool_name":"Bash","session_id":"s"}"#,
        r#"{"type":"tool_progress","tool_use_id":"t","session_id":"s"}"#,
        r#"{"type":"tool_progress","tool_use_id":"t","tool_name":"Bash"}"#,
    ] {
        assert_eq!(
            refusal(TYPE_TOOL_PROGRESS, frame),
            "claude native: invalid frame for a known type: tool_progress requires tool_use_id, tool_name, and session_id",
            "{frame}"
        );
    }
}

#[test]
fn a_conversation_reset_frame_carries_the_new_conversation() {
    match decode(
        TYPE_CONVERSATION,
        &format!(
            r#"{{"type":"conversation_reset","new_conversation_id":"conv2","uuid":"r1","session_id":"{SESSION_ID}"}}"#
        ),
    ) {
        Observation::ConversationReset(frame) => {
            assert_eq!(frame.new_conversation_id, "conv2");
            assert_eq!(frame.uuid, "r1");
            assert_eq!(frame.session_id, SESSION_ID);
        }
        other => panic!("{other:?}"),
    }
    for frame in [
        r#"{"type":"conversation_reset","uuid":"r1","session_id":"s"}"#,
        r#"{"type":"conversation_reset","new_conversation_id":"c","session_id":"s"}"#,
        r#"{"type":"conversation_reset","new_conversation_id":"c","uuid":"r1"}"#,
    ] {
        assert_eq!(
            refusal(TYPE_CONVERSATION, frame),
            "claude native: invalid frame for a known type: conversation_reset requires new_conversation_id, uuid, and session_id",
            "{frame}"
        );
    }
}

#[test]
fn a_keep_alive_is_read_without_its_body_or_subtype() {
    match decode_observation(TYPE_KEEP_ALIVE, "ping", b"not json").unwrap() {
        Observation::Unknown { kind, subtype } => {
            assert_eq!((kind.as_str(), subtype.as_str()), (TYPE_KEEP_ALIVE, ""));
        }
        other => panic!("{other:?}"),
    }
}
