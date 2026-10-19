use jsonschema::{Draft, Retrieve, Uri, Validator};
use serde_json::Value;

pub const SCHEMA_BASE: &str = "https://open-agent-protocol.local/v0.1/";

pub const SCHEMAS: [(&str, &str); 13] = [
    (
        "action.schema.json",
        include_str!("../../../schema/v0.1/action.schema.json"),
    ),
    (
        "auth.schema.json",
        include_str!("../../../schema/v0.1/auth.schema.json"),
    ),
    (
        "capabilities.schema.json",
        include_str!("../../../schema/v0.1/capabilities.schema.json"),
    ),
    (
        "common.schema.json",
        include_str!("../../../schema/v0.1/common.schema.json"),
    ),
    (
        "envelope.schema.json",
        include_str!("../../../schema/v0.1/envelope.schema.json"),
    ),
    (
        "inference.schema.json",
        include_str!("../../../schema/v0.1/inference.schema.json"),
    ),
    (
        "interaction.schema.json",
        include_str!("../../../schema/v0.1/interaction.schema.json"),
    ),
    (
        "manifest.schema.json",
        include_str!("../../../schema/v0.1/manifest.schema.json"),
    ),
    (
        "pack.schema.json",
        include_str!("../../../schema/v0.1/pack.schema.json"),
    ),
    (
        "provider-envelope.schema.json",
        include_str!("../../../schema/v0.1/provider-envelope.schema.json"),
    ),
    (
        "provider.schema.json",
        include_str!("../../../schema/v0.1/provider.schema.json"),
    ),
    (
        "run.schema.json",
        include_str!("../../../schema/v0.1/run.schema.json"),
    ),
    (
        "session.schema.json",
        include_str!("../../../schema/v0.1/session.schema.json"),
    ),
];

#[derive(Debug, thiserror::Error)]
pub enum SchemaError {
    #[error("embedded schema {0} is not in the bundle")]
    Missing(String),
    #[error("decode embedded schema {name}: {source}")]
    Decode {
        name: String,
        #[source]
        source: serde_json::Error,
    },
    #[error("compile schema {name}: {message}")]
    Compile { name: String, message: String },
}

fn document(name: &str) -> Result<Value, SchemaError> {
    let (_, text) = SCHEMAS
        .iter()
        .find(|(known, _)| *known == name)
        .ok_or_else(|| SchemaError::Missing(name.to_owned()))?;
    serde_json::from_str(text).map_err(|source| SchemaError::Decode {
        name: name.to_owned(),
        source,
    })
}

struct Bundle;

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

pub(crate) fn compile(name: &str) -> Result<Validator, SchemaError> {
    let root = document(name)?;
    jsonschema::options()
        .with_draft(Draft::Draft202012)
        .with_base_uri(format!("{SCHEMA_BASE}{name}"))
        .with_retriever(Bundle)
        .should_validate_formats(false)
        .build(&root)
        .map_err(|err| SchemaError::Compile {
            name: name.to_owned(),
            message: err.to_string(),
        })
}
