use oap_adapter_deepseek::native::{
    decode_strict, observed_only, ContentBlock, Event, InitializeParams, InitializeResult,
    SessionEventNotification, SessionPromptResult, SubagentFinishedNotification, SERVER_VERSION,
};

#[test]
fn a_session_event_decodes_with_its_raw_data_kept() {
    let notification: SessionEventNotification = decode_strict(
        br#"{"sessionId":"s1","event":{"type":"turn/start","seq":3,"time":7,"data":{"turn":1},"sourceEventSeqs":[1,2]}}"#,
    )
    .unwrap();
    assert_eq!(notification.session_id, "s1");
    assert_eq!(notification.event.kind, "turn/start");
    assert_eq!(notification.event.seq, 3);
    assert_eq!(notification.event.data_bytes(), br#"{"turn":1}"#);
    assert_eq!(notification.event.source_event_seqs, Some(vec![1, 2]));
}

#[test]
fn a_repeated_key_is_refused_at_any_depth() {
    let failure = decode_strict::<SessionEventNotification>(
        br#"{"sessionId":"s","event":{"seq":1,"seq":2}}"#,
    )
    .unwrap_err();
    assert_eq!(failure, "duplicate object key \"seq\"");
}

#[test]
fn an_unknown_member_is_refused() {
    let failure = decode_strict::<Event>(br#"{"type":"turn/start","extra":true}"#).unwrap_err();
    assert!(failure.starts_with("unknown field `extra`"), "{failure}");
}

#[test]
fn null_members_and_null_array_elements_read_as_zero_values() {
    let event: Event =
        decode_strict(br#"{"type":null,"seq":null,"sourceEventSeqs":[4,null]}"#).unwrap();
    assert_eq!(event.kind, "");
    assert_eq!(event.seq, 0);
    assert_eq!(event.source_event_seqs, Some(vec![4, 0]));
    let finished: SubagentFinishedNotification =
        decode_strict(br#"{"lastAssistantMessage":[null,{"type":"text","text":"hi"}]}"#).unwrap();
    assert_eq!(finished.last_assistant_message.len(), 2);
    assert_eq!(finished.last_assistant_message[0].kind, "");
    assert_eq!(finished.last_assistant_message[1].text, "hi");
    let block: ContentBlock = decode_strict(br#"{"type":"x","content":[null]}"#).unwrap();
    assert_eq!(block.content.unwrap().len(), 1);
    assert!(decode_strict::<Event>(b"null").unwrap().kind.is_empty());
}

#[test]
fn invalid_utf8_bytes_and_unpaired_surrogates_read_as_replacement_characters() {
    let mut data = br#"{"type":"a"#.to_vec();
    data.extend_from_slice(&[0xff, 0xfe]);
    data.extend_from_slice(br#"\ud800b"}"#);
    let event: Event = decode_strict(&data).unwrap();
    assert_eq!(event.kind, "a\u{fffd}\u{fffd}\u{fffd}b");
}

#[test]
fn a_non_object_and_an_empty_document_are_refused_with_go_text() {
    assert_eq!(
        decode_strict::<Event>(b" [1]").unwrap_err(),
        "json: cannot unmarshal array into Go value of type native.Event"
    );
    assert_eq!(
        decode_strict::<SessionEventNotification>(b"\"s\"").unwrap_err(),
        "json: cannot unmarshal string into Go value of type native.SessionEventNotification"
    );
    assert_eq!(decode_strict::<Event>(b" \n").unwrap_err(), "EOF");
    assert_eq!(Event::default().data_as::<Event>().unwrap_err(), "EOF");
}

#[test]
fn handshake_and_prompt_results_accept_members_the_pin_does_not_name() {
    let result: InitializeResult = serde_json::from_str(
        r#"{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"1","build":"x"},"protocol":2}"#,
    )
    .unwrap();
    assert_eq!(result.server_info.version, "1");
    let prompt: SessionPromptResult =
        serde_json::from_str(r#"{"messageId":"m1","queued":false}"#).unwrap();
    assert_eq!(prompt.message_id, "m1");
}

#[test]
fn initialize_params_omit_empty_optional_members() {
    let params = InitializeParams {
        cwd: "/w".into(),
        provider: "deepseek".into(),
        model: "m".into(),
        ..InitializeParams::default()
    };
    assert_eq!(
        serde_json::to_string(&params).unwrap(),
        r#"{"cwd":"/w","provider":"deepseek","model":"m"}"#
    );
}

#[test]
fn the_server_version_comes_from_the_catalog_and_observed_events_are_pinned() {
    assert_eq!(
        *SERVER_VERSION,
        oap_harness::current("deepseek-harness").unwrap().admits[0]
    );
    assert!(observed_only("compaction/start"));
    assert!(!observed_only("turn/start"));
}
