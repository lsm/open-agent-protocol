use std::collections::BTreeSet;
use std::path::PathBuf;

use oxap_validation::{Phase, ProviderValidator, Validator};
use serde::Deserialize;
use serde_json::Value;

fn fixtures() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures")
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct KnownGaps {
    version: u32,
    go: Vec<String>,
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
    let _ = (gaps.go, gaps.zig);
    let listed: BTreeSet<&str> = gaps.rust.iter().map(String::as_str).collect();
    let validator = Validator::new().unwrap();
    let provider = ProviderValidator::new().unwrap();
    let mut judged = BTreeSet::new();
    let mut wrong = Vec::new();
    let mut agreeing = Vec::new();
    for entry in manifest["fixtures"].as_array().unwrap() {
        let id = entry["id"].as_str().unwrap();
        let path = entry["path"].as_str().unwrap();
        let packs: Vec<_> = entry["packs"]
            .as_array()
            .into_iter()
            .flatten()
            .map(|p| fixtures().join(p.as_str().unwrap()))
            .collect();
        if entry["kind"] == "load-invalid" {
            let directories: Vec<_> = std::iter::once(fixtures().join(path))
                .chain(packs)
                .collect();
            match Validator::with_packs(&directories) {
                Ok(_) => wrong.push(format!("{id}: expected a load refusal")),
                Err(error) => {
                    let want: BTreeSet<_> = entry["codes"]
                        .as_array()
                        .unwrap()
                        .iter()
                        .map(|c| c.as_str().unwrap())
                        .collect();
                    if error.codes() != want {
                        wrong.push(format!("{id}: want {want:?}, got {:?}", error.codes()));
                    }
                }
            }
            judged.insert(id);
            continue;
        }
        let configured = match (packs.is_empty(), entry["mode"].as_str()) {
            (true, Some("tolerant")) => Some(Validator::tolerant().unwrap()),
            (false, Some("tolerant")) => Some(Validator::tolerant_with_packs(&packs).unwrap()),
            (false, _) => Some(Validator::with_packs(&packs).unwrap()),
            (true, _) => None,
        };
        let data = std::fs::read(fixtures().join(path)).unwrap();
        let result = if entry["profile"] == "model-provider-core" {
            provider.validate(&data, path)
        } else {
            configured
                .as_ref()
                .unwrap_or(&validator)
                .validate(&data, path)
        };
        let mut want: Vec<&str> = entry["codes"]
            .as_array()
            .map(|codes| codes.iter().filter_map(Value::as_str).collect())
            .unwrap_or_default();
        let mut got: Vec<&str> = result.diagnostics.iter().map(|d| d.code.as_str()).collect();
        want.sort_unstable();
        want.dedup();
        got.sort_unstable();
        got.dedup();
        let declared_phase = entry["phase"].as_str().unwrap_or_default();
        let got_phase = result
            .primary_phase()
            .map(Phase::as_str)
            .unwrap_or_default();
        let agrees = want == got && (declared_phase.is_empty() || declared_phase == got_phase);
        match (agrees, listed.contains(id)) {
            (false, false) => wrong.push(format!(
                "{id}: want {declared_phase} {want:?}, got {got_phase} {got:?}"
            )),
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
