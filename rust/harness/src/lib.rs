use std::sync::OnceLock;

use serde::{Deserialize, Serialize};
use serde_json::Value;

pub const SCHEMA_FILE: &str = "harness.schema.json";

const SCHEMA: &str = include_str!("../../../harnesses/harness.schema.json");

pub const FILES: [(&str, &str); 8] = [
    ("acp.json", include_str!("../../../harnesses/acp.json")),
    (
        "claude-code.json",
        include_str!("../../../harnesses/claude-code.json"),
    ),
    (
        "codex-app-server.json",
        include_str!("../../../harnesses/codex-app-server.json"),
    ),
    (
        "copilot-cli.json",
        include_str!("../../../harnesses/copilot-cli.json"),
    ),
    (
        "deepseek-harness.json",
        include_str!("../../../harnesses/deepseek-harness.json"),
    ),
    (
        "hermes.json",
        include_str!("../../../harnesses/hermes.json"),
    ),
    (
        "opencode.json",
        include_str!("../../../harnesses/opencode.json"),
    ),
    ("pi.json", include_str!("../../../harnesses/pi.json")),
];

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Status {
    Current,
    Supported,
    Floor,
    Retired,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Harness {
    pub id: String,
    pub name: String,
    pub versions: Vec<Version>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Version {
    pub label: String,
    pub status: Status,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub endpoint_version: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub capability_revision: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub admits: Vec<String>,
    pub ledgers: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub corpus: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub corpus_from: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub components: Vec<Component>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub artifacts: Vec<Artifact>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sources: Vec<Source>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Component {
    pub name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub package: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub version: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub repository: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Artifact {
    pub component: String,
    pub platform: String,
    pub kind: String,
    pub name: String,
    pub sha256: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub bytes: Option<u64>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Source {
    pub component: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub tag: String,
    pub commit: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub tree: String,
}

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("{SCHEMA_FILE}: {0}")]
    Schema(String),
    #[error("{name}: {message}")]
    Harness { name: String, message: String },
    #[error("harness catalog holds no harness")]
    Empty,
}

fn harness_error(name: &str, message: impl ToString) -> Error {
    Error::Harness {
        name: name.to_owned(),
        message: message.to_string(),
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Catalog {
    pub harnesses: Vec<Harness>,
}

impl Catalog {
    pub fn load<'a>(files: impl IntoIterator<Item = (&'a str, &'a str)>) -> Result<Self, Error> {
        let schema: Value =
            serde_json::from_str(SCHEMA).map_err(|err| Error::Schema(err.to_string()))?;
        let validator =
            jsonschema::draft202012::new(&schema).map_err(|err| Error::Schema(err.to_string()))?;
        let mut harnesses = Vec::new();
        for (name, text) in files {
            harnesses.push(parse(&validator, name, text)?);
        }
        if harnesses.is_empty() {
            return Err(Error::Empty);
        }
        Ok(Self { harnesses })
    }

    pub fn embedded() -> &'static Catalog {
        static CATALOG: OnceLock<Catalog> = OnceLock::new();
        CATALOG.get_or_init(|| match Catalog::load(FILES) {
            Ok(catalog) => catalog,
            Err(err) => panic!("the embedded harness catalog is invalid: {err}"),
        })
    }

    pub fn harness(&self, id: &str) -> Option<&Harness> {
        self.harnesses.iter().find(|harness| harness.id == id)
    }
}

fn parse(validator: &jsonschema::Validator, name: &str, text: &str) -> Result<Harness, Error> {
    let instance: Value = serde_json::from_str(text).map_err(|err| harness_error(name, err))?;
    validator
        .validate(&instance)
        .map_err(|err| harness_error(name, err))?;
    let harness: Harness = serde_json::from_str(text).map_err(|err| harness_error(name, err))?;
    if format!("{}.json", harness.id) != name {
        return Err(harness_error(
            name,
            format!("harness {:?} belongs in {}.json", harness.id, harness.id),
        ));
    }
    Ok(harness)
}

impl Harness {
    pub fn versioned(&self, status: Status) -> Option<&Version> {
        self.versions
            .iter()
            .find(|version| version.status == status)
    }

    pub fn current(&self) -> Option<&Version> {
        self.versioned(Status::Current)
    }
}

pub fn current(id: &str) -> Option<&'static Version> {
    Catalog::embedded().harness(id)?.current()
}

impl Version {
    pub fn source(&self, component: &str) -> Option<&Source> {
        self.sources
            .iter()
            .find(|source| source.component == component)
    }

    pub fn component(&self, name: &str) -> Option<&Component> {
        self.components
            .iter()
            .find(|candidate| candidate.name == name)
    }

    pub fn artifact(&self, component: &str, platform: &str, kind: &str) -> Option<&Artifact> {
        self.artifacts.iter().find(|artifact| {
            artifact.component == component
                && artifact.platform == platform
                && artifact.kind == kind
        })
    }
}
