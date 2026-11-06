use oap_protocol::*;

use super::runs::Run;
use super::snapshot_claims::{Claim, Kind};
use super::{os, payload, State};
use crate::codes;

impl Run {
    pub(super) fn mutation_model(&self) -> Option<&str> {
        self.controls
            .model_control()
            .and_then(|(scope, model)| (scope == SCOPE_SESSION).then_some(model))
    }
}

impl State {
    pub(super) fn capture_model(&mut self, at: (usize, usize), e: &Envelope) {
        let p: SessionState = payload(e);
        let window = self
            .requests
            .get(&os(&e.in_reply_to))
            .map_or(at.0, |r| r.index);
        let position = p
            .as_of
            .as_ref()
            .and_then(|capture| capture.model_run_sequence.as_ref());
        let anchor = position.and_then(|position| self.runs.get(&os(&position.run_id)));
        let latest = self
            .runs
            .values()
            .filter(|run| {
                run.session == p.session_id.as_str()
                    && run.started_at.is_some_and(|start| start <= window)
                    && run.mutation_model().is_some()
                    && anchor.is_none_or(|anchor| run.started_at > anchor.started_at)
            })
            .max_by_key(|run| run.started_at);
        let pointer = "/payload/as_of/model_run_sequence";
        let mut code = codes::SESSION_STATE_MISMATCH;
        let mut message = None;
        let mut expected = None;
        match position {
            None => expected = latest.and_then(Run::mutation_model).map(str::to_owned),
            Some(position) if position.genesis() => {
                if latest.is_some() {
                    message = Some("a model-affecting run superseded the genesis capture");
                } else {
                    expected = self
                        .session_models
                        .get(p.session_id.as_str())
                        .and_then(|track| track.opening.clone());
                }
            }
            Some(position) => match anchor.filter(|run| run.session == p.session_id.as_str()) {
                None => message = Some("model capture must name a run admitted on this session"),
                Some(run) if !run.started => {
                    self.snapshot_claims.push(Claim {
                        kind: Kind::Model(p.current_model_id.clone()),
                        run: os(&position.run_id),
                        sequence: Some(position.sequence),
                        at,
                        envelope: e.clone(),
                    });
                }
                Some(run) if run.start_sequence != Some(position.sequence) => {
                    message = Some("model capture must name the run's start sequence")
                }
                Some(run) if run.mutation_model().is_none() => {
                    message = Some("model capture must name a session model mutation")
                }
                Some(_) if latest.is_some() => {
                    message = Some("a later model-affecting run superseded the capture")
                }
                Some(run) => expected = run.mutation_model().map(str::to_owned),
            },
        }
        let mut pointer = pointer;
        if expected.is_some_and(|expected| expected != p.current_model_id) {
            code = codes::PREMATURE_SESSION_MUTATION;
            pointer = "/payload/current_model_id";
            message = Some("the model must match the session mutation at the capture position");
        }
        if let Some(message) = message {
            self.add(code, at, e, pointer, message);
        }
    }
}
