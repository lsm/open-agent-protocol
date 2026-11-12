use oap_adapter::{Descriptor, Error, OpenRequest};
use oap_protocol::CompactionPolicy;

use crate::process::ProcessConfig;

#[derive(Clone, Debug, Default)]
pub struct Config {
    pub process: ProcessConfig,
    pub provider: String,
    pub model: String,
    pub max_tokens: Option<i64>,
    pub journal_capacity: usize,
}

#[derive(Clone, Debug)]
pub struct Launch {
    pub process: ProcessConfig,
    pub patch: Option<String>,
}

pub struct Adapter {
    config: Config,
}

impl Adapter {
    pub fn new(config: Config) -> Result<Self, Error> {
        Ok(Self { config })
    }

    pub fn describe(&self) -> Descriptor {
        Descriptor::default()
    }

    pub fn launch(&self, _request: &OpenRequest) -> Result<Launch, Error> {
        Ok(Launch {
            process: self.config.process.clone(),
            patch: None,
        })
    }
}

pub fn compaction_patch(_policy: Option<&CompactionPolicy>) -> Result<Option<String>, Error> {
    Ok(None)
}
