use std::path::Path;

use oxap_validation::pack::PackLoader;
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

#[test]
fn full_constructor_refuses_nested_packs_absent_units_and_incomplete_capabilities() {
    struct Temporary(std::path::PathBuf);
    impl Drop for Temporary {
        fn drop(&mut self) {
            std::fs::remove_dir_all(&self.0).unwrap();
        }
    }
    let directory = Temporary(std::env::temp_dir().join(format!(
        "oap-complete-corpus-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )));
    std::fs::create_dir(&directory.0).unwrap();
    std::fs::write(directory.0.join("types.json"), "{}").unwrap();
    let mut descriptor = serde_json::json!({
        "id":"com.example.corpus", "version":"1", "schemas":["types.json"],
        "fixtures":"manifest.json"
    });
    std::fs::write(
        directory.0.join("pack.json"),
        serde_json::to_vec(&descriptor).unwrap(),
    )
    .unwrap();
    for (patch, accepted) in [
        (serde_json::json!({}), true),
        (serde_json::json!({"packs":["other"]}), false),
        (serde_json::json!({"units":[]}), false),
        (serde_json::json!({"units":null}), false),
        (serde_json::json!({"mode":"other"}), false),
        (serde_json::json!({"provenance":[""]}), false),
        (serde_json::json!({"units":["core"]}), false),
    ] {
        let mut entry = serde_json::json!({
            "id":"entry", "path":"trace.json", "kind":"positive", "valid":true,
            "units":["ext:com.example.corpus/1"]
        });
        entry
            .as_object_mut()
            .unwrap()
            .extend(patch.as_object().unwrap().clone());
        std::fs::write(
            directory.0.join("manifest.json"),
            serde_json::to_vec(&serde_json::json!({"version":1,"fixtures":[entry]})).unwrap(),
        )
        .unwrap();
        let result = PackLoader::load(std::slice::from_ref(&directory.0));
        assert_eq!(result.is_ok(), accepted, "{patch}");
    }
    descriptor["capability_keys"] = serde_json::json!(["com.example.corpus.key"]);
    std::fs::write(
        directory.0.join("pack.json"),
        serde_json::to_vec(&descriptor).unwrap(),
    )
    .unwrap();
    for aspects in [vec![], vec!["gate"], vec!["honour"], vec!["gate", "honour"]] {
        let entries: Vec<_> = aspects
            .iter()
            .map(|aspect| {
                serde_json::json!({
                    "id":aspect, "path":format!("{aspect}.json"), "kind":"semantic-invalid",
                    "valid":false, "phase":"semantic", "codes":["unavailable_capability"],
                    "units":["ext:com.example.corpus/1"],
                    "covers":[{"capability":"com.example.corpus.key", "aspect":aspect}]
                })
            })
            .collect();
        std::fs::write(
            directory.0.join("manifest.json"),
            serde_json::to_vec(&serde_json::json!({"version":1,"fixtures":entries})).unwrap(),
        )
        .unwrap();
        let result = PackLoader::load(std::slice::from_ref(&directory.0));
        assert_eq!(result.is_ok(), aspects.len() == 2, "{aspects:?}");
    }
}
