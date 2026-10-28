use std::borrow::Cow;
use std::collections::HashSet;

pub const NESTING_LIMIT: usize = 10_000;

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum WalkError {
    Duplicate(String),
    Trailing,
    TooDeep,
    Syntax,
}

impl std::fmt::Display for WalkError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Duplicate(key) => write!(f, "duplicate object key {}", go_quote(key)),
            Self::Trailing => f.write_str("trailing JSON value"),
            Self::TooDeep => f.write_str("exceeded max depth"),
            Self::Syntax => f.write_str("invalid JSON"),
        }
    }
}

impl std::error::Error for WalkError {}

struct Level {
    object: bool,
    expect_key: bool,
    seen: HashSet<String>,
}

pub fn reject_duplicate_keys(data: &[u8]) -> Result<(), WalkError> {
    let mut stack: Vec<Level> = Vec::new();
    let mut at = skip_space(data, 0);
    let mut settled = false;
    while at < data.len() {
        if settled {
            return Err(WalkError::Trailing);
        }
        let byte = data[at];
        let mut value = true;
        match byte {
            b'{' | b'[' => {
                if stack.len() >= NESTING_LIMIT {
                    return Err(WalkError::TooDeep);
                }
                stack.push(Level {
                    object: byte == b'{',
                    expect_key: byte == b'{',
                    seen: HashSet::new(),
                });
                at += 1;
                value = false;
            }
            b'}' | b']' => {
                stack.pop().ok_or(WalkError::Syntax)?;
                at += 1;
            }
            b',' | b':' => {
                at += 1;
                value = false;
            }
            b'"' => {
                let (text, next) = string(data, at)?;
                at = next;
                if let Some(top) = stack.last_mut() {
                    if top.object && top.expect_key {
                        if !top.seen.insert(text.clone()) {
                            return Err(WalkError::Duplicate(text));
                        }
                        top.expect_key = false;
                        value = false;
                    }
                }
            }
            _ => {
                let start = at;
                while at < data.len() && !b",:]} \t\r\n".contains(&data[at]) {
                    at += 1;
                }
                if at == start {
                    return Err(WalkError::Syntax);
                }
            }
        }
        if value {
            match stack.last_mut() {
                None => settled = true,
                Some(top) if top.object => top.expect_key = true,
                Some(_) => {}
            }
        }
        at = skip_space(data, at);
    }
    if !stack.is_empty() || !settled {
        return Err(WalkError::Syntax);
    }
    Ok(())
}

fn skip_space(data: &[u8], mut at: usize) -> usize {
    while at < data.len() && matches!(data[at], b' ' | b'\t' | b'\r' | b'\n') {
        at += 1;
    }
    at
}

fn string(data: &[u8], start: usize) -> Result<(String, usize), WalkError> {
    let mut at = start + 1;
    while at < data.len() {
        match data[at] {
            b'"' => {
                let literal =
                    std::str::from_utf8(&data[start..=at]).map_err(|_| WalkError::Syntax)?;
                let text: String = serde_json::from_str(&replace_lone_surrogates(literal))
                    .map_err(|_| WalkError::Syntax)?;
                return Ok((text, at + 1));
            }
            b'\\' => at += 2,
            _ => at += 1,
        }
    }
    Err(WalkError::Syntax)
}

fn hex_escape_at(data: &[u8], at: usize) -> Option<u32> {
    let digits = data.get(at..at + 6)?;
    if digits[0] != b'\\' || digits[1] != b'u' {
        return None;
    }
    let mut code = 0;
    for digit in &digits[2..] {
        code = code * 16 + (*digit as char).to_digit(16)?;
    }
    Some(code)
}

fn surrogate(code: u32) -> bool {
    (0xd800..=0xdfff).contains(&code)
}

pub fn replace_lone_surrogates(data: &str) -> Cow<'_, str> {
    let bytes = data.as_bytes();
    if !(0..bytes.len()).any(|at| hex_escape_at(bytes, at).is_some_and(surrogate)) {
        return Cow::Borrowed(data);
    }
    let mut out = String::with_capacity(data.len());
    let mut at = 0;
    let mut quoted = false;
    while at < bytes.len() {
        let byte = bytes[at];
        if quoted && byte == b'\\' {
            let width = match hex_escape_at(bytes, at) {
                Some(code)
                    if (0xd800..=0xdbff).contains(&code)
                        && hex_escape_at(bytes, at + 6)
                            .is_some_and(|low| (0xdc00..=0xdfff).contains(&low)) =>
                {
                    12
                }
                Some(code) if surrogate(code) => {
                    out.push_str("\\ufffd");
                    at += 6;
                    continue;
                }
                Some(_) => 6,
                None => 2.min(bytes.len() - at),
            };
            out.push_str(&data[at..at + width]);
            at += width;
            continue;
        }
        if byte == b'"' {
            quoted = !quoted;
        }
        let width = utf8_width(byte);
        out.push_str(&data[at..at + width]);
        at += width;
    }
    Cow::Owned(out)
}

fn utf8_width(lead: u8) -> usize {
    match lead {
        0x00..=0x7f => 1,
        0xc0..=0xdf => 2,
        0xe0..=0xef => 3,
        _ => 4,
    }
}

pub fn go_marshal(json: &str) -> String {
    let mut out = String::with_capacity(json.len());
    let mut quoted = false;
    let mut escaped = false;
    for character in json.chars() {
        if quoted {
            if escaped {
                escaped = false;
            } else if character == '\\' {
                escaped = true;
            } else if character == '"' {
                quoted = false;
            }
            match character {
                '<' => out.push_str("\\u003c"),
                '>' => out.push_str("\\u003e"),
                '&' => out.push_str("\\u0026"),
                '\u{2028}' => out.push_str("\\u2028"),
                '\u{2029}' => out.push_str("\\u2029"),
                other => out.push(other),
            }
            continue;
        }
        match character {
            ' ' | '\t' | '\r' | '\n' => {}
            '"' => {
                quoted = true;
                out.push('"');
            }
            other => out.push(other),
        }
    }
    out
}

pub fn go_quote(text: &str) -> String {
    let mut out = String::with_capacity(text.len() + 2);
    out.push('"');
    for character in text.chars() {
        match character {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\u{b}' => out.push_str("\\v"),
            other if (other as u32) < 0x20 || other == '\u{7f}' => {
                out.push_str(&format!("\\x{:02x}", other as u32))
            }
            other => out.push(other),
        }
    }
    out.push('"');
    out
}
