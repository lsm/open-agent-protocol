pub(crate) fn integral_spelling(number: &str) -> Option<String> {
    if !number.contains(['.', 'e', 'E'])
        || serde_json::from_str::<serde_json::Number>(number).is_err()
    {
        return None;
    }
    let (negative, unsigned) = match number.strip_prefix('-') {
        Some(rest) => (true, rest),
        None => (false, number),
    };
    let (mantissa, exponent) = match unsigned.find(['e', 'E']) {
        Some(at) => {
            let exponent: i32 = unsigned[at + 1..].trim_start_matches('+').parse().ok()?;
            if !(-40..=40).contains(&exponent) {
                return None;
            }
            (&unsigned[..at], exponent)
        }
        None => (unsigned, 0),
    };
    let (whole, fraction) = mantissa.split_once('.').unwrap_or((mantissa, ""));
    let digits = format!("{whole}{fraction}");
    let digits = digits.trim_start_matches('0');
    let scale = exponent - i32::try_from(fraction.len()).ok()?;
    let magnitude = if scale >= 0 {
        format!("{digits}{}", "0".repeat(usize::try_from(scale).ok()?))
    } else {
        let cut = usize::try_from(-scale).ok()?;
        let kept = digits.len().checked_sub(cut);
        match kept {
            Some(kept) if digits[kept..].bytes().all(|b| b == b'0') => digits[..kept].to_owned(),
            None if digits.bytes().all(|b| b == b'0') => String::new(),
            _ => return None,
        }
    };
    let magnitude = magnitude.trim_start_matches('0');
    if magnitude.is_empty() {
        return Some("0".to_owned());
    }
    Some(if negative {
        format!("-{magnitude}")
    } else {
        magnitude.to_owned()
    })
}

#[cfg(test)]
mod tests {
    use super::integral_spelling;

    #[test]
    fn integral_spellings_read_as_their_integer() {
        for (spelled, integer) in [
            ("1.0", "1"),
            ("1.7e12", "1700000000000"),
            ("1e2", "100"),
            ("-0.0", "0"),
            ("2.50e1", "25"),
            ("-3.00", "-3"),
            ("0.0e5", "0"),
            ("1200e-2", "12"),
            ("9007199254740993.0", "9007199254740993"),
        ] {
            assert_eq!(
                integral_spelling(spelled).as_deref(),
                Some(integer),
                "{spelled}"
            );
        }
    }

    #[test]
    fn fractions_plain_integers_far_exponents_and_non_numbers_are_not_respelled() {
        for spelled in [
            "1.5", "2.25", "1e41", "1e-41", "7", "\"1.0\"", "25E-1e1", "1.",
        ] {
            assert_eq!(integral_spelling(spelled), None, "{spelled}");
        }
    }
}
