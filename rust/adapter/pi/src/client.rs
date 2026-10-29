use std::sync::Arc;

use crossbeam_channel::Receiver;
use serde_json::value::RawValue;

use crate::native::{Command, ExtensionUiResponse, SessionState};
use crate::rpc::{Error, Inbound};

pub trait Client: Send + Sync {
    fn call(&self, command: &Command, want: bool) -> Result<Option<Box<RawValue>>, Error>;

    fn respond(&self, response: &ExtensionUiResponse) -> Result<(), Error>;

    fn inbound(&self) -> Receiver<Inbound>;

    fn done(&self) -> Receiver<()>;

    fn err(&self) -> Option<String>;

    fn close(&self) -> Result<(), Error>;
}

pub trait ClientFactory: Send + Sync {
    fn start(&self) -> Result<(Arc<dyn Client>, SessionState), Error>;
}

impl<F> ClientFactory for F
where
    F: Fn() -> Result<(Arc<dyn Client>, SessionState), Error> + Send + Sync,
{
    fn start(&self) -> Result<(Arc<dyn Client>, SessionState), Error> {
        self()
    }
}
