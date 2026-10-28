use oap_protocol::*;

use super::controls::Controls;
use super::{os, payload, s, State};
use crate::codes;

#[derive(Default)]
pub(super) struct SessionModel {
    current: Option<String>,
    guarded: Option<String>,
}

impl State {
    pub(super) fn apply_model_control(&mut self, session: &str, controls: &Controls) {
        let Some((scope, model)) = controls.model_control() else {
            return;
        };
        let track = self.session_models.entry(session.to_owned()).or_default();
        if scope == SCOPE_RUN {
            track.guarded = track.current.clone();
        } else if scope == SCOPE_SESSION {
            track.current = Some(model.to_owned());
        }
    }

    pub(super) fn promote_controls(&mut self, run_id: &str, was_started: bool) {
        let Some(run) = self.runs.get_mut(run_id) else {
            return;
        };
        if was_started || !run.started || !run.deferred_model {
            return;
        }
        run.deferred_model = false;
        let (session, controls) = (run.session.clone(), run.controls.clone());
        self.apply_model_control(&session, &controls);
    }

    pub(super) fn open_model(&mut self, e: &Envelope) {
        let p: SessionState = payload(e);
        if !p.current_model_id.is_empty() {
            let track = self.session_models.entry(s(&p.session_id)).or_default();
            track.current = Some(p.current_model_id);
        }
    }

    pub(super) fn switched_model(&mut self, e: &Envelope) {
        let p: SessionModelSwitchResponse = payload(e);
        let Some(request) = self.requests.get(&os(&e.in_reply_to)) else {
            return;
        };
        let asked: SessionModelSwitchRequest = payload(&request.envelope);
        if asked.session_id != p.session_id || asked.model_id != p.model_id {
            return;
        }
        let track = self.session_models.entry(s(&p.session_id)).or_default();
        track.current = Some(p.model_id);
        track.guarded = None;
    }

    pub(super) fn state_model(&mut self, at: (usize, usize), e: &Envelope) {
        let p: SessionState = payload(e);
        let track = self.session_models.entry(s(&p.session_id)).or_default();
        let moved = track
            .guarded
            .clone()
            .filter(|expected| *expected != p.current_model_id);
        track.current = Some(p.current_model_id.clone());
        if let Some(expected) = moved {
            let message = "a per_run model selection moved the session default";
            let pointer = "/payload/current_model_id";
            let actual = p.current_model_id;
            self.add_expected(
                codes::UNAPPLIED_CONTROL,
                at,
                e,
                pointer,
                message,
                &expected,
                &actual,
                &[],
            );
        }
    }
}
