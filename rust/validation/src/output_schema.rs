use jsonschema::{Draft, Retrieve, Uri, Validator};
use serde_json::Value;

const OUTPUT_SCHEMA_BASE: &str = "https://open-agent-protocol.local/output-schema";

pub struct OutputSchema {
    compiled: Validator,
}

struct Refusing;

impl Retrieve for Refusing {
    fn retrieve(
        &self,
        uri: &Uri<String>,
    ) -> Result<Value, Box<dyn std::error::Error + Send + Sync>> {
        Err(format!(
            "reference {:?} is outside the registered resources",
            uri.as_str()
        )
        .into())
    }
}

fn object_rooted(document: &serde_json::Map<String, Value>) -> Result<(), String> {
    match document.get("type") {
        None => Ok(()),
        Some(Value::String(declared)) if declared == "object" => Ok(()),
        Some(Value::String(declared)) => Err(format!(
            "output_schema root type is {declared:?}; a structured result is an object"
        )),
        Some(Value::Array(entries)) if entries.is_empty() => {
            Err("output_schema root type list is empty".to_owned())
        }
        Some(Value::Array(entries)) => match entries
            .iter()
            .find(|entry| entry.as_str() != Some("object"))
        {
            Some(entry) => Err(format!(
                "output_schema root type list names {entry}; a structured result is an object"
            )),
            None => Ok(()),
        },
        Some(_) => Err("output_schema root type is not a string or a list of strings".to_owned()),
    }
}

impl OutputSchema {
    pub fn compile(raw: &str) -> Result<Self, String> {
        if raw.trim().is_empty() {
            return Err("output_schema is empty".to_owned());
        }
        let document: Value = serde_json::from_str(raw)
            .map_err(|err| format!("output_schema is not valid JSON: {err}"))?;
        let Value::Object(object) = &document else {
            return Err("output_schema must be a JSON Schema object".to_owned());
        };
        object_rooted(object)?;
        let compiled = jsonschema::options()
            .with_draft(Draft::Draft202012)
            .with_base_uri(OUTPUT_SCHEMA_BASE)
            .with_retriever(Refusing)
            .should_validate_formats(false)
            .build(&document)
            .map_err(|err| format!("output_schema does not compile: {err}"))?;
        Ok(Self { compiled })
    }

    pub fn validate(&self, document: &str) -> Result<(), String> {
        let value: Value = serde_json::from_str(document)
            .map_err(|err| format!("result is not valid JSON: {err}"))?;
        self.compiled
            .validate(&value)
            .map_err(|err| err.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::OutputSchema;

    #[test]
    fn a_schema_compiles_only_when_it_roots_an_object() {
        for raw in [r#"{}"#, r#"{"type":"object"}"#, r#"{"type":["object"]}"#] {
            assert!(OutputSchema::compile(raw).is_ok(), "{raw}");
        }
        for (raw, reason) in [
            ("", "is empty"),
            ("{", "not valid JSON"),
            ("[]", "must be a JSON Schema object"),
            (r#"{"type":"string"}"#, "a structured result is an object"),
            (r#"{"type":[]}"#, "list is empty"),
            (r#"{"type":["object","null"]}"#, "list names"),
            (r#"{"type":7}"#, "not a string or a list"),
            (
                r#"{"$ref":"https://example.invalid/schema"}"#,
                "does not compile",
            ),
        ] {
            let err = OutputSchema::compile(raw)
                .err()
                .unwrap_or_else(|| panic!("{raw} compiled"));
            assert!(err.contains(reason), "{raw}: {err}");
        }
    }

    #[test]
    fn a_compiled_schema_judges_a_result() {
        let schema = OutputSchema::compile(
            r#"{"type":"object","required":["ok"],"properties":{"ok":{"const":true}}}"#,
        )
        .unwrap();
        assert!(schema.validate(r#"{"ok":true}"#).is_ok());
        assert!(schema.validate(r#"{"ok":false}"#).is_err());
        assert!(schema.validate("not json").is_err());
    }

    #[test]
    fn numbers_compare_exactly_past_double_precision() {
        let schema = OutputSchema::compile(
            r#"{"type":"object","properties":{"n":{"const":0.10000000000000000000001}}}"#,
        )
        .unwrap();
        assert!(schema
            .validate(r#"{"n":0.10000000000000000000001}"#)
            .is_ok());
        assert!(schema
            .validate(r#"{"n":0.10000000000000000000002}"#)
            .is_err());
    }
}
