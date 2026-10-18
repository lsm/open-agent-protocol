use std::path::PathBuf;

use oap_protocol::{decode, Envelope};
use serde_json::Value;

fn fixtures() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures")
}

fn envelopes_of(trace: &Value) -> Vec<Value> {
    match trace {
        Value::Array(items) => items.clone(),
        Value::Object(object) => match object.get("events") {
            Some(Value::Array(items)) => items.clone(),
            _ => vec![trace.clone()],
        },
        _ => Vec::new(),
    }
}

#[test]
fn every_positive_core_fixture_decodes_and_reencodes_to_the_same_json() {
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(fixtures().join("manifest.json")).unwrap()).unwrap();
    let mut judged = 0;
    for entry in manifest["fixtures"].as_array().unwrap() {
        if entry["kind"] != "positive"
            || entry
                .get("profile")
                .is_some_and(|p| p != "agent-control-core")
        {
            continue;
        }
        let path = fixtures().join(entry["path"].as_str().unwrap());
        let bytes = std::fs::read(&path).unwrap();
        let trace: Value = serde_json::from_slice(&bytes).unwrap();
        let expected = envelopes_of(&trace);
        let decoded: Vec<Envelope> = expected
            .iter()
            .map(|value| Envelope::parse(value.to_string().as_bytes()))
            .collect::<Result<_, _>>()
            .unwrap_or_else(|err| panic!("{}: {err}", path.display()));
        for (envelope, original) in decoded.iter().zip(&expected) {
            assert_eq!(
                &serde_json::to_value(envelope).unwrap(),
                original,
                "{}",
                path.display()
            );
        }
        if matches!(trace, Value::Array(_)) {
            assert_eq!(
                decode(bytes.as_slice()).unwrap().len(),
                expected.len(),
                "{}",
                path.display()
            );
        }
        judged += 1;
    }
    assert!(judged >= 200, "judged only {judged} positive fixtures");
}
