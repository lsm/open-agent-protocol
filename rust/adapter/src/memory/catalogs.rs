use oap_protocol::*;

use super::admit::{compaction_threshold, effective_policy, session_sources};
use super::descriptor::features;
use super::session::Inner;
use super::{
    declared_sources, scripted_catalog, CAPABILITY_REVISION, MODEL_PRIMARY, MODEL_SECONDARY,
};
use crate::{refuse_unadvertised_live_settings, Catalog, Error, ToolCatalog};

pub fn model_catalog() -> Vec<ModelDescriptor> {
    let model = |id: &str, name: &str, default: bool| ModelDescriptor {
        id: id.to_owned(),
        display_name: name.to_owned(),
        provider_id: "reference".to_owned(),
        context_window: 8192,
        default,
        ..ModelDescriptor::default()
    };
    vec![
        model(MODEL_PRIMARY, "Reference Model A", true),
        model(MODEL_SECONDARY, "Reference Model B", false),
    ]
}

pub fn provider_catalog() -> Vec<ProviderDescriptor> {
    vec![ProviderDescriptor {
        id: "reference".to_owned(),
        display_name: "Reference Provider".to_owned(),
        wire: WIRE_OPENAI_CHAT_COMPLETIONS.to_owned(),
        kind: PROVIDER_DIRECT.to_owned(),
        ..ProviderDescriptor::default()
    }]
}

impl Inner {
    fn open(&self) -> Result<(), Error> {
        if self.closed {
            return Err(Error::SessionClosed);
        }
        Ok(())
    }

    fn names_this(&self, session: &SessionId) -> bool {
        *session == self.state.session_id
    }

    pub(super) fn models(&self, request: &ModelsRequest) -> Result<Catalog, Error> {
        self.open()?;
        if !request.session_id.as_str().is_empty() && !self.names_this(&request.session_id) {
            return Err(Error::InvalidSubmission);
        }
        Ok(Catalog {
            revision: CAPABILITY_REVISION.to_owned(),
            models: ModelsResponse {
                session_id: self.state.session_id.clone(),
                current_model_id: self.state.current_model_id.clone(),
                models: model_catalog(),
                providers: provider_catalog(),
                ..ModelsResponse::default()
            },
        })
    }

    pub(super) fn switch_model(
        &mut self,
        request: &SessionModelSwitchRequest,
    ) -> Result<(SessionModelSwitchResponse, SessionState), Error> {
        self.open()?;
        if !self.names_this(&request.session_id) {
            return Err(Error::InvalidSubmission);
        }
        if request.model_id != MODEL_PRIMARY && request.model_id != MODEL_SECONDARY {
            return Err(Error::ModelNotFound(request.model_id.clone()));
        }
        let previous =
            std::mem::replace(&mut self.state.current_model_id, request.model_id.clone());
        self.state.updated_at_ms = self.clock.now_ms();
        let response = SessionModelSwitchResponse {
            session_id: self.state.session_id.clone(),
            model_id: request.model_id.clone(),
            previous_model_id: previous,
        };
        Ok((response, self.state.clone()))
    }

    pub(super) fn update_settings(
        &mut self,
        request: &SessionSettingsUpdateRequest,
    ) -> Result<(SessionSettingsUpdateResponse, SessionState), Error> {
        let advertised = CapabilityDescriptor {
            features: features(),
            ..CapabilityDescriptor::default()
        };
        refuse_unadvertised_live_settings(request, &advertised)?;
        let threshold = compaction_threshold(request.compaction_policy.as_ref())?;
        self.open()?;
        if !self.names_this(&request.session_id) {
            return Err(Error::InvalidSubmission);
        }
        let policy = Some(effective_policy(request.compaction_policy.as_ref()));
        let previous = std::mem::replace(&mut self.state.compaction_policy, policy.clone());
        self.threshold = threshold;
        self.state.updated_at_ms = self.clock.now_ms();
        let response = SessionSettingsUpdateResponse {
            session_id: self.state.session_id.clone(),
            compaction_policy: policy,
            previous_compaction_policy: previous,
            ..SessionSettingsUpdateResponse::default()
        };
        Ok((response, self.state.clone()))
    }

    pub(super) fn tools(&self, request: &ToolsListRequest) -> Result<ToolCatalog, Error> {
        self.open()?;
        let scoped = request
            .session_id
            .as_ref()
            .filter(|id| !id.as_str().is_empty());
        if scoped.is_some_and(|id| !self.names_this(id)) {
            return Err(Error::RunNotFound);
        }
        let mut tools = scripted_catalog();
        let sources = match scoped {
            Some(_) => {
                tools.extend(self.provided.iter().cloned());
                session_sources(&self.attached)
            }
            None => declared_sources(),
        };
        Ok(ToolCatalog {
            revision: CAPABILITY_REVISION.to_owned(),
            tools: ToolsListResponse {
                session_id: request.session_id.clone(),
                sources,
                tools,
            },
        })
    }
}
