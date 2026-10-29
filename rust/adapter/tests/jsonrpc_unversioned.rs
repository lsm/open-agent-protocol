use oap_adapter::jsonrpc::{Dialect, Error, ErrorObject, Kind, Message, RequestId};
use serde_json::value::RawValue;

const CODEX: Dialect = Dialect::unversioned("codex app-server");

fn parse(text: &str) -> Result<Message, Error> {
    Message::parse(&CODEX, text.as_bytes())
}

fn raw(text: &str) -> Option<Box<RawValue>> {
    Some(RawValue::from_string(text.into()).unwrap())
}

#[test]
fn an_unversioned_frame_reads_as_each_message_kind() {
    for (input, kind) in [
        (
            r#"{"id":0,"method":"initialize","params":null}"#,
            Kind::Request,
        ),
        (r#"{"method":"initialized"}"#, Kind::Notification),
        (r#"{"id":"x","result":null}"#, Kind::Response),
        (
            r#"{"id":-1,"error":{"code":-32600,"message":"bad"}}"#,
            Kind::Error,
        ),
    ] {
        assert_eq!(parse(input).unwrap().kind, kind, "{input}");
    }
}

#[test]
fn an_ambiguous_foreign_or_repeated_frame_is_refused() {
    for input in [
        "[]",
        "{}",
        r#"{"id":1}"#,
        r#"{"result":null}"#,
        r#"{"method":"x","result":null}"#,
        r#"{"id":1,"result":null,"error":{"code":1,"message":"bad"}}"#,
        r#"{"jsonrpc":"2.0","id":1,"result":null}"#,
        r#"{"method":""}"#,
        r#"{"id":null,"method":"x"}"#,
        r#"{"method":"x"}{"method":"y"}"#,
        r#"{"id":1,"id":2,"result":null}"#,
        r#"{"id":1,"result":null,"result":{"ok":true}}"#,
        r#"{"method":"x","params":{"a":1,"a":2}}"#,
        r#"{"id":1,"error":{"code":1,"code":2,"message":"bad"}}"#,
    ] {
        assert!(parse(input).is_err(), "accepted {input}");
    }
    assert_eq!(
        parse(r#"{"jsonrpc":"2.0","id":1,"method":"x"}"#)
            .unwrap_err()
            .to_string(),
        "codex app-server rpc: invalid message: jsonrpc member is not part of the Codex dialect"
    );
    assert_eq!(
        parse("{}").unwrap_err().to_string(),
        "codex app-server rpc: invalid message: empty object"
    );
}

#[test]
fn an_unversioned_error_needs_a_code_and_a_nonempty_message() {
    assert!(parse(r#"{"id":1,"error":{"message":"m"}}"#).is_err());
    assert!(parse(r#"{"id":1,"error":{"code":1,"message":""}}"#).is_err());
    assert!(parse(r#"{"id":1,"error":{"code":1}}"#).is_err());
}

#[test]
fn an_unversioned_request_keeps_its_trace_and_every_message_encodes_without_jsonrpc() {
    let message = parse(r#"{"id":7,"method":"x","params":[1],"trace":{"t":1}}"#).unwrap();
    assert_eq!(
        message.trace.as_deref().map(RawValue::get),
        Some(r#"{"t":1}"#)
    );
    assert_eq!(
        message.encode(&CODEX).unwrap(),
        r#"{"id":7,"method":"x","params":[1],"trace":{"t":1}}"#
    );
    for message in [
        Message::request(
            RequestId::Integer(1),
            "initialize",
            raw(r#"{"clientInfo":{}}"#),
        ),
        Message::notification("initialized", None),
        Message::response(RequestId::String("request".into()), raw("null").unwrap()),
        Message::error_response(
            RequestId::Integer(2),
            ErrorObject {
                code: -32601,
                message: "unsupported".into(),
                data: None,
            },
        ),
    ] {
        let encoded = message.encode(&CODEX).unwrap();
        assert!(!encoded.contains("jsonrpc"), "{encoded}");
        parse(&encoded).unwrap_or_else(|failure| panic!("{encoded}: {failure}"));
    }
}

#[test]
fn a_versioned_dialect_keeps_its_jsonrpc_member_and_wording() {
    let acp = Dialect::lenient("acp");
    assert_eq!(
        Message::notification("x", None).encode(&acp).unwrap(),
        r#"{"jsonrpc":"2.0","method":"x"}"#
    );
    assert!(Message::parse(&acp, b"[]")
        .unwrap_err()
        .to_string()
        .starts_with("acp rpc: invalid JSON-RPC message: "));
    assert!(parse("[]")
        .unwrap_err()
        .to_string()
        .starts_with("codex app-server rpc: invalid message: "));
}
