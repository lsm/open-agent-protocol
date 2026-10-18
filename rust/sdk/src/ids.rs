use std::time::{SystemTime, UNIX_EPOCH};

use rand::Rng;

const CROCKFORD: &[u8; 32] = b"0123456789ABCDEFGHJKMNPQRSTVWXYZ";
const NANO_ALPHABET: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

pub(crate) const ULID_LEN: usize = 26;
pub(crate) const SESSION_ID_LEN: usize = 21;

pub(crate) fn new_ulid() -> String {
    let millis = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis())
        .unwrap_or(0) as u64
        & 0x0000_FFFF_FFFF_FFFF;

    let mut out = String::with_capacity(ULID_LEN);
    for shift in (0..10).rev() {
        let index = ((millis >> (shift * 5)) & 0x1f) as usize;
        out.push(crockford_char(index));
    }

    let mut rng = rand::rng();
    for _ in 0..16 {
        out.push(crockford_char(rng.random_range(0..CROCKFORD.len())));
    }
    out
}

fn crockford_char(index: usize) -> char {
    char::from(CROCKFORD.get(index).copied().unwrap_or(b'0'))
}

fn nano_char(index: usize) -> char {
    char::from(NANO_ALPHABET.get(index).copied().unwrap_or(b'0'))
}

pub(crate) fn new_session_id() -> String {
    let mut rng = rand::rng();
    (0..SESSION_ID_LEN)
        .map(|_| nano_char(rng.random_range(0..NANO_ALPHABET.len())))
        .collect()
}

pub(crate) fn is_session_id(value: &str) -> bool {
    value.len() == SESSION_ID_LEN && value.bytes().all(|b| b.is_ascii_alphanumeric())
}

#[cfg_attr(not(test), allow(dead_code))]
pub(crate) fn is_ulid(value: &str) -> bool {
    value.len() == ULID_LEN
        && value
            .bytes()
            .all(|b| CROCKFORD.contains(&b.to_ascii_uppercase()))
}

pub(crate) fn now_millis() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_millis() as i64)
        .unwrap_or(0)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ulid_has_wire_shape() {
        for _ in 0..64 {
            let id = new_ulid();
            assert_eq!(id.len(), ULID_LEN);
            assert!(is_ulid(&id), "{id} is not a ULID");
        }
    }

    #[test]
    fn ulid_timestamp_segment_is_not_overflowed() {
        for _ in 0..64 {
            let id = new_ulid();
            let first = id.as_bytes()[0];
            assert!(first <= b'7', "ULID timestamp segment overflows: {id}");
        }
    }

    #[test]
    fn ulids_are_unique() {
        let a = new_ulid();
        let b = new_ulid();
        assert_ne!(a, b);
    }

    #[test]
    fn session_ids_have_wire_shape() {
        for _ in 0..64 {
            let id = new_session_id();
            assert!(is_session_id(&id), "{id} is not a session id");
        }
    }

    #[test]
    fn session_id_validation_rejects_bad_shapes() {
        assert!(!is_session_id(""));
        assert!(!is_session_id("too-short"));
        assert!(!is_session_id("0123456789012345678901"));
        assert!(!is_session_id("01234567890123456789-"));
    }
}
