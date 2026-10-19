use std::path::PathBuf;

use oap_validation::{Phase, Validator};
use serde_json::Value;

fn fixtures() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures")
}

fn core_entries() -> Vec<Value> {
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(fixtures().join("manifest.json")).unwrap()).unwrap();
    manifest["fixtures"]
        .as_array()
        .unwrap()
        .iter()
        .filter(|entry| {
            entry["kind"] != "load-invalid"
                && entry.get("packs").is_none()
                && entry.get("mode").is_none_or(|mode| mode == "strict")
                && entry
                    .get("profile")
                    .is_none_or(|profile| profile == "agent-control-core")
        })
        .cloned()
        .collect()
}

#[test]
fn every_decode_and_schema_fixture_draws_exactly_its_declared_codes() {
    let validator = Validator::new().unwrap();
    let mut judged = 0;
    let mut wrong = Vec::new();
    for entry in core_entries() {
        let phase = entry["phase"].as_str().unwrap_or_default();
        if phase != "decode" && phase != "schema" {
            continue;
        }
        let path = entry["path"].as_str().unwrap();
        let result = validator.validate(&std::fs::read(fixtures().join(path)).unwrap(), path);
        let mut want: Vec<&str> = entry["codes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|code| code.as_str().unwrap())
            .collect();
        let mut got: Vec<&str> = result
            .diagnostics
            .iter()
            .map(|diagnostic| diagnostic.code.as_str())
            .collect();
        want.sort_unstable();
        got.sort_unstable();
        let got_phase = result
            .primary_phase()
            .map(Phase::as_str)
            .unwrap_or_default();
        if want != got || got_phase != phase {
            wrong.push(format!(
                "{path}: want {phase} {want:?}, got {got_phase} {got:?}"
            ));
        }
        judged += 1;
    }
    assert!(
        wrong.is_empty(),
        "{} wrong:\n{}",
        wrong.len(),
        wrong.join("\n")
    );
    assert!(judged >= 40, "judged only {judged}");
}

#[test]
fn every_positive_and_semantic_fixture_passes_decode_and_schema() {
    let validator = Validator::new().unwrap();
    let mut judged = 0;
    let mut refused = Vec::new();
    for entry in core_entries() {
        let phase = entry["phase"].as_str().unwrap_or("semantic");
        if phase != "semantic" {
            continue;
        }
        let path = entry["path"].as_str().unwrap();
        let result = validator.validate(&std::fs::read(fixtures().join(path)).unwrap(), path);
        if !result.valid() {
            refused.push(format!("{path}: {}", result.diagnostics[0]));
        }
        judged += 1;
    }
    assert!(
        refused.is_empty(),
        "{} refused:\n{}",
        refused.len(),
        refused.join("\n")
    );
    assert!(judged >= 450, "judged only {judged}");
}
