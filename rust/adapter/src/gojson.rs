use std::borrow::Cow;
use std::collections::HashSet;

use crate::quote_table::PRINTABLE;

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
    seen: HashSet<String>,
}

#[derive(Clone, Copy, PartialEq, Eq)]
enum Expect {
    Value,
    ValueOrClose,
    Key,
    KeyOrClose,
    Colon,
    CommaOrClose,
}

pub fn reject_duplicate_keys(data: &[u8]) -> Result<(), WalkError> {
    std::str::from_utf8(data).map_err(|_| WalkError::Syntax)?;
    let end = walk_value(data, 0)?;
    let rest = skip_space(data, end);
    if rest == data.len() {
        return Ok(());
    }
    match walk_value(data, rest) {
        Ok(_) | Err(WalkError::Trailing) => Err(WalkError::Trailing),
        Err(error) => Err(error),
    }
}

fn walk_value(data: &[u8], start: usize) -> Result<usize, WalkError> {
    let mut stack: Vec<Level> = Vec::new();
    let mut expect = Expect::Value;
    let mut at = skip_space(data, start);
    loop {
        let Some(&byte) = data.get(at) else {
            return Err(WalkError::Syntax);
        };
        let mut finished = false;
        match (expect, byte) {
            (Expect::Value | Expect::ValueOrClose, b'{' | b'[') => {
                if stack.len() >= NESTING_LIMIT {
                    return Err(WalkError::TooDeep);
                }
                stack.push(Level {
                    object: byte == b'{',
                    seen: HashSet::new(),
                });
                expect = if byte == b'{' {
                    Expect::KeyOrClose
                } else {
                    Expect::ValueOrClose
                };
                at += 1;
            }
            (Expect::ValueOrClose, b']') | (Expect::KeyOrClose, b'}') => {
                stack.pop();
                at += 1;
                finished = true;
            }
            (Expect::CommaOrClose, b']' | b'}') => {
                let object = stack.last().is_some_and(|level| level.object);
                if object != (byte == b'}') {
                    return Err(WalkError::Syntax);
                }
                stack.pop();
                at += 1;
                finished = true;
            }
            (Expect::CommaOrClose, b',') => {
                let object = stack.last().is_some_and(|level| level.object);
                expect = if object { Expect::Key } else { Expect::Value };
                at += 1;
            }
            (Expect::Key | Expect::KeyOrClose, b'"') => {
                let (key, next) = string(data, at)?;
                let level = stack.last_mut().ok_or(WalkError::Syntax)?;
                if !level.seen.insert(key.clone()) {
                    return Err(WalkError::Duplicate(key));
                }
                expect = Expect::Colon;
                at = next;
            }
            (Expect::Colon, b':') => {
                expect = Expect::Value;
                at += 1;
            }
            (Expect::Value | Expect::ValueOrClose, b'"') => {
                at = string(data, at)?.1;
                finished = true;
            }
            (Expect::Value | Expect::ValueOrClose, _) => {
                at = scalar(data, at)?;
                finished = true;
            }
            _ => return Err(WalkError::Syntax),
        }
        if finished {
            if stack.is_empty() {
                return Ok(at);
            }
            expect = Expect::CommaOrClose;
        }
        at = skip_space(data, at);
    }
}

fn digits(data: &[u8], mut at: usize) -> (usize, bool) {
    let from = at;
    while data.get(at).is_some_and(u8::is_ascii_digit) {
        at += 1;
    }
    (at, at > from)
}

fn scalar(data: &[u8], start: usize) -> Result<usize, WalkError> {
    for literal in ["true", "false", "null"] {
        if data[start..].starts_with(literal.as_bytes()) {
            return Ok(start + literal.len());
        }
    }
    let mut at = start;
    if data.get(at) == Some(&b'-') {
        at += 1;
    }
    match data.get(at) {
        Some(b'0') => at += 1,
        Some(b'1'..=b'9') => at = digits(data, at).0,
        _ => return Err(WalkError::Syntax),
    }
    if data.get(at) == Some(&b'.') {
        let (next, some) = digits(data, at + 1);
        if !some {
            return Err(WalkError::Syntax);
        }
        at = next;
    }
    if matches!(data.get(at), Some(b'e' | b'E')) {
        at += 1;
        if matches!(data.get(at), Some(b'+' | b'-')) {
            at += 1;
        }
        let (next, some) = digits(data, at);
        if !some {
            return Err(WalkError::Syntax);
        }
        at = next;
    }
    Ok(at)
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
            '\u{b}' => out.push_str("\\v"),
            '\u{c}' => out.push_str("\\f"),
            other if (other as u32) < 0x80 && !printable(other) => {
                out.push_str(&format!("\\x{:02x}", other as u32))
            }
            other if printable(other) => out.push(other),
            other if (other as u32) < 0x10000 => out.push_str(&format!("\\u{:04x}", other as u32)),
            other => out.push_str(&format!("\\U{:08x}", other as u32)),
        }
    }
    out.push('"');
    out
}

pub fn printable(character: char) -> bool {
    let code = character as u32;
    PRINTABLE
        .binary_search_by(|(low, high)| {
            if code < *low {
                std::cmp::Ordering::Greater
            } else if code > *high {
                std::cmp::Ordering::Less
            } else {
                std::cmp::Ordering::Equal
            }
        })
        .is_ok()
}
