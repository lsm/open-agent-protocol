use std::io::{BufRead, ErrorKind};

pub const DEFAULT_FRAME_LIMIT: usize = 8 << 20;

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Error {
    FrameTooLarge,
    InvalidFrame(String),
    Eof,
    Io(String),
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::FrameTooLarge => {
                f.write_str("opencode httpapi: SSE event exceeds configured limit")
            }
            Self::InvalidFrame(detail) => {
                write!(f, "opencode httpapi: invalid SSE frame: {detail}")
            }
            Self::Eof => f.write_str("EOF"),
            Self::Io(text) => f.write_str(text),
        }
    }
}

impl std::error::Error for Error {}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SseEvent {
    pub name: String,
    pub id: String,
    pub data: String,
}

pub struct SseDecoder<R> {
    reader: R,
    limit: usize,
}

impl<R: BufRead> SseDecoder<R> {
    pub fn new(reader: R, limit: usize) -> Self {
        Self {
            reader,
            limit: if limit == 0 {
                DEFAULT_FRAME_LIMIT
            } else {
                limit
            },
        }
    }

    pub fn decode(&mut self) -> Result<SseEvent, Error> {
        let mut data = String::new();
        let mut event = SseEvent::default();
        let mut size = 0;
        loop {
            let line = self.read_line()?;
            size += line.len();
            if size > self.limit {
                return Err(Error::FrameTooLarge);
            }
            if line.is_empty() {
                if data.is_empty() && event.name.is_empty() && event.id.is_empty() {
                    continue;
                }
                if data.is_empty() {
                    return Err(Error::InvalidFrame("event without data".into()));
                }
                event.data = data;
                return Ok(event);
            }
            if line.starts_with(':') {
                continue;
            }
            let (name, value) = match line.split_once(':') {
                Some((name, value)) => (name, value.strip_prefix(' ').unwrap_or(value)),
                None => (line.as_str(), ""),
            };
            match name {
                "data" => {
                    if !data.is_empty() {
                        data.push('\n');
                    }
                    data.push_str(value);
                }
                "event" => {
                    if !event.name.is_empty() {
                        return Err(Error::InvalidFrame("duplicate event field".into()));
                    }
                    event.name = value.to_owned();
                }
                "id" => {
                    if !event.id.is_empty() {
                        return Err(Error::InvalidFrame("duplicate id field".into()));
                    }
                    if value.contains('\0') {
                        return Err(Error::InvalidFrame("id contains NUL".into()));
                    }
                    event.id = value.to_owned();
                }
                _ => {}
            }
        }
    }

    fn read_line(&mut self) -> Result<String, Error> {
        let mut line = Vec::with_capacity(1024);
        loop {
            let available = match self.reader.fill_buf() {
                Ok(available) => available,
                Err(error) if error.kind() == ErrorKind::Interrupted => continue,
                Err(error) => return Err(Error::Io(error.to_string())),
            };
            if available.is_empty() {
                if line.is_empty() {
                    return Err(Error::Eof);
                }
                return Err(Error::InvalidFrame("unterminated line".into()));
            }
            let newline = available.iter().position(|byte| *byte == b'\n');
            let take = newline.map_or(available.len(), |at| at + 1);
            if line.len() + take > self.limit + 2 {
                return Err(Error::FrameTooLarge);
            }
            line.extend_from_slice(&available[..take]);
            self.reader.consume(take);
            if newline.is_none() {
                continue;
            }
            line.pop();
            if line.last() == Some(&b'\r') {
                line.pop();
            }
            if line.contains(&b'\r') {
                return Err(Error::InvalidFrame("bare CR inside line".into()));
            }
            return String::from_utf8(line)
                .map_err(|_| Error::InvalidFrame("frame is not UTF-8".into()));
        }
    }
}
