use oap_protocol::*;

use super::{payload, s, State};
use crate::codes;

pub(super) struct HeldPosition {
    at: (usize, usize),
    envelope: Envelope,
    session: String,
    position: ModelEventPosition,
}

impl State {
    fn position_owner(&self, position: &ModelEventPosition) -> Option<String> {
        match &position.switch_request_id {
            Some(request) => self
                .requests
                .get(request.as_str())
                .filter(|r| r.envelope_type == EnvelopeType::SESSION_MODEL_SWITCH_REQUEST)
                .map(|r| r.session.clone()),
            None => position
                .run_id
                .as_ref()
                .and_then(|run| self.runs.get(run.as_str()))
                .map(|run| run.session.clone()),
        }
    }

    fn foreign_position(&mut self, held: &HeldPosition, owner: &str) {
        let (pointer, message, related) = match &held.position.switch_request_id {
            Some(request) => (
                "/payload/as_of_model_event/switch_request_id",
                "catalog names a model switch owned by another session",
                request.as_str(),
            ),
            None => (
                "/payload/as_of_model_event/run_id",
                "catalog names a model event in a run owned by another session",
                held.position.run_id.as_ref().map_or("", |run| run.as_str()),
            ),
        };
        self.add_expected(
            codes::SCOPE_MISMATCH,
            held.at,
            &held.envelope,
            pointer,
            message,
            &held.session,
            owner,
            &[related],
        );
    }

    fn judge_position(&mut self, held: HeldPosition) {
        match self.position_owner(&held.position) {
            None => self.held_positions.push(held),
            Some(owner) if owner != held.session => self.foreign_position(&held, &owner),
            Some(_) => {}
        }
    }

    pub(super) fn models_position(&mut self, at: (usize, usize), e: &Envelope) {
        let p: ModelsResponse = payload(e);
        if let Some(position) = p.as_of_model_event {
            self.judge_position(HeldPosition {
                at,
                envelope: e.clone(),
                session: s(&p.session_id),
                position,
            });
        }
    }

    pub(super) fn reconcile_held_positions(&mut self) {
        for held in std::mem::take(&mut self.held_positions) {
            self.judge_position(held);
        }
    }
}
