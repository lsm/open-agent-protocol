use std::path::Path;

use oap_validation::pack::PackLoader;

#[test]
fn resource_construction_refuses_external_references_and_identifiers() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs");
    let mut failures = Vec::new();
    for name in ["bad-external-ref", "bad-schema-id-escape"] {
        match PackLoader::read(&[root.join(name)])
            .unwrap()
            .read_resources()
        {
            Ok(_) => failures.push(format!("{name}: external resource was accepted")),
            Err(error) => assert_eq!(
                error.codes().into_iter().collect::<Vec<_>>(),
                ["pack_external_ref"],
                "{name}"
            ),
        }
    }
    assert!(failures.is_empty(), "{}", failures.join("\n"));
}
