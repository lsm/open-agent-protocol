use oap_adapter_pi::native::{
    decode_strict, validate_event, ExtensionUiRequest, ExtensionUiResponse,
};

fn request(
    id: &str,
    method: &str,
    fill: impl FnOnce(&mut ExtensionUiRequest),
) -> ExtensionUiRequest {
    let mut request = ExtensionUiRequest {
        kind: "extension_ui_request".into(),
        id: id.into(),
        method: method.into(),
        ..ExtensionUiRequest::default()
    };
    fill(&mut request);
    request
}

#[test]
fn extension_requests_validate_the_pinned_union() {
    let valid = [
        request("1", "select", |r| {
            r.title = "T".into();
            r.options = Some(Vec::new());
        }),
        request("2", "confirm", |r| {
            r.title = "T".into();
            r.message = "M".into();
        }),
        request("3", "input", |r| r.title = "T".into()),
        request("4", "editor", |r| r.title = "T".into()),
        request("5", "notify", |r| {
            r.message = "M".into();
            r.notify_type = "warning".into();
        }),
        request("6", "setStatus", |r| r.status_key = "k".into()),
        request("7", "setWidget", |r| {
            r.widget_key = "k".into();
            r.widget_placement = "aboveEditor".into();
        }),
        request("8", "setTitle", |r| r.title = "T".into()),
        request("9", "set_editor_text", |_| {}),
    ];
    for request in valid {
        assert_eq!(request.validate(), Ok(()), "{}", request.method);
    }
    let invalid = [
        request("x", "select", |_| {}),
        request("x", "notify", |r| {
            r.message = "M".into();
            r.notify_type = "bogus".into();
        }),
        request("x", "setWidget", |r| {
            r.widget_key = "k".into();
            r.widget_placement = "side".into();
        }),
        request("x", "confirm", |r| {
            r.title = "T".into();
            r.message = "M".into();
            r.widget_key = "foreign".into();
        }),
    ];
    for request in invalid {
        assert!(request.validate().is_err(), "accepted {request:?}");
    }
}

#[test]
fn an_extension_response_requires_exactly_one_outcome() {
    let response = |fill: fn(&mut ExtensionUiResponse)| {
        let mut response = ExtensionUiResponse {
            kind: "extension_ui_response".into(),
            id: "x".into(),
            ..ExtensionUiResponse::default()
        };
        fill(&mut response);
        response
    };
    for invalid in [
        response(|_| {}),
        response(|r| {
            r.value = Some("x".into());
            r.cancelled = true;
        }),
    ] {
        assert_eq!(
            invalid.validate().unwrap_err().to_string(),
            "pi native: invalid message: extension response requires exactly one outcome"
        );
    }
    assert_eq!(response(|r| r.confirmed = Some(false)).validate(), Ok(()));
}

#[test]
fn tool_events_admit_the_parent_call_of_a_nested_call() {
    for (kind, frame) in [
        (
            "tool_execution_start",
            r#"{"type":"tool_execution_start","toolCallId":"c","toolName":"n","args":{},"parentToolCallId":"p"}"#,
        ),
        (
            "tool_execution_update",
            r#"{"type":"tool_execution_update","toolCallId":"c","toolName":"n","args":{},"partialResult":{},"parentToolCallId":"p"}"#,
        ),
        (
            "tool_execution_end",
            r#"{"type":"tool_execution_end","toolCallId":"c","toolName":"n","result":{},"isError":false,"parentToolCallId":"p"}"#,
        ),
    ] {
        assert_eq!(validate_event(frame, kind), Ok(()), "{kind}");
    }
}

#[test]
fn v110_events_admit_a_timed_tool_end_and_an_aborted_settlement_and_nothing_else() {
    for (kind, frame) in [
        (
            "tool_execution_end",
            r#"{"type":"tool_execution_end","toolCallId":"c","toolName":"n","result":{},"isError":false,"durationMs":12.5}"#,
        ),
        (
            "agent_settled",
            r#"{"type":"agent_settled","aborted":true}"#,
        ),
    ] {
        assert_eq!(validate_event(frame, kind), Ok(()), "{kind}");
    }
    assert!(validate_event(
        r#"{"type":"agent_settled","aborted":false,"invented":1}"#,
        "agent_settled"
    )
    .is_err());
}

#[test]
fn an_extension_request_decodes_strictly_with_a_null_widget_line_or_option_as_empty() {
    let request: ExtensionUiRequest = decode_strict(
        r#"{"type":"extension_ui_request","id":"7","method":"setWidget","widgetKey":"k","widgetLines":["a",null]}"#,
    )
    .unwrap();
    let select: ExtensionUiRequest = decode_strict(
        r#"{"type":"extension_ui_request","id":"1","method":"select","title":"T","options":[null,"b"]}"#,
    )
    .unwrap();
    assert_eq!(select.options, Some(vec![String::new(), "b".to_owned()]));
    assert_eq!(
        request.widget_lines,
        Some(vec!["a".to_owned(), String::new()])
    );
    assert!(decode_strict::<ExtensionUiRequest>(
        r#"{"type":"extension_ui_request","id":"7","method":"setWidget","bogus":1}"#
    )
    .is_err());
}
