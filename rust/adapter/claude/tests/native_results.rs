use oap_adapter_claude::native::{
    decode_observation, Observation, ResultFrame, StreamEventFrame, RESULT_ERROR_DURING_EXECUTION,
    RESULT_ERROR_MAX_TURNS, RESULT_SUCCESS, TERMINAL_COMPLETED, TYPE_RESULT, TYPE_STREAM_EVENT,
};

const SESSION_ID: &str = "3b926aac-d113-4b86-9dc1-0c36b2013f93";
const TURN_UUID: &str = "aaaa1111-0000-4000-8000-000000000001";

fn result(subtype: &str, frame: &str) -> ResultFrame {
    match decode_observation(TYPE_RESULT, subtype, frame.as_bytes()).unwrap() {
        Observation::Result(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

fn stream_event(frame: &str) -> StreamEventFrame {
    match decode_observation(TYPE_STREAM_EVENT, "", frame.as_bytes()).unwrap() {
        Observation::StreamEvent(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

fn refusal(kind: &str, subtype: &str, frame: &str) -> String {
    decode_observation(kind, subtype, frame.as_bytes())
        .unwrap_err()
        .to_string()
}

fn base(is_error: &str, terminal: &str) -> String {
    format!(
        r#"{{"type":"result","subtype":"success","duration_ms":130,"duration_api_ms":75,"is_error":{is_error},"num_turns":1,"session_id":"{SESSION_ID}","total_cost_usd":0.000096,"usage":{{"input_tokens":7,"output_tokens":5}},"permission_denials":[],"terminal_reason":"{terminal}","result":"fixture response","user_message_uuid":"{TURN_UUID}","user_message_uuids":["{TURN_UUID}"],"queued_turn_count":0,"uuid":"r1","new_future_field":{{"x":1}}}}"#
    )
}

#[test]
fn a_result_frame_carries_what_settles_a_run() {
    let completed = result(RESULT_SUCCESS, &base("false", TERMINAL_COMPLETED));
    assert!(!completed.is_error);
    assert!(!completed.cancelled());
    assert_eq!(completed.result, "fixture response");
    assert_eq!(completed.queued_turn_count, Some(0));
    assert_eq!(completed.total_cost_usd, Some(0.000096));
    assert_eq!(
        (completed.usage.input_tokens, completed.usage.output_tokens),
        (Some(7), Some(5))
    );
    assert_eq!(completed.usage.cache_read_input_tokens, None);
    assert_eq!(completed.user_message_uuids, [TURN_UUID]);

    let api_error = result(RESULT_SUCCESS, &base("true", "api_error"));
    assert!(api_error.is_error);
    assert!(!api_error.cancelled());

    let interrupted = result(
        RESULT_ERROR_DURING_EXECUTION,
        &format!(
            r#"{{"type":"result","subtype":"error_during_execution","duration_ms":66,"duration_api_ms":0,"is_error":true,"num_turns":2,"session_id":"{SESSION_ID}","stop_reason":null,"usage":{{"input_tokens":0,"output_tokens":0}},"modelUsage":{{}},"permission_denials":[],"terminal_reason":"aborted_streaming","errors":["[ede_diagnostic] result_type=user"],"user_message_uuid":"{TURN_UUID}","user_message_uuids":["{TURN_UUID}"],"queued_turn_count":0,"uuid":"r2"}}"#
        ),
    );
    assert!(interrupted.cancelled());
    assert!(interrupted.is_error);
    assert_eq!(interrupted.num_turns, Some(2));
    assert_eq!(interrupted.usage.input_tokens, Some(0));
    assert_eq!(interrupted.errors, ["[ede_diagnostic] result_type=user"]);

    let tools = result(
        RESULT_ERROR_DURING_EXECUTION,
        r#"{"type":"result","subtype":"error_during_execution","session_id":"s","terminal_reason":"aborted_tools"}"#,
    );
    assert!(tools.cancelled());
    assert!(!tools.max_turns());
}

#[test]
fn max_turns_reads_from_the_subtype_or_the_terminal_reason() {
    let by_subtype = result(
        RESULT_ERROR_MAX_TURNS,
        &format!(
            r#"{{"type":"result","subtype":"error_max_turns","duration_ms":1,"duration_api_ms":1,"is_error":true,"num_turns":3,"session_id":"{SESSION_ID}","errors":["Max turns reached"],"uuid":"r3"}}"#
        ),
    );
    assert!(by_subtype.max_turns());
    let by_reason = result(
        RESULT_SUCCESS,
        r#"{"type":"result","subtype":"success","session_id":"s","terminal_reason":"max_turns"}"#,
    );
    assert!(by_reason.max_turns());
    assert!(!result(RESULT_SUCCESS, &base("false", TERMINAL_COMPLETED)).max_turns());
}

#[test]
fn a_result_needs_a_subtype_and_a_session_but_not_a_known_subtype() {
    let future = result(
        "error_new_future",
        r#"{"type":"result","subtype":"error_new_future","duration_ms":1,"duration_api_ms":1,"is_error":true,"num_turns":1,"session_id":"s","errors":["future failure"]}"#,
    );
    assert_eq!(future.subtype, "error_new_future");
    assert_eq!(
        refusal(
            TYPE_RESULT,
            "",
            r#"{"type":"result","subtype":"","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":1,"session_id":"s"}"#
        ),
        "claude native: invalid frame for a known type: result frame requires subtype"
    );
    assert_eq!(
        refusal(
            TYPE_RESULT,
            RESULT_SUCCESS,
            r#"{"type":"result","subtype":"success","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":1}"#
        ),
        "claude native: invalid frame for a known type: result frame requires session_id"
    );
}

#[test]
fn a_null_member_of_a_result_is_its_zero_value_as_go_reads_it() {
    let frame = result(
        RESULT_SUCCESS,
        r#"{"type":"result","subtype":"success","session_id":"s","duration_ms":null,"is_error":null,"num_turns":null,"usage":null,"errors":[null,"e"],"origin":null,"queued_turn_count":null}"#,
    );
    assert_eq!(frame.duration_ms, 0);
    assert!(!frame.is_error);
    assert_eq!(frame.num_turns, None);
    assert_eq!(frame.usage.input_tokens, None);
    assert_eq!(frame.errors, ["", "e"]);
    assert!(frame.origin.is_none());
}

#[test]
fn a_stream_event_projects_text_and_thinking_deltas() {
    let text = stream_event(&format!(
        r#"{{"type":"stream_event","event":{{"type":"content_block_delta","index":0,"delta":{{"type":"text_delta","text":"fixture response"}}}},"session_id":"{SESSION_ID}","parent_tool_use_id":null,"uuid":"e1","user_message_uuid":"{TURN_UUID}","user_message_uuids":["{TURN_UUID}"]}}"#
    ));
    assert_eq!(
        text.stream_delta(),
        Some(("text", "fixture response".to_owned()))
    );
    assert_eq!(text.user_message_uuid, TURN_UUID);
    assert_eq!(text.user_message_uuids, [TURN_UUID]);
    assert_eq!(text.parent_tool_use_id, None);

    let thinking = stream_event(&format!(
        r#"{{"type":"stream_event","event":{{"type":"content_block_delta","index":0,"delta":{{"type":"thinking_delta","thinking":"hmm"}}}},"session_id":"{SESSION_ID}","uuid":"e2"}}"#
    ));
    assert_eq!(
        thinking.stream_delta(),
        Some(("thinking", "hmm".to_owned()))
    );

    for event in [
        r#"{"type":"message_stop"}"#,
        r#"{"type":"content_block_delta","delta":{"type":"input_json_delta","partial_json":"{"}}"#,
        r#"{"type":"content_block_delta","delta":null}"#,
        r#"{"type":"content_block_delta","delta":{"type":"text_delta","text":7}}"#,
        r#""content_block_delta""#,
        "null",
    ] {
        let frame = stream_event(&format!(
            r#"{{"type":"stream_event","event":{event},"session_id":"{SESSION_ID}","uuid":"e3"}}"#
        ));
        assert_eq!(frame.stream_delta(), None, "{event}");
    }
}

#[test]
fn a_stream_event_needs_an_event_a_uuid_and_a_session() {
    for frame in [
        format!(r#"{{"type":"stream_event","session_id":"{SESSION_ID}","uuid":"e4"}}"#),
        format!(r#"{{"type":"stream_event","event":{{}},"session_id":"{SESSION_ID}"}}"#),
        r#"{"type":"stream_event","event":{},"uuid":"e4"}"#.to_owned(),
    ] {
        assert_eq!(
            refusal(TYPE_STREAM_EVENT, "", &frame),
            "claude native: invalid frame for a known type: stream_event requires event, uuid, and session_id",
            "{frame}"
        );
    }
}
