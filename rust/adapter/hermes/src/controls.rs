use oap_adapter::{refuse_unadvertised_live_settings, Error, EventStream, Recovery, ResumeRequest};
use oap_protocol::{
    CapabilityDescriptor, RunCancelResponse, RunId, RunStatus, SessionSettingsUpdateRequest,
    SessionSettingsUpdateResponse, SessionState,
};

use crate::adapter::{features, set_reasoning};
use crate::client::call;
use crate::native::{InterruptParams, InterruptResult, METHOD_SESSION_INTERRUPT};
use crate::session::{hold, Shared};

impl Shared {
    pub(crate) fn cancel(&self, id: &RunId) -> Result<RunCancelResponse, Error> {
        let session_id = {
            let inner = self.lock();
            if !inner
                .run
                .as_ref()
                .is_some_and(|run| run.started && run.id == *id)
            {
                return Err(Error::RunNotFound);
            }
            inner.state.session_id.clone()
        };
        let params = InterruptParams {
            session_id: self.native_id.clone(),
        };
        call::<InterruptResult>(
            self.client.as_ref(),
            METHOD_SESSION_INTERRUPT,
            &params,
            None,
        )
        .map_err(Error::harness)?;
        Ok(RunCancelResponse {
            session_id,
            run_id: id.clone(),
            accepted: true,
            status: RunStatus::CANCELLING,
        })
    }

    pub(crate) fn update_settings(
        &self,
        request: SessionSettingsUpdateRequest,
    ) -> Result<(SessionSettingsUpdateResponse, SessionState), Error> {
        let descriptor = CapabilityDescriptor {
            features: features(),
            ..CapabilityDescriptor::default()
        };
        refuse_unadvertised_live_settings(&request, &descriptor)?;
        let _prompt = hold(&self.prompt);
        {
            let inner = self.lock();
            if inner.closed || inner.unusable {
                return Err(Error::SessionClosed);
            }
            if request.session_id != inner.state.session_id {
                return Err(Error::RunNotFound);
            }
            if inner.run.is_some() {
                return Err(Error::RunActive);
            }
        }
        let level = request.reasoning_level;
        if !level.is_empty() {
            set_reasoning(self.client.as_ref(), &self.native_id, &level)?;
        }
        let mut inner = self.lock();
        let mut response = SessionSettingsUpdateResponse {
            session_id: inner.state.session_id.clone(),
            ..SessionSettingsUpdateResponse::default()
        };
        if !level.is_empty() {
            response.previous_reasoning_level =
                std::mem::replace(&mut inner.state.reasoning_level, level.clone());
            response.reasoning_level = level;
        }
        inner.state.updated_at_ms = self.runtime.now_ms();
        Ok((response, inner.state.clone()))
    }

    pub(crate) fn resume(&self, request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        let inner = self.lock();
        if inner.closed {
            return Err(Error::SessionClosed);
        }
        let run = request.run_id.as_str();
        let live = inner
            .run
            .as_ref()
            .filter(|live| live.started && live.id == request.run_id);
        let latest = match live {
            Some(live) => live.next - 1,
            None => self.journal.ended(run).ok_or(Error::RunNotFound)?,
        };
        if request.after_sequence > latest {
            return Err(Error::ReplayCursorFuture);
        }
        let (recovery, replay) =
            self.journal
                .resume(inner.state.clone(), run, request.after_sequence, latest);
        match recovery.replay_gap {
            Some(gap) => Err(Error::ReplayGap(gap)),
            None => Ok((recovery, replay)),
        }
    }
}
