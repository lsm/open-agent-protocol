package harness

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/harnesses"
)

func loadCatalog(t *testing.T) Catalog {
	t.Helper()
	catalog, err := Load(harnesses.Files)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func repositoryTree(t *testing.T) fs.FS {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return os.DirFS(filepath.Join(filepath.Dir(file), "..", ".."))
}

func pinsFor(catalog Catalog) map[string]AdapterPin {
	pins := map[string]AdapterPin{}
	for _, entry := range catalog.Harnesses {
		for _, version := range entry.Versions {
			if version.Status == StatusCurrent {
				pins[entry.ID] = AdapterPin{CapabilityRevision: version.CapabilityRevision, Corpus: version.Corpus}
			}
		}
	}
	return pins
}

func harnessNamed(t *testing.T, catalog *Catalog, id string) *Harness {
	t.Helper()
	for i := range catalog.Harnesses {
		if catalog.Harnesses[i].ID == id {
			return &catalog.Harnesses[i]
		}
	}
	t.Fatalf("catalog holds no harness %q", id)
	return nil
}

func currentOf(t *testing.T, catalog *Catalog, id string) *Version {
	t.Helper()
	entry := harnessNamed(t, catalog, id)
	for i := range entry.Versions {
		if entry.Versions[i].Status == StatusCurrent {
			return &entry.Versions[i]
		}
	}
	t.Fatalf("%s has no current version", id)
	return nil
}

func versionLabelled(t *testing.T, entry *Harness, label string) *Version {
	t.Helper()
	for i := range entry.Versions {
		if entry.Versions[i].Label == label {
			return &entry.Versions[i]
		}
	}
	t.Fatalf("%s has no version %q", entry.ID, label)
	return nil
}

func assertOnly(t *testing.T, findings []Finding, code Code, detail string) {
	t.Helper()
	if len(findings) != 1 || findings[0].Code != code || !strings.Contains(findings[0].Detail, detail) {
		t.Fatalf("want exactly one %s finding naming %q, got %v", code, detail, findings)
	}
}

func TestTheCatalogPassesEveryRule(t *testing.T) {
	catalog := loadCatalog(t)
	if findings := Check(repositoryTree(t), catalog, pinsFor(catalog)); len(findings) != 0 {
		t.Fatalf("the catalog disagrees with the tree: %v", findings)
	}
}

func TestAHarnessWithoutExactlyOneCurrentVersionFails(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		catalog := loadCatalog(t)
		pins := pinsFor(catalog)
		currentOf(t, &catalog, "claude-code").Status = StatusSupported
		assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeCurrentCount, "0 current versions")
	})
	t.Run("two", func(t *testing.T) {
		catalog := loadCatalog(t)
		pins := pinsFor(catalog)
		versionLabelled(t, harnessNamed(t, &catalog, "deepseek-harness"), "dsh-v0.1.5-rc.2").Status = StatusCurrent
		assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeCurrentCount, "2 current versions")
	})
}

func TestARevisionRepeatedWithinAHarnessFails(t *testing.T) {
	catalog := loadCatalog(t)
	pins := pinsFor(catalog)
	claude := harnessNamed(t, &catalog, "claude-code")
	repeat := *currentOf(t, &catalog, "claude-code")
	repeat.Label, repeat.Status = repeat.Label+"-repeat", StatusSupported
	claude.Versions = append(claude.Versions, repeat)
	assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeDuplicateRevision, repeat.CapabilityRevision)
}

func TestARevisionItsCorpusDoesNotCarryFails(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		catalog := loadCatalog(t)
		pi := currentOf(t, &catalog, "pi")
		recorded := pi.CapabilityRevision
		pi.CapabilityRevision = recorded + "-mutated"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeCorpusRevision, fmt.Sprintf("%q, the catalog says %q", recorded, pi.CapabilityRevision))
	})
	t.Run("floor", func(t *testing.T) {
		catalog := loadCatalog(t)
		pi, claude := *currentOf(t, &catalog, "pi"), currentOf(t, &catalog, "claude-code")
		floor := Version{Label: "floor", Status: StatusFloor, Ledgers: claude.Ledgers, Corpus: pi.Corpus}
		entry := harnessNamed(t, &catalog, "claude-code")
		entry.Versions = append(entry.Versions, floor)
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeCorpusRevision, fmt.Sprintf("%q, the catalog says %q", pi.CapabilityRevision, claude.CapabilityRevision))
	})
}

func TestAPathThatDoesNotExistFails(t *testing.T) {
	t.Run("corpus", func(t *testing.T) {
		catalog := loadCatalog(t)
		currentOf(t, &catalog, "pi").Corpus = "fixtures/adapters/pi-v0.0.0"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeMissingPath, "corpus fixtures/adapters/pi-v0.0.0")
	})
	t.Run("corpus that is a file", func(t *testing.T) {
		catalog := loadCatalog(t)
		pi := currentOf(t, &catalog, "pi")
		pi.Corpus += "/manifest.json"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeMissingPath, "is not a directory")
	})
	t.Run("ledger", func(t *testing.T) {
		catalog := loadCatalog(t)
		currentOf(t, &catalog, "pi").Ledgers = []string{"research/pi-v0.0.0-mapping.md"}
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeMissingPath, "ledger research/pi-v0.0.0-mapping.md")
	})
}

func TestADigestNoLedgerRecordsFails(t *testing.T) {
	unrecorded := strings.Repeat("a", 64)
	t.Run("artifact", func(t *testing.T) {
		catalog := loadCatalog(t)
		currentOf(t, &catalog, "pi").Artifacts[0].SHA256 = unrecorded
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnledgeredDigest, "pi-linux-x64.tar.gz sha256 "+unrecorded)
	})
	t.Run("source commit", func(t *testing.T) {
		catalog := loadCatalog(t)
		currentOf(t, &catalog, "pi").Sources[0].Commit = unrecorded[:40]
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnledgeredDigest, "pi commit "+unrecorded[:40])
	})
	t.Run("recorded only in another version's ledger", func(t *testing.T) {
		catalog := loadCatalog(t)
		pi := currentOf(t, &catalog, "pi")
		artifact := currentOf(t, &catalog, "claude-code").Artifacts[0]
		artifact.Component = pi.Artifacts[0].Component
		pi.Artifacts = append(pi.Artifacts, artifact)
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnledgeredDigest, artifact.Name)
	})
}

func TestAGoAdapterRevisionOtherThanTheCurrentVersionsFails(t *testing.T) {
	catalog := loadCatalog(t)
	pins := pinsFor(catalog)
	pins["pi"] = AdapterPin{CapabilityRevision: pins["pi"].CapabilityRevision + "-mutated", Corpus: pins["pi"].Corpus}
	assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeAdapterRevision, fmt.Sprintf("CapabilityRevision is %q", pins["pi"].CapabilityRevision))
}

func TestAGoAdapterCorpusOtherThanTheCurrentVersionsFails(t *testing.T) {
	catalog := loadCatalog(t)
	pins := pinsFor(catalog)
	pins["claude-code"] = AdapterPin{CapabilityRevision: pins["claude-code"].CapabilityRevision, Corpus: pins["pi"].Corpus}
	assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeAdapterCorpus, fmt.Sprintf("CorpusDirectory is %q", pins["pi"].Corpus))
}

func TestAGoAdapterPinnedToAHarnessTheCatalogLacksFails(t *testing.T) {
	catalog := loadCatalog(t)
	pins := pinsFor(catalog)
	pins["makai"] = AdapterPin{CapabilityRevision: "makai-oap-v1", Corpus: "fixtures/adapters/makai"}
	assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeUnknownHarness, "does not hold")
}

func TestALabelKeyingTwoVersionsFails(t *testing.T) {
	catalog := loadCatalog(t)
	versionLabelled(t, harnessNamed(t, &catalog, "deepseek-harness"), "47f9438").Label = "dsh-v0.1.5-rc.2"
	assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeDuplicateLabel, "more than one version")
}

func TestCorpusFromNamingNoOtherVersionFails(t *testing.T) {
	for _, label := range []string{"dsh-v0.1.4", currentOf(t, func() *Catalog { c := loadCatalog(t); return &c }(), "deepseek-harness").Label} {
		t.Run(label, func(t *testing.T) {
			catalog := loadCatalog(t)
			currentOf(t, &catalog, "deepseek-harness").CorpusFrom = label
			assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeCorpusFrom, label)
		})
	}
}

func TestAnArtifactOrSourceNamingAnUnlistedComponentFails(t *testing.T) {
	t.Run("artifact", func(t *testing.T) {
		catalog := loadCatalog(t)
		currentOf(t, &catalog, "pi").Artifacts[0].Component = "pi-cli"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnknownComponent, `artifact pi-linux-x64.tar.gz names component "pi-cli"`)
	})
	t.Run("source", func(t *testing.T) {
		catalog := loadCatalog(t)
		currentOf(t, &catalog, "pi").Sources[0].Component = "pi-cli"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnknownComponent, `names component "pi-cli"`)
	})
}
