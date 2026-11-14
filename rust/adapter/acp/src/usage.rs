use std::collections::BTreeMap;

use oxap_adapter::gojson::{remarshal_any, replace_lone_surrogates};
use oxap_adapter::jsonrpc::NotificationMessage;
use oxap_protocol::Usage;
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::native::{decode, SessionUpdateParams, UpdateHeader, METHOD_SESSION_UPDATE};

pub(crate) type Extensions = BTreeMap<String, Box<RawValue>>;

#[derive(Default)]
pub struct UsageScope {
    context: Option<Box<RawValue>>,
    prompted: bool,
}

impl UsageScope {
    pub(crate) fn consume_prompt(&mut self) -> bool {
        !std::mem::replace(&mut self.prompted, true)
    }

    pub fn observe(&mut self, notification: &NotificationMessage, native_id: &str) -> bool {
        if notification.method != METHOD_SESSION_UPDATE {
            return false;
        }
        let Some(raw) = notification.params.as_deref() else {
            return false;
        };
        let Ok(params) = decode::<SessionUpdateParams>(raw.get().as_bytes()) else {
            return false;
        };
        if params.session_id != native_id {
            return false;
        }
        let Some(raw) = params.update.as_deref() else {
            return false;
        };
        let Ok(header) = decode::<UpdateHeader>(raw.get().as_bytes()) else {
            return false;
        };
        if header.session_update != "usage_update" {
            return false;
        }
        self.update(raw.get());
        true
    }

    pub(crate) fn update(&mut self, raw: &str) {
        let Some(fields) = Fields::parse(raw) else {
            return;
        };
        let (Some(used), Some(size)) = (fields.count("used"), fields.count("size")) else {
            return;
        };
        let mut context = BTreeMap::from([
            ("used_tokens", encoded(&used)),
            ("size_tokens", encoded(&size)),
        ]);
        if let Some(cost) = fields.0.get("cost").and_then(|raw| cost(raw)) {
            context.insert("cost", cost);
        }
        self.context = Some(encoded(&context));
    }

    pub(crate) fn settle(&mut self, raw: Option<&RawValue>) -> (Option<Usage>, Extensions) {
        let first = self.consume_prompt();
        let turn = raw.and_then(|raw| turn(raw.get()));
        let core = turn.as_ref().filter(|_| first).map(|turn| Usage {
            input_tokens: turn.get("input_tokens").copied(),
            output_tokens: turn.get("output_tokens").copied(),
            total_tokens: turn.get("total_tokens").copied(),
            ..Usage::default()
        });
        let mut extensions = Extensions::new();
        if let Some(turn) = turn {
            extensions.insert("com.agentclientprotocol.turn_usage".into(), encoded(&turn));
        }
        if let Some(context) = &self.context {
            extensions.insert("com.agentclientprotocol.context".into(), context.clone());
        }
        (core, extensions)
    }
}

fn turn(raw: &str) -> Option<BTreeMap<&'static str, u64>> {
    let fields = Fields::parse(raw)?;
    let mut turn = BTreeMap::from([
        ("total_tokens", fields.count("totaltokens")?),
        ("input_tokens", fields.count("inputtokens")?),
        ("output_tokens", fields.count("outputtokens")?),
    ]);
    for (native, key) in [
        ("thoughttokens", "thought_tokens"),
        ("cachedreadtokens", "cached_read_tokens"),
        ("cachedwritetokens", "cached_write_tokens"),
    ] {
        if let Some(value) = fields.count(native) {
            turn.insert(key, value);
        }
    }
    Some(turn)
}

fn cost(raw: &RawValue) -> Option<Box<RawValue>> {
    let fields = Fields::parse(raw.get())?;
    let amount: f64 = serde_json::from_str(fields.0.get("amount")?.get()).ok()?;
    let currency: String = serde_json::from_str(fields.0.get("currency")?.get()).ok()?;
    if currency.is_empty() {
        return None;
    }
    let json = serde_json::json!({"amount":amount, "currency":currency});
    RawValue::from_string(remarshal_any(&json.to_string()).ok()?).ok()
}

fn encoded(value: &impl Serialize) -> Box<RawValue> {
    serde_json::value::to_raw_value(value).expect("usage contains only valid JSON values")
}

struct Fields(BTreeMap<String, Box<RawValue>>);

impl Fields {
    fn parse(raw: &str) -> Option<Self> {
        serde_json::from_str(&replace_lone_surrogates(raw)).ok()
    }

    fn count(&self, key: &str) -> Option<u64> {
        let raw = self.0.get(key)?.get().trim();
        if !raw.bytes().all(|byte| byte.is_ascii_digit()) {
            return None;
        }
        raw.parse().ok()
    }
}

impl<'de> Deserialize<'de> for Fields {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct Visitor;
        impl<'de> serde::de::Visitor<'de> for Visitor {
            type Value = Fields;
            fn expecting(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                f.write_str("a JSON object")
            }
            fn visit_map<A: serde::de::MapAccess<'de>>(
                self,
                mut map: A,
            ) -> Result<Fields, A::Error> {
                let mut fields = BTreeMap::new();
                while let Some((name, value)) = map.next_entry::<String, Box<RawValue>>()? {
                    let name = name
                        .chars()
                        .map(|character| match character {
                            '\u{17f}' => 's',
                            '\u{212a}' => 'k',
                            other => other.to_ascii_lowercase(),
                        })
                        .collect();
                    fields.insert(name, value);
                }
                Ok(Fields(fields))
            }
        }
        deserializer.deserialize_map(Visitor)
    }
}
