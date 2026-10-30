use std::collections::HashSet;

use oap_protocol::*;

use super::{payload, s, State};

pub(super) struct SessionCatalog {
    revision: String,
    ids: HashSet<String>,
}

impl State {
    pub(super) fn record_model_catalog(&mut self, e: &Envelope) {
        let revision = e.capability_revision.clone().unwrap_or_default();
        if revision.is_empty() || revision != self.current_capability {
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
        let ids = p.models.into_iter().map(|model| model.id).collect();
        self.model_catalogs
            .insert(session, SessionCatalog { revision, ids });
    }

    pub(super) fn unlisted_model(&self, session: &str, model: &str) -> bool {
        self.model_catalogs.get(session).is_some_and(|catalog| {
            catalog.revision == self.current_capability && !catalog.ids.contains(model)
        })
    }
}
