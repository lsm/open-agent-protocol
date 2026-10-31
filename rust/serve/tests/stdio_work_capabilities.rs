mod support;

use std::collections::BTreeMap;
use std::sync::Arc;

use oap_adapter::{Adapter, BoxFuture, Descriptor, Error, OpenRequest, Session};
use oap_protocol::{CapabilityDescriptor, FeatureSupport, SupportLevel};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::{json, Value};

struct Advertising(BTreeMap<String, FeatureSupport>, &'static str);

impl Adapter for Advertising {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        let descriptor = Descriptor {
            capability_revision: self.1.into(),
            capabilities: CapabilityDescriptor {
                features: self.0.clone(),
                ..CapabilityDescriptor::default()
            },
            ..Descriptor::default()
        };
        Box::pin(async move { Ok(descriptor) })
    }

    fn open(&self, _: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async { Err(Error::SessionClosed) })
    }
}

fn level(level: SupportLevel) -> FeatureSupport {
    FeatureSupport {
        level,
        ..FeatureSupport::default()
    }
}

fn capabilities(registry: Registry) -> Value {
    let hub = Hub::new(registry);
    let output = support::Captured::default();
    serve(
        &hub,
        std::io::Cursor::new(b"{\"id\":1,\"op\":\"work.capabilities\"}\n".to_vec()),
        output.clone(),
        &StdioOptions::default(),
    )
    .unwrap();
    let line: Value = serde_json::from_slice(&output.bytes()).unwrap();
    assert_eq!(line["ok"], true, "{line}");
    line["result"].clone()
}

#[test]
fn work_capabilities_lists_each_adapters_verbs_and_native_reach() {
    let answer = capabilities(Registry::default_registry());
    assert_eq!(
        answer,
        json!({"adapters": [{
            "adapter": "memory",
            "any_directory": false,
            "verbs": ["work.list", "work.start", "work.send", "work.status", "work.stop", "work.read"],
            "native": {"list": false, "read": false, "search": false},
        }]})
    );
}

#[test]
fn a_verb_resting_on_a_withdrawn_feature_is_not_listed_and_an_unprobed_adapter_is_unavailable() {
    let mut registry = Registry::new();
    let features = [
        ("session.message.submit", level(SupportLevel::UNAVAILABLE)),
        ("run.cancel", level(SupportLevel::NATIVE)),
    ]
    .into_iter()
    .map(|(key, support)| (key.to_owned(), support))
    .collect();
    registry
        .register("adv", Arc::new(Advertising(features, "rev-1")))
        .unwrap();
    registry
        .register("bare", Arc::new(Advertising(BTreeMap::new(), "")))
        .unwrap();
    let answer = capabilities(registry);
    assert_eq!(
        answer["adapters"][0]["verbs"],
        json!(["work.list", "work.status", "work.stop", "work.read"])
    );
    assert_eq!(
        answer["unavailable"],
        json!([{"adapter": "bare", "message": "adapter descriptor carries no capability revision"}])
    );
}
