use std::fmt;
use std::io::{BufRead, BufReader, ErrorKind, Read, Write};

pub const DEFAULT_FRAME_LIMIT: usize = 8 << 20;
const HEADER_LIMIT: usize = 1028;

#[derive(Debug)]
pub enum FrameError {
    Eof,
    Frame,
    Io(std::io::Error),
}

impl fmt::Display for FrameError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Eof => f.write_str("EOF"),
            Self::Frame => f.write_str("invalid Copilot Content-Length frame"),
            Self::Io(error) => error.fmt(f),
        }
    }
}

impl std::error::Error for FrameError {}

pub struct Decoder<R> {
    reader: BufReader<R>,
    limit: usize,
}

fn limit_or_default(limit: usize) -> usize {
    if limit == 0 {
        DEFAULT_FRAME_LIMIT
    } else {
        limit
    }
}

fn trim_spaces(bytes: &[u8]) -> &[u8] {
    let start = bytes
        .iter()
        .position(|byte| *byte != b' ')
        .unwrap_or(bytes.len());
    let end = bytes
        .iter()
        .rposition(|byte| *byte != b' ')
        .map_or(start, |at| at + 1);
    &bytes[start..end.max(start)]
}

impl<R: Read> Decoder<R> {
    pub fn new(reader: R, limit: usize) -> Self {
        Self {
            reader: BufReader::with_capacity(HEADER_LIMIT, reader),
            limit: limit_or_default(limit),
        }
    }

    fn line(&mut self, size: &mut usize) -> Result<Vec<u8>, FrameError> {
        let mut line = Vec::new();
        loop {
            let available = match self.reader.fill_buf() {
                Ok(available) => available,
                Err(error) if error.kind() == ErrorKind::Interrupted => continue,
                Err(_) => return Err(FrameError::Frame),
            };
            if available.is_empty() {
                if *size == 0 && line.is_empty() {
                    return Err(FrameError::Eof);
                }
                return Err(FrameError::Frame);
            }
            let newline = available.iter().position(|byte| *byte == b'\n');
            let take = newline.map_or(available.len(), |at| at + 1);
            if *size + line.len() + take > HEADER_LIMIT {
                return Err(FrameError::Frame);
            }
            line.extend_from_slice(&available[..take]);
            self.reader.consume(take);
            if newline.is_some() {
                *size += line.len();
                return Ok(line);
            }
        }
    }

    pub fn read(&mut self) -> Result<Vec<u8>, FrameError> {
        let mut size = 0;
        let mut length: Option<u64> = None;
        loop {
            let line = self.line(&mut size)?;
            let Some(content) = line.strip_suffix(b"\r\n") else {
                return Err(FrameError::Frame);
            };
            if content.is_empty() {
                break;
            }
            let colon = content
                .iter()
                .position(|byte| *byte == b':')
                .ok_or(FrameError::Frame)?;
            let (key, value) = (&content[..colon], &content[colon + 1..]);
            if !trim_spaces(key).eq_ignore_ascii_case(b"Content-Length") {
                continue;
            }
            if length.is_some() {
                return Err(FrameError::Frame);
            }
            let value = trim_spaces(value);
            if value.is_empty() || !value.iter().all(u8::is_ascii_digit) {
                return Err(FrameError::Frame);
            }
            let digits = std::str::from_utf8(value).map_err(|_| FrameError::Frame)?;
            length = Some(digits.parse().map_err(|_| FrameError::Frame)?);
        }
        let length = match length {
            Some(length) if length > 0 && length <= self.limit as u64 => length as usize,
            _ => return Err(FrameError::Frame),
        };
        let mut body = vec![0; length];
        self.reader
            .read_exact(&mut body)
            .map_err(|_| FrameError::Frame)?;
        if std::str::from_utf8(&body).is_err() {
            return Err(FrameError::Frame);
        }
        Ok(body)
    }
}

pub struct Encoder<W> {
    writer: W,
    limit: usize,
}

impl<W: Write> Encoder<W> {
    pub fn new(writer: W, limit: usize) -> Self {
        Self {
            writer,
            limit: limit_or_default(limit),
        }
    }

    pub fn write(&mut self, body: &[u8]) -> Result<(), FrameError> {
        if body.is_empty() || body.len() > self.limit || std::str::from_utf8(body).is_err() {
            return Err(FrameError::Frame);
        }
        let mut frame = format!("Content-Length: {}\r\n\r\n", body.len()).into_bytes();
        frame.extend_from_slice(body);
        let mut rest = frame.as_slice();
        while !rest.is_empty() {
            match self.writer.write(rest) {
                Ok(0) => return Err(FrameError::Io(ErrorKind::WriteZero.into())),
                Ok(written) => rest = &rest[written..],
                Err(error) if error.kind() == ErrorKind::Interrupted => {}
                Err(error) => return Err(FrameError::Io(error)),
            }
        }
        Ok(())
    }
}
