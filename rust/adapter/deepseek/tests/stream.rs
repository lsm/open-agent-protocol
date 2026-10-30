use oap_adapter_deepseek::native::{
    decode_strict, stream_record, AssistantAttempt, AssistantMessageEvent,
};

fn chunk(body: &str) -> Result<(), String> {
    stream_record(&format!(r#"{{"type":"chunk","time":1,"chunk":{body}}}"#)).map(|_| ())
}

#[test]
fn chunked_text_needs_one_delay_between_each_pair_of_texts() {
    let record = stream_record(
        r#"{"type":"text-chunks","time0":0,"index":1,"dt":[5,null],"texts":["a",null,"c"]}"#,
    )
    .unwrap();
    assert_eq!(record.kind, "text-chunks");
    assert_eq!(record.index, 1);
    assert_eq!(record.texts, ["a", "", "c"]);
    assert_eq!(
        stream_record(
            r#"{"type":"reasoning-chunks","time0":0,"index":0,"dt":[],"texts":["a","b"]}"#
        )
        .unwrap_err(),
        "deepseek native: invalid pinned message: invalid reasoning-chunks record"
    );
}

#[test]
fn chunked_tool_calls_need_an_id_and_arguments() {
    let record = stream_record(
        r#"{"type":"tool-call-chunks","time0":0,"index":0,"dt":[1],"id":"c1","name":"read","args":["{","}"]}"#,
    )
    .unwrap();
    assert_eq!((record.id.as_str(), record.name.as_str()), ("c1", "read"));
    assert_eq!(record.args, ["{", "}"]);
    assert_eq!(
        stream_record(r#"{"type":"tool-call-chunks","time0":0,"index":0,"dt":[],"args":["{}"]}"#)
            .unwrap_err(),
        "deepseek native: invalid pinned message: invalid tool-call-chunks record"
    );
}

#[test]
fn an_unknown_or_null_record_is_refused_by_its_type() {
    assert_eq!(
        stream_record(r#"{"type":"tick"}"#).unwrap_err(),
        "deepseek native: invalid pinned message: unknown assistant stream record \"tick\""
    );
    assert_eq!(
        stream_record("null").unwrap_err(),
        "deepseek native: invalid pinned message: unknown assistant stream record \"\""
    );
    assert!(
        stream_record(r#"{"type":"text-chunks","texts":["a"],"extra":1}"#)
            .unwrap_err()
            .starts_with("unknown field `extra`")
    );
}

#[test]
fn stream_chunks_follow_the_pinned_shapes() {
    for valid in [
        r#"{"type":"block-start","index":0,"blockType":"text"}"#,
        r#"{"type":"text-delta","index":null,"text":"hi"}"#,
        r#"{"type":"tool-call-delta","index":0,"id":"c1","argumentsDelta":"{"}"#,
        r#"{"type":"block-end","index":0,"block":{"type":"text","text":"hi"}}"#,
        r#"{"type":"block-end","index":0,"block":{"type":"image","attachment":{"attachmentId":"a","mediaType":"image/png"},"offloaded":true}}"#,
        r#"{"type":"usage","usage":{"inputTokens":1,"outputTokens":2,"totalTokens":3}}"#,
        r#"{"type":"finish","reason":{"kind":"stop"}}"#,
        r#"{"type":"finish","reason":{"kind":"error","failure":{"message":"m","code":"c"}}}"#,
    ] {
        assert_eq!(chunk(valid), Ok(()), "{valid}");
    }
    for invalid in [
        r#"{"type":"text-delta","index":-1,"text":"hi"}"#,
        r#"{"type":"text-delta","text":"hi"}"#,
        r#"{"type":"block-start","index":0,"blockType":""}"#,
        r#"{"type":"block-end","index":0,"block":{"type":"text"}}"#,
        r#"{"type":"block-end","index":0,"block":{"type":"tool-call","id":"c","name":"n","arguments":"{\"a\":1,\"a\":2}"}}"#,
        r#"{"type":"usage","usage":{"inputTokens":-1,"outputTokens":0}}"#,
        r#"{"type":"finish","reason":{"kind":"stop","failure":{}}}"#,
        r#"{"type":"finish","reason":{"kind":"error","failure":{"message":"m"}}}"#,
        r#"{"type":"heartbeat"}"#,
        r#"null"#,
    ] {
        assert_eq!(
            chunk(invalid),
            Err("deepseek native: invalid pinned message: invalid chunk record".into()),
            "{invalid}"
        );
    }
}

#[test]
fn assistant_events_decode_their_stream_records() {
    let attempt: AssistantAttempt = decode_strict(
        br#"{"turn":1,"step":2,"stream":[{"type":"chunk","time":0,"chunk":{"type":"finish","reason":{"kind":"stop"}}}]}"#,
    )
    .unwrap();
    assert_eq!(attempt.stream.len(), 1);
    assert_eq!(attempt.stream[0].kind, "chunk");
    let message: AssistantMessageEvent =
        decode_strict(br#"{"turn":1,"stream":null,"usage":{"inputTokens":1,"outputTokens":1}}"#)
            .unwrap();
    assert!(message.stream.is_empty());
    assert_eq!(message.usage.unwrap().input_tokens, 1);
    let refused =
        decode_strict::<AssistantAttempt>(br#"{"stream":[{"type":"tick"}]}"#).unwrap_err();
    assert!(
        refused.starts_with(
            "deepseek native: invalid pinned message: unknown assistant stream record \"tick\""
        ),
        "{refused}"
    );
}

#[test]
fn the_record_type_reads_lone_surrogates_and_only_json_whitespace_as_go_does() {
    assert_eq!(
        stream_record(r#"{"type":"\ud800"}"#).unwrap_err(),
        "deepseek native: invalid pinned message: unknown assistant stream record \"\u{fffd}\""
    );
    assert!(!stream_record("\u{a0}null")
        .unwrap_err()
        .starts_with("deepseek native: invalid pinned message"));
}
