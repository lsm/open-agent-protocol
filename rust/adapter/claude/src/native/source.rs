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

pub(super) fn decode<T: DeserializeOwned>(text: &str) -> serde_json::Result<T> {
    let normalised = match replace_lone_surrogates(text) {
        Cow::Borrowed(_) => return serde_json::from_str(text),
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
    serde_json::from_str(&normalised)
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

pub(super) fn structure<'de, D, T>(deserializer: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Deserialize<'de> + Default,
{
    let value = <&'de RawValue>::deserialize(deserializer)?;
    match value.get().as_bytes().first() {
        Some(b'n') => Ok(T::default()),
        Some(b'{') => serde_json::from_str(value.get()).map_err(D::Error::custom),
        _ => Err(D::Error::custom(format!(
            "cannot unmarshal {} into a Go struct",
            value.get()
        ))),
    }
}
