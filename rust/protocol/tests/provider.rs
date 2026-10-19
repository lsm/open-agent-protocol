use std::path::PathBuf;

use oap_protocol::{EnvelopeType, ProviderEnvelope, PROVIDER_PROFILE};
use serde_json::Value;

fn fixtures() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures")
}

#[test]
fn every_positive_provider_fixture_decodes_and_reencodes_to_the_same_json() {
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(fixtures().join("manifest.json")).unwrap()).unwrap();
    let mut judged = 0;
    for entry in manifest["fixtures"].as_array().unwrap() {
        if entry["kind"] != "positive" || entry["profile"] != "model-provider-core" {
            continue;
        }
        let path = fixtures().join(entry["path"].as_str().unwrap());
        let trace: Value = serde_json::from_slice(&std::fs::read(&path).unwrap()).unwrap();
        let Value::Array(envelopes) = trace else {
            continue;
        };
        for original in envelopes {
            let envelope = ProviderEnvelope::parse(original.to_string().as_bytes())
                .unwrap_or_else(|err| panic!("{}: {err}", path.display()));
            assert_eq!(
                serde_json::to_value(&envelope).unwrap(),
                original,
                "{}",
                path.display()
            );
            judged += 1;
        }
    }
    assert!(judged >= 20, "judged only {judged} provider envelopes");
}

#[test]
fn a_provider_envelope_reads_an_integral_sequence_in_any_spelling_and_keeps_an_unknown_member() {
    let envelope = ProviderEnvelope::parse(
        br#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.started","id":"e1","inference_id":"i1","sequence":1.0,"future":true,"payload":{}}"#,
    )
    .unwrap();
    assert_eq!(envelope.sequence, Some(1));
    assert_eq!(
        envelope.inference_id.as_ref().map(|id| id.as_str()),
        Some("i1")
    );
    assert!(envelope.unknown.contains_key("future"));
    assert!(ProviderEnvelope::parse(br#"{"sequence":"1"}"#).is_err());
}

#[test]
fn a_new_provider_envelope_names_the_provider_profile() {
    let envelope = ProviderEnvelope::new(
        EnvelopeType::from_static("provider.describe.request"),
        "d1",
        &serde_json::json!({}),
    )
    .unwrap();
    assert_eq!(envelope.profile, PROVIDER_PROFILE);
    assert_eq!(
        envelope.decode_payload::<Value>().unwrap(),
        serde_json::json!({})
    );
}
