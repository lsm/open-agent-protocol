use serde::de::Error as _;
use serde::Deserialize;
use serde_json::Value;

use super::{go_marshal, replace_lone_surrogates, NESTING_LIMIT};

fn depth(text: &str) -> usize {
    let (mut deepest, mut level, mut quoted, mut escaped) = (0, 0usize, false, false);
    for byte in text.bytes() {
        if quoted {
            match (escaped, byte) {
                (true, _) => escaped = false,
                (false, b'\\') => escaped = true,
                (false, b'"') => quoted = false,
                _ => {}
            }
            continue;
        }
        match byte {
            b'"' => quoted = true,
            b'{' | b'[' => {
                level += 1;
                deepest = deepest.max(level);
            }
            b'}' | b']' => level = level.saturating_sub(1),
            _ => {}
        }
    }
    deepest
}

pub fn remarshal_any(json: &str) -> Result<String, serde_json::Error> {
    let text = replace_lone_surrogates(json);
    if depth(&text) > NESTING_LIMIT {
        return Err(serde_json::Error::custom(format!(
            "exceeded max depth of {NESTING_LIMIT}"
        )));
    }
    let mut deserializer = serde_json::Deserializer::from_str(&text);
    deserializer.disable_recursion_limit();
    let value = Value::deserialize(&mut deserializer)?;
    deserializer.end()?;
    let mut out = String::with_capacity(text.len());
    write_value(&value, &mut out);
    Ok(go_marshal(&out))
}

pub fn go_float(value: f64) -> String {
    let magnitude = value.abs();
    if magnitude != 0.0 && !(1e-6..1e21).contains(&magnitude) {
        let text = format!("{value:e}");
        return match text.split_once('e') {
            Some((mantissa, exponent)) if !exponent.starts_with('-') => {
                format!("{mantissa}e+{exponent}")
            }
            _ => text,
        };
    }
    format!("{value}")
}

fn write_value(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(flag) => out.push_str(if *flag { "true" } else { "false" }),
        Value::Number(number) => out.push_str(&go_float(number.as_f64().unwrap_or(0.0))),
        Value::String(text) => out.push_str(&Value::String(text.clone()).to_string()),
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_value(item, out);
            }
            out.push(']');
        }
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort();
            out.push('{');
            for (index, key) in keys.into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                out.push_str(&Value::String(key.clone()).to_string());
                out.push(':');
                write_value(&members[key], out);
            }
            out.push('}');
        }
    }
}
