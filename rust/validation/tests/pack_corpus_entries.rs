use std::path::Path;

use oap_validation::pack::PackLoader;

#[test]
fn staged_corpus_entry_check_accepts_the_shared_pack_manifests() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs");
    for name in [
        "storage",
        "descriptor-member",
        "bad-fixture-claims-core",
        "bad-fixture-claims-sibling",
    ] {
        let compiled = PackLoader::read(&[root.join(name)])
            .unwrap()
            .read_resources()
            .unwrap()
            .compile()
            .unwrap();
        compiled.check_corpus_entries().unwrap();
    }
}

#[test]
fn public_corpus_checks_refuse_bad_expectations_and_preserve_null_defaults() {
    struct Temporary(std::path::PathBuf);
    impl Drop for Temporary {
        fn drop(&mut self) {
            std::fs::remove_dir_all(&self.0).unwrap();
        }
    }
    let directory = Temporary(std::env::temp_dir().join(format!(
        "oap-corpus-entries-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )));
    std::fs::create_dir(&directory.0).unwrap();
    std::fs::write(directory.0.join("types.json"), "{}").unwrap();
    std::fs::write(directory.0.join("manifest.json"), "{}").unwrap();
    std::fs::write(
        directory.0.join("pack.json"),
        serde_json::to_vec(&serde_json::json!({
            "id":"com.example.corpus", "version":"1", "schemas":["types.json"],
            "fixtures":"manifest.json"
        }))
        .unwrap(),
    )
    .unwrap();
    let compiled = PackLoader::read(std::slice::from_ref(&directory.0))
        .unwrap()
        .read_resources()
        .unwrap()
        .compile()
        .unwrap();
    for (patch, accepted) in [
        (serde_json::json!({}), true),
        (serde_json::json!({"valid":7}), false),
        (serde_json::json!({"phase":"schema"}), false),
        (serde_json::json!({"rules":[null]}), true),
        (serde_json::json!({"profile":"unknown"}), false),
        (serde_json::json!({"kind":"other"}), false),
        (
            serde_json::json!({"kind":"semantic-invalid", "valid":false, "phase":"semantic", "codes":["com.example.corpus.error"]}),
            false,
        ),
        (
            serde_json::json!({"kind":"load-invalid", "valid":false, "phase":"load", "codes":["load-invalid"]}),
            false,
        ),
        (
            serde_json::json!({"kind":"semantic-invalid", "valid":null, "phase":"semantic", "codes":["unavailable_capability"]}),
            true,
        ),
        (
            serde_json::json!({"kind":"semantic-invalid", "valid":false, "phase":"semantic", "codes":["sequence_gap"], "profile":"model-provider-core", "scope":"frame", "units":["provider-core"]}),
            false,
        ),
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
            serde_json::to_vec(&serde_json::json!({"version":1, "fixtures":[entry]})).unwrap(),
        )
        .unwrap();
        let result = compiled.check_corpus_entries();
        assert_eq!(result.is_ok(), accepted, "{patch}: {result:?}");
    }
}
