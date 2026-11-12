use oap_adapter::{Error, EventStream, InteractionResolution, Recovery, ResumeRequest};
use oap_protocol::{RunCancelResponse, RunId, SessionState};

pub struct Session;

fn unported() -> Error {
    Error::Harness(String::new())
}

impl Session {
    pub fn state(&self) -> Result<SessionState, Error> {
        Err(unported())
    }

    pub fn resolve(&self, _: InteractionResolution) -> Result<(), Error> {
        Err(unported())
    }

    pub fn cancel(&self, _: &RunId) -> Result<RunCancelResponse, Error> {
        Err(unported())
    }

    pub fn resume(&self, _: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        Err(unported())
    }

    pub fn close(&self) -> Result<(), Error> {
        Err(unported())
    }

    pub fn native_session_id(&self) -> String {
        String::new()
    }
}
