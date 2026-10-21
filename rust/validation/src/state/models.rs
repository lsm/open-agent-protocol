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
}
