use serde::{Deserialize, Deserializer};
use serde_json::value::RawValue;

pub(crate) fn is_false(value: &bool) -> bool {
    !*value
}

pub(crate) fn is_zero_u64(value: &u64) -> bool {
    *value == 0
}

pub(crate) fn is_zero_i64(value: &i64) -> bool {
    *value == 0
}

pub(crate) fn raw<'de, D: Deserializer<'de>>(
    deserializer: D,
) -> Result<Option<Box<RawValue>>, D::Error> {
    Box::<RawValue>::deserialize(deserializer).map(Some)
}
