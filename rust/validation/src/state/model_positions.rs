use oxap_protocol::*;

use super::{os, payload, s, State};
use crate::codes;

pub(super) struct Mark {
    model: String,
    run: String,
    sequence: u64,
    switch: String,
    index: usize,
}

pub(super) struct HeldPosition {
    at: (usize, usize),
    envelope: Envelope,
    session: String,
    model: String,
    position: Option<ModelEventPosition>,
    window: usize,
    arrived: bool,
}

impl State {
    pub(super) fn mark_model(
        &mut self,
        session: &str,
        model: &str,
        run: &str,
        sequence: u64,
        switch: &str,
        index: usize,
    ) {
        if model.is_empty() {
            return;
        }
        self.model_marks
            .entry(session.to_owned())
            .or_default()
            .push(Mark {
                model: model.to_owned(),
                run: run.to_owned(),
                sequence,
                switch: switch.to_owned(),
                index,
            });
    }

    pub(super) fn mark_started_model(&mut self, at: (usize, usize), e: &Envelope) {
        if e.envelope_type != EnvelopeType::RUN_STARTED {
            return;
        }
        let Some((run, sequence)) = self
            .runs
            .get(&os(&e.run_id))
            .filter(|run| !run.terminal)
            .zip(e.sequence)
        else {
            return;
        };
        if let Some(model) = run.mutation_model().map(str::to_owned) {
            let (session, id) = (run.session.clone(), os(&e.run_id));
            self.mark_model(&session, &model, &id, sequence, "", at.0);
        }
    }

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

    fn position_reached(&self, session: &str, position: &ModelEventPosition) -> bool {
        match &position.switch_request_id {
            Some(request) => self
                .requests
                .get(request.as_str())
                .is_some_and(|r| r.session == session && r.responded),
            None => position
                .run_id
                .as_ref()
                .and_then(|run| self.runs.get(run.as_str()))
                .is_some_and(|run| run.session == session && run.next > position.sequence),
        }
    }

    fn mark_at(&self, session: &str, position: &ModelEventPosition) -> Option<String> {
        self.model_marks.get(session)?.iter().find_map(|mark| {
            let matched = match &position.switch_request_id {
                Some(request) => mark.switch == request.as_str(),
                None => {
                    mark.run == position.run_id.as_ref().map_or("", |run| run.as_str())
                        && mark.sequence == position.sequence
                }
            };
            matched.then(|| mark.model.clone())
        })
    }

    fn foreign_position(
        &mut self,
        held: &HeldPosition,
        position: &ModelEventPosition,
        owner: &str,
    ) {
        let (pointer, message, related) = match &position.switch_request_id {
            Some(request) => (
                "/payload/as_of_model_event/switch_request_id",
                "catalog names a model switch owned by another session",
                request.as_str(),
            ),
            None => (
                "/payload/as_of_model_event/run_id",
                "catalog names a model event in a run owned by another session",
                position.run_id.as_ref().map_or("", |run| run.as_str()),
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

    fn current_mismatch(&mut self, held: &HeldPosition, expected: &str, message: &str) {
        let run = held
            .position
            .as_ref()
            .and_then(|p| p.run_id.as_ref())
            .map_or("", |run| run.as_str())
            .to_owned();
        self.add_expected(
            codes::SESSION_STATE_MISMATCH,
            held.at,
            &held.envelope,
            "/payload/current_model_id",
            message,
            expected,
            &held.model,
            &[run.as_str()],
        );
    }

    fn judge_position(&mut self, mut held: HeldPosition) {
        if let Some(position) = held.position.clone() {
            let owner = self.position_owner(&position);
            if let Some(owner) = owner.as_ref().filter(|owner| **owner != held.session) {
                return self.foreign_position(&held, &position, owner);
            }
            if held.model.is_empty() {
                if owner.is_none() {
                    held.arrived = false;
                    self.held_positions.push(held);
                }
                return;
            }
            if let Some(mark) = self.mark_at(&held.session, &position) {
                if mark != held.model {
                    self.current_mismatch(
                        &held,
                        &mark,
                        "catalog reports a model the session did not hold at the event it names",
                    );
                }
                return;
            }
            if !self.position_reached(&held.session, &position) {
                held.arrived = false;
                return self.held_positions.push(held);
            }
        }
        if held.model.is_empty() {
            return;
        }
        let window: Vec<String> = self
            .model_marks
            .get(&held.session)
            .map(|marks| {
                marks
                    .iter()
                    .skip(held.window)
                    .map(|mark| mark.model.clone())
                    .collect()
            })
            .unwrap_or_default();
        if let Some(last) = window.last().filter(|_| !window.contains(&held.model)) {
            let message = if held.arrived {
                "catalog reports a model the session never held while the query was in flight"
            } else {
                "catalog names an event that moved no model, and reports a model the session never held"
            };
            let last = last.clone();
            self.current_mismatch(&held, &last, message);
        }
    }

    pub(super) fn models_position(&mut self, at: (usize, usize), e: &Envelope) {
        let p: ModelsResponse = payload(e);
        let session = s(&p.session_id);
        let asked = self.requests.get(&os(&e.in_reply_to)).map(|r| r.index);
        let window = match (asked, self.model_marks.get(&session)) {
            (Some(asked), Some(marks)) => marks
                .iter()
                .filter(|mark| mark.index < asked)
                .count()
                .saturating_sub(1),
            _ => 0,
        };
        self.judge_position(HeldPosition {
            at,
            envelope: e.clone(),
            session,
            model: p.current_model_id,
            position: p.as_of_model_event,
            window,
            arrived: true,
        });
    }

    pub(super) fn reconcile_held_positions(&mut self) {
        for held in std::mem::take(&mut self.held_positions) {
            self.judge_position(held);
        }
    }
}
