use std::path::PathBuf;

use oap_validation::{codes, Phase, Validator, IMPLEMENTED};
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
        let phase = entry["phase"]
            .as_str()
            .filter(|phase| !phase.is_empty())
            .unwrap_or("semantic");
        if phase != "semantic" {
            continue;
        }
        let path = entry["path"].as_str().unwrap();
        let result = validator.validate(&std::fs::read(fixtures().join(path)).unwrap(), path);
        if let Some(diagnostic) = result
            .diagnostics
            .iter()
            .find(|d| d.phase != Phase::Semantic)
        {
            refused.push(format!("{path}: {diagnostic}"));
        }
        judged += 1;
    }
    assert!(
        refused.is_empty(),
        "{} refused:\n{}",
        refused.len(),
        refused.join("\n")
    );
    assert!(judged >= 640, "judged only {judged}");
}

const GAPS: [(&str, &str); 4] = [
    (
        "models-stale-catalog-does-not-displace",
        codes::STALE_CAPABILITY_REVISION,
    ),
    (
        "models-stale-catalog-raises-no-change",
        codes::STALE_CAPABILITY_REVISION,
    ),
    (
        "queue-capabilities-bound-moves-within-revision",
        codes::STALE_CAPABILITY_REVISION,
    ),
    (
        "queue-capabilities-level-moves-within-revision",
        codes::STALE_CAPABILITY_REVISION,
    ),
];

#[test]
fn every_semantic_fixture_draws_exactly_its_declared_codes_among_those_implemented() {
    let validator = Validator::new().unwrap();
    let mut judged = 0;
    let mut wrong = Vec::new();
    let mut closed = Vec::new();
    for entry in core_entries() {
        let phase = entry["phase"]
            .as_str()
            .filter(|phase| !phase.is_empty())
            .unwrap_or("semantic");
        if phase != "semantic" {
            continue;
        }
        let path = entry["path"].as_str().unwrap();
        let id = entry["id"].as_str().unwrap();
        let result = validator.validate(&std::fs::read(fixtures().join(path)).unwrap(), path);
        let declared: Vec<&str> = entry["codes"]
            .as_array()
            .map(|codes| codes.iter().filter_map(Value::as_str).collect())
            .unwrap_or_default();
        let gapped: Vec<&str> = GAPS
            .iter()
            .filter(|(fixture, _)| *fixture == id)
            .map(|(_, code)| *code)
            .collect();
        let mut want: Vec<&str> = declared
            .iter()
            .copied()
            .filter(|code| IMPLEMENTED.contains(code) && !gapped.contains(code))
            .collect();
        let mut got: Vec<&str> = result
            .diagnostics
            .iter()
            .map(|d| d.code.as_str())
            .filter(|code| !gapped.contains(code))
            .collect();
        for code in &gapped {
            let raised = result
                .diagnostics
                .iter()
                .filter(|d| d.code == *code)
                .count();
            if raised == declared.iter().filter(|declared| *declared == code).count() {
                closed.push(format!("{id}: {code}"));
            }
        }
        want.sort_unstable();
        got.sort_unstable();
        if want != got {
            wrong.push(format!("{id}: want {want:?}, got {got:?}"));
        }
        judged += 1;
    }
    assert!(
        closed.is_empty(),
        "listed as gaps, now raised:\n{}",
        closed.join("\n")
    );
    assert!(
        wrong.is_empty(),
        "{} wrong:\n{}",
        wrong.len(),
        wrong.join("\n")
    );
    assert!(judged >= 640, "judged only {judged}");
}
