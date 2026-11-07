use std::path::Path;

use oap_validation::pack::PackLoader;
use serde_json::Value;

#[test]
fn compiled_packs_validate_complete_envelopes_and_member_values() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    for (pack, trace, valid) in [
        ("storage", "valid/ext-advertised-key-gated.json", true),
        (
            "storage",
            "schema-invalid/ext-packed-type-malformed.json",
            false,
        ),
        (
            "descriptor-member",
            "valid/ext-descriptor-member-advertised.json",
            true,
        ),
        (
            "storage",
            "schema-invalid/ext-core-invalid-stays-invalid.json",
            false,
        ),
    ] {
        let compiled = PackLoader::load(&[root.join("packs").join(pack)]).unwrap();
        let trace: Value =
            serde_json::from_slice(&std::fs::read(root.join(trace)).unwrap()).unwrap();
        let results: Vec<_> = trace
            .as_array()
            .unwrap()
            .iter()
            .map(|e| compiled.validate_schema(e, "public-schema-contract"))
            .collect();
        assert_eq!(results.iter().all(Result::is_ok), valid, "{results:?}");
    }
}

#[test]
fn member_errors_point_into_the_original_payload() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let compiled = PackLoader::load(&[root.join("packs/descriptor-member")]).unwrap();
    let mut trace: Value = serde_json::from_slice(
        &std::fs::read(root.join("valid/ext-descriptor-member-advertised.json")).unwrap(),
    )
    .unwrap();
    trace[1]["payload"]["com.example.descriptor.region"] = serde_json::json!(17);
    let diagnostic = compiled
        .validate_schema(&trace[1], "member-pointer")
        .unwrap_err();
    assert_eq!(diagnostic.code, "schema_invalid");
    assert_eq!(diagnostic.pointer, "/payload/com.example.descriptor.region");
}
