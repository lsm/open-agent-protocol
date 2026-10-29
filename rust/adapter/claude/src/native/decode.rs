use std::fmt;

use serde::de::DeserializeOwned;

use super::*;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Invalid(pub String);

impl fmt::Display for Invalid {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "claude native: invalid frame for a known type: {}",
            self.0
        )
    }
}

impl std::error::Error for Invalid {}

fn invalid(detail: impl Into<String>) -> Invalid {
    Invalid(detail.into())
}

fn unmarshal<T: DeserializeOwned>(raw: &[u8]) -> Result<T, Invalid> {
    let text = String::from_utf8_lossy(raw);
    serde_json::from_str(&oap_adapter::gojson::replace_lone_surrogates(&text))
        .map_err(|error| invalid(error.to_string()))
}

#[derive(Clone, Debug)]
pub enum Observation {
    User(Box<UserFrame>),
    Assistant(Box<AssistantFrame>),
    Unknown { kind: String, subtype: String },
}

pub fn decode_observation(kind: &str, subtype: &str, raw: &[u8]) -> Result<Observation, Invalid> {
    Ok(match kind {
        TYPE_USER => {
            let frame: UserFrame = unmarshal(raw)?;
            if frame.message.role.is_empty() || frame.message.content.is_none() {
                return Err(invalid(
                    "user frame requires message.role and message.content",
                ));
            }
            Observation::User(Box::new(frame))
        }
        TYPE_ASSISTANT => {
            let frame: AssistantFrame = unmarshal(raw)?;
            if frame.message.model.is_empty() || frame.message.content.is_none() {
                return Err(invalid(
                    "assistant frame requires message.model and message.content",
                ));
            }
            Observation::Assistant(Box::new(frame))
        }
        _ => Observation::Unknown {
            kind: kind.into(),
            subtype: subtype.into(),
        },
    })
}
