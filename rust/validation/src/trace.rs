use std::collections::HashSet;
use std::fmt;

use serde::de::{Deserialize, Deserializer, MapAccess, SeqAccess, Visitor};
use serde_json::value::RawValue;

use crate::{codes, Diagnostic, Phase};

pub(crate) struct RawEnvelope {
    pub raw: Box<RawValue>,
    pub line: usize,
}

fn malformed(fixture: &str, message: impl Into<String>) -> Diagnostic {
    Diagnostic::new(fixture, Phase::Decode, codes::MALFORMED_JSON, message)
}

pub(crate) fn parse_trace(data: &[u8], fixture: &str) -> Result<Vec<RawEnvelope>, Box<Diagnostic>> {
    let text = String::from_utf8_lossy(data);
    let data = text.as_bytes().trim_ascii();
    if data.is_empty() {
        return Ok(Vec::new());
    }
    if data[0] == b'[' {
        let items: Vec<Box<RawValue>> = serde_json::from_slice(data)
            .map_err(|err| Box::new(malformed(fixture, err.to_string())))?;
        return Ok(items
            .into_iter()
            .map(|raw| RawEnvelope { raw, line: 0 })
            .collect());
    }
    if let Ok(raw) = serde_json::from_slice::<Box<RawValue>>(data) {
        return Ok(vec![RawEnvelope { raw, line: 1 }]);
    }
    let mut out = Vec::new();
    for (index, line) in data.split(|b| *b == b'\n').enumerate() {
        let line_data = line.trim_ascii();
        if line_data.is_empty() {
            continue;
        }
        let number = index + 1;
        let raw = serde_json::from_slice::<Box<RawValue>>(line_data).map_err(|err| {
            Box::new(malformed(fixture, format!("line {number}: {err}")).at(out.len(), number))
        })?;
        out.push(RawEnvelope { raw, line: number });
    }
    Ok(out)
}

struct Scan(Option<String>);

impl<'de> Deserialize<'de> for Scan {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        deserializer.deserialize_any(ScanVisitor)
    }
}

struct ScanVisitor;

impl<'de> Visitor<'de> for ScanVisitor {
    type Value = Scan;

    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("any JSON value")
    }

    fn visit_bool<E>(self, _: bool) -> Result<Scan, E> {
        Ok(Scan(None))
    }

    fn visit_i64<E>(self, _: i64) -> Result<Scan, E> {
        Ok(Scan(None))
    }

    fn visit_u64<E>(self, _: u64) -> Result<Scan, E> {
        Ok(Scan(None))
    }

    fn visit_f64<E>(self, _: f64) -> Result<Scan, E> {
        Ok(Scan(None))
    }

    fn visit_str<E>(self, _: &str) -> Result<Scan, E> {
        Ok(Scan(None))
    }

    fn visit_unit<E>(self) -> Result<Scan, E> {
        Ok(Scan(None))
    }

    fn visit_seq<A: SeqAccess<'de>>(self, mut seq: A) -> Result<Scan, A::Error> {
        let mut found = None;
        while let Some(Scan(nested)) = seq.next_element()? {
            found = found.or(nested);
        }
        Ok(Scan(found))
    }

    fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Scan, A::Error> {
        let mut seen = HashSet::new();
        let mut found = None;
        while let Some(key) = map.next_key::<String>()? {
            if !seen.insert(key.clone()) && found.is_none() {
                found = Some(key);
            }
            let Scan(nested) = map.next_value()?;
            found = found.or(nested);
        }
        Ok(Scan(found))
    }
}

pub fn duplicate_key(raw: &str) -> Option<String> {
    serde_json::from_str::<Scan>(raw)
        .ok()
        .and_then(|Scan(found)| found)
}
