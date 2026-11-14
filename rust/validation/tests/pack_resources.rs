use std::collections::BTreeSet;
use std::path::Path;

use oxap_validation::pack::PackLoader;
use serde_json::Value;

#[test]
fn member_path_and_dependency_refusals_match_the_shared_manifest() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let codes = [
        "pack_member_duplicate",
        "pack_member_target_unknown",
        "pack_restates_core_member",
        "pack_schema_path_escape",
        "pack_dependency_missing",
    ];
    let mut judged = 0;
    for fixture in manifest["fixtures"].as_array().unwrap() {
        if fixture["kind"] != "load-invalid" {
            continue;
        }
        let expected: BTreeSet<_> = fixture["codes"]
            .as_array()
            .unwrap()
            .iter()
            .map(|c| c.as_str().unwrap())
            .collect();
        if !expected.iter().all(|code| codes.contains(code)) {
            continue;
        }
        let paths = [root.join(fixture["path"].as_str().unwrap())];
        let error = PackLoader::read(&paths)
            .and_then(PackLoader::read_resources)
            .err()
            .unwrap();
        assert_eq!(error.codes(), expected, "{}: {error}", fixture["id"]);
        judged += 1;
    }
    assert_eq!(judged, 6);
}

#[test]
fn resources_retain_schema_documents_and_contained_manifest_paths() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs");
    let resources = PackLoader::read(&[root.join("storage"), root.join("index")])
        .unwrap()
        .read_resources()
        .unwrap();
    assert_eq!(resources.descriptors().count(), 2);
    let document = &resources.documents()
        ["https://open-agent-protocol.local/ext/com.example.storage/1.0.0/storage.schema.json"];
    assert!(document["$defs"]["objectsRead"].is_object());
    assert_eq!(resources.documents().len(), 2);
    let manifest =
        &resources.manifests()["https://open-agent-protocol.local/ext/com.example.storage/1.0.0/"];
    assert_eq!(
        *manifest,
        std::fs::canonicalize(root.join("storage/fixtures/manifest.json")).unwrap()
    );
}

#[test]
fn a_member_needs_its_own_valid_gate() {
    let directory =
        std::env::temp_dir().join(format!("oap-pack-member-gates-{}", std::process::id()));
    std::fs::create_dir(&directory).unwrap();
    let mut observed = Vec::new();
    for mixed in [false, true] {
        let mut gate = serde_json::json!({"payload_type":"run.completed", "member":"com.example.stage.extra", "ungated":true});
        let mut descriptor = serde_json::json!({
            "id":"com.example.stage", "version":"1", "schemas":["types.json"],
            "payload_members":[{"payload_type":"run.completed", "member":"com.example.stage.extra", "schema":{}}]
        });
        if mixed {
            gate["type"] = "com.example.stage.event".into();
            descriptor["envelope_types"] = serde_json::json!([{"type":"com.example.stage.event", "role":"event", "schema":"types.json"}]);
        } else {
            gate["capability"] = "com.example.stage.feature".into();
            descriptor["capability_keys"] = serde_json::json!(["com.example.stage.feature"]);
        }
        descriptor["gates"] = serde_json::json!([gate]);
        std::fs::write(
            directory.join("pack.json"),
            serde_json::to_vec(&descriptor).unwrap(),
        )
        .unwrap();
        let codes = PackLoader::read(std::slice::from_ref(&directory))
            .err()
            .map(|error| {
                error
                    .codes()
                    .into_iter()
                    .map(str::to_owned)
                    .collect::<Vec<_>>()
            })
            .unwrap_or_default();
        observed.push(codes);
    }
    std::fs::remove_dir_all(directory).unwrap();
    assert_eq!(
        observed,
        vec![vec!["pack_ungated_type"], vec!["pack_ungated_type"]]
    );
}
