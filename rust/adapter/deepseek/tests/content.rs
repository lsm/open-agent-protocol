use oap_adapter_deepseek::content::{chunk_part, final_content, stream_parts};
use oap_adapter_deepseek::native::{AssistantStreamRecord, ContentBlock};
use oap_protocol::ToolCallId;
use serde_json::value::RawValue;

fn raw(text: &str) -> Box<RawValue> {
    RawValue::from_string(text.into()).unwrap()
}

fn json<T: serde::Serialize>(value: &T) -> String {
    serde_json::to_string(value).unwrap()
}

fn block(kind: &str, text: &str) -> ContentBlock {
    ContentBlock {
        kind: kind.into(),
        text: text.into(),
        ..ContentBlock::default()
    }
}

fn tools(id: &str) -> Option<(ToolCallId, String)> {
    (id == "native-call").then(|| ("tool-call-1".into(), "read".into()))
}

#[test]
fn chunks_map_text_and_reasoning_deltas_and_drop_everything_else() {
    assert_eq!(
        json(&chunk_part(&raw(r#"{"type":"text-delta","index":0,"text":"hi"}"#)).unwrap()),
        r#"{"type":"text","text":"hi"}"#
    );
    assert_eq!(
        json(&chunk_part(&raw(r#"{"type":"reasoning-delta","text":"why"}"#)).unwrap()),
        r#"{"type":"reasoning","reasoning":"why"}"#
    );
    for dropped in [
        r#"{"type":"tool-call-delta","text":"x"}"#,
        r#"{"type":"text-delta","text":"x","extra":1}"#,
        r#"{"type":"text-delta","type":"text-delta"}"#,
        r#"[]"#,
    ] {
        assert!(chunk_part(&raw(dropped)).is_none(), "{dropped}");
    }
}

#[test]
fn stream_records_expand_in_order() {
    let records = [
        AssistantStreamRecord {
            kind: "text-chunks".into(),
            texts: vec!["a".into(), "b".into()],
            ..AssistantStreamRecord::default()
        },
        AssistantStreamRecord {
            kind: "reasoning-chunks".into(),
            texts: vec!["r".into()],
            ..AssistantStreamRecord::default()
        },
        AssistantStreamRecord {
            kind: "chunk".into(),
            chunk: Some(raw(r#"{"type":"text-delta","text":"c"}"#)),
            ..AssistantStreamRecord::default()
        },
        AssistantStreamRecord {
            kind: "tool-call".into(),
            ..AssistantStreamRecord::default()
        },
    ];
    assert_eq!(
        json(&stream_parts(&records)),
        r#"[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"reasoning","reasoning":"r"},{"type":"text","text":"c"}]"#
    );
}

#[test]
fn final_content_is_a_string_for_one_text_block_and_parts_otherwise() {
    assert_eq!(json(&final_content(&[], &tools).unwrap()), r#""""#);
    assert_eq!(
        json(&final_content(&[block("text", "done")], &tools).unwrap()),
        r#""done""#
    );
    let call = ContentBlock {
        kind: "tool-call".into(),
        id: "native-call".into(),
        name: "read".into(),
        arguments: r#"{"path":"a"}"#.into(),
        ..ContentBlock::default()
    };
    assert_eq!(
        json(&final_content(&[block("reasoning", "think"), call], &tools).unwrap()),
        r#"[{"type":"reasoning","reasoning":"think"},{"type":"tool_call","tool_call_id":"tool-call-1","name":"read","arguments_json":{"path":"a"}}]"#
    );
}

#[test]
fn final_content_refuses_what_go_refuses() {
    let call = |id: &str, name: &str| ContentBlock {
        kind: "tool-call".into(),
        id: id.into(),
        name: name.into(),
        ..ContentBlock::default()
    };
    for (blocks, wanted) in [
        (
            vec![call("other", "read")],
            r#"final message references unknown tool "other""#,
        ),
        (
            vec![call("native-call", "write")],
            r#"final message references unknown tool "native-call""#,
        ),
        (
            vec![block("tool-result", "")],
            "assistant message contains invalid tool-result block",
        ),
        (
            vec![block("image", "")],
            "assistant message contains invalid image block",
        ),
        (
            vec![block("audio", "")],
            r#"unknown assistant content block "audio""#,
        ),
    ] {
        assert_eq!(final_content(&blocks, &tools).unwrap_err(), wanted);
    }
}
