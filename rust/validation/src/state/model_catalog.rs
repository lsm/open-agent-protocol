use std::collections::HashSet;

use oap_protocol::*;

use super::controls::describe_refusal;
use super::{payload, s, State};
use crate::codes;

const MODEL_NOT_FOUND: &str = "model_not_found";

pub(super) struct SessionCatalog {
    revision: String,
    ids: HashSet<String>,
}

pub(super) struct HeldModel {
    pub(super) at: (usize, usize),
    pub(super) envelope: Envelope,
    pub(super) model: String,
    pub(super) revision: String,
    pub(super) admitted: bool,
}

impl State {
    pub(super) fn record_model_catalog(&mut self, e: &Envelope) {
        let revision = e.capability_revision.clone().unwrap_or_default();
        if revision.is_empty() || revision != self.current_capability || self.capabilities_stale {
            return;
        }
        let binding = matches!(
            self.features.get(FEATURE_MODELS_LIST),
            Some(level) if *level == SupportLevel::NATIVE || *level == SupportLevel::EMULATED
        );
        let p: ModelsResponse = payload(e);
        let session = s(&p.session_id);
        if !binding {
            self.model_catalogs.remove(&session);
            return;
        }
        let ids: HashSet<String> = p.models.into_iter().map(|model| model.id).collect();
        let held = self.held_models.remove(&session).unwrap_or_default();
        for entry in held.iter().filter(|entry| entry.revision == revision) {
            self.judge_held_model(entry, ids.contains(&entry.model));
        }
        self.model_catalogs
            .insert(session, SessionCatalog { revision, ids });
    }

    pub(super) fn catalog_lists(&self, session: &str, model: &str) -> Option<bool> {
        self.model_catalogs
            .get(session)
            .filter(|catalog| catalog.revision == self.current_capability)
            .map(|catalog| catalog.ids.contains(model))
    }

    pub(super) fn hold_model(&mut self, session: &str, held: HeldModel) {
        self.held_models
            .entry(session.to_owned())
            .or_default()
            .push(held);
    }

    fn judge_held_model(&mut self, entry: &HeldModel, listed: bool) {
        if !entry.admitted && listed {
            self.false_model_miss(entry.at, &entry.envelope, &entry.model);
        } else if !entry.admitted {
            self.model_miss_refusal(entry.at, &entry.envelope, &entry.model);
        } else if !listed {
            let message =
                "admission accepted a model the catalog served under this revision does not list";
            let submission = entry.envelope.in_reply_to.clone().unwrap_or_default();
            self.add_expected(
                codes::MODEL_NOT_IN_CATALOG,
                entry.at,
                &entry.envelope,
                "/payload/model_id",
                message,
                "a listed model id",
                &entry.model,
                &[submission.as_str()],
            );
        }
    }

    pub(super) fn false_model_miss(&mut self, at: (usize, usize), e: &Envelope, model: &str) {
        let err = payload::<ErrorResponse>(e).error;
        if err.code != MODEL_NOT_FOUND {
            return;
        }
        let message = "refusal reports a model the endpoint's own catalog lists";
        self.add_expected(
            codes::MODEL_NOT_IN_CATALOG,
            at,
            e,
            "/payload/error",
            message,
            "an admission",
            &describe_refusal(&err),
            &[model],
        );
    }

    fn model_miss_refusal(&mut self, at: (usize, usize), e: &Envelope, model: &str) {
        let err = payload::<ErrorResponse>(e).error;
        let named = err.details.get("model_id").and_then(|value| value.as_str()) == Some(model);
        if err.code == MODEL_NOT_FOUND && named {
            return;
        }
        let message =
            "refusal of an unlisted model does not tell the caller which id to stop sending";
        let expected = format!("{MODEL_NOT_FOUND} model_id={model}");
        self.add_expected(
            codes::MODEL_NOT_IN_CATALOG,
            at,
            e,
            "/payload/error",
            message,
            &expected,
            &describe_refusal(&err),
            &[model],
        );
    }
}
