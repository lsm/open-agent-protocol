use oap_protocol::Envelope;
use serde_json::Value;

use crate::diagnostic::sort_diagnostics;
use crate::schema::{compile, SchemaError};
use crate::trace::{duplicate_key, parse_trace};
use crate::{codes, Diagnostic, Phase, ValidationResult};

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
            Err(diagnostic) => {
                return ValidationResult {
                    diagnostics: vec![*diagnostic],
                }
            }
        };
        let mut diagnostics = Vec::new();
        let mut envelopes = Vec::new();
        for (index, item) in items.iter().enumerate() {
            let value: Value = match serde_json::from_str(item.raw.get()) {
                Ok(value) => value,
                Err(err) => {
                    diagnostics.push(
                        Diagnostic::new(
                            fixture,
                            Phase::Decode,
                            codes::MALFORMED_JSON,
                            err.to_string(),
                        )
                        .at(index, item.line),
                    );
                    continue;
                }
            };
            if let Some(key) = duplicate_key(item.raw.get()) {
                diagnostics.push(
                    Diagnostic::new(
                        fixture,
                        Phase::Decode,
                        codes::DUPLICATE_KEY,
                        format!("duplicate object key {key:?}"),
                    )
                    .at(index, item.line),
                );
                continue;
            }
            if let Err(err) = self.core.validate(&value) {
                let mut diagnostic = Diagnostic::new(
                    fixture,
                    Phase::Schema,
                    codes::SCHEMA_INVALID,
                    err.to_string(),
                )
                .at(index, item.line);
                diagnostic.pointer = err.instance_path().to_string();
                diagnostics.push(diagnostic);
                continue;
            }
            match Envelope::parse(item.raw.get().as_bytes()) {
                Ok(envelope) => envelopes.push(envelope),
                Err(err) => diagnostics.push(
                    Diagnostic::new(
                        fixture,
                        Phase::Decode,
                        codes::MALFORMED_JSON,
                        err.to_string(),
                    )
                    .at(index, item.line),
                ),
            }
        }
        sort_diagnostics(&mut diagnostics);
        ValidationResult { diagnostics }
    }
}
