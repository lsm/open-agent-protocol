use oap_adapter_pi::native::{
    decode_state, decode_strict, validate_state, Command, ImageContent, Response, SessionState,
};

fn with(kind: &str, fill: impl FnOnce(&mut Command)) -> Command {
    let mut command = Command::of(kind);
    fill(&mut command);
    command
}

fn text(value: &str) -> Option<String> {
    Some(value.to_owned())
}

#[test]
fn all_pinned_commands_validate() {
    let commands = [
        with("prompt", |c| c.message = text("m")),
        with("steer", |c| c.message = text("m")),
        with("follow_up", |c| c.message = text("m")),
        Command::of("abort"),
        Command::of("clear_queue"),
        Command::of("new_session"),
        Command::of("get_state"),
        with("set_model", |c| {
            c.provider = text("p");
            c.model_id = text("m");
        }),
        Command::of("cycle_model"),
        Command::of("get_available_models"),
        with("set_thinking_level", |c| c.level = "max".into()),
        Command::of("cycle_thinking_level"),
        Command::of("get_available_thinking_levels"),
        with("set_steering_mode", |c| c.mode = "all".into()),
        with("set_follow_up_mode", |c| c.mode = "one-at-a-time".into()),
        Command::of("compact"),
        with("set_auto_compaction", |c| c.enabled = Some(false)),
        with("set_auto_retry", |c| c.enabled = Some(true)),
        Command::of("abort_retry"),
        with("bash", |c| c.bash_command = text("ls")),
        Command::of("abort_bash"),
        Command::of("get_session_stats"),
        Command::of("export_html"),
        with("switch_session", |c| c.session_path = text("/x")),
        with("fork", |c| c.entry_id = text("e")),
        Command::of("clone"),
        Command::of("get_fork_messages"),
        Command::of("get_entries"),
        Command::of("get_tree"),
        Command::of("get_last_assistant_text"),
        with("set_session_name", |c| c.name = text("n")),
        Command::of("get_messages"),
        Command::of("get_commands"),
    ];
    for command in commands {
        assert_eq!(command.validate(), Ok(()), "{}", command.kind);
    }
}

#[test]
fn a_command_refuses_a_field_of_another_variant_an_unknown_type_and_a_missing_requirement() {
    assert_eq!(
        with("abort", |c| c.message = text("not valid"))
            .validate()
            .unwrap_err()
            .to_string(),
        "pi native: invalid message: field message is not valid for abort"
    );
    assert_eq!(
        Command::of("dance").validate().unwrap_err().to_string(),
        "pi native: invalid message: unknown command type \"dance\""
    );
    assert_eq!(
        Command::of("set_model").validate().unwrap_err().to_string(),
        "pi native: invalid message: set_model requires provider"
    );
    assert_eq!(
        with("set_thinking_level", |c| c.level = "huge".into())
            .validate()
            .unwrap_err()
            .to_string(),
        "pi native: invalid message: invalid thinking level \"huge\""
    );
}

#[test]
fn a_command_marshals_with_pi_s_field_names() {
    let command = with("prompt", |c| {
        c.id = "req_1".into();
        c.message = text("hello");
        c.streaming_behavior = "followUp".into();
    });
    assert_eq!(
        serde_json::to_string(&command).unwrap(),
        r#"{"id":"req_1","type":"prompt","message":"hello","streamingBehavior":"followUp"}"#
    );
}

#[test]
fn a_response_is_decoded_strictly_and_must_agree_with_its_outcome() {
    assert!(decode_strict::<Response>(
        r#"{"type":"response","type":"response","command":"prompt","success":true}"#
    )
    .is_err());
    assert!(decode_strict::<Response>(
        r#"{"type":"response","command":"prompt","success":true,"extra":1}"#
    )
    .is_err());
    let failed: Response =
        decode_strict(r#"{"type":"response","command":"prompt","success":false}"#).unwrap();
    assert!(failed.validate().is_err());
    let refused: Response = decode_strict(
        r#"{"id":"req_1","type":"response","command":"prompt","success":false,"error":"busy"}"#,
    )
    .unwrap();
    assert_eq!(refused.validate(), Ok(()));
}

#[test]
fn decode_strict_refuses_a_key_written_twice_at_any_depth_and_admits_the_same_bytes_without_it() {
    for raw in [
        r#"{"a":1,"a":2}"#,
        r#"{"a":{"b":1,"b":2}}"#,
        r#"{"a":[{"b":1,"b":2}]}"#,
        r#"{"a":1,"a":2,"a":3}"#,
    ] {
        let refused = decode_strict::<serde_json::Value>(raw).unwrap_err();
        assert!(refused.contains("duplicate object key"), "{raw}: {refused}");
    }
    let value: serde_json::Value = decode_strict(r#"{"a":{"b":[1,2]}}"#).unwrap();
    assert!(value.get("a").is_some());
}

#[test]
fn session_state_needs_an_identity_known_modes_and_a_known_thinking_level() {
    let state: SessionState = decode_strict(
        r#"{"thinkingLevel":"off","isStreaming":false,"isCompacting":false,"steeringMode":"all","followUpMode":"one-at-a-time","sessionId":"s1","autoCompactionEnabled":true,"messageCount":0,"pendingMessageCount":0}"#,
    )
    .unwrap();
    assert_eq!(validate_state(&state), Ok(()));
    for broken in [
        SessionState {
            session_id: String::new(),
            ..state.clone()
        },
        SessionState {
            steering_mode: "some".into(),
            ..state.clone()
        },
        SessionState {
            thinking_level: "huge".into(),
            ..state.clone()
        },
        SessionState {
            message_count: -1,
            ..state.clone()
        },
    ] {
        assert!(validate_state(&broken).is_err(), "{broken:?}");
    }
}

#[test]
fn a_command_decodes_like_go_s_strict_decoder_a_null_is_zero_and_an_unknown_image_member_is_refused(
) {
    let command: Command = decode_strict(
        r#"{"id":null,"type":"prompt","message":"m","images":[{"type":"image","data":null},null]}"#,
    )
    .unwrap();
    assert_eq!(command.id, "");
    assert_eq!(
        command.images,
        [
            ImageContent {
                kind: "image".into(),
                ..ImageContent::default()
            },
            ImageContent::default(),
        ]
    );
    let bare: Command = decode_strict(r#"{"type":"steer","images":null,"level":null}"#).unwrap();
    assert!(bare.images.is_empty());
    assert!(decode_strict::<Command>(
        r#"{"type":"prompt","images":[{"type":"image","data":"x","mimeType":"image/png","extra":1}]}"#
    )
    .is_err());
}

#[test]
fn a_lone_surrogate_escape_decodes_as_the_replacement_character_as_go_s_decoder_reads_it() {
    let response: Response = decode_strict(
        r#"{"id":"req_1","type":"response","command":"prompt","success":false,"error":"bad \ud800 half"}"#,
    )
    .unwrap();
    assert_eq!(response.error, "bad \u{fffd} half");
}

#[test]
fn the_lenient_state_matches_keys_as_go_does_and_keeps_the_last_value() {
    let state = decode_state(
        r#"{"sessionid":"folded","sessionId":"exact","SESSIONID":"last","messagecount":2,"unknown":1}"#,
    )
    .unwrap();
    assert_eq!(state.session_id, "last");
    assert_eq!(state.message_count, 2);
    assert_eq!(decode_state("null").unwrap().session_id, "");
}
