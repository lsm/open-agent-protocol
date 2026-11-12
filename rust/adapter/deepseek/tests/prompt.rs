use std::cell::Cell;

use oap_adapter::Error;
use oap_adapter_deepseek::prompt::native_prompt;
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode,
};

fn user(content: MessageContent, id: Option<&str>) -> Message {
    Message {
        id: id.map(Into::into),
        role: MessageRole::USER,
        content,
        ..Message::default()
    }
}

fn content(value: serde_json::Value) -> MessageContent {
    serde_json::from_value(value).unwrap()
}

fn request(messages: Vec<Message>) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "session".into(),
        delivery: RequestedDeliveryMode::AUTO,
        messages,
        ..MessageSubmitRequest::default()
    }
}

fn counter() -> impl Fn(&str) -> String {
    let next = Cell::new(0);
    move |kind| {
        next.set(next.get() + 1);
        format!("{kind}-{}", next.get())
    }
}

#[test]
fn text_and_text_parts_become_text_blocks_and_missing_ids_are_minted() {
    let parts = content(serde_json::json!([
        {"type": "text", "text": "two"},
        {"type": "text", "text": "three"}
    ]));
    let (blocks, ids) = native_prompt(
        &request(vec![
            user(MessageContent::from_text("one"), None),
            user(parts, Some("kept")),
        ]),
        &counter(),
    )
    .unwrap();
    let texts: Vec<_> = blocks
        .iter()
        .map(|block| (block.kind.as_str(), block.text.as_str()))
        .collect();
    assert_eq!(texts, [("text", "one"), ("text", "two"), ("text", "three")]);
    let ids: Vec<_> = ids.iter().map(|id| id.as_str().to_owned()).collect();
    assert_eq!(ids, ["message-1", "kept"]);
}

#[test]
fn every_shape_go_refuses_is_an_invalid_submission() {
    let image = content(serde_json::json!([
        {"type": "image", "image": {"url": "https://example.invalid/a.png"}}
    ]));
    let assistant = Message {
        role: MessageRole::ASSISTANT,
        ..user(MessageContent::from_text("x"), None)
    };
    let mut queued = request(vec![user(MessageContent::from_text("x"), None)]);
    queued.delivery = "later".into();
    let mut unscoped = request(vec![user(MessageContent::from_text("x"), None)]);
    unscoped.session_id = "".into();
    for (name, submitted) in [
        ("empty", request(Vec::new())),
        ("unscoped", unscoped),
        ("unknown delivery", queued),
        ("assistant", request(vec![assistant])),
        ("image", request(vec![user(image, None)])),
        (
            "number",
            request(vec![user(content(serde_json::json!(7)), None)]),
        ),
    ] {
        assert!(
            matches!(
                native_prompt(&submitted, &counter()),
                Err(Error::InvalidSubmission)
            ),
            "{name}"
        );
    }
}

#[test]
fn an_unadvertised_control_is_refused_before_the_messages_are_read() {
    let mut instructed = request(Vec::new());
    instructed.instructions = Some("be brief".into());
    let mut queued = request(Vec::new());
    queued.delivery = RequestedDeliveryMode::QUEUE;
    for (submitted, feature) in [
        (instructed, "run.instructions"),
        (queued, "session.message.delivery.queue"),
    ] {
        match native_prompt(&submitted, &counter()) {
            Err(Error::UnsupportedControl(control)) => assert_eq!(control.feature, feature),
            other => panic!("{feature}: {other:?}"),
        }
    }
}

#[test]
fn null_content_becomes_an_empty_text_block_as_in_go() {
    let (blocks, _) = native_prompt(
        &request(vec![user(content(serde_json::Value::Null), Some("m"))]),
        &counter(),
    )
    .unwrap();
    let texts: Vec<_> = blocks
        .iter()
        .map(|block| (block.kind.as_str(), block.text.as_str()))
        .collect();
    assert_eq!(texts, [("text", "")]);
}

#[test]
fn lone_surrogate_escapes_become_replacement_characters_as_in_go() {
    let text: MessageContent = serde_json::from_str(r#""a\ud800b""#).unwrap();
    let parts: MessageContent =
        serde_json::from_str(r#"[{"type":"text","text":"c\udfffd"}]"#).unwrap();
    let (blocks, _) = native_prompt(
        &request(vec![user(text, Some("m")), user(parts, Some("n"))]),
        &counter(),
    )
    .unwrap();
    let texts: Vec<_> = blocks.iter().map(|block| block.text.as_str()).collect();
    assert_eq!(texts, ["a\u{fffd}b", "c\u{fffd}d"]);
}
