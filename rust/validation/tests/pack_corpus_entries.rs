use std::path::Path;

use oap_validation::pack::PackLoader;

#[test]
fn staged_corpus_entry_check_accepts_the_shared_pack_manifests() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs");
    for name in ["storage", "descriptor-member", "bad-fixture-claims-core", "bad-ext-claim"] {
        let compiled = PackLoader::read(&[root.join(name)])
            .unwrap()
            .read_resources()
            .unwrap()
            .compile()
            .unwrap();
        compiled.check_corpus_entries().unwrap();
    }
}
