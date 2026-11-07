use std::collections::BTreeSet;

use serde_json::{json, Value};

pub(crate) fn widen(document: &Value) -> Value {
    walk(document, document, false)
}

pub(crate) fn foreign_status(value: &str) -> bool {
    !matches!(
        value,
        "" | "queued"
            | "running"
            | "waiting_for_input"
            | "cancelling"
            | "completed"
            | "failed"
            | "cancelled"
    )
}

pub(crate) fn foreign_admission(value: &str) -> bool {
    !matches!(
        value,
        "" | "started" | "queued" | "steered" | "side_started" | "rejected"
    )
}

pub(crate) fn foreign_delivery(value: &str) -> bool {
    !matches!(value, "" | "auto" | "queue" | "steer" | "btw")
}

fn walk(document: &Value, node: &Value, condition: bool) -> Value {
    match node {
        Value::Array(values) => Value::Array(
            values
                .iter()
                .map(|v| walk(document, v, condition))
                .collect(),
        ),
        Value::Object(object) => {
            let mut out = object.clone();
            let mut rewritten = false;
            if !condition {
                for key in ["oneOf", "anyOf"] {
                    if let Some(members) = out.get(key).and_then(Value::as_array) {
                        if let Some(fallback) = fallback(document, members) {
                            let mut branches: Vec<_> =
                                members.iter().map(|m| walk(document, m, false)).collect();
                            branches.push(fallback);
                            out.remove(key);
                            out.insert("anyOf".to_owned(), Value::Array(branches));
                            rewritten = true;
                        }
                    }
                }
                if out
                    .get("enum")
                    .and_then(Value::as_array)
                    .is_some_and(|v| !v.is_empty() && v.iter().all(Value::is_string))
                {
                    out.remove("enum");
                    out.entry("type").or_insert(json!("string"));
                }
                if out.get("additionalProperties") == Some(&Value::Bool(false)) {
                    out.remove("additionalProperties");
                }
            }
            for (key, value) in &mut out {
                if matches!(
                    key.as_str(),
                    "enum" | "const" | "type" | "required" | "additionalProperties"
                ) || (rewritten && key == "anyOf")
                {
                    continue;
                }
                *value = walk(document, value, condition || key == "if");
            }
            Value::Object(out)
        }
        _ => node.clone(),
    }
}

fn fallback(document: &Value, members: &[Value]) -> Option<Value> {
    let mut discriminator = None;
    let mut known = Vec::new();
    let mut common: Option<BTreeSet<String>> = None;
    for member in members {
        let resolved = if let Some(reference) = member.get("$ref").and_then(Value::as_str) {
            document.pointer(reference.strip_prefix('#').filter(|p| p.starts_with('/'))?)?
        } else {
            member
        };
        let properties = resolved.get("properties")?.as_object()?;
        let mut constants = properties
            .iter()
            .filter_map(|(name, schema)| schema.get("const").map(|value| (name, value)));
        let (name, value) = constants.next()?;
        if constants.next().is_some()
            || discriminator
                .as_ref()
                .is_some_and(|previous| previous != name)
        {
            return None;
        }
        discriminator = Some(name.clone());
        known.push(value.clone());
        let required = resolved
            .get("required")
            .and_then(Value::as_array)
            .into_iter()
            .flatten()
            .filter_map(Value::as_str)
            .map(str::to_owned)
            .collect();
        common = Some(match common {
            None => required,
            Some(previous) => previous.intersection(&required).cloned().collect(),
        });
    }
    let discriminator = discriminator?;
    let mut required = common?;
    required.insert(discriminator.clone());
    Some(json!({
        "type":"object", "required":required,
        "properties":{discriminator:{"type":"string", "not":{"enum":known}}},
    }))
}
