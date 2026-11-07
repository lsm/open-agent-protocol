use std::path::Path;

use oap_validation::pack::PackLoader;
use serde_json::Value;

#[test]
fn compiled_resources_refuse_unpinned_and_foreign_branches() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs");
    for (name, code) in [
        ("bad-branch-unpinned", "pack_branch_unpinned"),
        ("bad-branch-undeclared-type", "pack_branch_undeclared_type"),
    ] {
        let error = PackLoader::read(&[root.join(name)])
            .unwrap()
            .read_resources()
            .unwrap()
            .compile()
            .err()
            .unwrap();
        assert_eq!(
            error.codes().into_iter().collect::<Vec<_>>(),
            [code],
            "{name}"
        );
    }
}

#[test]
fn compiled_resources_validate_real_envelope_and_member_values() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let compiled = PackLoader::read(&[root.join("packs/storage")])
        .unwrap()
        .read_resources()
        .unwrap()
        .compile()
        .unwrap();
    for (name, valid) in [
        ("valid/ext-advertised-key-gated.json", true),
        ("schema-invalid/ext-packed-type-malformed.json", false),
        ("schema-invalid/ext-core-invalid-stays-invalid.json", false),
    ] {
        let trace: Value =
            serde_json::from_slice(&std::fs::read(root.join(name)).unwrap()).unwrap();
        assert_eq!(
            trace
                .as_array()
                .unwrap()
                .iter()
                .all(|e| compiled.schema().is_valid(e)),
            valid,
            "{name}"
        );
    }
    let member = &compiled.member_schemas()[&(
        "run.completed".to_owned(),
        "com.example.storage.bytes_written".to_owned(),
    )];
    assert!(member.is_valid(&serde_json::json!(42)));
    assert!(!member.is_valid(&serde_json::json!(-1)));
}
