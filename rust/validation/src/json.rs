use serde_json::Value;

pub(crate) fn same_json(a: &str, b: &str) -> bool {
    match (
        serde_json::from_str::<Value>(a),
        serde_json::from_str::<Value>(b),
    ) {
        (Ok(left), Ok(right)) => same_value(&left, &right),
        _ => false,
    }
}

fn same_value(left: &Value, right: &Value) -> bool {
    match (left, right) {
        (Value::Number(left), Value::Number(right)) => {
            exact(&left.to_string()) == exact(&right.to_string())
        }
        (Value::Array(left), Value::Array(right)) => {
            left.len() == right.len()
                && left
                    .iter()
                    .zip(right)
                    .all(|(left, right)| same_value(left, right))
        }
        (Value::Object(left), Value::Object(right)) => {
            left.len() == right.len()
                && left.iter().all(|(key, value)| {
                    right.get(key).is_some_and(|other| same_value(value, other))
                })
        }
        _ => left == right,
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
