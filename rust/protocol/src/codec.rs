use std::io::{Read, Write};

use crate::{Envelope, Error};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Format {
    Json,
    Array,
    Jsonl,
}

pub fn decode(mut reader: impl Read) -> Result<Vec<Envelope>, Error> {
    let mut data = Vec::new();
    reader.read_to_end(&mut data)?;
    let trimmed = data.trim_ascii();
    if trimmed.is_empty() {
        return Ok(Vec::new());
    }
    if trimmed[0] == b'[' {
        return decode_array(trimmed);
    }
    if let Ok(envelope) = Envelope::parse(trimmed) {
        return Ok(vec![envelope]);
    }
    decode_jsonl(trimmed)
}

pub fn decode_array(data: &[u8]) -> Result<Vec<Envelope>, Error> {
    serde_json::from_slice(data).map_err(Error::Array)
}

pub fn decode_jsonl(data: &[u8]) -> Result<Vec<Envelope>, Error> {
    let mut envelopes = Vec::new();
    for (index, line) in data.split(|b| *b == b'\n').enumerate() {
        let line_data = line.trim_ascii();
        if line_data.is_empty() {
            continue;
        }
        let envelope = Envelope::parse(line_data).map_err(|source| Error::Line {
            line: index + 1,
            source: Box::new(source),
        })?;
        envelopes.push(envelope);
    }
    Ok(envelopes)
}

pub fn encode(mut writer: impl Write, envelopes: &[Envelope], format: Format) -> Result<(), Error> {
    match format {
        Format::Json => {
            let [envelope] = envelopes else {
                return Err(Error::NotOneEnvelope(envelopes.len()));
            };
            serde_json::to_writer(&mut writer, envelope).map_err(Error::Encode)?;
            writer.write_all(b"\n")?;
        }
        Format::Array => {
            serde_json::to_writer(&mut writer, envelopes).map_err(Error::Encode)?;
            writer.write_all(b"\n")?;
        }
        Format::Jsonl => {
            for envelope in envelopes {
                serde_json::to_writer(&mut writer, envelope).map_err(Error::Encode)?;
                writer.write_all(b"\n")?;
            }
        }
    }
    Ok(())
}
