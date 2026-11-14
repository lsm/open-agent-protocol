use oxap_adapter_claude::native::{
    decode_observation, Observation, TYPE_ASSISTANT, TYPE_RESULT, TYPE_STREAM_EVENT, TYPE_USER,
};

fn raw(value: &Option<Box<serde_json::value::RawValue>>) -> Option<&str> {
    value.as_deref().map(|raw| raw.get())
}

#[test]
fn an_assistant_block_keeps_its_raw_input_while_its_text_reads_the_replacement() {
    let frame = r#"{"type":"assistant","message":{"model":"m","content":[{"type":"tool_use","id":"t","name":"Bash\ud800","input":{"command":"echo \ud800"}}],"usage":{"note":"\ud800"}}}"#;
    match decode_observation(TYPE_ASSISTANT, "", frame.as_bytes()).unwrap() {
        Observation::Assistant(frame) => {
            let blocks = frame.message.content.unwrap();
            assert_eq!(blocks[0].name, "Bash\u{fffd}");
            assert_eq!(raw(&blocks[0].input), Some(r#"{"command":"echo \ud800"}"#));
            assert_eq!(raw(&frame.message.usage), Some(r#"{"note":"\ud800"}"#));
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_user_frame_keeps_its_raw_content_and_tool_result() {
    let frame = r#"{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t\ud800","content":"out \ud800"}]},"tool_use_result":{"stdout":"\ud800"}}"#;
    match decode_observation(TYPE_USER, "", frame.as_bytes()).unwrap() {
        Observation::User(frame) => {
            assert_eq!(raw(&frame.tool_use_result), Some(r#"{"stdout":"\ud800"}"#));
            let blocks = frame.blocks().unwrap();
            assert_eq!(blocks[0].tool_use_id, "t\u{fffd}");
            assert_eq!(raw(&blocks[0].content), Some(r#""out \ud800""#));
        }
        other => panic!("{other:?}"),
    }
    let text = r#"{"type":"user","message":{"role":"user","content":"half \ud800"}}"#;
    match decode_observation(TYPE_USER, "", text.as_bytes()).unwrap() {
        Observation::User(frame) => {
            assert_eq!(frame.text_content().as_deref(), Some("half \u{fffd}"))
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_result_keeps_its_raw_structured_output_usage_and_denials() {
    let frame = r#"{"type":"result","subtype":"success","session_id":"s","result":"r\ud800","structured_output":{"a":"\ud800"},"modelUsage":{"m\ud800":{}},"permission_denials":["\ud800"]}"#;
    match decode_observation(TYPE_RESULT, "success", frame.as_bytes()).unwrap() {
        Observation::Result(frame) => {
            assert_eq!(frame.result, "r\u{fffd}");
            assert_eq!(raw(&frame.structured_output), Some(r#"{"a":"\ud800"}"#));
            assert_eq!(raw(&frame.model_usage), Some(r#"{"m\ud800":{}}"#));
            assert_eq!(raw(&frame.permission_denials), Some(r#"["\ud800"]"#));
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_stream_event_keeps_its_raw_event_and_reads_its_delta_with_the_replacement() {
    let frame = r#"{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"d\ud800"}},"uuid":"e","session_id":"s"}"#;
    match decode_observation(TYPE_STREAM_EVENT, "", frame.as_bytes()).unwrap() {
        Observation::StreamEvent(frame) => {
            assert_eq!(
                raw(&frame.event),
                Some(
                    r#"{"type":"content_block_delta","delta":{"type":"text_delta","text":"d\ud800"}}"#
                )
            );
            assert_eq!(frame.stream_delta(), Some(("text", "d\u{fffd}".to_owned())));
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn an_ignored_key_with_a_lone_surrogate_escape_is_skipped_in_every_frame() {
    let assistant = r#"{"type":"assistant","x\ud800":1,"message":{"y\ud800":2,"model":"m","content":[{"type":"tool_use","z\ud800":3,"name":"Bash","input":{"k\ud800":"v"}}]}}"#;
    match decode_observation(TYPE_ASSISTANT, "", assistant.as_bytes()).unwrap() {
        Observation::Assistant(frame) => {
            let blocks = frame.message.content.unwrap();
            assert_eq!(blocks[0].name, "Bash");
            assert_eq!(raw(&blocks[0].input), Some(r#"{"k\ud800":"v"}"#));
        }
        other => panic!("{other:?}"),
    }
    let user = r#"{"type":"user","message":{"role":"user","content":[{"type":"tool_result","q\ud800":0,"tool_use_id":"t"}]}}"#;
    match decode_observation(TYPE_USER, "", user.as_bytes()).unwrap() {
        Observation::User(frame) => assert_eq!(frame.blocks().unwrap()[0].tool_use_id, "t"),
        other => panic!("{other:?}"),
    }
    let stream = r#"{"type":"stream_event","event":{"type":"content_block_delta","e\ud800":0,"delta":{"type":"text_delta","text":"d"}},"uuid":"e","session_id":"s"}"#;
    match decode_observation(TYPE_STREAM_EVENT, "", stream.as_bytes()).unwrap() {
        Observation::StreamEvent(frame) => {
            assert_eq!(frame.stream_delta(), Some(("text", "d".to_owned())))
        }
        other => panic!("{other:?}"),
    }
}
