use std::collections::{BTreeMap, BTreeSet};

use fluent_uri::{Iri, IriRef};
use serde_json::Value;

use super::resources::base;
use super::{entries, refusal, text, PackLoadError, PackResources};
use crate::schema::SCHEMA_BASE;

type Reference = (bool, Result<String, String>);

pub(super) fn check(resources: &PackResources) -> Result<(), PackLoadError> {
    let sources = resources.schema_documents();
    let mut owned = BTreeMap::new();
    let mut references = BTreeMap::new();
    for (_, descriptor) in resources.descriptors() {
        let owner = base(descriptor);
        let mut allowed = BTreeSet::new();
        let mut refs = Vec::new();
        for (uri, document) in sources.iter().filter(|(uri, _)| uri.starts_with(&owner)) {
            allowed.insert(uri.clone());
            collect(document, uri, &mut refs);
        }
        for (identifier, target) in &refs {
            if let (true, Ok(target)) = (identifier, target) {
                if target.starts_with(&owner) {
                    allowed.insert(target.clone());
                }
            }
        }
        references.insert(owner.clone(), refs);
        owned.insert(owner, allowed);
    }
    let mut found = Vec::new();
    for (_, descriptor) in resources.descriptors() {
        let owner = base(descriptor);
        let mut allowed = owned[&owner].clone();
        for dependency in entries(descriptor, "depends_on") {
            allowed.extend(owned[&base(dependency)].iter().cloned());
        }
        for (identifier, target) in &references[&owner] {
            let accepted = match target {
                Ok(target) if *identifier => target.starts_with(&owner),
                Ok(target) => {
                    allowed.contains(target)
                        || target
                            .strip_prefix(SCHEMA_BASE)
                            .is_some_and(|file| !file.is_empty() && !file.contains('/'))
                }
                Err(_) => false,
            };
            if !accepted {
                found.push(refusal(
                    "pack_external_ref",
                    text(descriptor, "id"),
                    &format!("unowned schema resource {target:?}"),
                ));
            }
        }
    }
    if found.is_empty() {
        Ok(())
    } else {
        Err(PackLoadError { refusals: found })
    }
}

fn collect(node: &Value, base: &str, out: &mut Vec<Reference>) {
    match node {
        Value::Object(object) => {
            let mut base = base.to_owned();
            if let Some(id) = object.get("$id").and_then(Value::as_str) {
                let target = resolve(&base, id);
                if let Ok(target) = &target {
                    base = target.clone();
                }
                out.push((true, target));
            }
            for (key, value) in object {
                if key == "$id" {
                    continue;
                }
                if matches!(key.as_str(), "$ref" | "$dynamicRef") {
                    if let Some(reference) = value.as_str() {
                        out.push((false, resolve(&base, reference)));
                        continue;
                    }
                }
                collect(value, &base, out);
            }
        }
        Value::Array(items) => {
            for item in items {
                collect(item, base, out);
            }
        }
        _ => {}
    }
}

pub(super) fn resolve(base: &str, reference: &str) -> Result<String, String> {
    let base = Iri::parse(base).map_err(|e| e.to_string())?.to_uri();
    let reference = IriRef::parse(reference)
        .map_err(|e| e.to_string())?
        .to_uri_ref();
    reference
        .resolve_against(&base)
        .map(|uri| uri.with_fragment(None).as_str().to_owned())
        .map_err(|e| e.to_string())
}
