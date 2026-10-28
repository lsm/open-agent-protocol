use serde::de::DeserializeOwned;
use serde::{Deserialize, Deserializer};
use serde_json::value::RawValue;

pub trait Decode: DeserializeOwned + Default {
    fn from_null() -> Result<Self, serde_json::Error> {
        Ok(Self::default())
    }
}

pub fn decode<T: Decode>(bytes: &[u8]) -> Result<T, serde_json::Error> {
    let raw: Box<RawValue> = serde_json::from_slice(bytes)?;
    if raw.get() == "null" {
        return T::from_null();
    }
    object(&raw)
}

fn object<T: DeserializeOwned + Default, E: serde::de::Error>(raw: &RawValue) -> Result<T, E> {
    match raw.get().as_bytes().first() {
        Some(b'n') => Ok(T::default()),
        Some(b'{') => serde_json::from_str(raw.get()).map_err(E::custom),
        _ => Err(E::custom(format!(
            "cannot unmarshal {} into a Go struct",
            raw.get()
        ))),
    }
}

pub(crate) fn zero<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

pub(crate) fn structures<'de, D, T>(deserializer: D) -> Result<Vec<T>, D::Error>
where
    D: Deserializer<'de>,
    T: DeserializeOwned + Default,
{
    slice(&Box::<RawValue>::deserialize(deserializer)?)
}

pub(crate) fn slice<T, E>(raw: &RawValue) -> Result<Vec<T>, E>
where
    T: DeserializeOwned + Default,
    E: serde::de::Error,
{
    match raw.get().as_bytes().first() {
        Some(b'n') => Ok(Vec::new()),
        Some(b'[') => {
            let items: Vec<Box<RawValue>> = serde_json::from_str(raw.get()).map_err(E::custom)?;
            items.iter().map(|item| object(item)).collect()
        }
        _ => Err(E::custom(format!(
            "cannot unmarshal {} into a Go slice",
            raw.get()
        ))),
    }
}

pub(crate) fn raw<'de, D>(deserializer: D) -> Result<Option<Box<RawValue>>, D::Error>
where
    D: Deserializer<'de>,
{
    Box::<RawValue>::deserialize(deserializer).map(Some)
}
