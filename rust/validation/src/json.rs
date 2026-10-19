use serde_json::Value;

pub(crate) fn same_json(a: &str, b: &str) -> bool {
    match (
        serde_json::from_str::<Value>(a),
        serde_json::from_str::<Value>(b),
    ) {
        (Ok(left), Ok(right)) => canonical(&left) == canonical(&right),
        _ => false,
    }
}

fn canonical(value: &Value) -> Value {
    match value {
        Value::Number(number) => Value::String(exact(&number.to_string())),
        Value::Array(items) => Value::Array(items.iter().map(canonical).collect()),
        Value::Object(members) => Value::Object(
            members
                .iter()
                .map(|(k, v)| (k.clone(), canonical(v)))
                .collect(),
        ),
        other => other.clone(),
    }
}

pub(crate) fn exact(number: &str) -> String {
    let (mantissa, exponent) = match number.find(['e', 'E']) {
        Some(at) => (&number[..at], number[at + 1..].parse::<i64>().unwrap_or(0)),
        None => (number, 0),
    };
    let negative = mantissa.starts_with('-');
    let unsigned = mantissa.trim_start_matches('-');
    let (whole, fraction) = unsigned.split_once('.').unwrap_or((unsigned, ""));
    let digits = format!("{whole}{fraction}");
    let scale = exponent - fraction.len() as i64;
    let trimmed = digits.trim_start_matches('0');
    if trimmed.is_empty() {
        return "0".to_owned();
    }
    let zeros = trimmed.len() - trimmed.trim_end_matches('0').len();
    let significant = &trimmed[..trimmed.len() - zeros];
    let sign = if negative { "-" } else { "" };
    format!("{sign}{significant}e{}", scale + zeros as i64)
}
