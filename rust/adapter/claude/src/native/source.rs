use std::borrow::Cow;
use std::cell::RefCell;

use serde::de::{DeserializeOwned, Error};
use serde::{Deserialize, Deserializer};
use serde_json::value::RawValue;

use oxap_adapter::gojson::replace_lone_surrogates;

struct Source {
    start: usize,
    len: usize,
    original: String,
}

thread_local! {
    static SOURCE: RefCell<Option<Source>> = const { RefCell::new(None) };
}

struct Restore(Option<Source>);

impl Drop for Restore {
    fn drop(&mut self) {
        SOURCE.with(|source| *source.borrow_mut() = self.0.take());
    }
}

fn with_source<R>(
    text: &str,
    read: impl FnOnce(&str) -> serde_json::Result<R>,
) -> serde_json::Result<R> {
    let normalised = match replace_lone_surrogates(text) {
        Cow::Borrowed(_) => return read(text),
        Cow::Owned(normalised) => normalised,
    };
    let previous = SOURCE.with(|source| {
        source.borrow_mut().replace(Source {
            start: normalised.as_ptr() as usize,
            len: normalised.len(),
            original: text.to_owned(),
        })
    });
    let _restore = Restore(previous);
    read(&normalised)
}

fn from_text<T: DeserializeOwned>(text: &str) -> serde_json::Result<T> {
    serde_json::from_str(text)
}

pub(super) fn decode<T: DeserializeOwned>(text: &str) -> serde_json::Result<T> {
    with_source(text, from_text)
}

pub(super) fn decode_slice<T: DeserializeOwned + Default>(
    text: &str,
) -> serde_json::Result<Vec<T>> {
    with_source(text, |text| slice(serde_json::from_str(text)?))
}

fn original(value: &RawValue) -> Box<RawValue> {
    let text = value.get();
    SOURCE
        .with(|source| {
            let source = source.borrow();
            let source = source.as_ref()?;
            let offset = (text.as_ptr() as usize).checked_sub(source.start)?;
            if offset + text.len() > source.len {
                return None;
            }
            let span = source.original.get(offset..offset + text.len())?;
            RawValue::from_string(span.to_owned()).ok()
        })
        .unwrap_or_else(|| value.to_owned())
}

pub(super) fn raw<'de, D: Deserializer<'de>>(
    deserializer: D,
) -> Result<Option<Box<RawValue>>, D::Error> {
    <&'de RawValue>::deserialize(deserializer).map(|value| Some(original(value)))
}

fn object<'de, T: Deserialize<'de> + Default>(value: &'de RawValue) -> serde_json::Result<T> {
    match value.get().as_bytes().first() {
        Some(b'n') => Ok(T::default()),
        Some(b'{') => serde_json::from_str(value.get()),
        _ => Err(serde_json::Error::custom(format!(
            "cannot unmarshal {} into a Go struct",
            value.get()
        ))),
    }
}

pub(super) fn slice<'de, T: Deserialize<'de> + Default>(
    value: &'de RawValue,
) -> serde_json::Result<Vec<T>> {
    match value.get().as_bytes().first() {
        Some(b'n') => Ok(Vec::new()),
        Some(b'[') => serde_json::from_str::<Vec<&'de RawValue>>(value.get())?
            .into_iter()
            .map(object)
            .collect(),
        _ => Err(serde_json::Error::custom(format!(
            "cannot unmarshal {} into a Go slice",
            value.get()
        ))),
    }
}

pub(super) fn structure<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    object(<&'de RawValue>::deserialize(deserializer)?).map_err(D::Error::custom)
}

pub(super) fn structures<'de, D, T>(deserializer: D) -> Result<Vec<T>, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    slice(<&'de RawValue>::deserialize(deserializer)?).map_err(D::Error::custom)
}
