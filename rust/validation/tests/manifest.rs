use std::collections::BTreeSet;
use std::path::PathBuf;

use oap_validation::Validator;
use serde::Deserialize;
use serde_json::Value;

fn fixtures() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures")
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct KnownGaps {
    version: u32,
    zig: Vec<String>,
    rust: Vec<String>,
}

#[test]
fn every_fixture_draws_exactly_its_declared_codes_unless_rust_lists_it_as_a_known_gap() {
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(fixtures().join("manifest.json")).unwrap()).unwrap();
    let gaps: KnownGaps =
        serde_json::from_slice(&std::fs::read(fixtures().join("known-gaps.json")).unwrap())
            .unwrap();
    assert_eq!(gaps.version, 1);
    let _ = gaps.zig;
    let listed: BTreeSet<&str> = gaps.rust.iter().map(String::as_str).collect();
    let validator = Validator::new().unwrap();
    let mut judged = BTreeSet::new();
    let mut wrong = Vec::new();
    let mut agreeing = Vec::new();
    for entry in manifest["fixtures"].as_array().unwrap() {
        if entry["kind"] == "load-invalid" {
            continue;
        }
        let id = entry["id"].as_str().unwrap();
        let path = entry["path"].as_str().unwrap();
        let result = validator.validate(&std::fs::read(fixtures().join(path)).unwrap(), path);
        let mut want: Vec<&str> = entry["codes"]
            .as_array()
            .map(|codes| codes.iter().filter_map(Value::as_str).collect())
            .unwrap_or_default();
        let mut got: Vec<&str> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
        want.sort_unstable();
        got.sort_unstable();
        match (want == got, listed.contains(id)) {
            (false, false) => wrong.push(format!("{id}: want {want:?}, got {got:?}")),
            (true, true) => agreeing.push(id),
            _ => {}
        }
        judged.insert(id);
    }
    let unjudged: Vec<&&str> = listed.iter().filter(|id| !judged.contains(**id)).collect();
    assert!(unjudged.is_empty(), "known gaps never judged: {unjudged:?}");
    assert!(
        agreeing.is_empty(),
        "known gaps that now agree, delete them: {agreeing:?}"
    );
    assert!(
        wrong.is_empty(),
        "{} disagree and are not known gaps:\n{}",
        wrong.len(),
        wrong.join("\n")
    );
    assert!(judged.len() >= 700, "judged only {}", judged.len());
}
