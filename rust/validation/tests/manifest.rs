use std::path::PathBuf;

use oap_validation::{Phase, Validator, IMPLEMENTED};
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

const DISAGREEING: [&str; 12] = [
    "compaction-response-scope-mismatch",
    "models-held-position-in-another-session",
    "models-held-position-in-another-session-no-current",
    "models-position-in-another-session",
    "models-position-in-another-session-no-current",
    "models-stale-catalog-does-not-displace",
    "models-stale-catalog-raises-no-change",
    "queue-capabilities-bound-moves-within-revision",
    "queue-capabilities-level-moves-within-revision",
    "scope-mismatch",
    "steer-explicit-target-mismatch",
    "tools-list-unscoped-answer-scoped",
];

#[test]
fn every_semantic_fixture_draws_exactly_its_declared_codes_among_those_implemented_unless_listed() {
    let validator = Validator::new().unwrap();
    let mut judged = Vec::new();
    let mut wrong = Vec::new();
    let mut agreeing = Vec::new();
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
        let mut want: Vec<&str> = entry["codes"]
            .as_array()
            .map(|codes| {
                codes
                    .iter()
                    .filter_map(Value::as_str)
                    .filter(|code| IMPLEMENTED.contains(code))
                    .collect()
            })
            .unwrap_or_default();
        let mut got: Vec<&str> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
        want.sort_unstable();
        got.sort_unstable();
        let listed = DISAGREEING.contains(&id);
        if want != got && !listed {
            wrong.push(format!("{id}: want {want:?}, got {got:?}"));
        }
        if want == got && listed {
            agreeing.push(id.to_owned());
        }
        judged.push(id.to_owned());
    }
    let unjudged: Vec<&str> = DISAGREEING
        .iter()
        .copied()
        .filter(|id| !judged.iter().any(|j| j == id))
        .collect();
    assert!(unjudged.is_empty(), "listed but never judged: {unjudged:?}");
    assert!(
        agreeing.is_empty(),
        "listed as disagreeing, now agree: {agreeing:?}"
    );
    assert!(
        wrong.is_empty(),
        "{} wrong:\n{}",
        wrong.len(),
        wrong.join("\n")
    );
    assert!(judged.len() >= 640, "judged only {}", judged.len());
}
