use std::io::{BufRead, ErrorKind, Write};

use super::{Dialect, Error, Fault, Message};

pub const DEFAULT_FRAME_LIMIT: usize = 8 << 20;

pub struct Decoder<R> {
    dialect: Dialect,
    reader: R,
    limit: usize,
}

fn limit_or_default(limit: usize) -> usize {
    if limit == 0 {
        DEFAULT_FRAME_LIMIT
    } else {
        limit
    }
}

impl<R: BufRead> Decoder<R> {
    pub fn new(dialect: Dialect, reader: R, limit: usize) -> Self {
        Self {
            dialect,
            reader,
            limit: limit_or_default(limit),
        }
    }

    pub fn decode(&mut self) -> Result<Message, Error> {
        let frame = self
            .read_frame()
            .map_err(|fault| self.dialect.error(fault))?;
        Message::parse(&self.dialect, &frame)
    }

    fn read_frame(&mut self) -> Result<Vec<u8>, Fault> {
        let invalid = |detail: &str| Fault::InvalidMessage(detail.to_owned());
        let mut frame = Vec::with_capacity(self.limit.min(4096));
        loop {
            let available = match self.reader.fill_buf() {
                Ok(available) => available,
                Err(error) if error.kind() == ErrorKind::Interrupted => continue,
                Err(error) => return Err(Fault::Io(error)),
            };
            if available.is_empty() {
                if frame.is_empty() {
                    return Err(Fault::Io(ErrorKind::UnexpectedEof.into()));
                }
                return Err(invalid("unterminated frame"));
            }
            let newline = available.iter().position(|byte| *byte == b'\n');
            let take = newline.map_or(available.len(), |at| at + 1);
            if frame.len() + take > self.limit + 1 {
                return Err(Fault::FrameTooLarge);
            }
            frame.extend_from_slice(&available[..take]);
            self.reader.consume(take);
            if newline.is_none() {
                continue;
            }
            frame.pop();
            if frame.contains(&b'\r') {
                return Err(invalid("carriage return is not valid framing"));
            }
            if frame.is_empty() {
                return Err(invalid("empty frame"));
            }
            if std::str::from_utf8(&frame).is_err() {
                return Err(invalid("frame is not UTF-8"));
            }
            return Ok(frame);
        }
    }
}

pub struct Encoder<W> {
    dialect: Dialect,
    writer: W,
    limit: usize,
}

impl<W: Write> Encoder<W> {
    pub fn new(dialect: Dialect, writer: W, limit: usize) -> Self {
        Self {
            dialect,
            writer,
            limit: limit_or_default(limit),
        }
    }

    pub fn encode(&mut self, message: &Message) -> Result<(), Error> {
        let mut data = message.encode(&self.dialect)?.into_bytes();
        if self.dialect.limit_writes && data.len() > self.limit {
            return Err(self.dialect.error(Fault::FrameTooLarge));
        }
        data.push(b'\n');
        self.writer
            .write_all(&data)
            .map_err(|error| self.dialect.error(Fault::Io(error)))
    }
}
