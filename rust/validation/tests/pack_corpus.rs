use std::path::Path;

use oap_validation::pack::PackLoader;
use serde_json::Value;

#[test]
fn complete_loader_refuses_every_shared_load_invalid_pack() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    for fixture in manifest["fixtures"].as_array().unwrap() {
        if fixture["kind"] != "load-invalid" {
            continue;
        }
        let directories: Vec<_> = std::iter::once(fixture["path"].as_str().unwrap())
            .chain(
                fixture["packs"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .map(|p| p.as_str().unwrap()),
            )
            .map(|p| root.join(p))
            .collect();
        let error = PackLoader::load(&directories).err().unwrap();
        let expected: std::collections::BTreeSet<_> = fixture["codes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|c| c.as_str().unwrap())
            .collect();
        assert_eq!(error.codes(), expected, "{}", fixture["id"]);
    }
}

#[test]
fn complete_loader_keeps_compiled_schema_and_member_access() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs");
    let compiled = PackLoader::load(&[root.join("storage")]).unwrap();
    assert_eq!(compiled.resources().descriptors().count(), 1);
    let member = &compiled.member_schemas()[&(
        "run.completed".to_owned(),
        "com.example.storage.bytes_written".to_owned(),
    )];
    assert!(member.is_valid(&serde_json::json!(42)));
    assert!(!member.is_valid(&serde_json::json!(-1)));
}
