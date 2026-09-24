package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

type Code string

const (
	CodeCurrentCount      Code = "current_count"
	CodeDuplicateLabel    Code = "duplicate_label"
	CodeDuplicateRevision Code = "duplicate_revision"
	CodeUnknownComponent  Code = "unknown_component"
	CodeCorpusFrom        Code = "corpus_from"
	CodeMissingPath       Code = "missing_path"
	CodeUnledgeredDigest  Code = "unledgered_digest"
	CodeCorpusRevision    Code = "corpus_revision"
	CodeUnknownHarness    Code = "unknown_harness"
	CodeAdapterRevision   Code = "adapter_revision"
	CodeAdapterCorpus     Code = "adapter_corpus"
)

type Finding struct {
	Harness string
	Version string
	Code    Code
	Detail  string
}

func (f Finding) Error() string {
	subject := f.Harness
	if f.Version != "" {
		subject += " " + f.Version
	}
	return fmt.Sprintf("%s: %s: %s", subject, f.Code, f.Detail)
}

type AdapterPin struct {
	CapabilityRevision string
	Corpus             string
}

func Check(tree fs.FS, catalog Catalog, adapters map[string]AdapterPin) []Finding {
	var findings []Finding
	for _, harness := range catalog.Harnesses {
		findings = append(findings, checkHarness(tree, harness)...)
	}
	ids := make([]string, 0, len(adapters))
	for id := range adapters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		findings = append(findings, checkAdapter(catalog, id, adapters[id])...)
	}
	return findings
}

func currentVersions(harness Harness) []Version {
	var current []Version
	for _, version := range harness.Versions {
		if version.Status == StatusCurrent {
			current = append(current, version)
		}
	}
	return current
}

func checkHarness(tree fs.FS, harness Harness) []Finding {
	var findings []Finding
	current := currentVersions(harness)
	if len(current) != 1 {
		findings = append(findings, Finding{Harness: harness.ID, Code: CodeCurrentCount, Detail: fmt.Sprintf("%d current versions, want exactly one", len(current))})
	}
	labels := map[string]bool{}
	revisions := map[string]string{}
	for _, version := range harness.Versions {
		if labels[version.Label] {
			findings = append(findings, Finding{Harness: harness.ID, Version: version.Label, Code: CodeDuplicateLabel, Detail: "the label keys more than one version"})
		}
		labels[version.Label] = true
		if version.CapabilityRevision == "" {
			continue
		}
		if other, seen := revisions[version.CapabilityRevision]; seen {
			findings = append(findings, Finding{Harness: harness.ID, Version: version.Label, Code: CodeDuplicateRevision, Detail: fmt.Sprintf("capability revision %q is also %s's", version.CapabilityRevision, other)})
			continue
		}
		revisions[version.CapabilityRevision] = version.Label
	}
	for _, version := range harness.Versions {
		findings = append(findings, checkVersion(tree, harness, version, labels, current)...)
	}
	return findings
}

func checkVersion(tree fs.FS, harness Harness, version Version, labels map[string]bool, current []Version) []Finding {
	var findings []Finding
	add := func(code Code, format string, args ...any) {
		findings = append(findings, Finding{Harness: harness.ID, Version: version.Label, Code: code, Detail: fmt.Sprintf(format, args...)})
	}
	components := map[string]bool{}
	for _, component := range version.Components {
		components[component.Name] = true
	}
	for _, artifact := range version.Artifacts {
		if !components[artifact.Component] {
			add(CodeUnknownComponent, "artifact %s names component %q, which the version does not list", artifact.Name, artifact.Component)
		}
	}
	for _, source := range version.Sources {
		if !components[source.Component] {
			add(CodeUnknownComponent, "source %s names component %q, which the version does not list", source.Commit, source.Component)
		}
	}
	if version.CorpusFrom != "" && (version.CorpusFrom == version.Label || !labels[version.CorpusFrom]) {
		add(CodeCorpusFrom, "corpus_from %q names no other version of %s", version.CorpusFrom, harness.ID)
	}
	var ledgers strings.Builder
	readable := true
	for _, ledger := range version.Ledgers {
		data, err := fs.ReadFile(tree, ledger)
		if err != nil {
			add(CodeMissingPath, "ledger %s: %v", ledger, err)
			readable = false
			continue
		}
		ledgers.Write(data)
		ledgers.WriteByte('\n')
	}
	if readable {
		text := ledgers.String()
		for _, digest := range versionDigests(version) {
			if !strings.Contains(text, digest.value) {
				add(CodeUnledgeredDigest, "%s %s appears in none of %s", digest.what, digest.value, strings.Join(version.Ledgers, ", "))
			}
		}
	}
	if version.Corpus == "" {
		return findings
	}
	want := version.CapabilityRevision
	if version.Status == StatusFloor {
		if len(current) != 1 {
			return findings
		}
		want = current[0].CapabilityRevision
	}
	carried, err := corpusRevisions(tree, version.Corpus)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		add(CodeMissingPath, "corpus %s: %v", version.Corpus, err)
	case err != nil:
		add(CodeCorpusRevision, "corpus %s: %v", version.Corpus, err)
	case len(carried) == 0:
		add(CodeCorpusRevision, "corpus %s carries no capability revision in any expected-oap.json", version.Corpus)
	default:
		for _, revision := range carried {
			if revision != want {
				add(CodeCorpusRevision, "corpus %s expects capability revision %q, the catalog says %q", version.Corpus, revision, want)
			}
		}
	}
	return findings
}

type digest struct {
	what  string
	value string
}

func versionDigests(version Version) []digest {
	var digests []digest
	for _, artifact := range version.Artifacts {
		digests = append(digests, digest{what: fmt.Sprintf("%s %s %s sha256", artifact.Platform, artifact.Kind, artifact.Name), value: artifact.SHA256})
	}
	for _, source := range version.Sources {
		digests = append(digests, digest{what: source.Component + " commit", value: source.Commit})
		if source.Tree != "" {
			digests = append(digests, digest{what: source.Component + " tree", value: source.Tree})
		}
	}
	return digests
}

func corpusRevisions(tree fs.FS, corpus string) ([]string, error) {
	info, err := fs.Stat(tree, corpus)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory: %w", corpus, fs.ErrNotExist)
	}
	seen := map[string]bool{}
	err = fs.WalkDir(tree, corpus, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "expected-oap.json" {
			return nil
		}
		data, err := fs.ReadFile(tree, name)
		if err != nil {
			return err
		}
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		collectRevisions(value, seen)
		return nil
	})
	if err != nil {
		return nil, err
	}
	revisions := make([]string, 0, len(seen))
	for revision := range seen {
		revisions = append(revisions, revision)
	}
	sort.Strings(revisions)
	return revisions, nil
}

func collectRevisions(value any, seen map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, member := range typed {
			if revision, ok := member.(string); ok && key == "capability_revision" {
				seen[revision] = true
				continue
			}
			collectRevisions(member, seen)
		}
	case []any:
		for _, item := range typed {
			collectRevisions(item, seen)
		}
	}
}

func checkAdapter(catalog Catalog, id string, pin AdapterPin) []Finding {
	var harness *Harness
	for i := range catalog.Harnesses {
		if catalog.Harnesses[i].ID == id {
			harness = &catalog.Harnesses[i]
		}
	}
	if harness == nil {
		return []Finding{{Harness: id, Code: CodeUnknownHarness, Detail: "a Go adapter is pinned to a harness the catalog does not hold"}}
	}
	current := currentVersions(*harness)
	if len(current) != 1 {
		return nil
	}
	var findings []Finding
	if pin.CapabilityRevision != current[0].CapabilityRevision {
		findings = append(findings, Finding{Harness: id, Version: current[0].Label, Code: CodeAdapterRevision, Detail: fmt.Sprintf("the Go adapter's CapabilityRevision is %q, the current version's is %q", pin.CapabilityRevision, current[0].CapabilityRevision)})
	}
	if pin.Corpus != current[0].Corpus {
		findings = append(findings, Finding{Harness: id, Version: current[0].Label, Code: CodeAdapterCorpus, Detail: fmt.Sprintf("the Go adapter's CorpusDirectory is %q, the current version's corpus is %q", pin.Corpus, current[0].Corpus)})
	}
	return findings
}
