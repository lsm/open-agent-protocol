use std::collections::BTreeSet;
use std::path::Path;

use oap_validation::pack::PackLoader;
use serde_json::Value;

const CODES: [&str; 10] = [
    "pack_unprefixed_name",
    "pack_foreign_prefix",
    "pack_id_collision",
    "pack_type_duplicate",
    "pack_role_undeclared",
    "pack_reply_target_unknown",
    "pack_reply_target_ambiguous",
    "pack_refusal_undeclared",
    "pack_response_gated",
    "pack_ungated_type",
];

#[test]
fn staged_descriptor_reading_enforces_shared_type_declarations() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let mut judged = 0;
    for fixture in manifest["fixtures"].as_array().unwrap() {
        if fixture["kind"] != "load-invalid" {
            continue;
        }
        let expected: BTreeSet<_> = fixture["codes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|code| code.as_str().unwrap())
            .collect();
        if !expected.iter().all(|code| CODES.contains(code)) {
            continue;
        }
        let mut paths = vec![root.join(fixture["path"].as_str().unwrap())];
        paths.extend(
            fixture["packs"]
                .as_array()
                .into_iter()
                .flatten()
                .map(|p| root.join(p.as_str().unwrap())),
        );
        let error = PackLoader::read(&paths)
            .err()
            .expect("a malformed declaration was accepted");
        assert_eq!(error.codes(), expected, "{}: {error}", fixture["id"]);
        judged += 1;
    }
    assert_eq!(judged, 12);
}

#[test]
fn a_descriptor_reader_retains_the_named_roots_and_descriptors() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs");
    let loader = PackLoader::read(&[root.join("storage"), root.join("index")]).unwrap();
    let descriptors: Vec<_> = loader.descriptors().collect();
    assert_eq!(descriptors.len(), 2);
    assert!(descriptors[0].0.is_absolute());
    assert_eq!(descriptors[0].1["id"], "com.example.storage");
    assert_eq!(descriptors[1].1["id"], "com.example.index");
}
