use std::collections::{BTreeMap, BTreeSet};
use std::path::{Path, PathBuf};

use serde::Serialize;
use serde_json::Value;

use crate::schema::compile;

#[derive(Debug, Serialize)]
pub struct PackRefusal {
    pub code: String,
    pub pack: String,
    pub message: String,
}

#[derive(Debug, thiserror::Error)]
#[error("extension pack refused: {refusals:?}")]
pub struct PackLoadError {
    pub refusals: Vec<PackRefusal>,
}

impl PackLoadError {
    pub fn codes(&self) -> BTreeSet<&str> {
        self.refusals
            .iter()
            .map(|r| r.code.as_str())
            .filter(|c| !c.is_empty())
            .collect()
    }
}

#[doc = include_str!("../PACKS.md")]
pub struct PackLoader {
    packs: Vec<(PathBuf, Value)>,
}

impl PackLoader {
    pub fn read(directories: &[PathBuf]) -> Result<Self, PackLoadError> {
        let schema = compile("pack.schema.json").map_err(|e| failure("", e.to_string()))?;
        let mut packs = Vec::new();
        for directory in directories {
            let root = std::fs::canonicalize(directory).map_err(|e| failure("", e.to_string()))?;
            let data =
                std::fs::read(root.join("pack.json")).map_err(|e| failure("", e.to_string()))?;
            let descriptor: Value =
                serde_json::from_slice(&data).map_err(|e| failure("", e.to_string()))?;
            if let Err(error) = schema.validate(&descriptor) {
                return Err(failure(text(&descriptor, "id"), error.to_string()));
            }
            packs.push((root, descriptor));
        }
        let mut refusals = Vec::new();
        for (index, (_, descriptor)) in packs.iter().enumerate() {
            let id = text(descriptor, "id");
            for (_, other) in &packs[index + 1..] {
                let other = text(other, "id");
                if id == other
                    || id.starts_with(&format!("{other}."))
                    || other.starts_with(&format!("{id}."))
                {
                    refusals.push(refusal(
                        "pack_id_collision",
                        id,
                        "loaded pack IDs must be prefix-free",
                    ));
                }
            }
        }
        if !refusals.is_empty() {
            return Err(PackLoadError { refusals });
        }
        for (_, descriptor) in &packs {
            refusals.extend(declarations(descriptor));
        }
        if !refusals.is_empty() {
            return Err(PackLoadError { refusals });
        }
        Ok(Self { packs })
    }

    pub fn descriptors(&self) -> impl Iterator<Item = (&Path, &Value)> {
        self.packs
            .iter()
            .map(|(root, descriptor)| (root.as_path(), descriptor))
    }
}

fn failure(pack: &str, message: String) -> PackLoadError {
    PackLoadError {
        refusals: vec![refusal("", pack, &message)],
    }
}

fn refusal(code: &str, pack: &str, message: &str) -> PackRefusal {
    PackRefusal {
        code: code.to_owned(),
        pack: pack.to_owned(),
        message: message.to_owned(),
    }
}

pub(super) fn entries<'a>(node: &'a Value, key: &str) -> impl Iterator<Item = &'a Value> {
    node.get(key)
        .and_then(Value::as_array)
        .into_iter()
        .flatten()
}

pub(super) fn text<'a>(node: &'a Value, key: &str) -> &'a str {
    node.get(key).and_then(Value::as_str).unwrap_or_default()
}

fn contained(id: &str, name: &str) -> Option<&'static str> {
    if name.starts_with(&format!("{id}.")) {
        return None;
    }
    let core = crate::validator::envelope_branches()
        .keys()
        .any(|core| core.split('.').take(2).eq(name.split('.').take(2)));
    Some(if name.split('.').count() < 3 || core {
        "pack_unprefixed_name"
    } else {
        "pack_foreign_prefix"
    })
}

fn declarations(descriptor: &Value) -> Vec<PackRefusal> {
    let id = text(descriptor, "id");
    let mut found = Vec::new();
    let mut types = BTreeMap::new();
    for name in entries(descriptor, "capability_keys").chain(entries(descriptor, "error_codes")) {
        if let Some(code) = contained(id, name.as_str().unwrap()) {
            found.push(refusal(
                code,
                id,
                "a declared name is outside the pack namespace",
            ));
        }
    }
    for entry in entries(descriptor, "envelope_types") {
        let name = text(entry, "type");
        if let Some(code) = contained(id, name) {
            found.push(refusal(
                code,
                id,
                "an envelope type is outside the pack namespace",
            ));
        } else if types.insert(name, entry).is_some() {
            found.push(refusal(
                "pack_type_duplicate",
                id,
                "an envelope type was declared twice",
            ));
        }
    }
    if !found.is_empty() {
        return found;
    }
    let mut answered = BTreeSet::new();
    for (name, entry) in &types {
        let role = text(entry, "role");
        if !matches!(role, "request" | "response" | "event") {
            found.push(refusal(
                "pack_role_undeclared",
                id,
                "an envelope type must declare its role",
            ));
            continue;
        }
        if text(entry, "schema").is_empty() {
            found.push(refusal("", id, "an envelope type contributes no branch"));
        }
        if role == "response" {
            let target = text(entry, "replies_to");
            if !types
                .get(target)
                .is_some_and(|entry| text(entry, "role") == "request")
            {
                found.push(refusal(
                    "pack_reply_target_unknown",
                    id,
                    "a response must answer a request declared by this pack",
                ));
            } else if !answered.insert(target) {
                found.push(refusal(
                    "pack_reply_target_ambiguous",
                    id,
                    "a request has more than one declared response",
                ));
            }
        }
        if role != "request" && entries(entry, "refusals").next().is_some() {
            found.push(refusal(
                "pack_refusal_undeclared",
                id,
                "only a request may declare refusals",
            ));
        }
        for code in entries(entry, "refusals") {
            if !entries(descriptor, "error_codes").any(|declared| declared == code) {
                found.push(refusal(
                    "pack_refusal_undeclared",
                    id,
                    &format!("{name} names an undeclared refusal"),
                ));
            }
        }
    }
    let mut gated = BTreeSet::new();
    for gate in entries(descriptor, "gates") {
        let name = text(gate, "type");
        let target = if name.is_empty() {
            if text(gate, "payload_type").is_empty() || text(gate, "member").is_empty() {
                found.push(refusal("", id, "a gate must name a type or payload member"));
                continue;
            }
            format!("{}#{}", text(gate, "payload_type"), text(gate, "member"))
        } else {
            name.to_owned()
        };
        let capability = text(gate, "capability");
        if (gate["ungated"] == true) == !capability.is_empty() {
            found.push(refusal(
                "",
                id,
                "a gate names exactly one of capability or ungated",
            ));
            continue;
        }
        if !capability.is_empty()
            && !entries(descriptor, "capability_keys").any(|key| key == capability)
        {
            found.push(refusal(
                contained(id, capability).unwrap_or_default(),
                id,
                "a gate names an undeclared capability",
            ));
            continue;
        }
        if !name.is_empty() {
            let Some(entry) = types.get(name) else {
                found.push(refusal("", id, "a gate names an undeclared envelope type"));
                continue;
            };
            if text(entry, "role") == "response" {
                found.push(refusal(
                    "pack_response_gated",
                    id,
                    "a response inherits its request's gate",
                ));
                continue;
            }
        }
        if !gated.insert(target) {
            found.push(refusal("", id, "a declaration has more than one gate"));
        }
    }
    for (name, entry) in &types {
        if text(entry, "role") != "response" && !gated.contains(*name) {
            found.push(refusal(
                "pack_ungated_type",
                id,
                "a request or event must name its gate or declare itself ungated",
            ));
        }
    }
    found
}
