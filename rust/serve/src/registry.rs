use std::collections::BTreeMap;
use std::sync::Arc;

use oap_adapter::memory::{Config, Memory};
use oap_adapter::{duplicate_environment_name, Adapter};
use oap_protocol::{is_tool_source_kind, ToolSourceAttachment, TOOL_SOURCE_PROCESS};

#[derive(Debug, thiserror::Error, PartialEq, Eq)]
pub enum RegistryError {
    #[error("serve: adapter name is required")]
    NoAdapterName,
    #[error("serve: adapter {0:?} is already registered")]
    AdapterExists(String),
    #[error("serve: tool source id is required")]
    NoToolSourceId,
    #[error("serve: tool source {0:?} is already registered")]
    ToolSourceExists(String),
    #[error("serve: tool source {id:?}: {reason}")]
    ToolSource { id: String, reason: String },
}

#[derive(Default)]
pub struct Registry {
    adapters: BTreeMap<String, Arc<dyn Adapter>>,
    directories: BTreeMap<String, String>,
    tool_sources: BTreeMap<String, ToolSourceAttachment>,
}

impl Registry {
    pub fn new() -> Self {
        Self::default()
    }

    pub fn default_registry() -> Self {
        let mut registry = Self::new();
        registry
            .adapters
            .insert("memory".into(), Arc::new(Memory::new(Config::default())));
        registry
    }

    pub fn register(
        &mut self,
        name: &str,
        implementation: Arc<dyn Adapter>,
    ) -> Result<(), RegistryError> {
        if name.is_empty() {
            return Err(RegistryError::NoAdapterName);
        }
        if self.adapters.contains_key(name) {
            return Err(RegistryError::AdapterExists(name.into()));
        }
        self.adapters.insert(name.into(), implementation);
        Ok(())
    }

    pub fn lookup(&self, name: &str) -> Option<Arc<dyn Adapter>> {
        self.adapters.get(name).cloned()
    }

    pub fn names(&self) -> Vec<String> {
        self.adapters.keys().cloned().collect()
    }

    pub fn set_working_directory(&mut self, name: &str, directory: &str) {
        if directory.is_empty() {
            self.directories.remove(name);
        } else {
            self.directories.insert(name.into(), directory.into());
        }
    }

    pub fn working_directory(&self, name: &str) -> &str {
        self.directories.get(name).map_or("", String::as_str)
    }

    pub fn register_tool_source(
        &mut self,
        id: &str,
        mut source: ToolSourceAttachment,
    ) -> Result<(), RegistryError> {
        if id.is_empty() {
            return Err(RegistryError::NoToolSourceId);
        }
        validate_tool_source(id, &source.kind, &source.command, &source.environment)?;
        if self.tool_sources.contains_key(id) {
            return Err(RegistryError::ToolSourceExists(id.into()));
        }
        source.id = id.into();
        self.tool_sources.insert(id.into(), source);
        Ok(())
    }

    pub fn tool_source(&self, id: &str) -> Option<&ToolSourceAttachment> {
        self.tool_sources.get(id)
    }

    pub fn tool_source_names(&self) -> Vec<String> {
        self.tool_sources.keys().cloned().collect()
    }
}

pub(crate) fn validate_tool_source(
    id: &str,
    kind: &str,
    command: &str,
    environment: &[String],
) -> Result<(), RegistryError> {
    let refuse = |reason: String| {
        Err(RegistryError::ToolSource {
            id: id.into(),
            reason,
        })
    };
    if kind.is_empty() {
        return refuse("kind is required".into());
    }
    if !is_tool_source_kind(kind) {
        return refuse(format!("kind {kind:?} is not a tool source kind"));
    }
    if kind == TOOL_SOURCE_PROCESS && command.is_empty() {
        return refuse("a process source needs a command".into());
    }
    if let Some(name) = duplicate_environment_name(environment) {
        return refuse(format!("environment names {name:?} twice"));
    }
    Ok(())
}
