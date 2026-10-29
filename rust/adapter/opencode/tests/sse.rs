use std::io::Cursor;

use oap_adapter_opencode::sse::{Error, SseDecoder};

fn decoder(stream: &str, limit: usize) -> SseDecoder<Cursor<Vec<u8>>> {
    SseDecoder::new(Cursor::new(stream.as_bytes().to_vec()), limit)
}

fn first_failure(stream: &str) -> Error {
    let mut decoder = decoder(stream, 0);
    loop {
        if let Err(error) = decoder.decode() {
            return error;
        }
    }
}

#[test]
fn event_message_frames_decode_with_names_ids_and_joined_data() {
    let mut decoder = decoder(
        "event: message\ndata: {\"a\":1}\n\ndata: {\"b\":2}\n\n: heartbeat\n\nevent: message\nid: evt_1\ndata: line1\ndata: line2\n\n",
        0,
    );
    let first = decoder.decode().unwrap();
    assert_eq!(
        (first.name.as_str(), first.data.as_str()),
        ("message", "{\"a\":1}")
    );
    let second = decoder.decode().unwrap();
    assert_eq!(
        (second.name.as_str(), second.data.as_str()),
        ("", "{\"b\":2}")
    );
    let third = decoder.decode().unwrap();
    assert_eq!(
        (third.id.as_str(), third.data.as_str()),
        ("evt_1", "line1\nline2")
    );
    assert_eq!(decoder.decode(), Err(Error::Eof));
}

#[test]
fn crlf_line_endings_are_accepted() {
    let mut decoder = decoder("event: message\r\ndata: {}\r\n\r\n", 0);
    let event = decoder.decode().unwrap();
    assert_eq!(
        (event.name.as_str(), event.data.as_str()),
        ("message", "{}")
    );
    assert_eq!(decoder.decode(), Err(Error::Eof));
}

#[test]
fn malformed_framing_is_an_invalid_frame() {
    for (stream, detail) in [
        ("event: message\rdata: {}\n\n", "bare CR inside line"),
        ("event: message\n\n", "event without data"),
        ("event: a\nevent: b\ndata: {}\n\n", "duplicate event field"),
        ("id: 1\nid: 2\ndata: {}\n\n", "duplicate id field"),
        ("id: a\0b\ndata: {}\n\n", "id contains NUL"),
        ("data: {}", "unterminated line"),
    ] {
        assert_eq!(
            first_failure(stream),
            Error::InvalidFrame(detail.into()),
            "{stream:?}"
        );
    }
}

#[test]
fn unknown_fields_are_ignored() {
    let event = decoder("custom: 1\nretry: 5\ndata: {}\n\n", 0)
        .decode()
        .unwrap();
    assert_eq!(event.data, "{}");
}

#[test]
fn a_frame_over_the_limit_is_too_large() {
    let stream = format!("data: {{\"a\":\"{}\"}}\n\n", "x".repeat(64));
    assert_eq!(decoder(&stream, 16).decode(), Err(Error::FrameTooLarge));
}

#[test]
fn invalid_frames_name_the_opencode_transport() {
    assert_eq!(
        Error::InvalidFrame("event without data".into()).to_string(),
        "opencode httpapi: invalid SSE frame: event without data"
    );
    assert_eq!(
        Error::FrameTooLarge.to_string(),
        "opencode httpapi: SSE event exceeds configured limit"
    );
}
