use std::collections::BTreeMap;

use oap_adapter::runtime::Runtime;
use oap_adapter::{Descriptor, Error, JournalDescriptor};
use oap_protocol::{
    CapabilityDescriptor, EndpointDescriptor, FeatureSupport, SupportLevel, PROFILE, VERSION,
};

use crate::process::ProcessConfig;

const ENDPOINT_ID: &str = "hermes.gateway";

#[derive(Clone, Default)]
pub struct Config {
    pub process: ProcessConfig,
    pub model: String,
    pub journal_capacity: usize,
    pub runtime: Runtime,
}

#[derive(Clone)]
pub struct Adapter {
    config: Config,
}

pub fn pin() -> &'static oap_harness::Version {
    oap_harness::current("hermes").expect("embedded catalog pins hermes")
}

impl Adapter {
    pub fn new(_: Config) -> Result<Self, Error> {
        Err(Error::Harness(String::new()))
    }

    pub fn describe(&self) -> Descriptor {
        Descriptor {
            capabilities: CapabilityDescriptor {
                endpoint: EndpointDescriptor {
                    id: ENDPOINT_ID.into(),
                    ..EndpointDescriptor::default()
                },
                protocol_versions: vec![VERSION.into()],
                profiles: vec![PROFILE.into()],
                features: BTreeMap::<String, FeatureSupport>::new(),
                ..CapabilityDescriptor::default()
            },
            capability_revision: pin().capability_revision.clone(),
            journal: JournalDescriptor {
                capacity: self.config.journal_capacity,
                replay: SupportLevel::DEGRADED,
                ..JournalDescriptor::default()
            },
            ..Descriptor::default()
        }
    }
}
