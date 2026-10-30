use oap_adapter_deepseek::native::{
    decode_notification, decode_strict, validate_initialize_params, validate_initialize_result,
    validate_prompt, InitializeParams, InitializeResult, Notification, ServerInfo,
    SessionPromptParams, SERVER_NAME, SERVER_VERSION,
};

fn refused(detail: &str) -> String {
    format!("deepseek native: invalid pinned message: {detail}")
}

fn error(method: &str, data: &str) -> String {
    decode_notification(method, data.as_bytes()).unwrap_err()
}

#[test]
fn a_session_event_needs_a_session_and_a_valid_event() {
    assert!(matches!(
        decode_notification(
            "session.event",
            br#"{"sessionId":"s","event":{"type":"turn/start","seq":1,"time":1,"data":{"turn":1}}}"#
        ),
        Ok(Notification::SessionEvent(_))
    ));
    assert_eq!(
        error(
            "session.event",
            r#"{"event":{"type":"turn/start","seq":1,"time":1,"data":{"turn":1}}}"#
        ),
        refused("session.event sessionId is required")
    );
    assert_eq!(
        error(
            "session.event",
            r#"{"sessionId":"s","event":{"type":"turn/start","seq":1,"time":1,"data":{"turn":0}}}"#
        ),
        refused("invalid turn/start")
    );
    assert_eq!(
        error("session.event", "[]"),
        refused(
            "json: cannot unmarshal array into Go value of type native.SessionEventNotification"
        )
    );
    assert_eq!(
        error("session.tick", "{}"),
        refused("unknown notification \"session.tick\"")
    );
}

#[test]
fn status_and_subagent_notifications_need_their_identity() {
    assert!(matches!(
        decode_notification("session.status", br#"{"sessionId":"s","status":"idle"}"#),
        Ok(Notification::SessionStatus(_))
    ));
    assert_eq!(
        error("session.status", r#"{"sessionId":"s","status":"paused"}"#),
        refused("invalid session.status")
    );
    assert_eq!(
        error("subagent.started", r#"{"parentSessionId":"p"}"#),
        refused("invalid subagent.started")
    );
    let finished = |extra: &str| {
        format!(
            r#"{{"provider":"p","agentId":"a","parentSessionId":"p","childSessionId":"c","status":"ok","stopReason":"completed"{extra}}}"#
        )
    };
    assert!(decode_notification("subagent.finished", finished("").as_bytes()).is_ok());
    assert!(decode_notification(
        "subagent.finished",
        finished(r#","lastAssistantMessage":[{"type":"text","text":"done"}]"#).as_bytes()
    )
    .is_ok());
    assert_eq!(
        error(
            "subagent.finished",
            &finished(r#","lastAssistantMessage":null"#)
        ),
        refused("invalid subagent.finished lastAssistantMessage")
    );
    assert_eq!(
        error(
            "subagent.finished",
            &finished("").replace("completed", "done")
        ),
        refused("invalid subagent stopReason")
    );
    assert_eq!(
        error(
            "subagent.finished",
            &finished("").replace(r#""ok""#, r#""fine""#)
        ),
        refused("invalid subagent status")
    );
}

#[test]
fn the_handshake_and_prompt_follow_the_pin() {
    let params = |max_tokens: Option<i64>| InitializeParams {
        cwd: "/w".into(),
        provider: "p".into(),
        model: "m".into(),
        max_tokens,
        ..InitializeParams::default()
    };
    assert_eq!(
        validate_initialize_params(&params(Some(9_007_199_254_740_991))),
        Ok(())
    );
    assert_eq!(
        validate_initialize_params(&params(Some(0))),
        Err(refused("maxTokens must be a positive safe integer"))
    );
    assert_eq!(
        validate_initialize_params(&InitializeParams::default()),
        Err(refused("initialize cwd, provider, and model are required"))
    );
    let result = |name: &str, version: &str| InitializeResult {
        server_info: ServerInfo {
            name: name.into(),
            version: version.into(),
        },
    };
    assert_eq!(
        validate_initialize_result(&result(SERVER_NAME, &SERVER_VERSION)),
        Ok(())
    );
    assert_eq!(
        validate_initialize_result(&result("other", "1")),
        Err(refused("unexpected serverInfo \"other\"/\"1\""))
    );
    let prompt: SessionPromptParams =
        decode_strict(br#"{"sessionId":"s","contentBlocks":[{"type":"text","text":"hi"}]}"#)
            .unwrap();
    assert_eq!(validate_prompt(&prompt), Ok(()));
    let empty: SessionPromptParams =
        decode_strict(br#"{"sessionId":"s","contentBlocks":[]}"#).unwrap();
    assert_eq!(validate_prompt(&empty), Ok(()));
    let absent: SessionPromptParams = decode_strict(br#"{"sessionId":"s"}"#).unwrap();
    assert_eq!(
        validate_prompt(&absent),
        Err(refused("prompt sessionId and contentBlocks are required"))
    );
    let bad: SessionPromptParams =
        decode_strict(br#"{"sessionId":"s","contentBlocks":[{"type":"text","id":"x"}]}"#).unwrap();
    assert_eq!(
        validate_prompt(&bad),
        Err(refused("prompt contentBlocks are invalid"))
    );
}
