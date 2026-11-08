use std::sync::{Arc, Mutex, MutexGuard};

use oap_adapter::{refuse_unadvertised_controls, Error, EventStream, SubmitRequest};
use oap_protocol::{MessageId, MessageRole, MessageSubmitResponse, RunId, SessionStatus};

use crate::native::{ContentBlock, PromptParams};
use crate::session::{Inner, Session};
use crate::{TextRun, TextRunContext};

pub(crate) struct Run {
    pub(crate) id: RunId,
    pub(crate) text: Arc<Mutex<TextRun>>,
    pub(crate) admitted: crossbeam_channel::Receiver<()>,
}

impl Run {
    pub(crate) fn lock(&self) -> MutexGuard<'_, TextRun> {
        self.text.lock().unwrap_or_else(|p| p.into_inner())
    }
}

impl Inner {
    pub(crate) fn refresh(&mut self) {
        if self.process.is_none() {
            return;
        }
        let Some(run) = self.active.clone() else {
            return;
        };
        let state = run.lock().state();
        self.refresh_run(&run.id, state);
    }

    pub(crate) fn refresh_run(&mut self, id: &RunId, state: crate::TextRunState) {
        if state.closed {
            self.settle_permission_routes();
        }
        let cursor = state.sequence.to_string();
        if state.status.is_empty() || self.state.transcript_cursor == cursor {
            return;
        }
        self.state.status = if state.closed {
            SessionStatus::IDLE
        } else {
            SessionStatus::RUNNING
        };
        self.state.active_run_id = if state.closed { None } else { Some(id.clone()) };
        self.state.transcript_cursor = cursor;
        self.state.updated_at_ms = state.updated_at_ms;
    }
}

struct Admission<'a> {
    session: &'a Session,
    run: Arc<Run>,
    pending: bool,
    _release: crossbeam_channel::Sender<()>,
}

impl Drop for Admission<'_> {
    fn drop(&mut self) {
        if self.pending {
            let mut inner = self.session.lock();
            let _ = self
                .run
                .lock()
                .prompt_error("ACP prompt ended before acceptance", true, 0);
            if inner.process.is_some() {
                inner.active = None;
                inner.state.status = SessionStatus::IDLE;
                inner.state.active_run_id = None;
                inner.state.updated_at_ms = self.session.runtime.now_ms();
            }
        }
    }
}

impl Session {
    fn new_id(&self, kind: &str) -> String {
        self.runtime.new_id(kind)
    }

    pub(crate) async fn submit_text(
        &self,
        submit: SubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        let _submitting = self.submitting.lock().await;
        let request = submit.request;
        refuse_unadvertised_controls(&request, &[])?;
        for feature in ["session.message.submit", "run.streaming"] {
            if !request.allows_degraded(feature) {
                return Err(Error::DegradedControl(feature.into()));
            }
        }
        if request.session_id.as_str().is_empty()
            || request.messages.is_empty()
            || !matches!(request.delivery.as_str(), "" | "auto")
        {
            return Err(Error::InvalidSubmission);
        }
        let mut prompt = Vec::new();
        let mut message_ids = Vec::new();
        for message in request.messages {
            if message.role != MessageRole::USER {
                return Err(Error::UnsupportedInput);
            }
            prompt.push(ContentBlock {
                kind: "text".into(),
                text: message.content.text().ok_or(Error::UnsupportedInput)?,
                ..ContentBlock::default()
            });
            message_ids.push(
                message
                    .id
                    .filter(|id| !id.as_str().is_empty())
                    .unwrap_or_else(|| MessageId::from(self.new_id("message"))),
            );
        }
        let (release, admitted) = crossbeam_channel::bounded(0);
        let (process, run, permissions) = {
            let mut inner = self.lock();
            let process = inner.process.clone().ok_or(Error::SessionClosed)?;
            if request.session_id != inner.state.session_id {
                return Err(Error::RunNotFound);
            }
            if inner
                .active
                .as_ref()
                .is_some_and(|run| !run.lock().state().closed)
            {
                return Err(Error::RunActive);
            }
            inner.refresh();
            while let Some(index) = inner.workers.iter().position(|worker| worker.is_finished()) {
                let _ = inner.workers.swap_remove(index).join();
            }
            let ids = self.runtime.clone();
            let id: RunId = self.new_id("run").into();
            let run = Run {
                id: id.clone(),
                text: Arc::new(Mutex::new(
                    TextRun::new_in_scope(
                        TextRunContext {
                            session_id: inner.state.session_id.clone(),
                            native_id: self.native_id.clone(),
                            run_id: id,
                            message_id: format!(
                                "{}/{}",
                                inner.state.session_id,
                                self.new_id("message")
                            )
                            .into(),
                            capability_revision: Some(self.revision.clone()),
                        },
                        move |kind| ids.new_id(kind),
                        inner.usage.clone(),
                    )
                    .with_runtime(self.runtime.clone()),
                )),
                admitted,
            };
            run.lock().tools.seen = inner.tool_history.clone();
            run.lock().journal = Some(inner.journal.clone());
            let run = Arc::new(run);
            inner.state.status = SessionStatus::RUNNING;
            inner.state.active_run_id = Some(run.id.clone());
            inner.state.updated_at_ms = self.runtime.now_ms();
            inner.active = Some(run.clone());
            (
                process,
                run,
                inner.permissions.clone().ok_or(Error::SessionClosed)?,
            )
        };
        let mut admission = Admission {
            session: self,
            run: run.clone(),
            pending: true,
            _release: release,
        };
        let mut call = process.prompt(
            PromptParams {
                session_id: self.native_id.clone(),
                prompt,
            },
            None,
        );
        let admitted = call.admitted().await;
        let mut inner = self.lock();
        if inner.process.is_none() {
            return Err(Error::SessionClosed);
        }
        admitted.map_err(Error::harness)?;
        let stream = run.lock().start(0)?;
        let state = run.lock().state();
        inner.state.transcript_cursor = state.sequence.to_string();
        inner.state.updated_at_ms = state.updated_at_ms;
        let response = MessageSubmitResponse {
            session_id: inner.state.session_id.clone(),
            accepted: true,
            submission_id: self.new_id("submission").into(),
            requested_delivery: "auto".into(),
            effective_delivery: "start".into(),
            delivery_resolution: "session_idle".into(),
            admission: "started".into(),
            run_id: Some(run.id.clone()),
            status: "running".into(),
            message_ids,
            ..MessageSubmitResponse::default()
        };
        let shared = self.inner.clone();
        inner.workers.push(std::thread::spawn(move || {
            let result = futures::executor::block_on(call);
            let work = match result {
                Ok(result) => permissions.complete(run.text.clone(), result),
                Err(error) => permissions.prompt_failure(run.text.clone(), error),
            };
            let _ = futures::executor::block_on(work);
            shared.lock().unwrap_or_else(|p| p.into_inner()).refresh();
        }));
        admission.pending = false;
        Ok((response, stream))
    }
}
