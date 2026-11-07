use std::collections::BTreeSet;
use std::path::PathBuf;

use serde_json::Value;

use super::corpus::{self, Manifest};
use super::resources::clean_path;
use super::{entries, failure, refusal, text, CompiledPacks, PackLoadError, PackLoader};

impl PackLoader {
    pub fn load(directories: &[PathBuf]) -> Result<CompiledPacks, PackLoadError> {
        let compiled = Self::read(directories)?.read_resources()?.compile()?;
        let loaded = compiled
            .resources()
            .descriptors()
            .map(|(_, d)| unit(d))
            .collect();
        corpus::check(&compiled, |descriptor, manifest| {
            check(descriptor, manifest, &loaded)
        })?;
        Ok(compiled)
    }
}

fn unit(descriptor: &Value) -> String {
    format!(
        "ext:{}/{}",
        text(descriptor, "id"),
        text(descriptor, "version")
    )
}

fn check(
    descriptor: &Value,
    manifest: Option<&Manifest>,
    loaded: &BTreeSet<String>,
) -> Result<(), PackLoadError> {
    let id = text(descriptor, "id");
    let own = unit(descriptor);
    let invalid = |message: String| failure(id, message);
    let keys: Vec<_> = entries(descriptor, "capability_keys")
        .filter_map(Value::as_str)
        .collect();
    let Some(manifest) = manifest else {
        return if keys.is_empty() {
            Ok(())
        } else {
            Err(invalid(
                "declares capability keys without a fixture manifest".to_owned(),
            ))
        };
    };
    let mut ids = BTreeSet::new();
    let mut paths = BTreeSet::new();
    let mut covered = BTreeSet::new();
    for entry in &manifest.fixtures {
        if entry.id.is_empty() || entry.path.is_empty() {
            return Err(invalid("fixture lacks id or path".to_owned()));
        }
        let clean = clean_path(&entry.path)
            .map_err(|_| invalid("fixture path outside fixture root".to_owned()))?;
        corpus::check_entry(entry, id)?;
        let provider = !entry.profile.is_empty();
        if !provider {
            for coverage in &entry.covers {
                if coverage.capability.trim().is_empty()
                    || !matches!(coverage.aspect.as_str(), "gate" | "honour")
                    || entry.kind != "semantic-invalid"
                {
                    return Err(invalid("invalid negative capability coverage".to_owned()));
                }
            }
            if !matches!(entry.mode.as_str(), "" | "strict" | "tolerant") {
                return Err(invalid("unsupported validation mode".to_owned()));
            }
            if !entry.packs.is_empty() {
                return Err(invalid(
                    "a pack fixture cannot load further packs".to_owned(),
                ));
            }
            if entry.units.is_empty() {
                return Err(invalid("fixture has no conformance units".to_owned()));
            }
            for claimed in &entry.units {
                let code = if !claimed.starts_with("ext:") {
                    "pack_fixture_claims_core_unit"
                } else if !loaded.contains(claimed) {
                    "ext_claim_without_pack"
                } else if claimed != &own {
                    ""
                } else {
                    continue;
                };
                return Err(PackLoadError {
                    refusals: vec![refusal(
                        code,
                        id,
                        "a pack corpus must claim its own loaded extension unit",
                    )],
                });
            }
            if entry.provenance.iter().any(|p| p.trim().is_empty()) {
                return Err(invalid("fixture has empty provenance".to_owned()));
            }
        }
        let path = if provider {
            clean.to_string_lossy().into_owned()
        } else {
            entry.path.clone()
        };
        if !ids.insert(&entry.id) || !paths.insert(path) {
            return Err(invalid("duplicate fixture id or path".to_owned()));
        }
        if entry.kind == "semantic-invalid" && entry.units.contains(&own) {
            covered.extend(
                entry
                    .covers
                    .iter()
                    .map(|c| (c.capability.as_str(), c.aspect.as_str())),
            );
        }
    }
    for key in keys {
        for aspect in ["gate", "honour"] {
            if !covered.contains(&(key, aspect)) {
                return Err(invalid(format!(
                    "corpus completeness: {key} has no negative {aspect} fixture under {own}"
                )));
            }
        }
    }
    Ok(())
}
