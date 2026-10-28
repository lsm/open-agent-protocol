use oap_adapter::jsonrpc::{Dialect, Error, ErrorObject, Fault, Kind, Message, RequestId};
use serde_json::value::RawValue;

const D: Dialect = Dialect::lenient("acp");

fn raw(text: &str) -> Box<RawValue> {
    RawValue::from_string(text.to_owned()).unwrap()
}

fn parse(text: &str) -> Result<Message, Error> {
    Message::parse(&D, text.as_bytes())
}

#[test]
fn request_ids_round_trip_in_both_domains() {
    for id in [
        RequestId::String("1".into()),
        RequestId::String(String::new()),
        RequestId::Integer(1),
        RequestId::Integer(-1),
        RequestId::Integer(i64::MAX),
        RequestId::Integer(i64::MIN),
    ] {
        let encoded = Message::response(id.clone(), raw("null"))
            .encode(&D)
            .unwrap();
        assert_eq!(Message::parse(&D, encoded.as_bytes()).unwrap().id, Some(id));
    }
    assert_ne!(RequestId::String("1".into()), RequestId::Integer(1));
}

#[test]
fn request_ids_refuse_every_other_json_type() {
    for input in [
        "null",
        "true",
        "1.0",
        "1.5",
        "1e0",
        "9223372036854775808",
        "{}",
        "[]",
    ] {
        let frame = format!(r#"{{"jsonrpc":"2.0","id":{input},"result":null}}"#);
        assert!(
            matches!(
                parse(&frame),
                Err(Error {
                    fault: Fault::InvalidId(_),
                    ..
                })
            ),
            "{input}: {:?}",
            parse(&frame)
        );
    }
}

#[test]
fn messages_are_classified_by_strict_json_rpc_shape() {
    for (input, kind) in [
        (
            r#"{"jsonrpc":"2.0","id":0,"method":"initialize","params":{}}"#,
            Kind::Request,
        ),
        (
            r#"{"jsonrpc":"2.0","method":"session/update","params":[]}"#,
            Kind::Notification,
        ),
        (
            r#"{"jsonrpc":"2.0","id":"x","result":null}"#,
            Kind::Response,
        ),
        (
            r#"{"jsonrpc":"2.0","id":-1,"error":{"code":-32600,"message":"bad"}}"#,
            Kind::Error,
        ),
    ] {
        assert_eq!(parse(input).unwrap().kind, kind, "{input}");
    }
}

#[test]
fn invalid_shapes_are_refused() {
    for input in [
        "[]",
        "{}",
        "null",
        r#"{"jsonrpc":"2.0"}"#,
        r#"{"jsonrpc":"2.0","id":1}"#,
        r#"{"jsonrpc":"2.0","result":null}"#,
        r#"{"jsonrpc":"2.0","method":"x","result":null}"#,
        r#"{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":1,"message":"bad"}}"#,
        r#"{"id":1,"result":null}"#,
        r#"{"jsonrpc":"1.0","id":1,"result":null}"#,
        r#"{"jsonrpc":2,"id":1,"result":null}"#,
        r#"{"jsonrpc":"2.0","method":""}"#,
        r#"{"jsonrpc":"2.0","id":null,"method":"x"}"#,
        r#"{"jsonrpc":"2.0","method":"x","params":null}"#,
        r#"{"jsonrpc":"2.0","method":"x","params":"bad"}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":null}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":"x","message":"bad"}}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":1}}"#,
        r#"{"jsonrpc":"2.0","method":"x"}{"jsonrpc":"2.0","method":"y"}"#,
    ] {
        assert!(
            matches!(
                parse(input),
                Err(Error {
                    fault: Fault::InvalidMessage(_) | Fault::InvalidId(_),
                    ..
                })
            ),
            "{input}: {:?}",
            parse(input)
        );
    }
    assert!(matches!(
        Message::parse(&D, &[b'{', 0xff, b'}']),
        Err(Error {
            fault: Fault::InvalidMessage(_),
            ..
        })
    ));
}

#[test]
fn a_key_written_twice_is_refused_by_name() {
    for input in [
        r#"{"jsonrpc":"2.0","id":1,"id":2,"result":null}"#,
        r#"{"jsonrpc":"2.0","id":1,"result":null,"result":{"ok":true}}"#,
        r#"{"jsonrpc":"2.0","method":"x","params":{"a":1,"a":2}}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":1,"code":2,"message":"bad"}}"#,
        r#"{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":{"n":1,"n":2}}}"#,
        r#"{"jsonrpc":"2.0","id":1,"method":"a","method":"b"}"#,
    ] {
        let error = parse(input).unwrap_err();
        assert!(
            error
                .to_string()
                .starts_with("acp rpc: invalid JSON-RPC message: duplicate object key"),
            "{input}: {error}"
        );
    }
    let message = parse(r#"{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":1}}"#).unwrap();
    assert_eq!(message.method, "a");
}

#[test]
fn encoded_messages_sort_their_members_as_go_marshals_a_map() {
    let cases = [
        (
            Message::request(
                RequestId::Integer(1),
                "initialize",
                Some(raw(r#"{"protocolVersion": 1}"#)),
            ),
            r#"{"id":1,"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":1}}"#,
        ),
        (
            Message::notification("session/cancel", None),
            r#"{"jsonrpc":"2.0","method":"session/cancel"}"#,
        ),
        (
            Message::response(RequestId::String("r<1>".into()), raw("null")),
            r#"{"id":"r\u003c1\u003e","jsonrpc":"2.0","result":null}"#,
        ),
        (
            Message::error_response(
                RequestId::Integer(2),
                ErrorObject {
                    code: -32601,
                    message: "a & b".into(),
                    data: None,
                },
            ),
            r#"{"error":{"code":-32601,"message":"a \u0026 b"},"id":2,"jsonrpc":"2.0"}"#,
        ),
    ];
    for (message, want) in cases {
        let encoded = message.encode(&D).unwrap();
        assert_eq!(encoded, want);
        Message::parse(&D, encoded.as_bytes()).unwrap();
    }
}

#[test]
fn a_message_go_would_refuse_to_marshal_is_refused() {
    assert!(Message::notification("", None).encode(&D).is_err());
    assert!(Message::notification("x", Some(raw("1")))
        .encode(&D)
        .is_err());
    let mut response = Message::response(RequestId::Integer(1), raw("null"));
    response.id = None;
    assert!(response.encode(&D).is_err());
}

#[test]
fn a_lone_surrogate_escape_decodes_as_the_replacement_character() {
    let message =
        parse(r#"{"jsonrpc":"2.0","method":"x","params":{"prompt":"a\ud800b"}}"#).unwrap();
    let params: serde_json::Value = serde_json::from_str(message.params.unwrap().get()).unwrap();
    assert_eq!(params["prompt"], "a\u{fffd}b");
}
