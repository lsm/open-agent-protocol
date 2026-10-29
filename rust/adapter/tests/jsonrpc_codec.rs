use std::io::{Cursor, Write};

use oap_adapter::jsonrpc::{Decoder, Dialect, Encoder, Fault, Kind, Message};

const STRICT: Dialect = Dialect::strict("deepseek");
const LENIENT: Dialect = Dialect::lenient("acp");

fn decode(dialect: Dialect, input: &[u8], limit: usize) -> Result<Message, String> {
    Decoder::new(dialect, Cursor::new(input.to_vec()), limit)
        .decode()
        .map_err(|error| error.to_string())
}

#[test]
fn a_strict_decoder_takes_one_lf_terminated_object_per_frame() {
    let message = decode(
        STRICT,
        b"{\"jsonrpc\":\"2.0\",\"method\":\"session.status\",\"params\":{\"sessionId\":\"s\",\"status\":\"idle\"}}\n",
        0,
    )
    .unwrap();
    assert_eq!(message.kind, Kind::Notification);
    for raw in [
        &b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\r\n"[..],
        b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"}",
        b"{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"method\":\"y\"}\n",
        b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"} garbage\n",
        b" {\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n",
        b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"} \n",
        b"[]\n",
        b"{\"jsonrpc\":\"2.0\",\"method\":\"\xff\"}\n",
    ] {
        assert!(
            decode(STRICT, raw, 0).is_err(),
            "{}",
            String::from_utf8_lossy(raw)
        );
    }
}

#[test]
fn a_frame_with_a_carriage_return_an_empty_line_an_unterminated_tail_or_bad_utf8_is_invalid() {
    for (raw, detail) in [
        (
            &b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\r\n"[..],
            "carriage return is not valid framing",
        ),
        (b"\n{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n", "empty frame"),
        (
            b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"",
            "unterminated frame",
        ),
        (
            b"{\"jsonrpc\":\"2.0\",\"method\":\"\xff\xfe\"}\n",
            "frame is not UTF-8",
        ),
    ] {
        let refused = decode(LENIENT, raw, 0).unwrap_err();
        assert!(refused.contains(detail), "{refused}");
    }
}

#[test]
fn the_frame_limit_counts_bytes_before_the_newline() {
    let frame = r#"{"jsonrpc":"2.0","method":"x"}"#;
    let line = format!("{frame}\n");
    assert!(decode(STRICT, line.as_bytes(), frame.len()).is_ok());
    let error = Decoder::new(STRICT, Cursor::new(format!("{frame} \n")), frame.len())
        .decode()
        .unwrap_err();
    assert!(matches!(error.fault, Fault::FrameTooLarge));
    assert_eq!(
        error.to_string(),
        "deepseek rpc: frame exceeds configured limit"
    );
}

#[test]
fn a_decoder_serves_consecutive_frames_and_then_end_of_stream() {
    let mut decoder = Decoder::new(
        LENIENT,
        Cursor::new(
            b"{\"jsonrpc\":\"2.0\",\"method\":\"a\"}\n{\"jsonrpc\":\"2.0\",\"method\":\"b\"}\n"
                .to_vec(),
        ),
        0,
    );
    assert_eq!(decoder.decode().unwrap().method, "a");
    assert_eq!(decoder.decode().unwrap().method, "b");
    let end = decoder.decode().unwrap_err();
    assert!(matches!(&end.fault, Fault::Io(io) if io.kind() == std::io::ErrorKind::UnexpectedEof));
}

struct ShortWriter {
    written: Vec<u8>,
    max: usize,
}

impl Write for ShortWriter {
    fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
        let take = data.len().min(self.max);
        self.written.extend_from_slice(&data[..take]);
        Ok(take)
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn an_encoder_writes_one_lf_terminated_frame_through_partial_writes() {
    let mut writer = ShortWriter {
        written: Vec::new(),
        max: 2,
    };
    Encoder::new(STRICT, &mut writer, 0)
        .encode(&Message::notification("x", None))
        .unwrap();
    assert_eq!(writer.written, b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n");
}

#[test]
fn a_strict_encoder_refuses_an_oversize_frame_without_writing_and_a_lenient_one_writes_it() {
    let mut out = Vec::new();
    let error = Encoder::new(STRICT, &mut out, 4)
        .encode(&Message::notification("x", None))
        .unwrap_err();
    assert!(matches!(error.fault, Fault::FrameTooLarge));
    assert!(out.is_empty());
    Encoder::new(LENIENT, &mut out, 4)
        .encode(&Message::notification("x", None))
        .unwrap();
    assert_eq!(out, b"{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n");
}
