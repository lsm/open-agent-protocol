use std::collections::BTreeMap;

use serde_json::{value::RawValue, Value};

use crate::{codes, Diagnostic, Phase};

pub(crate) fn diagnostic(raw: &str, value: &Value, fixture: &str) -> Option<Diagnostic> {
    let kind = value.get("type")?.as_str()?;
    if !matches!(
        kind,
        "content.completed"
            | "run.completed"
            | "run.failed"
            | "run.cancelled"
            | "inference.completed"
            | "inference.failed"
    ) {
        return None;
    }
    let envelope: BTreeMap<String, &RawValue> = serde_json::from_str(raw).ok()?;
    let payload: BTreeMap<String, &RawValue> =
        serde_json::from_str(envelope.get("payload")?.get()).ok()?;
    let usage: BTreeMap<String, &RawValue> =
        serde_json::from_str(payload.get("usage")?.get()).ok()?;
    for field in [
        "input_tokens",
        "output_tokens",
        "total_tokens",
        "cache_read_input_tokens",
        "cache_write_input_tokens",
        "reasoning_tokens",
    ] {
        let Some(number) = usage.get(field) else {
            continue;
        };
        let token = number.get().as_bytes();
        if token == b"0"
            || matches!(token.first(), Some(b'1'..=b'9')) && token.iter().all(u8::is_ascii_digit)
        {
            continue;
        }
        let mut diagnostic = Diagnostic::new(
            fixture,
            Phase::Decode,
            codes::PAYLOAD_DECODE,
            "usage count must use canonical unsigned decimal spelling",
        );
        diagnostic.pointer = format!("/payload/usage/{field}");
        diagnostic.envelope_type = kind.to_owned();
        diagnostic.envelope_id = value
            .get("id")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_owned();
        return Some(diagnostic);
    }
    None
}
