use oap_adapter::Error;
use oap_protocol::SessionState;

pub struct Session {
    thread_id: String,
}

impl Session {
    pub fn native_session_id(&self) -> String {
        self.thread_id.clone()
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        Ok(SessionState::default())
    }

    pub fn close(&self) -> Result<(), Error> {
        Ok(())
    }
}
