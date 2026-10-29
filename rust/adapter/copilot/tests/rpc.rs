use std::io::{Cursor, Read, Write};

use oap_adapter_copilot::rpc::{Decoder, Encoder, FrameError};

struct Fragments(Cursor<Vec<u8>>);

impl Read for Fragments {
    fn read(&mut self, buffer: &mut [u8]) -> std::io::Result<usize> {
        let take = buffer.len().min(1);
        self.0.read(&mut buffer[..take])
    }
}

struct ShortWriter(Vec<u8>);

impl Write for ShortWriter {
    fn write(&mut self, data: &[u8]) -> std::io::Result<usize> {
        let take = data.len().min(2);
        self.0.extend_from_slice(&data[..take]);
        Ok(take)
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

struct StalledWriter;

impl Write for StalledWriter {
    fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
        Ok(0)
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn fragmented_and_coalesced_frames_round_trip_and_an_optional_header_is_skipped() {
    let body = "{\"text\":\"你好\"}".as_bytes();
    let mut written = ShortWriter(Vec::new());
    let mut encoder = Encoder::new(&mut written, 0);
    encoder.write(body).unwrap();
    encoder.write(b"{}").unwrap();
    let mut decoder = Decoder::new(Fragments(Cursor::new(written.0)), 0);
    assert_eq!(decoder.read().unwrap(), body);
    assert_eq!(decoder.read().unwrap(), b"{}");
    assert!(matches!(decoder.read(), Err(FrameError::Eof)));
    let mut typed = Decoder::new(
        Cursor::new(b"Content-Type: application/json\r\ncontent-length:  2\r\n\r\n{}".to_vec()),
        0,
    );
    assert_eq!(typed.read().unwrap(), b"{}");
}

#[test]
fn a_malformed_header_a_wrong_length_or_a_bad_body_is_refused() {
    let long_line = format!("{}\r\n\r\n", "A".repeat(1025));
    let many_headers = format!("{}Content-Length: 2\r\n\r\n{{}}", "X: y\r\n".repeat(180));
    let frames: Vec<&[u8]> = vec![
        b"Content-Length: 0\r\n\r\n",
        b"Content-Length: -1\r\n\r\nx",
        b"Content-Length: +1\r\n\r\nx",
        b"Content-Length: 1a\r\n\r\nx",
        b"Content-Length: 1\r\nContent-Length: 1\r\n\r\nx",
        b"Content-Length 1\r\n\r\nx",
        b"X: y\r\n\r\nx",
        b"Content-Length: 5\r\n\r\n{}",
        b"Content-Length: 1\n\nx",
        b"Content-Length: 1\r\n\r\n\xff",
        b"Content-Length: 999999999999999999999999\r\n\r\n",
        long_line.as_bytes(),
        many_headers.as_bytes(),
    ];
    for frame in frames {
        let read = Decoder::new(Cursor::new(frame.to_vec()), 4).read();
        assert!(
            matches!(read, Err(FrameError::Frame)),
            "{:?}: {read:?}",
            String::from_utf8_lossy(frame)
        );
    }
    let stalled = Encoder::new(StalledWriter, 0).write(b"{}");
    assert!(
        matches!(&stalled, Err(FrameError::Io(error)) if error.kind() == std::io::ErrorKind::WriteZero)
    );
    for body in [&b""[..], b"12345", b"\xff"] {
        assert!(matches!(
            Encoder::new(std::io::sink(), 4).write(body),
            Err(FrameError::Frame)
        ));
    }
}
