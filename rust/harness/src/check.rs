use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};
use std::fmt;
use std::path::Path;

use serde_json::Value;

use crate::{Catalog, Harness, Status, Version};

#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Code {
    CurrentCount,
    DuplicateLabel,
    DuplicateRevision,
    UnknownComponent,
    CorpusFrom,
    MissingPath,
    UnledgeredDigest,
    CorpusRevision,
    UnknownHarness,
    AdapterRevision,
    AdapterCorpus,
}

impl Code {
    pub fn as_str(self) -> &'static str {
        match self {
            Code::CurrentCount => "current_count",
            Code::DuplicateLabel => "duplicate_label",
            Code::DuplicateRevision => "duplicate_revision",
            Code::UnknownComponent => "unknown_component",
            Code::CorpusFrom => "corpus_from",
            Code::MissingPath => "missing_path",
            Code::UnledgeredDigest => "unledgered_digest",
            Code::CorpusRevision => "corpus_revision",
            Code::UnknownHarness => "unknown_harness",
            Code::AdapterRevision => "adapter_revision",
            Code::AdapterCorpus => "adapter_corpus",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Finding {
    pub harness: String,
    pub version: String,
    pub code: Code,
    pub detail: String,
}

impl fmt::Display for Finding {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.harness)?;
        if !self.version.is_empty() {
            write!(f, " {}", self.version)?;
        }
        write!(f, ": {}: {}", self.code.as_str(), self.detail)
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct AdapterPin {
    pub capability_revision: String,
    pub corpus: String,
}

pub fn check(
    tree: &Path,
    catalog: &Catalog,
    adapters: &BTreeMap<String, AdapterPin>,
) -> Vec<Finding> {
    let mut findings = Vec::new();
    for harness in &catalog.harnesses {
        check_harness(tree, harness, &mut findings);
    }
    for (id, pin) in adapters {
        check_adapter(catalog, id, pin, &mut findings);
    }
    findings
}

fn current_versions(harness: &Harness) -> Vec<&Version> {
    harness
        .versions
        .iter()
        .filter(|version| version.status == Status::Current)
        .collect()
}

fn finding(harness: &str, version: &str, code: Code, detail: String) -> Finding {
    Finding {
        harness: harness.to_owned(),
        version: version.to_owned(),
        code,
        detail,
    }
}

fn check_harness(tree: &Path, harness: &Harness, findings: &mut Vec<Finding>) {
    let current = current_versions(harness);
    if current.len() != 1 {
        findings.push(finding(
            &harness.id,
            "",
            Code::CurrentCount,
            format!("{} current versions, want exactly one", current.len()),
        ));
    }
    let mut labels = HashSet::new();
    let mut revisions: HashMap<&str, &str> = HashMap::new();
    for version in &harness.versions {
        if !labels.insert(version.label.as_str()) {
            findings.push(finding(
                &harness.id,
                &version.label,
                Code::DuplicateLabel,
                "the label keys more than one version".into(),
            ));
        }
        if version.capability_revision.is_empty() {
            continue;
        }
        if let Some(other) = revisions.get(version.capability_revision.as_str()) {
            findings.push(finding(
                &harness.id,
                &version.label,
                Code::DuplicateRevision,
                format!(
                    "capability revision {:?} is also {other}'s",
                    version.capability_revision
                ),
            ));
            continue;
        }
        revisions.insert(&version.capability_revision, &version.label);
    }
    for version in &harness.versions {
        check_version(tree, harness, version, &labels, &current, findings);
    }
}

fn check_version(
    tree: &Path,
    harness: &Harness,
    version: &Version,
    labels: &HashSet<&str>,
    current: &[&Version],
    findings: &mut Vec<Finding>,
) {
    let mut add = |code: Code, detail: String| {
        findings.push(finding(&harness.id, &version.label, code, detail));
    };
    let components: HashSet<&str> = version
        .components
        .iter()
        .map(|component| component.name.as_str())
        .collect();
    for artifact in &version.artifacts {
        if !components.contains(artifact.component.as_str()) {
            add(
                Code::UnknownComponent,
                format!(
                    "artifact {} names component {:?}, which the version does not list",
                    artifact.name, artifact.component
                ),
            );
        }
    }
    for source in &version.sources {
        if !components.contains(source.component.as_str()) {
            add(
                Code::UnknownComponent,
                format!(
                    "source {} names component {:?}, which the version does not list",
                    source.commit, source.component
                ),
            );
        }
    }
    if !version.corpus_from.is_empty()
        && (version.corpus_from == version.label || !labels.contains(version.corpus_from.as_str()))
    {
        add(
            Code::CorpusFrom,
            format!(
                "corpus_from {:?} names no other version of {}",
                version.corpus_from, harness.id
            ),
        );
    }
    let mut ledgers = String::new();
    let mut readable = true;
    for ledger in &version.ledgers {
        match std::fs::read_to_string(tree.join(ledger)) {
            Ok(text) => {
                ledgers.push_str(&text);
                ledgers.push('\n');
            }
            Err(err) => {
                add(Code::MissingPath, format!("ledger {ledger}: {err}"));
                readable = false;
            }
        }
    }
    if readable {
        for (what, value) in version_digests(version) {
            if !ledgers.contains(value) {
                add(
                    Code::UnledgeredDigest,
                    format!(
                        "{what} {value} appears in none of {}",
                        version.ledgers.join(", ")
                    ),
                );
            }
        }
    }
    if version.corpus.is_empty() {
        return;
    }
    let want = match (version.status, current) {
        (Status::Floor, [current]) => &current.capability_revision,
        (Status::Floor, _) => return,
        _ => &version.capability_revision,
    };
    let corpus = &version.corpus;
    let dir = tree.join(corpus);
    if !dir.exists() {
        add(
            Code::MissingPath,
            format!("corpus {corpus}: no such file or directory"),
        );
        return;
    }
    if !dir.is_dir() {
        add(
            Code::MissingPath,
            format!("corpus {corpus}: {corpus} is not a directory"),
        );
        return;
    }
    match corpus_revisions(&dir) {
        Err(err) => add(Code::CorpusRevision, format!("corpus {corpus}: {err}")),
        Ok(carried) if carried.is_empty() => add(
            Code::CorpusRevision,
            format!("corpus {corpus} carries no capability revision in any expected-oap.json"),
        ),
        Ok(carried) => {
            for revision in carried.into_iter().filter(|revision| revision != want) {
                add(
                    Code::CorpusRevision,
                    format!(
                        "corpus {corpus} expects capability revision {revision:?}, the catalog says {want:?}"
                    ),
                );
            }
        }
    }
}

fn version_digests(version: &Version) -> Vec<(String, &str)> {
    let mut digests = Vec::new();
    for artifact in &version.artifacts {
        digests.push((
            format!(
                "{} {} {} sha256",
                artifact.platform, artifact.kind, artifact.name
            ),
            artifact.sha256.as_str(),
        ));
    }
    for source in &version.sources {
        digests.push((
            format!("{} commit", source.component),
            source.commit.as_str(),
        ));
        if !source.tree.is_empty() {
            digests.push((format!("{} tree", source.component), source.tree.as_str()));
        }
    }
    digests
}

fn corpus_revisions(dir: &Path) -> Result<BTreeSet<String>, String> {
    let mut seen = BTreeSet::new();
    let mut pending = vec![dir.to_path_buf()];
    while let Some(next) = pending.pop() {
        let entries =
            std::fs::read_dir(&next).map_err(|err| format!("{}: {err}", next.display()))?;
        for entry in entries {
            let path = entry.map_err(|err| err.to_string())?.path();
            if path.is_dir() {
                pending.push(path);
                continue;
            }
            if path
                .file_name()
                .is_none_or(|name| name != "expected-oap.json")
            {
                continue;
            }
            let text = std::fs::read_to_string(&path)
                .map_err(|err| format!("{}: {err}", path.display()))?;
            let value: Value =
                serde_json::from_str(&text).map_err(|err| format!("{}: {err}", path.display()))?;
            collect_revisions(&value, &mut seen);
        }
    }
    Ok(seen)
}

fn collect_revisions(value: &Value, seen: &mut BTreeSet<String>) {
    match value {
        Value::Object(members) => {
            for (key, member) in members {
                match member {
                    Value::String(revision) if key == "capability_revision" => {
                        seen.insert(revision.clone());
                    }
                    _ => collect_revisions(member, seen),
                }
            }
        }
        Value::Array(items) => items.iter().for_each(|item| collect_revisions(item, seen)),
        _ => {}
    }
}

fn check_adapter(catalog: &Catalog, id: &str, pin: &AdapterPin, findings: &mut Vec<Finding>) {
    let Some(harness) = catalog.harness(id) else {
        findings.push(finding(
            id,
            "",
            Code::UnknownHarness,
            "an adapter is pinned to a harness the catalog does not hold".into(),
        ));
        return;
    };
    let [current] = current_versions(harness)[..] else {
        return;
    };
    if pin.capability_revision != current.capability_revision {
        findings.push(finding(
            id,
            &current.label,
            Code::AdapterRevision,
            format!(
                "the adapter's capability revision is {:?}, the current version's is {:?}",
                pin.capability_revision, current.capability_revision
            ),
        ));
    }
    if pin.corpus != current.corpus {
        findings.push(finding(
            id,
            &current.label,
            Code::AdapterCorpus,
            format!(
                "the adapter's corpus directory is {:?}, the current version's corpus is {:?}",
                pin.corpus, current.corpus
            ),
        ));
    }
}
