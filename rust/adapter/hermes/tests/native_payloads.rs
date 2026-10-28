use oap_adapter_hermes::native::{
    decode_strict, DeltaPayload, Event, MessageCompletePayload, ToolCompletePayload,
    ToolStartPayload, UsageTickPayload,
};

fn event(frame: &str) -> Event {
    decode_strict(frame.as_bytes()).unwrap()
}

fn complete(frame: &str) -> Result<MessageCompletePayload, String> {
    decode_strict(event(frame).payload_bytes())
}

#[test]
fn an_interrupted_settlement_carries_null_text_and_its_persisted_turn() {
    let payload = complete(
        r#"{"type":"message.complete","session_id":"s","seq":4,"payload":{"text":null,"usage":{"model":"m","input":0,"output":0,"reasoning":0,"prompt":0,"completion":0,"total":0,"calls":0,"compressions":0,"active_subagents":0},"status":"interrupted","persisted_turn":{"row_ids":[1,2],"complete":false,"user_row_id":1}}}"#,
    )
    .unwrap();
    assert_eq!(
        (payload.text.as_str(), payload.status.as_str()),
        ("", "interrupted")
    );
    assert!(payload.persisted_turn.is_some());
}

#[test]
fn message_complete_admits_response_reuse_and_transform_flags_and_refuses_an_undeclared_member() {
    let payload = complete(
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"hi","status":"complete","response_reused":true,"response_transformed":null}}"#,
    )
    .unwrap();
    assert_eq!(payload.response_reused, Some(true));
    assert_eq!(payload.response_transformed, None);
    assert!(complete(
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"hi","status":"complete","response_recycled":true}}"#,
    )
    .is_err());
}

#[test]
fn message_complete_usage_tolerates_extensible_readouts_but_the_payload_does_not() {
    let payload = complete(
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"hi","usage":{"model":"m","input":1,"output":2,"reasoning":0,"prompt":1,"completion":2,"total":3,"calls":1,"active_subagents":2,"avg_latency_s":1.4,"avg_tps":9.2,"future_readout":true},"status":"complete"}}"#,
    )
    .unwrap();
    assert_eq!(payload.usage.unwrap().active_subagents, Some(2));
    assert!(complete(
        r#"{"type":"message.complete","session_id":"s","seq":3,"payload":{"text":"hi","usage":{"model":"m","input":1,"output":2,"reasoning":0,"prompt":1,"completion":2,"total":3,"calls":1},"status":"complete","surprise":1}}"#,
    )
    .is_err());
}

#[test]
fn an_error_surface_carries_provider_identity() {
    let payload = complete(
        r#"{"type":"message.complete","session_id":"s","seq":9,"payload":{"text":"Error: boom","usage":{},"status":"error","error":"boom","recoverable":true,"error_surface":{"layer":"provider","code":"auth","retryable":false,"provider":"openai","model":"gpt-5.6-sol"}}}"#,
    )
    .unwrap();
    let surface = payload.error_surface.unwrap();
    assert_eq!(
        (surface.provider.as_str(), surface.model.as_str()),
        ("openai", "gpt-5.6-sol")
    );
    assert!(payload.recoverable);
}

#[test]
fn decode_strict_reads_null_as_zero_and_refuses_empty_input_a_trailing_value_and_a_wrong_shape() {
    let delta: DeltaPayload = decode_strict(b"null").unwrap();
    assert_eq!(delta.text, "");
    assert_eq!(decode_strict::<DeltaPayload>(b"  ").unwrap_err(), "EOF");
    assert!(decode_strict::<DeltaPayload>(br#"{"text":"a"} {}"#).is_err());
    assert!(decode_strict::<DeltaPayload>(br#"[1]"#).is_err());
    assert!(decode_strict::<UsageTickPayload>(br#"{"usage":null}"#).is_ok());
}

#[test]
fn tool_args_must_be_an_object_or_null() {
    for args in [r#"{"path":"a"}"#, "null"] {
        let start: ToolStartPayload =
            decode_strict(format!(r#"{{"tool_id":"t","name":"read","args":{args}}}"#).as_bytes())
                .unwrap();
        assert_eq!(start.args.unwrap().get(), args);
    }
    let refused =
        decode_strict::<ToolCompletePayload>(br#"{"tool_id":"t","args":[1]}"#).unwrap_err();
    assert!(
        refused
            .contains("json: cannot unmarshal [1] into Go value of type map[string]interface {}"),
        "{refused}"
    );
}

#[test]
fn an_event_keeps_its_payload_bytes_and_marshals_without_its_zero_members() {
    let frame =
        event(r#"{"type":"message.delta","session_id":"s","seq":2,"payload":{"text":"x"}}"#);
    assert_eq!(frame.payload_bytes(), br#"{"text":"x"}"#);
    let bare = event(r#"{"type":"gateway.ready"}"#);
    assert_eq!(bare.payload_bytes(), b"");
    assert_eq!(
        serde_json::to_string(&bare).unwrap(),
        r#"{"type":"gateway.ready"}"#
    );
}

#[test]
fn a_lone_surrogate_escape_decodes_as_the_replacement_character_as_go_s_decoder_reads_it() {
    let delta: DeltaPayload = decode_strict(br#"{"text":"bad \ud800 half"}"#).unwrap();
    assert_eq!(delta.text, "bad \u{fffd} half");
}

#[test]
fn an_event_envelope_refuses_an_unknown_member() {
    let refused =
        decode_strict::<Event>(br#"{"type":"error","payload":{"message":"x"},"bogus":1}"#)
            .unwrap_err();
    assert!(refused.contains("unknown field `bogus`"), "{refused}");
}
