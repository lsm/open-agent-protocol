use std::sync::Arc;
use std::time::Instant;

use crossbeam_channel::Receiver;
use oap_adapter::Error;
use serde_json::value::RawValue;

use crate::process::ProcessConfig;
use crate::rpc::{self, Inbound};

pub const NATIVE_PROTOCOL: &str = "hermes adapter: invalid native protocol observation";

pub trait Client: Send + Sync {
    fn call(
        &self,
        method: &str,
        params: Box<RawValue>,
        deadline: Option<Instant>,
    ) -> Result<Box<RawValue>, rpc::Error>;

    fn inbound(&self) -> Receiver<Inbound>;

    fn done(&self) -> Receiver<()>;

    fn err(&self) -> Option<String>;

    fn close(&self) -> Result<(), String>;

    fn stored_session_id(&self) -> String {
        String::new()
    }
}

pub trait ClientFactory: Send + Sync {
    fn start(&self, deadline: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error>;

    fn start_with_reasoning(
        &self,
        _effort: &str,
        deadline: Option<Instant>,
    ) -> Result<(Arc<dyn Client>, String), Error> {
        self.start(deadline)
    }
}

#[derive(Clone, Debug, Default)]
pub struct ProcessFactory {
    pub config: ProcessConfig,
    pub model: String,
}

impl ClientFactory for ProcessFactory {
    fn start(&self, _deadline: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        Err(Error::Harness(String::new()))
    }
}
