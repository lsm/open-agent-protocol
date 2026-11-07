use std::collections::BTreeMap;

use fluent_uri::Iri;
use jsonschema::{error::ValidationErrorKind, Draft, ReferencingError, Registry, Validator};
use serde_json::{json, Value};

use super::resources::{base, document_uri, object_pointer};
use super::{entries, refusal, text, PackLoadError, PackResources};
use crate::schema::{Bundle, SCHEMAS, SCHEMA_BASE};

#[doc = include_str!("../../PACKS.md")]
pub struct CompiledPacks {
    resources: PackResources,
    schema: Validator,
    members: BTreeMap<(String, String), Validator>,
}

impl CompiledPacks {
    pub fn resources(&self) -> &PackResources {
        &self.resources
    }

    pub fn schema(&self) -> &Validator {
        &self.schema
    }

    pub fn member_schemas(&self) -> &BTreeMap<(String, String), Validator> {
        &self.members
    }
}

impl PackResources {
    pub fn compile(self) -> Result<CompiledPacks, PackLoadError> {
        let branches = branches(&self)?;
        let owners = self
            .descriptors()
            .map(|(_, d)| text(d, "id"))
            .collect::<Vec<_>>()
            .join(", ");
        let failed = |message: String, external: bool| PackLoadError {
            refusals: vec![refusal(
                if external { "pack_external_ref" } else { "" },
                &owners,
                &message,
            )],
        };
        let registry_error = |error: ReferencingError| {
            let external = matches!(error, ReferencingError::Unretrievable { .. });
            failed(error.to_string(), external)
        };
        let mut documents: BTreeMap<_, Value> = SCHEMAS
            .iter()
            .map(|(file, data)| {
                (
                    format!("{SCHEMA_BASE}{file}"),
                    serde_json::from_str(data).unwrap(),
                )
            })
            .collect();
        documents.extend(
            self.schema_documents()
                .into_iter()
                .map(|(uri, document)| (uri, document.clone())),
        );
        let root_uri = format!("{SCHEMA_BASE}envelope.schema.json");
        let union = documents.get_mut(&root_uri).unwrap()["oneOf"]
            .as_array_mut()
            .unwrap();
        for branch in union.iter_mut() {
            if let Some(known) = branch
                .pointer_mut("/properties/type/not/enum")
                .and_then(Value::as_array_mut)
            {
                known.extend(branches.keys().cloned().map(Value::String));
            }
        }
        union.extend(branches.values().map(|uri| json!({"$ref": uri})));
        let mut registry = Registry::new().draft(Draft::Draft202012).retriever(Bundle);
        for (uri, document) in &documents {
            let uri = Iri::parse(uri.as_str())
                .map_err(|e| failed(e.to_string(), false))?
                .to_uri();
            registry = registry
                .add(uri.as_str(), document.clone())
                .map_err(&registry_error)?;
        }
        let registry = registry.prepare().map_err(&registry_error)?;
        let build = |uri: &str, document: &Value| {
            jsonschema::options()
                .with_registry(&registry)
                .with_base_uri(uri)
                .with_retriever(Bundle)
                .should_validate_formats(false)
                .build(document)
                .map_err(|e| {
                    let external = matches!(
                        e.kind(),
                        ValidationErrorKind::Referencing(ReferencingError::Unretrievable { .. })
                    );
                    failed(e.to_string(), external)
                })
        };
        let schema = build(&root_uri, &documents[&root_uri])?;
        for (file, _) in SCHEMAS
            .iter()
            .filter(|(file, _)| *file != "envelope.schema.json")
        {
            let uri = format!("{SCHEMA_BASE}{file}");
            build(&uri, &documents[&uri])?;
        }
        let mut members = BTreeMap::new();
        for (_, descriptor) in self.descriptors() {
            for (index, member) in entries(descriptor, "payload_members").enumerate() {
                let uri = format!("{}member-{index}.json", base(descriptor));
                let compiled = build(&uri, &member["schema"])?;
                members.insert(
                    (
                        text(member, "payload_type").to_owned(),
                        text(member, "member").to_owned(),
                    ),
                    compiled,
                );
            }
        }
        Ok(CompiledPacks {
            resources: self,
            schema,
            members,
        })
    }
}

fn branches(resources: &PackResources) -> Result<BTreeMap<String, String>, PackLoadError> {
    let mut branches = BTreeMap::new();
    let mut found = Vec::new();
    for (_, descriptor) in resources.descriptors() {
        for entry in entries(descriptor, "envelope_types") {
            let reference = text(entry, "schema");
            let (file, pointer) = reference.split_once('#').unwrap_or((reference, ""));
            let uri = document_uri(descriptor, file);
            let branch = uri
                .as_ref()
                .ok()
                .and_then(|uri| resources.documents().get(uri))
                .and_then(|document| {
                    if pointer.is_empty() {
                        Some(document)
                    } else {
                        object_pointer(document, &format!("#{pointer}"))
                    }
                })
                .filter(|branch| branch.is_object());
            let code = match branch {
                None => Some(""),
                Some(branch) => match branch
                    .pointer("/properties/type/const")
                    .and_then(Value::as_str)
                {
                    None => Some("pack_branch_unpinned"),
                    Some(name) if name != text(entry, "type") => {
                        Some("pack_branch_undeclared_type")
                    }
                    _ => None,
                },
            };
            if let Some(code) = code {
                found.push(refusal(
                    code,
                    text(descriptor, "id"),
                    &format!("invalid declared branch {reference:?}"),
                ));
            } else {
                match Iri::parse(format!("{}#{pointer}", uri.unwrap())) {
                    Ok(uri) => {
                        branches.insert(
                            text(entry, "type").to_owned(),
                            uri.to_uri().as_str().to_owned(),
                        );
                    }
                    Err((error, _)) => {
                        found.push(refusal("", text(descriptor, "id"), &error.to_string()))
                    }
                }
            }
        }
    }
    if found.is_empty() {
        Ok(branches)
    } else {
        Err(PackLoadError { refusals: found })
    }
}
