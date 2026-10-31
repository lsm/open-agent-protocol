use std::collections::HashSet;

use oap_protocol::*;

use super::{payload, State};
use crate::codes;

impl State {
    pub(super) fn models_response_shape(&mut self, at: (usize, usize), e: &Envelope) {
        let p: ModelsResponse = payload(e);
        let mut ids = HashSet::new();
        let mut duplicate = None;
        let mut defaults = 0usize;
        for model in &p.models {
            if !ids.insert(model.id.as_str()) && duplicate.is_none() {
                duplicate = Some(model.id.as_str());
            }
            if model.default {
                defaults += 1;
            }
        }
        if let Some(duplicate) = duplicate {
            self.add_expected(
                codes::DUPLICATE_MODEL_ID, at, e, "/payload/models",
                "catalog lists two descriptors under one id, so an accepted model_id denotes neither",
                "one descriptor per id", duplicate, &[],
            );
        }
        if defaults > 1 {
            self.add_expected(
                codes::AMBIGUOUS_DEFAULT_MODEL,
                at,
                e,
                "/payload/models",
                "catalog marks more than one model as the default",
                "at most one default",
                &defaults.to_string(),
                &[],
            );
        }
        if !p.providers.is_empty() {
            self.catalog_providers(at, e, &p);
        }
        if !p.current_model_id.is_empty() && !ids.contains(p.current_model_id.as_str()) {
            self.add_expected(
                codes::MODEL_NOT_IN_CATALOG,
                at,
                e,
                "/payload/current_model_id",
                "catalog reports a current model it does not list",
                "one of the catalog's own ids",
                &p.current_model_id,
                &[],
            );
        }
    }

    fn catalog_providers(&mut self, at: (usize, usize), e: &Envelope, p: &ModelsResponse) {
        let mut declared = HashSet::new();
        for provider in &p.providers {
            if !declared.insert(provider.id.as_str()) {
                let message = "catalog lists two provider descriptors under one id, so a provider_id resolves to neither";
                self.add_expected(
                    codes::DUPLICATE_PROVIDER,
                    at,
                    e,
                    "/payload/providers",
                    message,
                    "one descriptor per id",
                    &provider.id,
                    &[],
                );
            }
        }
        let mut reported = HashSet::new();
        for model in &p.models {
            let provider = model.provider_id.as_str();
            if provider.is_empty() || declared.contains(provider) || !reported.insert(provider) {
                continue;
            }
            let message = "a model attributes itself to a provider the catalog does not declare";
            self.add_expected(
                codes::UNMATCHED_PROVIDER,
                at,
                e,
                "/payload/models",
                message,
                "a provider declared in providers",
                provider,
                &[model.id.as_str()],
            );
        }
    }
}
