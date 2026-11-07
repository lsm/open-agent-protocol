use std::collections::{BTreeMap, BTreeSet};
use std::path::{Component, Path, PathBuf};

use serde_json::Value;

use super::{contained, entries, refusal, text, PackLoadError, PackLoader, PackRefusal};
use crate::schema::SCHEMAS;

#[doc = include_str!("../../PACKS.md")]
pub struct PackResources {
    loader: PackLoader,
    documents: BTreeMap<String, Value>,
    manifests: BTreeMap<String, PathBuf>,
}

impl PackResources {
    pub fn descriptors(&self) -> impl Iterator<Item = (&Path, &Value)> {
        self.loader.descriptors()
    }

    pub fn documents(&self) -> &BTreeMap<String, Value> {
        &self.documents
    }

    pub(super) fn schema_documents(&self) -> Vec<(String, &Value)> {
        let mut documents: Vec<_> = self
            .documents
            .iter()
            .map(|(uri, document)| (uri.clone(), document))
            .collect();
        for (_, descriptor) in self.descriptors() {
            for (index, member) in entries(descriptor, "payload_members").enumerate() {
                documents.push((
                    format!("{}member-{index}.json", base(descriptor)),
                    &member["schema"],
                ));
            }
        }
        documents
    }

    pub fn manifests(&self) -> &BTreeMap<String, PathBuf> {
        &self.manifests
    }
}

impl PackLoader {
    pub fn read_resources(self) -> Result<PackResources, PackLoadError> {
        let mut found = Vec::new();
        let mut resources = PackResources {
            loader: self,
            documents: BTreeMap::new(),
            manifests: BTreeMap::new(),
        };
        for (root, descriptor) in &resources.loader.packs {
            let id = text(descriptor, "id");
            for path in entries(descriptor, "schemas").filter_map(Value::as_str) {
                let resolved = match contained_path(root, path) {
                    Ok(path) => path,
                    Err(message) => {
                        found.push(refusal("pack_schema_path_escape", id, &message));
                        continue;
                    }
                };
                match std::fs::read(resolved)
                    .map_err(|e| e.to_string())
                    .and_then(|data| serde_json::from_slice(&data).map_err(|e| e.to_string()))
                {
                    Ok(document) => {
                        let relative = clean_path(path).unwrap();
                        let relative = relative
                            .iter()
                            .map(|part| part.to_string_lossy())
                            .collect::<Vec<_>>()
                            .join("/");
                        resources
                            .documents
                            .insert(format!("{}{}", base(descriptor), relative), document);
                    }
                    Err(message) => found.push(refusal("", id, &message)),
                }
            }
            let manifest = text(descriptor, "fixtures");
            if !manifest.is_empty() {
                match contained_path(root, manifest) {
                    Ok(path) => {
                        resources.manifests.insert(base(descriptor), path);
                    }
                    Err(message) => found.push(refusal("pack_schema_path_escape", id, &message)),
                }
            }
        }
        if !found.is_empty() {
            return Err(PackLoadError { refusals: found });
        }
        let loaded: BTreeSet<_> = resources
            .descriptors()
            .map(|(_, d)| (text(d, "id"), text(d, "version")))
            .collect();
        for (_, descriptor) in resources.descriptors() {
            for dependency in entries(descriptor, "depends_on") {
                if !loaded.contains(&(text(dependency, "id"), text(dependency, "version"))) {
                    found.push(refusal(
                        "pack_dependency_missing",
                        text(descriptor, "id"),
                        "a dependency is not loaded at its exact version",
                    ));
                }
            }
        }
        if !found.is_empty() {
            return Err(PackLoadError { refusals: found });
        }
        super::references::check(&resources)?;
        Ok(resources)
    }
}

pub(super) fn base(descriptor: &Value) -> String {
    format!(
        "https://open-agent-protocol.local/ext/{}/{}/",
        text(descriptor, "id"),
        text(descriptor, "version")
    )
}

fn clean_path(relative: &str) -> Result<PathBuf, String> {
    if relative.is_empty() {
        return Err("an empty path is outside the pack root".to_owned());
    }
    let mut clean = PathBuf::new();
    for component in Path::new(relative).components() {
        match component {
            Component::Normal(part) => clean.push(part),
            Component::CurDir => {}
            Component::ParentDir if clean.pop() => {}
            _ => return Err("a path escapes the pack root".to_owned()),
        }
    }
    Ok(clean)
}

fn contained_path(root: &Path, relative: &str) -> Result<PathBuf, String> {
    let resolved =
        std::fs::canonicalize(root.join(clean_path(relative)?)).map_err(|e| e.to_string())?;
    if !resolved.starts_with(root) {
        return Err("a path resolves outside the pack root".to_owned());
    }
    Ok(resolved)
}

pub(super) fn members(descriptor: &Value, gated: &BTreeSet<String>) -> Vec<PackRefusal> {
    let core = core_members();
    let id = text(descriptor, "id");
    let mut found = Vec::new();
    let mut declared = BTreeSet::new();
    for member in entries(descriptor, "payload_members") {
        let target = text(member, "payload_type");
        let name = text(member, "member");
        let gate = entries(descriptor, "gates")
            .find(|gate| text(gate, "payload_type") == target && text(gate, "member") == name);
        let code = if !declared.insert((target, name)) {
            Some("pack_member_duplicate")
        } else if !core.contains_key(target)
            || (target == "capabilities.updated"
                && gate.is_some_and(|gate| !text(gate, "capability").is_empty()))
        {
            Some("pack_member_target_unknown")
        } else if core[target].contains(name) {
            Some("pack_restates_core_member")
        } else if let Some(code) = contained(id, name) {
            Some(code)
        } else if !gated.contains(&format!("{target}#{name}")) {
            Some("pack_ungated_type")
        } else {
            None
        };
        if let Some(code) = code {
            found.push(refusal(
                code,
                id,
                &format!("invalid payload member {name:?} on {target:?}"),
            ));
        }
    }
    found
}

fn core_members() -> BTreeMap<String, BTreeSet<String>> {
    let documents: BTreeMap<_, Value> = SCHEMAS
        .iter()
        .map(|(file, data)| (*file, serde_json::from_str(data).unwrap()))
        .collect();
    crate::validator::envelope_branches()
        .iter()
        .map(|(name, branch)| {
            let mut members = BTreeSet::new();
            collect_members(
                &documents,
                "envelope.schema.json",
                &documents["envelope.schema.json"]["$defs"][branch]["properties"]["payload"],
                &mut members,
                0,
            );
            (name.clone(), members)
        })
        .collect()
}

fn collect_members(
    documents: &BTreeMap<&str, Value>,
    file: &str,
    node: &Value,
    out: &mut BTreeSet<String>,
    depth: usize,
) {
    if depth > 8 {
        return;
    }
    if let Some(reference) = node.get("$ref").and_then(Value::as_str) {
        let (target, pointer) = reference.split_once('#').unwrap_or(("", reference));
        let target = if target.is_empty() { file } else { target };
        if let Some(value) = documents
            .get(target)
            .and_then(|document| object_pointer(document, &format!("#{pointer}")))
        {
            collect_members(documents, target, value, out, depth + 1);
        }
    }
    if let Some(properties) = node.get("properties").and_then(Value::as_object) {
        out.extend(properties.keys().cloned());
    }
    for member in entries(node, "allOf") {
        collect_members(documents, file, member, out, depth + 1);
    }
}

pub(super) fn object_pointer<'a>(document: &'a Value, pointer: &str) -> Option<&'a Value> {
    let mut node = document;
    for part in pointer.strip_prefix("#/")?.split('/') {
        let key = part.replace("~1", "/").replace("~0", "~");
        node = node.as_object()?.get(&key)?;
    }
    Some(node)
}
