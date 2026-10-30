use crate::{Catalog, EnvironmentValue};

pub const GLOBAL_BASE_URL_ENV: &str = "OAPX_BASE_URL";

pub const VERSIONED_WIRES: [&str; 3] = [
    "anthropic-messages",
    "openai-completions",
    "openai-responses",
];

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct BaseOverrides {
    pub global: String,
    pub anthropic: String,
    pub openai: String,
    pub deepseek: String,
    pub kimi_region: String,
}

fn first_env_value(env: &[EnvironmentValue], name: &str) -> String {
    if name.is_empty() {
        return String::new();
    }
    env.iter()
        .find(|held| held.name == name)
        .map(|held| held.value.clone())
        .unwrap_or_default()
}

fn trim_ascii_space(value: &str) -> &str {
    value.trim_matches([' ', '\t', '\r', '\n'])
}

pub fn normalize_kimi_region(value: &str) -> &'static str {
    let trimmed = trim_ascii_space(value);
    let is = |word: &str| trimmed.eq_ignore_ascii_case(word);
    if is("global") || is("moonshot") {
        "global"
    } else if is("china") || is("cn") || is("coding") {
        "china"
    } else {
        ""
    }
}

pub fn normalize_versioned_base_url(url: &str) -> &str {
    let trimmed = url.trim_end_matches('/');
    trimmed.strip_suffix("/v1").unwrap_or(trimmed)
}

pub fn uses_versioned_route(provider_id: &str, wire: &str) -> bool {
    provider_id != "github-copilot" && VERSIONED_WIRES.contains(&wire)
}

pub fn provider_arm(provider_id: &str, wire: &str) -> &'static str {
    match (provider_id, wire) {
        ("anthropic", "anthropic-messages") => "anthropic",
        ("openai", "openai-completions" | "openai-responses") => "openai",
        ("deepseek", "openai-completions") => "deepseek",
        ("openai-codex", "openai-codex-responses") => "openai-codex",
        ("kimi", "openai-completions") => "kimi",
        _ => "",
    }
}

pub fn base_url_with_overrides(overrides: &BaseOverrides, provider_id: &str, wire: &str) -> String {
    if !overrides.global.is_empty() {
        if uses_versioned_route(provider_id, wire) {
            return normalize_versioned_base_url(&overrides.global).into();
        }
        return overrides.global.trim_end_matches('/').into();
    }
    let chosen = match provider_arm(provider_id, wire) {
        "anthropic" => &overrides.anthropic,
        "openai" => &overrides.openai,
        "deepseek" => &overrides.deepseek,
        _ => return String::new(),
    };
    if chosen.is_empty() {
        return String::new();
    }
    normalize_versioned_base_url(chosen).into()
}

impl Catalog {
    pub fn first_base_url_env(&self, id: &str) -> &str {
        self.base_url_env(id).first().map_or("", String::as_str)
    }

    pub fn base_overrides_from_env(&self, env: &[EnvironmentValue]) -> BaseOverrides {
        BaseOverrides {
            global: first_env_value(env, GLOBAL_BASE_URL_ENV),
            anthropic: first_env_value(env, self.first_base_url_env("anthropic")),
            openai: first_env_value(env, self.first_base_url_env("openai")),
            deepseek: first_env_value(env, self.first_base_url_env("deepseek")),
            kimi_region: normalize_kimi_region(&first_env_value(env, self.region_env("kimi")))
                .into(),
        }
    }

    pub fn default_kimi_region(&self) -> &str {
        match self.default_region("kimi") {
            "" => "china",
            region => region,
        }
    }

    pub fn kimi_region<'a>(&'a self, overrides: &'a BaseOverrides, stored: &str) -> &'a str {
        if !overrides.kimi_region.is_empty() {
            return &overrides.kimi_region;
        }
        match normalize_kimi_region(stored) {
            "" => self.default_kimi_region(),
            normalized => normalized,
        }
    }

    pub fn resolve_base_url(
        &self,
        env: &[EnvironmentValue],
        id: &str,
        wire: &str,
        region: &str,
    ) -> Option<String> {
        let overrides = self.base_overrides_from_env(env);
        let overridden = base_url_with_overrides(&overrides, id, wire);
        if !overridden.is_empty() {
            return Some(overridden);
        }
        let region = if provider_arm(id, wire) == "kimi" {
            self.kimi_region(&overrides, region)
        } else {
            ""
        };
        self.base_url(id, wire, region).map(str::to_owned)
    }
}
