use std::sync::OnceLock;

use jsonschema::{Draft, Retrieve, Uri, Validator};
use oap_validation::{SCHEMAS, SCHEMA_BASE};
use serde_json::Value;

struct Bundle;

fn document(name: &str) -> Result<Value, String> {
    let (_, text) = SCHEMAS
        .iter()
        .find(|(known, _)| *known == name)
        .ok_or_else(|| format!("embedded schema {name} is not in the bundle"))?;
    serde_json::from_str(text).map_err(|err| format!("decode embedded schema {name}: {err}"))
}

impl Retrieve for Bundle {
    fn retrieve(
        &self,
        uri: &Uri<String>,
    ) -> Result<Value, Box<dyn std::error::Error + Send + Sync>> {
        let name = uri.as_str().strip_prefix(SCHEMA_BASE).ok_or_else(|| {
            format!(
                "reference {:?} is outside the registered resources",
                uri.as_str()
            )
        })?;
        Ok(document(name)?)
    }
}

fn envelope() -> &'static Result<Validator, String> {
    static ENVELOPE: OnceLock<Result<Validator, String>> = OnceLock::new();
    ENVELOPE.get_or_init(|| {
        let name = "envelope.schema.json";
        let root = document(name)?;
        jsonschema::options()
            .with_draft(Draft::Draft202012)
            .with_base_uri(format!("{SCHEMA_BASE}{name}"))
            .with_retriever(Bundle)
            .should_validate_formats(false)
            .build(&root)
            .map_err(|err| format!("compile schema {name}: {err}"))
    })
}

pub(crate) fn envelope_validator() -> Result<&'static Validator, String> {
    envelope().as_ref().map_err(Clone::clone)
}
