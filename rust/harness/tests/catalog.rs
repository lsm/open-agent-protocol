use std::path::PathBuf;

use oap_harness::{current, Catalog, Error, Status, FILES, SCHEMA_FILE};

fn harnesses_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../harnesses")
}

#[test]
fn the_embedded_list_names_every_harness_file_in_the_catalog_directory() {
    let mut on_disk: Vec<String> = std::fs::read_dir(harnesses_dir())
        .unwrap()
        .map(|entry| entry.unwrap().file_name().into_string().unwrap())
        .filter(|name| name.ends_with(".json") && name != SCHEMA_FILE)
        .collect();
    on_disk.sort();
    let embedded: Vec<&str> = FILES.iter().map(|(name, _)| *name).collect();
    assert_eq!(on_disk, embedded);
}

#[test]
fn every_harness_loads_with_exactly_one_current_version() {
    let catalog = Catalog::embedded();
    assert_eq!(catalog.harnesses.len(), FILES.len());
    for harness in &catalog.harnesses {
        let current = harness
            .versions
            .iter()
            .filter(|version| version.status == Status::Current)
            .count();
        assert_eq!(current, 1, "{}", harness.id);
        let version = harness.current().unwrap();
        assert!(!version.capability_revision.is_empty(), "{}", harness.id);
        assert!(!version.corpus.is_empty(), "{}", harness.id);
    }
}

#[test]
fn a_current_pin_reads_its_components_sources_and_artifacts() {
    let claude = current("claude-code").unwrap();
    assert!(claude.component("cli").is_some());
    assert!(claude
        .source("cli")
        .is_some_and(|source| source.commit.len() == 40));
    assert!(claude
        .artifact("cli", "linux-x64", "binary")
        .is_some_and(|artifact| artifact.sha256.len() == 64));
    assert!(claude.artifact("cli", "plan9", "binary").is_none());
    assert!(current("no-such-harness").is_none());
}

fn reject(name: &str, text: &str) -> String {
    match Catalog::load([(name, text)]) {
        Err(Error::Harness { message, .. }) => message,
        other => panic!("{name} was not refused: {other:?}"),
    }
}

#[test]
fn a_harness_file_is_refused_for_a_misplaced_id_an_unknown_member_or_a_repeated_one() {
    let acp = FILES[0].1;
    assert!(reject("other.json", acp).contains("belongs in acp.json"));
    let unknown = acp.replacen("\"name\":", "\"surprise\": 1, \"name\":", 1);
    assert!(!reject("acp.json", &unknown).is_empty());
    let repeated = acp.replacen("\"name\":", "\"id\": \"acp\", \"name\":", 1);
    assert!(!reject("acp.json", &repeated).is_empty());
    assert!(matches!(
        Catalog::load(Vec::<(&str, &str)>::new()),
        Err(Error::Empty)
    ));
}
