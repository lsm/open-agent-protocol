use std::io::Cursor;

use oxap_adapter::jsonrpc::{Decoder, Encoder, ErrorObject, Kind, Message, RequestId};
use oxap_adapter_hermes::rpc::DIALECT;
use serde_json::value::RawValue;

fn parse(frame: &str) -> Result<Message, String> {
    Message::parse(&DIALECT, frame.as_bytes()).map_err(|error| error.to_string())
}

fn raw(text: &str) -> Option<Box<RawValue>> {
    Some(RawValue::from_string(text.into()).unwrap())
}

fn decode(stream: &str, limit: usize) -> Result<Message, String> {
    Decoder::new(DIALECT, Cursor::new(stream.as_bytes().to_vec()), limit)
        .decode()
        .map_err(|error| error.to_string())
}

#[test]
fn the_pinned_gateway_shapes_parse_to_their_kinds() {
    for (frame, kind) in [
        (
            r#"{"jsonrpc":"2.0","id":1,"result":{"status":"streaming"}}"#,
            Kind::Response,
        ),
        (
            r#"{"jsonrpc":"2.0","id":"a","error":{"code":4001,"message":"session not found","data":{"x":1}}}"#,
            Kind::Error,
        ),
        (
            r#"{"jsonrpc":"2.0","method":"event","params":{"type":"message.start","session_id":"s","seq":1}}"#,
            Kind::Notification,
        ),
        (
            r#"{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}"#,
            Kind::Notification,
        ),
    ] {
        assert_eq!(parse(frame).unwrap().kind, kind, "{frame}");
    }
}

#[test]
fn a_frame_the_gateway_would_tolerate_is_refused_with_gos_text() {
    for (frame, want) in [
        (
            r#" {"jsonrpc":"2.0","id":1,"result":{}}"#,
            "invalid JSON-RPC message: frame must be exactly one JSON object",
        ),
        (
            "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}\r",
            "invalid JSON-RPC message: frame must be exactly one JSON object",
        ),
        (
            "[1,2]",
            "invalid JSON-RPC message: frame must be exactly one JSON object",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"id":2,"result":{}}"#,
            "invalid JSON-RPC message: duplicate object key \"id\"",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"result":{"a":1,"a":2}}"#,
            "invalid JSON-RPC message: duplicate object key \"a\"",
        ),
        (
            r#"{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}"#,
            "id must be a string or integer",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1.5,"result":{}}"#,
            "id must be a string or integer: \"1.5\"",
        ),
        (
            r#"{"id":1,"result":{}}"#,
            "invalid JSON-RPC message: jsonrpc must equal \"2.0\"",
        ),
        (
            r#"{"jsonrpc":"1.0","id":1,"result":{}}"#,
            "invalid JSON-RPC message: jsonrpc must equal \"2.0\"",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"result":{},"extra":true}"#,
            "invalid JSON-RPC message: unknown member \"extra\"",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"method":"m","params":[1]}"#,
            "invalid JSON-RPC message: params must be an object or null",
        ),
        (
            r#"{"jsonrpc":"2.0","method":"m","params":"x"}"#,
            "invalid JSON-RPC message: params must be an object or null",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"result":{},"error":{"code":1,"message":"m"}}"#,
            "invalid JSON-RPC message: result and error are mutually exclusive",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"method":"m","result":{}}"#,
            "invalid JSON-RPC message: method cannot accompany a response",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"params":{},"result":{}}"#,
            "invalid JSON-RPC message: params requires method",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1}"#,
            "invalid JSON-RPC message: unrecognized object shape",
        ),
        (
            r#"{"jsonrpc":"2.0","method":""}"#,
            "invalid JSON-RPC message: method must be a non-empty string",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"error":{"code":1}}"#,
            "invalid JSON-RPC message: error message is required",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":""}}"#,
            "invalid JSON-RPC message: malformed error object",
        ),
        (
            r#"{"jsonrpc":"2.0","id":1,"error":{"code":"x","message":"m"}}"#,
            "invalid JSON-RPC message: malformed error object",
        ),
    ] {
        assert_eq!(
            parse(frame).unwrap_err(),
            format!("hermes rpc: {want}"),
            "{frame}"
        );
    }
}

#[test]
fn a_value_after_the_object_is_refused() {
    let refused = parse(r#"{"jsonrpc":"2.0","id":1,"result":{}} {"x":1}"#).unwrap_err();
    assert!(
        refused.starts_with("hermes rpc: invalid JSON-RPC message: "),
        "{refused}"
    );
}

#[test]
fn a_key_written_twice_is_refused_at_any_depth() {
    for frame in [
        r#"{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":1,"k":2}}"#,
        r#"{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":{"n":1,"n":2}}}"#,
        r#"{"jsonrpc":"2.0","id":1,"method":"a","method":"b"}"#,
        r#"{"jsonrpc":"2.0","id":1,"result":{"k":1,"k":2}}"#,
    ] {
        let refused = parse(frame).unwrap_err();
        assert!(
            refused.contains("duplicate object key"),
            "{frame}: {refused}"
        );
    }
    let admitted = parse(r#"{"jsonrpc":"2.0","id":1,"method":"a","params":{"k":1}}"#).unwrap();
    assert_eq!(admitted.method, "a");
}

#[test]
fn null_params_and_a_null_result_are_kept() {
    let request = parse(r#"{"jsonrpc":"2.0","id":1,"method":"m","params":null}"#).unwrap();
    assert_eq!(request.kind, Kind::Request);
    assert_eq!(request.params.as_deref().map(RawValue::get), Some("null"));
    assert_eq!(
        request.encode(&DIALECT).unwrap(),
        r#"{"id":1,"jsonrpc":"2.0","method":"m","params":null}"#
    );
    let response = parse(r#"{"jsonrpc":"2.0","id":1,"result":null}"#).unwrap();
    assert_eq!(response.kind, Kind::Response);
    assert_eq!(
        response.encode(&DIALECT).unwrap(),
        r#"{"id":1,"jsonrpc":"2.0","result":null}"#
    );
}

#[test]
fn an_empty_string_id_parses_but_never_marshals() {
    for frame in [
        r#"{"jsonrpc":"2.0","id":"","result":{}}"#,
        r#"{"jsonrpc":"2.0","id":"","method":"m"}"#,
    ] {
        let message = parse(frame).unwrap();
        assert_eq!(message.id, Some(RequestId::String(String::new())));
        assert_eq!(
            message.encode(&DIALECT).unwrap_err().to_string(),
            "hermes rpc: invalid JSON-RPC message",
            "{frame}"
        );
    }
}

#[test]
fn messages_marshal_as_go_marshals_them() {
    for (message, want) in [
        (
            Message::request(
                RequestId::Integer(7),
                "prompt.submit",
                raw(r#"{"session_id":"s","text":"hi"}"#),
            ),
            Ok(
                r#"{"id":7,"jsonrpc":"2.0","method":"prompt.submit","params":{"session_id":"s","text":"hi"}}"#,
            ),
        ),
        (
            Message::request(RequestId::String(String::new()), "m", None),
            Err(()),
        ),
        (
            Message::request(RequestId::String("a".into()), "m", raw("[1]")),
            Err(()),
        ),
        (
            Message::request(
                RequestId::String("a<&>".into()),
                "m",
                raw("{\"t\":\"<\u{2028}>\"}"),
            ),
            Ok(
                r#"{"id":"a\u003c\u0026\u003e","jsonrpc":"2.0","method":"m","params":{"t":"\u003c\u2028\u003e"}}"#,
            ),
        ),
        (
            Message::notification("m", None),
            Ok(r#"{"jsonrpc":"2.0","method":"m"}"#),
        ),
        (Message::notification("", None), Err(())),
        (
            Message::error_response(
                RequestId::Integer(2),
                ErrorObject {
                    code: 4001,
                    message: "x".into(),
                    data: raw(r#"{"a":1}"#),
                },
            ),
            Ok(r#"{"error":{"code":4001,"message":"x","data":{"a":1}},"id":2,"jsonrpc":"2.0"}"#),
        ),
        (
            Message::error_response(
                RequestId::Integer(2),
                ErrorObject {
                    code: 4001,
                    message: String::new(),
                    data: None,
                },
            ),
            Ok(r#"{"error":{"code":4001,"message":""},"id":2,"jsonrpc":"2.0"}"#),
        ),
    ] {
        let encoded = message.encode(&DIALECT);
        match want {
            Ok(want) => assert_eq!(encoded.unwrap(), want),
            Err(()) => assert_eq!(
                encoded.unwrap_err().to_string(),
                "hermes rpc: invalid JSON-RPC message"
            ),
        }
    }
    let round = parse(r#"{"id":7,"jsonrpc":"2.0","method":"prompt.submit","params":{"session_id":"s","text":"hi"}}"#).unwrap();
    assert_eq!(round.method, "prompt.submit");
    assert_eq!(round.id, Some(RequestId::Integer(7)));
}

#[test]
fn the_decoder_frames_one_lf_terminated_object_per_line() {
    let split = "{\"jsonrpc\":\"2.0\",\"id\":1,\"re".to_owned() + "sult\":{\"text\":\"héllo\"}}\n";
    assert_eq!(decode(&split, 0).unwrap().kind, Kind::Response);
    for (stream, want) in [
        ("\n", "invalid JSON-RPC message: empty frame"),
        (
            "{}\r\n",
            "invalid JSON-RPC message: carriage return is not valid framing",
        ),
        ("{}", "invalid JSON-RPC message: unterminated frame"),
        (
            "42\n",
            "invalid JSON-RPC message: frame must be exactly one JSON object",
        ),
        (
            "{}\n{}\n",
            "invalid JSON-RPC message: jsonrpc must equal \"2.0\"",
        ),
        (
            &format!("{}\n", "a".repeat(16)),
            "invalid JSON-RPC message: frame must be exactly one JSON object",
        ),
    ] {
        let limit = if stream.starts_with('a') { 16 } else { 0 };
        assert_eq!(
            decode(stream, limit).unwrap_err(),
            format!("hermes rpc: {want}"),
            "{stream:?}"
        );
    }
    assert_eq!(
        decode(&format!("{}\n", "a".repeat(32)), 16).unwrap_err(),
        "hermes rpc: frame exceeds configured limit"
    );
}

#[test]
fn the_encoder_refuses_a_frame_over_its_limit_and_writes_nothing() {
    let mut written = Vec::new();
    let refused = Encoder::new(DIALECT, &mut written, 10)
        .encode(&Message::notification("long.method", None))
        .unwrap_err();
    assert_eq!(
        refused.to_string(),
        "hermes rpc: frame exceeds configured limit"
    );
    assert!(written.is_empty());
}
