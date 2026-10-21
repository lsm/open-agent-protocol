use std::collections::BTreeMap;
use std::sync::OnceLock;

use oap_protocol::Envelope;
use serde_json::Value;

use crate::diagnostic::sort_diagnostics;
use crate::schema::{compile, SchemaError, SCHEMAS};
use crate::state::State;
use crate::trace::{duplicate_key, parse_trace};
use crate::{codes, Diagnostic, Phase, ValidationResult};

const AUTH_REPLY: &str = "auth.login.reply.request";
const COMBINATORS: [&str; 3] = ["oneOf", "anyOf", "allOf"];

pub struct Validator {
    core: jsonschema::Validator,
}

impl Validator {
    pub fn new() -> Result<Self, SchemaError> {
        Ok(Self {
            core: compile("envelope.schema.json")?,
        })
    }

    pub fn validate(&self, data: &[u8], fixture: &str) -> ValidationResult {
        let items = match parse_trace(data, fixture) {
            Ok(items) => items,
            Err(mut diagnostic) => {
                if contains_auth_reply(&String::from_utf8_lossy(data)) {
                    diagnostic.message =
                        "malformed auth reply trace (sensitive answer redacted)".to_owned();
                }
                return ValidationResult {
                    diagnostics: vec![*diagnostic],
                };
            }
        };
        let mut diagnostics = Vec::new();
        let mut envelopes = Vec::new();
        for (index, item) in items.iter().enumerate() {
            let raw = item.raw.get();
            let sensitive = contains_auth_reply(raw);
            let redacted = |plain: String, redaction: &str| {
                if sensitive {
                    redaction.to_owned()
                } else {
                    plain
                }
            };
            let value: Value = match serde_json::from_str(raw) {
                Ok(value) => value,
                Err(err) => {
                    let message = redacted(
                        err.to_string(),
                        "malformed auth reply (sensitive answer redacted)",
                    );
                    diagnostics.push(
                        Diagnostic::new(fixture, Phase::Decode, codes::MALFORMED_JSON, message)
                            .at(index, item.line),
                    );
                    continue;
                }
            };
            if let Some(key) = duplicate_key(raw) {
                let message = redacted(
                    format!("duplicate object key {key:?}"),
                    "duplicate key in auth reply (sensitive answer redacted)",
                );
                diagnostics.push(
                    Diagnostic::new(fixture, Phase::Decode, codes::DUPLICATE_KEY, message)
                        .at(index, item.line),
                );
                continue;
            }
            if let Some(mut diagnostic) = self.schema_diagnostic(&value, fixture) {
                diagnostic = diagnostic.at(index, item.line);
                if sensitive {
                    diagnostic.message =
                        "invalid auth reply (sensitive answer redacted)".to_owned();
                    diagnostic.expected.clear();
                    diagnostic.actual.clear();
                }
                diagnostics.push(diagnostic);
                continue;
            }
            if let Some(diagnostic) = crate::usage::diagnostic(raw, &value, fixture) {
                diagnostics.push(diagnostic.at(index, item.line));
                continue;
            }
            match Envelope::parse(raw.as_bytes()) {
                Ok(envelope) => envelopes.push(envelope),
                Err(err) => {
                    let message = redacted(
                        err.to_string(),
                        "malformed auth reply envelope (sensitive answer redacted)",
                    );
                    diagnostics.push(
                        Diagnostic::new(fixture, Phase::Decode, codes::MALFORMED_JSON, message)
                            .at(index, item.line),
                    );
                }
            }
        }
        if diagnostics.is_empty() {
            let mut state = State::new(fixture);
            for (index, envelope) in envelopes.iter().enumerate() {
                state.apply(index, items[index].line, envelope);
            }
            state.close();
            diagnostics = state.diagnostics;
        }
        sort_diagnostics(&mut diagnostics);
        ValidationResult { diagnostics }
    }

    fn schema_diagnostic(&self, value: &Value, fixture: &str) -> Option<Diagnostic> {
        if self.core.is_valid(value) {
            return None;
        }
        let list = serde_json::to_value(self.core.evaluate(value).list()).unwrap_or_default();
        let details: Vec<&Value> = list
            .get("details")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter(|detail| detail.get("errors").is_some())
            .collect();
        let paths: Vec<&str> = details
            .iter()
            .map(|detail| {
                detail
                    .get("evaluationPath")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
            })
            .collect();
        let mut leaves: Vec<(Option<&'static str>, String, String)> = Vec::new();
        for (detail, path) in details.iter().zip(&paths) {
            let Some(errors) = detail.get("errors").and_then(Value::as_object) else {
                continue;
            };
            let pointer = detail
                .get("instanceLocation")
                .and_then(Value::as_str)
                .unwrap_or_default();
            let explained = paths
                .iter()
                .any(|other| other.len() > path.len() && other.starts_with(&format!("{path}/")));
            for (keyword, messages) in errors {
                if explained && COMBINATORS.contains(&keyword.as_str()) {
                    continue;
                }
                let messages = match messages {
                    Value::Array(items) => items
                        .iter()
                        .filter_map(Value::as_str)
                        .map(str::to_owned)
                        .collect(),
                    Value::String(message) => vec![message.clone()],
                    other => vec![other.to_string()],
                };
                for message in messages {
                    leaves.push((branch_of(path), pointer.to_owned(), message));
                }
            }
        }
        let declared = value
            .get("type")
            .and_then(Value::as_str)
            .unwrap_or_default();
        if let Some(want) = envelope_branches().get(declared) {
            let preferred: Vec<_> = leaves
                .iter()
                .filter(|(branch, _, _)| branch.is_none_or(|b| b == want.as_str()))
                .cloned()
                .collect();
            if !preferred.is_empty() {
                leaves = preferred;
            }
        }
        let (pointer, message) = leaves
            .into_iter()
            .map(|(_, pointer, message)| (pointer, message))
            .min()
            .unwrap_or_else(|| {
                (
                    String::new(),
                    "the envelope does not match its schema".to_owned(),
                )
            });
        let mut diagnostic =
            Diagnostic::new(fixture, Phase::Schema, codes::SCHEMA_INVALID, message);
        diagnostic.pointer = pointer;
        Some(diagnostic)
    }
}

fn contains_auth_reply(raw: &str) -> bool {
    raw.contains(AUTH_REPLY)
        || serde_json::from_str::<Value>(raw)
            .is_ok_and(|value| value.get("type").and_then(Value::as_str) == Some(AUTH_REPLY))
}

struct Branches {
    by_type: BTreeMap<String, String>,
    by_index: Vec<Option<String>>,
}

fn branches() -> &'static Branches {
    static BRANCHES: OnceLock<Branches> = OnceLock::new();
    BRANCHES.get_or_init(|| {
        let text = SCHEMAS
            .iter()
            .find(|(name, _)| *name == "envelope.schema.json")
            .map(|(_, text)| *text);
        let root: Value = text
            .and_then(|text| serde_json::from_str(text).ok())
            .unwrap_or_default();
        let mut by_type = BTreeMap::new();
        if let Some(defs) = root.get("$defs").and_then(Value::as_object) {
            for (name, def) in defs {
                if let Some(constant) = def
                    .pointer("/properties/type/const")
                    .and_then(Value::as_str)
                {
                    by_type.insert(constant.to_owned(), name.clone());
                }
            }
        }
        let by_index = root
            .get("oneOf")
            .and_then(Value::as_array)
            .map(|members| {
                members
                    .iter()
                    .map(|member| {
                        let name = member.get("$ref")?.as_str()?.strip_prefix("#/$defs/")?;
                        by_type
                            .values()
                            .find(|known| known.as_str() == name)
                            .cloned()
                    })
                    .collect()
            })
            .unwrap_or_default();
        Branches { by_type, by_index }
    })
}

fn envelope_branches() -> &'static BTreeMap<String, String> {
    &branches().by_type
}

fn branch_of(schema_location: &str) -> Option<&'static str> {
    let rest = schema_location.strip_prefix("/oneOf/")?;
    let index: usize = rest.split('/').next()?.parse().ok()?;
    branches().by_index.get(index)?.as_deref()
}
