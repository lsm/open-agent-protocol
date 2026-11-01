use std::collections::BTreeMap;
use std::path::Path;
use std::sync::Arc;

use oap_adapter::memory::{Config, Memory};
use oap_protocol::ToolSourceAttachment;
use serde_json::Value;

use crate::{Registry, RegistryError};

const ADAPTER_MEMBERS: [&str; 18] = [
    "type",
    "executable",
    "args",
    "environment",
    "working_directory",
    "model",
    "journal_capacity",
    "allowed_tools",
    "unrestricted_tools",
    "approval_policy",
    "sandbox",
    "provider",
    "max_tokens",
    "agent_config",
    "system_prompt",
    "endpoint",
    "agent",
    "any_directory",
];

const TOOL_SOURCE_MEMBERS: [&str; 7] = [
    "kind",
    "display_name",
    "protocol",
    "endpoint",
    "command",
    "args",
    "environment",
];

const HARNESS_TYPES: [&str; 9] = [
    "acp", "claude", "codex", "copilot", "deepseek", "hermes", "opencode", "pi", "oapx",
];

#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("serve: read config: {0}")]
    Read(#[from] std::io::Error),
    #[error("serve: parse config {path}: {reason}")]
    Parse { path: String, reason: String },
    #[error("serve: adapter {name:?}: {reason}")]
    Adapter { name: String, reason: String },
    #[error("serve: tool source {id:?}: {reason}")]
    ToolSource { id: String, reason: String },
    #[error(transparent)]
    Registry(#[from] RegistryError),
}

#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct AdapterEntry {
    #[serde(rename = "type")]
    kind: String,
    environment: Vec<String>,
    working_directory: String,
    journal_capacity: i64,
    any_directory: bool,
}

#[derive(serde::Deserialize, Default)]
#[serde(default)]
struct ToolSourceEntry {
    kind: String,
    display_name: String,
    protocol: String,
    endpoint: String,
    command: String,
    args: Vec<String>,
    environment: Vec<String>,
}

impl Registry {
    pub fn load(
        path: &Path,
        environ: impl Fn(&str) -> Option<String>,
    ) -> Result<Self, ConfigError> {
        let data = std::fs::read_to_string(path)?;
        let parse = |reason: String| ConfigError::Parse {
            path: path.display().to_string(),
            reason,
        };
        if oap_validation::duplicate_key(&data).is_some() {
            return Err(parse("config: not one JSON object: DuplicateField".into()));
        }
        let top: BTreeMap<String, Value> =
            serde_json::from_str(&data).map_err(|err| parse(err.to_string()))?;
        if let Some(name) = top
            .keys()
            .find(|name| *name != "adapters" && *name != "tool_sources")
        {
            return Err(parse(format!("config: the file: unknown field {name:?}")));
        }
        let mut registry = Registry::new();
        for (name, value) in
            members(&top, "adapters", &ADAPTER_MEMBERS, "adapter").map_err(parse)?
        {
            let entry: AdapterEntry =
                serde_json::from_value(value).map_err(|err| parse(err.to_string()))?;
            if entry.journal_capacity < 0 {
                return Err(parse(format!(
                    "config: adapter {name:?}: journal_capacity {} is negative; 0 keeps the default",
                    entry.journal_capacity
                )));
            }
            let refuse = |reason: String| ConfigError::Adapter {
                name: name.clone(),
                reason,
            };
            resolve_environment(&entry.environment, &environ).map_err(refuse)?;
            if entry.any_directory {
                return Err(refuse(
                    "any_directory is not served by the Rust hub yet".into(),
                ));
            }
            let kind = if entry.kind.is_empty() {
                name.clone()
            } else {
                entry.kind.clone()
            };
            let built: Arc<dyn oap_adapter::Adapter> = match kind.as_str() {
                "memory" => Arc::new(Memory::new(Config {
                    journal_capacity: entry.journal_capacity as usize,
                    ..Config::default()
                })),
                harness if HARNESS_TYPES.contains(&harness) => {
                    return Err(refuse(format!(
                        "type {harness:?} is not built in the Rust tree yet"
                    )))
                }
                other => return Err(refuse(format!("unknown type {other:?}"))),
            };
            registry.register(&name, built)?;
            registry.set_working_directory(&name, &entry.working_directory);
        }
        for (id, value) in
            members(&top, "tool_sources", &TOOL_SOURCE_MEMBERS, "tool source").map_err(parse)?
        {
            let entry: ToolSourceEntry =
                serde_json::from_value(value).map_err(|err| parse(err.to_string()))?;
            let refuse = |reason: String| ConfigError::ToolSource {
                id: id.clone(),
                reason,
            };
            for name in &entry.environment {
                let (name, literal) = match name.split_once('=') {
                    Some((name, _)) => (name, true),
                    None => (name.as_str(), false),
                };
                if !literal && !name.is_empty() && environ(name).is_none() {
                    return Err(refuse(format!(
                        "environment variable {name:?} is not set; export it or write {name}=<value>"
                    )));
                }
            }
            let environment = resolve_environment(&entry.environment, &environ).map_err(refuse)?;
            registry.register_tool_source(
                &id,
                ToolSourceAttachment {
                    id: id.as_str().into(),
                    kind: entry.kind.as_str().into(),
                    display_name: entry.display_name,
                    protocol: entry.protocol,
                    endpoint: entry.endpoint,
                    command: entry.command,
                    args: entry.args,
                    environment,
                },
            )?;
        }
        Ok(registry)
    }
}

fn members(
    top: &BTreeMap<String, Value>,
    key: &str,
    allowed: &[&str],
    what: &str,
) -> Result<Vec<(String, Value)>, String> {
    let Some(raw) = top.get(key) else {
        return Ok(Vec::new());
    };
    let entries: BTreeMap<String, Value> =
        serde_json::from_value(raw.clone()).map_err(|err| err.to_string())?;
    let mut found = Vec::new();
    for (name, value) in entries {
        let Value::Object(fields) = &value else {
            return Err(format!("config: {what} {name:?} is not an object"));
        };
        if let Some(unknown) = fields
            .keys()
            .find(|field| !allowed.contains(&field.as_str()))
        {
            return Err(format!(
                "config: {what} {name:?}: unknown field {unknown:?}"
            ));
        }
        found.push((name, value));
    }
    Ok(found)
}

fn resolve_environment(
    entries: &[String],
    environ: &impl Fn(&str) -> Option<String>,
) -> Result<Vec<String>, String> {
    let mut resolved = Vec::new();
    for entry in entries {
        let (name, literal) = match entry.split_once('=') {
            Some((name, _)) => (name, true),
            None => (entry.as_str(), false),
        };
        if name.is_empty() {
            return Err("environment entry has no variable name".into());
        }
        if literal {
            resolved.push(entry.clone());
        } else if let Some(value) = environ(name) {
            resolved.push(format!("{name}={value}"));
        }
    }
    Ok(resolved)
}
