use std::collections::HashMap;

use serde::Deserialize;
use serde_json::{Map, Value};

use crate::diagnostic::sort_diagnostics;
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
            if kind == "inference.completed" || kind == "inference.failed" {
                settled.insert(inference, frame.id.clone().unwrap_or_default());
            }
        }
        sort_diagnostics(&mut diagnostics);
        ValidationResult { diagnostics }
    }
}
