use crate::{Catalog, Endpoint, OAuthOrigin, Provider};

pub const WIRES: [&str; 6] = [
    "openai-completions",
    "openai-responses",
    "openai-codex-responses",
    "anthropic-messages",
    "ollama",
    "google-generative-ai",
];

pub fn joins_wire(wire: &str) -> bool {
    WIRES.contains(&wire)
}

const RESPONSES_ONLY_PREFIXES: [&str; 6] = [
    "o1-pro",
    "o3-pro",
    "gpt-5-pro",
    "gpt-5-codex",
    "gpt-5.1-codex-max",
    "computer-use-preview",
];

pub fn is_responses_only_model(model_id: &str) -> bool {
    RESPONSES_ONLY_PREFIXES
        .iter()
        .any(|prefix| model_id.starts_with(prefix))
        || model_id.contains("deep-research")
}

fn serves_region(endpoint: &Endpoint, region: &str) -> bool {
    endpoint.region == region
}

impl Provider {
    pub fn first_implemented_wire(&self) -> &str {
        self.wires
            .iter()
            .find(|wire| joins_wire(wire))
            .map_or("", String::as_str)
    }
}

impl Catalog {
    pub fn status(&self, id: &str) -> &str {
        match self.provider(id) {
            Some(provider) if !provider.status.is_empty() => &provider.status,
            _ => "supported",
        }
    }

    pub fn offering(&self, id: &str) -> &str {
        self.provider(id).map_or("", |provider| &provider.offering)
    }

    pub fn credential_env(&self, id: &str) -> &[String] {
        self.provider(id)
            .map_or(&[], |provider| &provider.credential_env)
    }

    pub fn base_url_env(&self, id: &str) -> &[String] {
        self.provider(id)
            .map_or(&[], |provider| &provider.base_url_env)
    }

    pub fn default_region(&self, id: &str) -> &str {
        self.provider(id)
            .map_or("", |provider| &provider.default_region)
    }

    pub fn region_env(&self, id: &str) -> &str {
        self.provider(id)
            .map_or("", |provider| &provider.region_env)
    }

    pub fn models_endpoint(&self, id: &str) -> &str {
        self.provider(id)
            .map_or("", |provider| &provider.models_path)
    }

    pub fn oauth_origin(&self, id: &str) -> Option<&OAuthOrigin> {
        self.provider(id)?.oauth_origin.as_ref()
    }

    pub fn wires(&self, id: &str) -> &[String] {
        self.provider(id).map_or(&[], |provider| &provider.wires)
    }

    pub fn endpoints(&self, id: &str) -> &[Endpoint] {
        self.provider(id)
            .map_or(&[], |provider| &provider.endpoints)
    }

    pub fn base_url(&self, id: &str, wire: &str, region: &str) -> Option<&str> {
        self.provider(id)?
            .endpoints
            .iter()
            .find(|endpoint| endpoint.wire == wire && serves_region(endpoint, region))
            .map(|endpoint| endpoint.base_url.as_str())
    }

    pub fn default_base_url(&self, id: &str, region: &str) -> Option<&str> {
        let provider = self.provider(id)?;
        if provider.base_url_source.is_empty() {
            return None;
        }
        provider
            .endpoints
            .iter()
            .find(|endpoint| serves_region(endpoint, region))
            .map(|endpoint| endpoint.base_url.as_str())
    }

    pub fn wire_for_model(&self, id: &str, model_id: &str) -> &str {
        let Some(provider) = self.provider(id) else {
            return "";
        };
        if id == "openai" && is_responses_only_model(model_id) && joins_wire("openai-responses") {
            return "openai-responses";
        }
        provider.first_implemented_wire()
    }
}
