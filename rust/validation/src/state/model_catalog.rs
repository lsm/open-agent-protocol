use std::collections::{BTreeMap, HashSet};

use oap_protocol::*;

use super::controls::describe_refusal;
use super::{payload, s, State};
use crate::codes;

const MODEL_NOT_FOUND: &str = "model_not_found";

pub(super) struct SessionCatalog {
    revision: String,
    ids: HashSet<String>,
    providers: HashSet<String>,
    models_served: serde_json::Value,
    providers_served: serde_json::Value,
}

fn served<'a, T: serde::Serialize + 'a>(
    entries: impl Iterator<Item = (&'a str, &'a T)>,
) -> serde_json::Value {
    serde_json::to_value(entries.collect::<BTreeMap<_, _>>()).unwrap_or_default()
}

pub(super) struct HeldModel {
    pub(super) at: (usize, usize),
    pub(super) envelope: Envelope,
    pub(super) model: String,
    pub(super) revision: String,
    pub(super) admitted: bool,
}

impl State {
    pub(super) fn record_model_catalog(&mut self, at: (usize, usize), e: &Envelope) {
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
        let declared: HashSet<&str> = p
            .providers
            .iter()
            .map(|provider| provider.id.as_str())
            .collect();
        let omitted: Vec<String> = self
            .attached_providers
            .get(&session)
            .map(|attached| {
                let mut aliases: Vec<String> = attached
                    .keys()
                    .filter(|alias| !declared.contains(alias.as_str()))
                    .cloned()
                    .collect();
                aliases.sort();
                aliases
            })
            .unwrap_or_default();
        for alias in omitted {
            let message = "catalog omitted an attached provider";
            self.add_expected(
                codes::UNMATCHED_PROVIDER,
                at,
                e,
                "/payload/providers",
                message,
                &alias,
                "absent",
                &[],
            );
        }
        if !binding {
            self.model_catalogs.remove(&session);
            return;
        }
        let models_served = served(p.models.iter().map(|model| (model.id.as_str(), model)));
        let providers_served = served(
            p.providers
                .iter()
                .map(|provider| (provider.id.as_str(), provider)),
        );
        let changed = self
            .model_catalogs
            .get(&session)
            .filter(|prior| prior.revision == revision)
            .and_then(|prior| {
                if prior.models_served != models_served {
                    Some("/payload/models")
                } else if prior.providers_served != providers_served {
                    Some("/payload/providers")
                } else {
                    None
                }
            });
        if let Some(pointer) = changed {
            self.add_expected(
                codes::UNANNOUNCED_CATALOG_CHANGE,
                at,
                e,
                pointer,
                "catalog changed under one capability revision without a capabilities.updated",
                &format!("the catalog served under {revision}"),
                "a different catalog",
                &[],
            );
        }
        let mut providers: HashSet<String> = p
            .providers
            .iter()
            .map(|provider| provider.id.clone())
            .collect();
        providers.extend(
            p.models
                .iter()
                .filter(|model| !model.provider_id.is_empty())
                .map(|model| model.provider_id.clone()),
        );
        let ids: HashSet<String> = p.models.into_iter().map(|model| model.id).collect();
        let held = self.held_models.remove(&session).unwrap_or_default();
        for entry in held.iter().filter(|entry| entry.revision == revision) {
            self.judge_held_model(entry, ids.contains(&entry.model));
        }
        self.model_catalogs.insert(
            session,
            SessionCatalog {
                revision,
                ids,
                providers,
                models_served,
                providers_served,
            },
        );
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

    pub(super) fn catalog_serves_provider(&self, session: &str, alias: &str) -> bool {
        self.model_catalogs
            .get(session)
            .filter(|catalog| catalog.revision == self.current_capability)
            .is_some_and(|catalog| catalog.providers.contains(alias))
    }
}
