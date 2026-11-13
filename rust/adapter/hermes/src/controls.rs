use oap_adapter::{Error, EventStream, Recovery, ResumeRequest};
use oap_protocol::{
    RunCancelResponse, RunId, SessionSettingsUpdateRequest, SessionSettingsUpdateResponse,
    SessionState,
};

use crate::session::Shared;

impl Shared {
    pub(crate) fn cancel(&self, _id: &RunId) -> Result<RunCancelResponse, Error> {
        Err(Error::RunNotFound)
    }

    pub(crate) fn update_settings(
        &self,
        _request: SessionSettingsUpdateRequest,
    ) -> Result<(SessionSettingsUpdateResponse, SessionState), Error> {
        Err(Error::RunActive)
    }

    pub(crate) fn resume(&self, _request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        Err(Error::RunNotFound)
    }
}
