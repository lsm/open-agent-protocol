use std::collections::BTreeMap;
use std::sync::Arc;

use oap_adapter::runtime::Runtime;
use oap_adapter::{Descriptor, Error, OpenRequest};
use oap_protocol::ReasoningLevel;
use serde_json::Value;

use crate::client::ClientFactory;
use crate::process::ProcessConfig;

#[derive(Clone, Default)]
pub struct Config {
    pub factory: Option<Arc<dyn ClientFactory>>,
    pub process: ProcessConfig,
    pub model: String,
    pub approval_policy: String,
    pub sandbox: String,
    pub resume_thread_id: String,
    pub journal_capacity: usize,
    pub runtime: Runtime,
}

#[derive(Clone)]
pub struct Adapter {
    config: Config,
}

impl Adapter {
    pub fn new(config: Config) -> Result<Self, Error> {
        Ok(Self { config })
    }

    pub fn describe(&self) -> Descriptor {
        let _ = &self.config;
        Descriptor::default()
    }
}

pub fn thread_config(_request: &OpenRequest) -> Result<BTreeMap<String, Value>, Error> {
    Ok(BTreeMap::new())
}

pub fn reasoning_level(_effort: &str) -> ReasoningLevel {
    ReasoningLevel::default()
}
