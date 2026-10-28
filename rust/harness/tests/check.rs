use std::collections::BTreeMap;
use std::path::PathBuf;

use oap_harness::{check, AdapterPin, Catalog, Code, Finding, Harness, Status, Version};

fn tree() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn catalog() -> Catalog {
    Catalog::embedded().clone()
}

fn pins(catalog: &Catalog) -> BTreeMap<String, AdapterPin> {
    catalog
        .harnesses
        .iter()
        .filter_map(|harness| {
            let current = harness.current()?;
            Some((
                harness.id.clone(),
                AdapterPin {
                    capability_revision: current.capability_revision.clone(),
                    corpus: current.corpus.clone(),
                },
            ))
        })
        .collect()
}

fn harness<'a>(catalog: &'a mut Catalog, id: &str) -> &'a mut Harness {
    catalog
        .harnesses
        .iter_mut()
        .find(|harness| harness.id == id)
        .unwrap()
}

fn current<'a>(catalog: &'a mut Catalog, id: &str) -> &'a mut Version {
    harness(catalog, id)
        .versions
        .iter_mut()
        .find(|version| version.status == Status::Current)
        .unwrap()
}

fn labelled<'a>(catalog: &'a mut Catalog, id: &str, label: &str) -> &'a mut Version {
    harness(catalog, id)
        .versions
        .iter_mut()
        .find(|version| version.label == label)
        .unwrap()
}

fn assert_only(findings: Vec<Finding>, code: Code, detail: &str) {
    assert!(
        findings.len() == 1 && findings[0].code == code && findings[0].detail.contains(detail),
        "want exactly one {code:?} finding naming {detail:?}, got {findings:?}"
    );
}

fn run(catalog: &Catalog) -> Vec<Finding> {
    check(&tree(), catalog, &pins(catalog))
}

#[test]
fn the_catalog_passes_every_rule() {
    let catalog = catalog();
    assert_eq!(run(&catalog), Vec::new());
}

#[test]
fn a_harness_without_exactly_one_current_version_fails() {
    let mut none = catalog();
    let held = pins(&none);
    current(&mut none, "claude-code").status = Status::Supported;
    assert_only(
        check(&tree(), &none, &held),
        Code::CurrentCount,
        "0 current versions",
    );
    let mut two = catalog();
    let held = pins(&two);
    labelled(&mut two, "deepseek-harness", "dsh-v0.1.5-rc.2").status = Status::Current;
    assert_only(
        check(&tree(), &two, &held),
        Code::CurrentCount,
        "2 current versions",
    );
}

#[test]
fn a_revision_repeated_within_a_harness_fails() {
    let mut catalog = catalog();
    let held = pins(&catalog);
    let mut repeat = current(&mut catalog, "claude-code").clone();
    repeat.label.push_str("-repeat");
    repeat.status = Status::Supported;
    let revision = repeat.capability_revision.clone();
    harness(&mut catalog, "claude-code").versions.push(repeat);
    assert_only(
        check(&tree(), &catalog, &held),
        Code::DuplicateRevision,
        &revision,
    );
}

#[test]
fn a_revision_its_corpus_does_not_carry_fails() {
    let mut mutated = catalog();
    let pi = current(&mut mutated, "pi");
    let recorded = pi.capability_revision.clone();
    pi.capability_revision.push_str("-mutated");
    let want = format!(
        "{recorded:?}, the catalog says {:?}",
        pi.capability_revision
    );
    assert_only(run(&mutated), Code::CorpusRevision, &want);

    let mut floored = catalog();
    let pi = current(&mut floored, "pi").clone();
    let claude = current(&mut floored, "claude-code").clone();
    let floor = Version {
        label: "floor".into(),
        status: Status::Floor,
        endpoint_version: String::new(),
        capability_revision: String::new(),
        admits: Vec::new(),
        ledgers: claude.ledgers.clone(),
        corpus: pi.corpus.clone(),
        corpus_from: String::new(),
        components: Vec::new(),
        artifacts: Vec::new(),
        sources: Vec::new(),
    };
    harness(&mut floored, "claude-code").versions.push(floor);
    let want = format!(
        "{:?}, the catalog says {:?}",
        pi.capability_revision, claude.capability_revision
    );
    assert_only(run(&floored), Code::CorpusRevision, &want);
}

#[test]
fn a_path_that_does_not_exist_fails() {
    let mut corpus = catalog();
    current(&mut corpus, "pi").corpus = "fixtures/adapters/pi-v0.0.0".into();
    assert_only(
        run(&corpus),
        Code::MissingPath,
        "corpus fixtures/adapters/pi-v0.0.0",
    );

    let mut file = catalog();
    current(&mut file, "pi").corpus.push_str("/manifest.json");
    assert_only(run(&file), Code::MissingPath, "is not a directory");

    let mut ledger = catalog();
    current(&mut ledger, "pi").ledgers = vec!["research/pi-v0.0.0-mapping.md".into()];
    assert_only(
        run(&ledger),
        Code::MissingPath,
        "ledger research/pi-v0.0.0-mapping.md",
    );
}

#[test]
fn a_digest_no_ledger_records_fails() {
    let unrecorded = "a".repeat(64);
    let mut artifact = catalog();
    current(&mut artifact, "pi").artifacts[0].sha256 = unrecorded.clone();
    assert_only(
        run(&artifact),
        Code::UnledgeredDigest,
        &format!("pi-linux-x64.tar.gz sha256 {unrecorded}"),
    );

    let mut commit = catalog();
    current(&mut commit, "pi").sources[0].commit = unrecorded[..40].to_owned();
    assert_only(
        run(&commit),
        Code::UnledgeredDigest,
        &format!("pi commit {}", &unrecorded[..40]),
    );

    let mut elsewhere = catalog();
    let mut borrowed = current(&mut elsewhere, "claude-code").artifacts[0].clone();
    let pi = current(&mut elsewhere, "pi");
    borrowed.component = pi.artifacts[0].component.clone();
    let name = borrowed.name.clone();
    pi.artifacts.push(borrowed);
    assert_only(run(&elsewhere), Code::UnledgeredDigest, &name);
}

#[test]
fn an_adapter_revision_other_than_the_current_versions_fails() {
    let catalog = catalog();
    let mut held = pins(&catalog);
    let pi = held.get_mut("pi").unwrap();
    pi.capability_revision.push_str("-mutated");
    let want = format!("revision is {:?}", pi.capability_revision);
    assert_only(
        check(&tree(), &catalog, &held),
        Code::AdapterRevision,
        &want,
    );
}

#[test]
fn an_adapter_corpus_other_than_the_current_versions_fails() {
    let catalog = catalog();
    let mut held = pins(&catalog);
    let corpus = held["pi"].corpus.clone();
    held.get_mut("claude-code").unwrap().corpus = corpus.clone();
    assert_only(
        check(&tree(), &catalog, &held),
        Code::AdapterCorpus,
        &format!("corpus directory is {corpus:?}"),
    );
}

#[test]
fn an_adapter_pinned_to_a_harness_the_catalog_lacks_fails() {
    let catalog = catalog();
    let mut held = pins(&catalog);
    held.insert(
        "makai".into(),
        AdapterPin {
            capability_revision: "makai-oap-v1".into(),
            corpus: "fixtures/adapters/makai".into(),
        },
    );
    assert_only(
        check(&tree(), &catalog, &held),
        Code::UnknownHarness,
        "does not hold",
    );
}

#[test]
fn a_label_keying_two_versions_fails() {
    let mut catalog = catalog();
    labelled(&mut catalog, "deepseek-harness", "47f9438").label = "dsh-v0.1.5-rc.2".into();
    assert_only(run(&catalog), Code::DuplicateLabel, "more than one version");
}

#[test]
fn corpus_from_naming_no_other_version_fails() {
    let own = catalog()
        .harness("deepseek-harness")
        .unwrap()
        .current()
        .unwrap()
        .label
        .clone();
    for label in ["dsh-v0.1.4".to_owned(), own] {
        let mut catalog = catalog();
        current(&mut catalog, "deepseek-harness").corpus_from = label.clone();
        assert_only(run(&catalog), Code::CorpusFrom, &label);
    }
}

#[test]
fn an_artifact_or_source_naming_an_unlisted_component_fails() {
    let mut artifact = catalog();
    current(&mut artifact, "pi").artifacts[0].component = "pi-cli".into();
    assert_only(
        run(&artifact),
        Code::UnknownComponent,
        "artifact pi-linux-x64.tar.gz names component \"pi-cli\"",
    );

    let mut source = catalog();
    current(&mut source, "pi").sources[0].component = "pi-cli".into();
    assert_only(
        run(&source),
        Code::UnknownComponent,
        "names component \"pi-cli\"",
    );
}

#[test]
fn a_finding_reads_as_harness_version_code_and_detail() {
    let finding = Finding {
        harness: "pi".into(),
        version: "v1".into(),
        code: Code::CorpusFrom,
        detail: "why".into(),
    };
    assert_eq!(finding.to_string(), "pi v1: corpus_from: why");
    let bare = Finding {
        version: String::new(),
        ..finding
    };
    assert_eq!(bare.to_string(), "pi: corpus_from: why");
}
