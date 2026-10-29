use oap_adapter_claude::native::{
    decode_observation, AssistantFrame, Observation, UserFrame, TYPE_ASSISTANT, TYPE_KEEP_ALIVE,
    TYPE_USER,
};

const SESSION_ID: &str = "3b926aac-d113-4b86-9dc1-0c36b2013f93";
const TURN_UUID: &str = "aaaa1111-0000-4000-8000-000000000001";
const TOOL_USE_ID: &str = "toolu_probe01";

fn assistant(frame: &str) -> AssistantFrame {
    match decode_observation(TYPE_ASSISTANT, "", frame.as_bytes()).unwrap() {
        Observation::Assistant(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

fn user(frame: &str) -> UserFrame {
    match decode_observation(TYPE_USER, "", frame.as_bytes()).unwrap() {
        Observation::User(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

#[test]
fn an_assistant_frame_carries_its_echo_its_blocks_and_its_error() {
    let frame = assistant(&format!(
        r#"{{"type":"assistant","message":{{"id":"msg_1","model":"claude-sonnet-4-5","content":[{{"type":"tool_use","id":"{TOOL_USE_ID}","name":"Bash","input":{{"command":"ls"}}}}],"stop_reason":null,"usage":{{"input_tokens":7}}}},"parent_tool_use_id":null,"session_id":"{SESSION_ID}","uuid":"a1","user_message_uuid":"{TURN_UUID}","user_message_uuids":["{TURN_UUID}"],"error":"rate_limit"}}"#
    ));
    assert_eq!(frame.user_message_uuid, TURN_UUID);
    assert_eq!(frame.user_message_uuids, [TURN_UUID]);
    assert_eq!(frame.error, "rate_limit");
    let blocks = frame.message.content.unwrap();
    assert_eq!((blocks.len(), blocks[0].name.as_str()), (1, "Bash"));
    let synthetic = assistant(&format!(
        r#"{{"type":"assistant","message":{{"id":"m","model":"<synthetic>","content":[{{"type":"text","text":"Not logged in"}}]}},"parent_tool_use_id":null,"session_id":"{SESSION_ID}","uuid":"a2","error":"authentication_failed","is_api_error_message":true,"user_message_uuid":"{TURN_UUID}"}}"#
    ));
    assert_eq!(synthetic.message.model, "<synthetic>");
    assert!(synthetic.is_api_error_message);
}

#[test]
fn an_assistant_frame_without_a_model_or_content_is_refused() {
    for frame in [
        r#"{"type":"assistant","message":{"content":[]}}"#,
        r#"{"type":"assistant","message":{"model":"m"}}"#,
        r#"{"type":"assistant","message":{"model":"m","content":null}}"#,
    ] {
        assert_eq!(
            decode_observation(TYPE_ASSISTANT, "", frame.as_bytes())
                .unwrap_err()
                .to_string(),
            "claude native: invalid frame for a known type: assistant frame requires message.model and message.content",
            "{frame}"
        );
    }
}

#[test]
fn a_user_frame_reads_as_tool_result_blocks_or_as_text_and_keeps_its_origin() {
    let results = user(&format!(
        r#"{{"type":"user","message":{{"role":"user","content":[{{"tool_use_id":"{TOOL_USE_ID}","type":"tool_result","content":"probe","is_error":false}}]}},"parent_tool_use_id":null,"session_id":"{SESSION_ID}","uuid":"u1","tool_use_result":{{"stdout":"probe","stderr":"","interrupted":false}}}}"#
    ));
    let blocks = results.blocks().unwrap();
    assert_eq!(blocks.len(), 1);
    assert_eq!(blocks[0].tool_use_id, TOOL_USE_ID);
    assert_eq!(blocks[0].is_error, Some(false));
    assert_eq!(results.text_content(), None);
    let interrupted = user(&format!(
        r#"{{"type":"user","message":{{"role":"user","content":"[Request interrupted by user]"}},"parent_tool_use_id":null,"session_id":"{SESSION_ID}","uuid":"u2"}}"#
    ));
    assert_eq!(
        interrupted.text_content().as_deref(),
        Some("[Request interrupted by user]")
    );
    assert!(interrupted.blocks().is_none());
    let notified = user(&format!(
        r#"{{"type":"user","message":{{"role":"user","content":"task done"}},"parent_tool_use_id":null,"session_id":"{SESSION_ID}","uuid":"u3","origin":{{"kind":"task-notification"}}}}"#
    ));
    assert_eq!(notified.origin.unwrap().kind, "task-notification");
    assert!(decode_observation(
        TYPE_USER,
        "",
        br#"{"type":"user","parent_tool_use_id":null}"#
    )
    .is_err());
}

#[test]
fn a_null_block_or_echo_element_is_its_zero_value_as_go_reads_it() {
    let frame = assistant(
        r#"{"type":"assistant","message":{"model":"m","content":[null,{"type":"text","text":"hi"}]},"user_message_uuids":[null,"u"]}"#,
    );
    let blocks = frame.message.content.unwrap();
    assert_eq!(
        blocks
            .iter()
            .map(|block| block.kind.as_str())
            .collect::<Vec<_>>(),
        ["", "text"]
    );
    assert_eq!(frame.user_message_uuids, ["", "u"]);
    let results = user(r#"{"type":"user","message":{"role":"user","content":[null]}}"#);
    assert_eq!(results.blocks().unwrap().len(), 1);
}

#[test]
fn a_lone_surrogate_escape_decodes_as_the_replacement_character_as_go_s_decoder_reads_it() {
    let frame = assistant(
        r#"{"type":"assistant","message":{"model":"m","content":[{"type":"text","text":"bad \ud800 half"}]}}"#,
    );
    assert_eq!(frame.message.content.unwrap()[0].text, "bad \u{fffd} half");
}

#[test]
fn unknown_types_and_keep_alive_are_tolerated() {
    for kind in [
        "future_frame",
        "auth_status",
        "prompt_suggestion",
        "tool_use_summary",
        "active_goal",
        TYPE_KEEP_ALIVE,
    ] {
        let frame = format!(r#"{{"type":"{kind}","payload":{{"x":1}}}}"#);
        match decode_observation(kind, "", frame.as_bytes()).unwrap() {
            Observation::Unknown { kind: seen, .. } => assert_eq!(seen, kind),
            other => panic!("{kind}: {other:?}"),
        }
    }
}
