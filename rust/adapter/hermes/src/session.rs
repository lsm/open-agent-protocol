use oap_adapter::Error;
use oap_protocol::SessionState;

pub struct Session;

impl Session {
    pub fn native_session_id(&self) -> String {
        String::new()
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        Err(Error::SessionClosed)
    }

    pub fn close(&self) -> Result<(), Error> {
        Ok(())
    }
}
