use std::collections::BTreeMap;

use serde::de::{DeserializeOwned, Error as _};
use serde::ser::SerializeMap;
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use serde_json::value::RawValue;

use crate::envelope::{counter, member};
use crate::{EnvelopeId, EnvelopeType, Error, InferenceId, PROTOCOL, VERSION};

pub const PROVIDER_PROFILE: &str = "open-agent-protocol.model-provider-core";

const MEMBERS: [&str; 12] = [
    "protocol",
    "version",
    "profile",
    "type",
    "id",
    "payload",
    "sequence",
    "timestamp_ms",
    "in_reply_to",
    "inference_id",
    "capability_revision",
    "extensions",
];

#[derive(Clone, Debug, Default)]
pub struct ProviderEnvelope {
    pub protocol: String,
    pub version: String,
    pub profile: String,
    pub envelope_type: EnvelopeType,
    pub id: EnvelopeId,
    pub payload: Option<Box<RawValue>>,
    pub sequence: Option<u64>,
    pub timestamp_ms: Option<i64>,
    pub in_reply_to: Option<EnvelopeId>,
    pub inference_id: Option<InferenceId>,
    pub capability_revision: Option<String>,
    pub extensions: BTreeMap<String, Box<RawValue>>,
    pub unknown: BTreeMap<String, Box<RawValue>>,
}

impl ProviderEnvelope {
    pub fn new(
        envelope_type: EnvelopeType,
        id: impl Into<EnvelopeId>,
        payload: &impl Serialize,
    ) -> Result<Self, Error> {
        let payload = serde_json::value::to_raw_value(payload).map_err(Error::MarshalPayload)?;
        Ok(Self {
            protocol: PROTOCOL.to_owned(),
            version: VERSION.to_owned(),
            profile: PROVIDER_PROFILE.to_owned(),
            envelope_type,
            id: id.into(),
            payload: Some(payload),
            ..Self::default()
        })
    }

    pub fn parse(data: &[u8]) -> Result<Self, Error> {
        serde_json::from_slice(data).map_err(Error::Envelope)
    }

    pub fn decode_payload<T: DeserializeOwned>(&self) -> Result<T, Error> {
        let payload = self.payload.as_ref().ok_or(Error::MissingPayload)?;
        serde_json::from_str(payload.get()).map_err(|source| Error::Payload {
            envelope_type: self.envelope_type.clone(),
            source,
        })
    }
}

impl<'de> Deserialize<'de> for ProviderEnvelope {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let members = BTreeMap::<String, Box<RawValue>>::deserialize(deserializer)?;
        let mut envelope = ProviderEnvelope::default();
        for (key, raw) in members {
            let name = MEMBERS
                .iter()
                .find(|name| **name == key)
                .copied()
                .unwrap_or("unknown");
            let failed = |source| {
                D::Error::custom(Error::Member {
                    member: name,
                    source,
                })
            };
            match key.as_str() {
                "protocol" => envelope.protocol = member(&raw).map_err(failed)?,
                "version" => envelope.version = member(&raw).map_err(failed)?,
                "profile" => envelope.profile = member(&raw).map_err(failed)?,
                "type" => envelope.envelope_type = member(&raw).map_err(failed)?,
                "id" => envelope.id = member(&raw).map_err(failed)?,
                "payload" => envelope.payload = Some(raw),
                "sequence" => envelope.sequence = counter(&raw).map_err(failed)?,
                "timestamp_ms" => envelope.timestamp_ms = counter(&raw).map_err(failed)?,
                "in_reply_to" => envelope.in_reply_to = member(&raw).map_err(failed)?,
                "inference_id" => envelope.inference_id = member(&raw).map_err(failed)?,
                "capability_revision" => {
                    envelope.capability_revision = member(&raw).map_err(failed)?
                }
                "extensions" => {
                    envelope.extensions = member::<Option<_>>(&raw)
                        .map_err(failed)?
                        .unwrap_or_default()
                }
                _ => {
                    envelope.unknown.insert(key, raw);
                }
            }
        }
        Ok(envelope)
    }
}

impl Serialize for ProviderEnvelope {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        let mut map = serializer.serialize_map(None)?;
        map.serialize_entry("protocol", &self.protocol)?;
        map.serialize_entry("version", &self.version)?;
        map.serialize_entry("profile", &self.profile)?;
        map.serialize_entry("type", &self.envelope_type)?;
        map.serialize_entry("id", &self.id)?;
        map.serialize_entry("payload", &self.payload)?;
        if let Some(sequence) = self.sequence {
            map.serialize_entry("sequence", &sequence)?;
        }
        if let Some(timestamp_ms) = self.timestamp_ms {
            map.serialize_entry("timestamp_ms", &timestamp_ms)?;
        }
        if let Some(id) = &self.in_reply_to {
            map.serialize_entry("in_reply_to", id)?;
        }
        if let Some(id) = &self.inference_id {
            map.serialize_entry("inference_id", id)?;
        }
        if let Some(revision) = &self.capability_revision {
            map.serialize_entry("capability_revision", revision)?;
        }
        if !self.extensions.is_empty() {
            map.serialize_entry("extensions", &self.extensions)?;
        }
        for (key, value) in &self.unknown {
            if !MEMBERS.contains(&key.as_str()) {
                map.serialize_entry(key, value)?;
            }
        }
        map.end()
    }
}
