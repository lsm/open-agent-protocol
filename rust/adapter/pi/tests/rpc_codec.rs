use std::io::Cursor;

use oap_adapter_pi::native::{Command, ExtensionUiResponse};
use oap_adapter_pi::rpc::{decode_frame, encode, read_frame, Error, Frame, Outbound};

fn decode(wire: &[u8], limit: usize) -> Result<Frame, Error> {
    let frame = read_frame(&mut Cursor::new(wire.to_vec()), limit)?;
    decode_frame(&frame)
}

#[test]
fn the_decoder_reads_each_frame_kind() {
    match decode(
        b"{\"id\":\"r\",\"type\":\"response\",\"command\":\"prompt\",\"success\":true}\n",
        0,
    )
    .unwrap()
    {
        Frame::Response(response) => assert_eq!(response.id, "r"),
        other => panic!("{other:?}"),
    }
    match decode(b"{\"id\":\"r\",\"type\":\"response\",\"command\":\"prompt\",\"success\":false,\"error\":\"no\"}\n", 0).unwrap() {
        Frame::Response(response) => assert_eq!(response.error, "no"),
        other => panic!("{other:?}"),
    }
    match decode(b"{\"type\":\"message_update\",\"usage\":{\"input\":1},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"x\"}}\n", 0).unwrap() {
        Frame::Event(event) => assert_eq!(event.kind, "message_update"),
        other => panic!("{other:?}"),
    }
    match decode(b"{\"type\":\"extension_ui_request\",\"id\":\"ui\",\"method\":\"confirm\",\"title\":\"T\",\"message\":\"M\"}\n", 0).unwrap() {
        Frame::ExtensionRequest(request) => assert_eq!(request.method, "confirm"),
        other => panic!("{other:?}"),
    }
    match decode(
        b"{\"type\":\"extension_error\",\"extensionPath\":\"x\",\"event\":\"y\",\"error\":\"z\"}\n",
        0,
    )
    .unwrap()
    {
        Frame::Event(event) => assert_eq!(event.kind, "extension_error"),
        other => panic!("{other:?}"),
    }
}

#[test]
fn the_decoder_refuses_invalid_frames() {
    for wire in [
        &b"\n"[..],
        b"{}\n",
        b"[]\n",
        b"{\"type\":\"unknown\"}\n",
        b"{\"type\":\"agent_start\",\"type\":\"agent_end\"}\n",
        b"{\"type\":\"response\",\"command\":\"prompt\",\"success\":false}\n",
        b"{\"type\":\"response\",\"command\":\"bogus\",\"success\":true}\n",
        b"{\"type\":\"extension_ui_request\",\"id\":\"\",\"method\":\"confirm\"}\n",
        b"{\"type\":\"agent_start\"}\r\n",
        b"{\"type\":\"agent_start\"}",
    ] {
        assert!(
            decode(wire, 0).is_err(),
            "accepted {:?}",
            String::from_utf8_lossy(wire)
        );
    }
}

#[test]
fn a_malformed_typed_event_is_an_invalid_frame() {
    for wire in [
        &b"{\"type\":\"tool_execution_end\"}\n"[..],
        b"{\"type\":\"agent_start\",\"unexpected\":true}\n",
        b"{\"type\":\"queue_update\",\"steering\":[]}\n",
    ] {
        let refused = decode(wire, 0).unwrap_err();
        assert!(matches!(refused, Error::InvalidFrame(_)), "{refused}");
        assert!(refused
            .to_string()
            .starts_with("pi rpc: invalid JSONL frame: "));
    }
}

#[test]
fn the_decoder_enforces_its_limit_and_utf8() {
    assert_eq!(
        decode(b"{\"type\":\"agent_start\"}\n", 5)
            .unwrap_err()
            .to_string(),
        "pi rpc: frame exceeds configured limit"
    );
    let mut bad = b"{\"type\":\"agent_start\",\"x\":\"".to_vec();
    bad.extend_from_slice(b"\xff\"}\n");
    assert!(matches!(decode(&bad, 0), Err(Error::InvalidFrame(_))));
}

#[test]
fn the_encoder_writes_one_lf_terminated_go_shaped_frame() {
    let command = Command {
        id: "x".into(),
        kind: "prompt".into(),
        message: Some("hello <b>".into()),
        ..Command::default()
    };
    assert_eq!(
        encode(&Outbound::Command(Box::new(command)), 0).unwrap(),
        b"{\"id\":\"x\",\"type\":\"prompt\",\"message\":\"hello \\u003cb\\u003e\"}\n"
    );
}

#[test]
fn the_encoder_refuses_an_invalid_message_and_an_oversize_frame() {
    assert_eq!(
        encode(&Outbound::Command(Box::new(Command::of("prompt"))), 0)
            .unwrap_err()
            .to_string(),
        "pi native: invalid message: prompt requires message"
    );
    let empty = ExtensionUiResponse {
        kind: "extension_ui_response".into(),
        id: "x".into(),
        ..ExtensionUiResponse::default()
    };
    assert!(encode(&Outbound::Response(empty), 0).is_err());
    assert_eq!(
        encode(&Outbound::Command(Box::new(Command::of("abort"))), 4)
            .unwrap_err()
            .to_string(),
        "pi rpc: frame exceeds configured limit"
    );
}
