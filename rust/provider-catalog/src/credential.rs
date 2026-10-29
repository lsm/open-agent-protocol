use crate::Catalog;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Source {
    None,
    Environment,
    Stored,
    OAuth,
}

impl Source {
    pub fn as_str(self) -> &'static str {
        match self {
            Self::None => "none",
            Self::Environment => "environment",
            Self::Stored => "stored",
            Self::OAuth => "oauth",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Credential {
    pub key: String,
    pub source: Source,
    pub name: String,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct EnvironmentValue {
    pub name: String,
    pub value: String,
}

impl EnvironmentValue {
    pub fn new(name: impl Into<String>, value: impl Into<String>) -> Self {
        Self {
            name: name.into(),
            value: value.into(),
        }
    }
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct StoredCredential {
    pub api_key: Option<String>,
    pub oauth_access: Option<String>,
}

pub fn api_key_from_names(env: &[EnvironmentValue], names: &[String]) -> Option<Credential> {
    for name in names {
        let Some(held) = env.iter().find(|held| &held.name == name) else {
            continue;
        };
        if held.value.is_empty() {
            continue;
        }
        return Some(Credential {
            key: held.value.clone(),
            source: Source::Environment,
            name: name.clone(),
        });
    }
    None
}

impl Catalog {
    pub fn api_key_for_provider(&self, env: &[EnvironmentValue], id: &str) -> Option<Credential> {
        api_key_from_names(env, self.credential_env(id))
    }

    pub fn accepts_auth(&self, id: &str, kind: &str) -> bool {
        self.provider(id)
            .is_some_and(|provider| provider.auth.iter().any(|accepted| accepted == kind))
    }

    pub fn needs_no_credential(&self, id: &str) -> bool {
        self.accepts_auth(id, "none")
    }

    fn stored_credential(&self, stored: Option<&StoredCredential>, id: &str) -> Option<Credential> {
        let stored = stored?;
        let (key, kind, source) = match (&stored.api_key, &stored.oauth_access) {
            (Some(key), _) => (key, "api_key", Source::Stored),
            (None, Some(access)) => (access, "oauth", Source::OAuth),
            (None, None) => return None,
        };
        if !self.accepts_auth(id, kind) || key.is_empty() {
            return None;
        }
        Some(Credential {
            key: key.clone(),
            source,
            name: id.into(),
        })
    }

    pub fn credential_key<'a>(&'a self, id: &'a str) -> &'a str {
        match self.provider(id) {
            Some(provider) if !provider.credential_group.is_empty() => &provider.credential_group,
            _ => id,
        }
    }

    pub fn lookup_credential(
        &self,
        env: &[EnvironmentValue],
        stored: Option<&StoredCredential>,
        id: &str,
    ) -> Option<Credential> {
        self.provider(id)?;
        self.api_key_for_provider(env, id)
            .or_else(|| self.stored_credential(stored, id))
    }
}
