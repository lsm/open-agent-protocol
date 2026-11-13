use std::time::{Duration, Instant};

use crossbeam_channel::Receiver;

use crate::native::ReadyPayload;
use crate::rpc::{error, Error, Fault};
use oap_adapter::jsonrpc::RpcClient;

#[derive(Clone, Debug, Default)]
pub struct ProcessConfig {
    pub path: String,
    pub args: Vec<String>,
    pub dir: String,
    pub env: Option<Vec<String>>,
    pub frame_limit: usize,
    pub queue_capacity: usize,
    pub write_queue_capacity: usize,
    pub stderr_limit: usize,
    pub exit_timeout: Option<Duration>,
}

pub struct Process {
    pub client: RpcClient,
    pub ready: ReadyPayload,
}

impl Process {
    pub fn start(config: &ProcessConfig, deadline: Option<Instant>) -> Result<Self, Error> {
        Self::start_with(config, deadline, &crossbeam_channel::never())
    }

    pub fn start_with(
        _: &ProcessConfig,
        _: Option<Instant>,
        _: &Receiver<()>,
    ) -> Result<Self, Error> {
        Err(error(Fault::Closed))
    }

    pub fn exited(&self) -> Receiver<()> {
        crossbeam_channel::never()
    }

    pub fn wait_error(&self) -> Option<String> {
        None
    }

    pub fn close(&self) -> Result<(), String> {
        Ok(())
    }
}
