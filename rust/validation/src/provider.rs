use std::collections::HashMap;

use serde::Deserialize;
use serde_json::{Map, Value};

use crate::diagnostic::sort_diagnostics;
use crate::json::same_json;
use crate::schema::{compile, SchemaError};
use crate::trace::{duplicate_key, parse_trace};
use crate::{codes, Diagnostic, Phase, ValidationResult};

const CREDENTIAL_HEADERS: [&str; 4] = [
    "authorization",
    "proxy-authorization",
    "x-api-key",
    "api-key",
];

const SCOPED: [&str; 6] = [
    "inference.started",
    "inference.part.started",
    "inference.part.delta",
    "inference.part.ended",
    "inference.completed",
    "inference.failed",
];

#[derive(Default, Deserialize)]
#[serde(default)]
struct Frame {
    #[serde(rename = "type")]
    kind: Option<String>,
    id: Option<String>,
    inference_id: Option<String>,
    sequence: Option<i64>,
    payload: Option<Value>,
}

#[derive(Clone, Default, Deserialize)]
#[serde(default)]
struct EndedCall {
    tool_call_id: String,
    name: String,
    arguments_json: Option<Value>,
}

#[derive(Clone, Default, Deserialize)]
#[serde(default)]
struct Ended {
    part_index: i64,
    part_kind: String,
    text: String,
    carry: String,
    tool_call: Option<EndedCall>,
}

struct Terminal {
    at: (usize, usize),
    id: String,
    kind: String,
    inference: String,
    content: Value,
}

fn member(part: &Map<String, Value>, name: &str) -> String {
    part.get(name)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

fn part_mismatch(
    ended: &Ended,
    part: &Map<String, Value>,
) -> Option<(String, String, &'static str)> {
    let kind = member(part, "type");
    if kind != ended.part_kind {
        return Some((
            ended.part_kind.clone(),
            kind,
            "the terminal's content part is not the kind the part that ended declared",
        ));
    }
    let carry = member(part, "carry");
    if carry != ended.carry {
        return Some((
            ended.carry.clone(),
            carry,
            "the terminal's content part does not carry the opaque value the part ended with",
        ));
    }
    match ended.part_kind.as_str() {
        "text" | "reasoning" => {
            let text = member(part, &ended.part_kind);
            (text != ended.text).then(|| {
                (
                    ended.text.clone(),
                    text,
                    "the terminal's text is not the accumulated string the part ended with",
                )
            })
        }
        "tool_call" => {
            let call = ended.tool_call.as_ref()?;
            let (id, name) = (member(part, "tool_call_id"), member(part, "name"));
            if id != call.tool_call_id || name != call.name {
                let expected = format!("{} {}", call.tool_call_id, call.name);
                return Some((
                    expected,
                    format!("{id} {name}"),
                    "the terminal's tool call is not the one the part ended with",
                ));
            }
            let arguments = part.get("arguments_json");
            let same = matches!((arguments, &call.arguments_json), (Some(a), Some(b)) if same_json(&a.to_string(), &b.to_string()));
            let shown = |value: Option<&Value>| value.map(Value::to_string).unwrap_or_default();
            (!same).then(|| {
                (
                    shown(call.arguments_json.as_ref()),
                    shown(arguments),
                    "the terminal's tool call arguments are not the ones the part ended with",
                )
            })
        }
        _ => None,
    }
}

fn assembly_defect(fixture: &str, terminal: &Terminal, ended: &[Ended]) -> Option<Diagnostic> {
    if ended.is_empty() {
        return None;
    }
    let mut ordered = ended.to_vec();
    ordered.sort_by_key(|part| part.part_index);
    let defect = |pointer: String, expected: String, actual: String, message: &str| {
        let mut diagnostic = Diagnostic::new(
            fixture,
            Phase::Semantic,
            codes::TERMINAL_NOT_ASSEMBLY,
            message,
        )
        .at(terminal.at.0, terminal.at.1);
        diagnostic.envelope_id = terminal.id.clone();
        diagnostic.envelope_type = terminal.kind.clone();
        diagnostic.pointer = pointer;
        diagnostic.expected = expected;
        diagnostic.actual = actual;
        diagnostic
    };
    let pointer = "/payload/message/content".to_owned();
    let Some(parts) = terminal.content.as_array() else {
        return Some(defect(pointer, format!("{} content parts assembled from the ended parts", ordered.len()), "content that is not a part array".to_owned(), "an inference that streamed parts settled with a terminal whose content is not their assembly"));
    };
    if parts.len() != ordered.len() {
        return Some(defect(
            pointer,
            format!("{} content parts", ordered.len()),
            parts.len().to_string(),
            "the terminal message does not carry one content part per ended part",
        ));
    }
    ordered
        .iter()
        .zip(parts)
        .enumerate()
        .find_map(|(index, (ended, part))| {
            let part = part.as_object()?;
            part_mismatch(ended, part).map(|(expected, actual, message)| {
                defect(format!("{pointer}/{index}"), expected, actual, message)
            })
        })
}

pub struct ProviderValidator {
    schema: jsonschema::Validator,
}

fn escape(token: &str) -> String {
    token.replace('~', "~0").replace('/', "~1")
}

fn bearer_shaped(value: &str) -> bool {
    value
        .trim_start_matches([' ', '\t'])
        .to_lowercase()
        .starts_with("bearer ")
}

fn collect_headers(headers: &Map<String, Value>, pointer: &str, found: &mut Vec<(String, String)>) {
    for (name, value) in headers {
        let text = value.as_str().unwrap_or_default();
        if CREDENTIAL_HEADERS.contains(&name.to_lowercase().as_str()) || bearer_shaped(text) {
            found.push((format!("{pointer}/{}", escape(name)), name.clone()));
        }
    }
}

fn credential_headers(kind: &str, payload: &Value) -> Vec<(String, String)> {
    let mut found = Vec::new();
    match kind {
        "inference.create.request" => {
            if let Some(headers) = payload.get("headers").and_then(Value::as_object) {
                collect_headers(headers, "/payload/headers", &mut found);
            }
        }
        "provider.describe.response" => {
            let providers = payload.get("providers").and_then(Value::as_array);
            for (index, descriptor) in providers.into_iter().flatten().enumerate() {
                if let Some(headers) = descriptor.get("headers").and_then(Value::as_object) {
                    collect_headers(
                        headers,
                        &format!("/payload/providers/{index}/headers"),
                        &mut found,
                    );
                }
            }
        }
        _ => {}
    }
    found.sort_by(|a, b| a.0.cmp(&b.0));
    found
}

fn semantic(
    fixture: &str,
    at: (usize, usize),
    frame: &Frame,
    code: &str,
    pointer: &str,
    message: &str,
) -> Diagnostic {
    let mut diagnostic = Diagnostic::new(fixture, Phase::Semantic, code, message).at(at.0, at.1);
    diagnostic.envelope_id = frame.id.clone().unwrap_or_default();
    diagnostic.envelope_type = frame.kind.clone().unwrap_or_default();
    diagnostic.pointer = pointer.to_owned();
    diagnostic
}

impl ProviderValidator {
    pub fn new() -> Result<Self, SchemaError> {
        Ok(Self {
            schema: compile("provider-envelope.schema.json")?,
        })
    }

    fn schema_error(&self, value: &Value) -> Option<String> {
        self.schema.validate(value).err().map(|err| err.to_string())
    }

    pub fn validate(&self, data: &[u8], fixture: &str) -> ValidationResult {
        let items = match parse_trace(data, fixture) {
            Ok(items) => items,
            Err(diagnostic) => {
                return ValidationResult {
                    diagnostics: vec![*diagnostic],
                }
            }
        };
        let mut diagnostics = Vec::new();
        let mut sequences: HashMap<String, i64> = HashMap::new();
        let mut settled: HashMap<String, String> = HashMap::new();
        let mut parts: HashMap<String, Vec<Ended>> = HashMap::new();
        let mut terminals: Vec<Terminal> = Vec::new();
        for (index, item) in items.iter().enumerate() {
            let at = (index, item.line);
            let raw = item.raw.get();
            let value: Value = match serde_json::from_str(raw) {
                Ok(value) => value,
                Err(err) => {
                    diagnostics.push(
                        Diagnostic::new(
                            fixture,
                            Phase::Decode,
                            codes::MALFORMED_JSON,
                            err.to_string(),
                        )
                        .at(at.0, at.1),
                    );
                    continue;
                }
            };
            if let Some(key) = duplicate_key(raw) {
                let message = format!("duplicate object key {key:?}");
                diagnostics.push(
                    Diagnostic::new(fixture, Phase::Decode, codes::DUPLICATE_KEY, message)
                        .at(at.0, at.1),
                );
                continue;
            }
            let frame: Frame = match serde_json::from_value(value.clone()) {
                Ok(frame) => frame,
                Err(err) => {
                    let diagnostic = match self.schema_error(&value) {
                        Some(message) => {
                            Diagnostic::new(fixture, Phase::Schema, codes::SCHEMA_INVALID, message)
                        }
                        None => Diagnostic::new(
                            fixture,
                            Phase::Decode,
                            codes::PAYLOAD_DECODE,
                            err.to_string(),
                        ),
                    };
                    diagnostics.push(diagnostic.at(at.0, at.1));
                    continue;
                }
            };
            let kind = frame.kind.clone().unwrap_or_default();
            let payload = frame.payload.clone().unwrap_or(Value::Null);
            if kind == "provider.credential.grant.request" && payload.get("value").is_some() {
                let mut diagnostic = semantic(fixture, at, &frame, codes::CREDENTIAL_IN_TRACE, "/payload/value", "the on-envelope credential tier is never journalled, traced or replayed, so a trace carrying one was assembled from a stream that recorded a secret");
                diagnostic.expected = "a grant that names a nonce and carries no value".to_owned();
                diagnostic.actual = "a grant carrying a credential value".to_owned();
                diagnostics.push(diagnostic);
                continue;
            }
            for (pointer, name) in credential_headers(&kind, &payload) {
                let mut diagnostic = semantic(fixture, at, &frame, codes::CREDENTIAL_IN_HEADERS, &pointer, "a credential must not travel in a header; a caller uses provider.credential.grant.request and an implementation resolves its own from configuration");
                diagnostic.expected = "a header carrying tenancy or routing metadata".to_owned();
                diagnostic.actual = name;
                diagnostics.push(diagnostic);
            }
            if let Some(message) = self.schema_error(&value) {
                let mut diagnostic =
                    Diagnostic::new(fixture, Phase::Schema, codes::SCHEMA_INVALID, message)
                        .at(at.0, at.1);
                diagnostic.envelope_id = frame.id.clone().unwrap_or_default();
                diagnostic.envelope_type = kind.clone();
                diagnostics.push(diagnostic);
                continue;
            }
            if let Some(diagnostic) = crate::usage::diagnostic(raw, &value, fixture) {
                diagnostics.push(diagnostic.at(at.0, at.1));
                continue;
            }
            let inference = frame.inference_id.clone().unwrap_or_default();
            if inference.is_empty() || !SCOPED.contains(&kind.as_str()) {
                continue;
            }
            let sequence = frame.sequence.unwrap_or(0);
            let last = sequences.get(&inference).copied();
            let finding = match last {
                None if sequence != 1 => Some((codes::SEQUENCE_GAP, "1".to_owned(), "the first scoped event of an inference does not open its sequence at 1")),
                Some(last) if sequence <= last => Some((codes::SEQUENCE_REGRESSION, (last + 1).to_string(), "a scoped event repeats or goes back on the sequence its inference had reached")),
                Some(last) if sequence != last + 1 => Some((codes::SEQUENCE_GAP, (last + 1).to_string(), "an inference skipped a sequence, so a consumer cannot tell a gap from a lost frame")),
                _ => None,
            };
            if let Some((code, expected, message)) = finding {
                let mut diagnostic = semantic(fixture, at, &frame, code, "/sequence", message);
                diagnostic.expected = expected;
                diagnostic.actual = sequence.to_string();
                diagnostics.push(diagnostic);
            }
            if last.is_none_or(|last| sequence > last) {
                sequences.insert(inference.clone(), sequence);
            }
            if let Some(terminal) = settled.get(&inference) {
                let mut diagnostic = semantic(
                    fixture,
                    at,
                    &frame,
                    codes::EVENT_AFTER_TERMINAL,
                    "/type",
                    "an inference emitted a scoped event after it had already settled",
                );
                diagnostic.expected = "no scoped event after a terminal".to_owned();
                diagnostic.actual = kind.clone();
                diagnostic.related_ids = vec![terminal.clone()];
                diagnostics.push(diagnostic);
            }
            let id = frame.id.clone().unwrap_or_default();
            match kind.as_str() {
                "inference.part.ended" => match serde_json::from_value::<Ended>(payload) {
                    Ok(ended) => parts.entry(inference).or_default().push(ended),
                    Err(err) => {
                        let mut diagnostic = Diagnostic::new(
                            fixture,
                            Phase::Decode,
                            codes::PAYLOAD_DECODE,
                            err.to_string(),
                        )
                        .at(at.0, at.1);
                        diagnostic.envelope_id = id;
                        diagnostic.envelope_type = kind;
                        diagnostics.push(diagnostic);
                    }
                },
                "inference.completed" => {
                    let content = payload
                        .get("message")
                        .and_then(|message| message.get("content"))
                        .cloned()
                        .unwrap_or(Value::Null);
                    terminals.push(Terminal {
                        at,
                        id: id.clone(),
                        kind,
                        inference: inference.clone(),
                        content,
                    });
                    settled.insert(inference, id);
                }
                "inference.failed" => {
                    settled.insert(inference, id);
                }
                _ => {}
            }
        }
        for terminal in &terminals {
            let ended = parts
                .get(&terminal.inference)
                .map(Vec::as_slice)
                .unwrap_or_default();
            if let Some(diagnostic) = assembly_defect(fixture, terminal, ended) {
                diagnostics.push(diagnostic);
            }
        }
        sort_diagnostics(&mut diagnostics);
        ValidationResult { diagnostics }
    }
}

#[cfg(test)]
mod tests {
    use serde_json::{json, Value};

    use super::{part_mismatch, Ended, EndedCall};

    fn call(arguments: Value) -> Ended {
        Ended {
            part_kind: "tool_call".into(),
            tool_call: Some(EndedCall {
                tool_call_id: "t".into(),
                name: "x".into(),
                arguments_json: Some(arguments),
            }),
            ..Ended::default()
        }
    }

    fn terminal(arguments: Value) -> serde_json::Map<String, Value> {
        json!({"type": "tool_call", "tool_call_id": "t", "name": "x", "arguments_json": arguments})
            .as_object()
            .unwrap()
            .clone()
    }

    #[test]
    fn tool_call_arguments_are_compared_by_value() {
        let ended = call(serde_json::from_str(r#"{"n":1,"m":[2]}"#).unwrap());
        let same = terminal(serde_json::from_str(r#"{"m":[2.0],"n":1.0}"#).unwrap());
        assert!(part_mismatch(&ended, &same).is_none());
        let different = terminal(json!({"n": 2, "m": [2]}));
        assert!(part_mismatch(&ended, &different).is_some());
    }
}
