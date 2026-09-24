package harness

import (
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
		versionLabelled(t, harnessNamed(t, &catalog, "claude-code"), "2.1.280").Status = StatusSupported
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
	repeat := *versionLabelled(t, claude, "2.1.280")
	repeat.Label, repeat.Status = "2.1.281", StatusSupported
	claude.Versions = append(claude.Versions, repeat)
	assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeDuplicateRevision, "claude-code-2.1.280-oap-v2")
}

func TestARevisionItsCorpusDoesNotCarryFails(t *testing.T) {
	t.Run("current", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").CapabilityRevision = "pi-v0.85.1-oap-v9"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeCorpusRevision, `"pi-v0.85.1-oap-v1", the catalog says "pi-v0.85.1-oap-v9"`)
	})
	t.Run("floor", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "claude-code"), "2.1.263").Corpus = "fixtures/adapters/pi-v0.85.1"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeCorpusRevision, `"pi-v0.85.1-oap-v1", the catalog says "claude-code-2.1.280-oap-v2"`)
	})
}

func TestAPathThatDoesNotExistFails(t *testing.T) {
	t.Run("corpus", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").Corpus = "fixtures/adapters/pi-v0.0.0"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeMissingPath, "corpus fixtures/adapters/pi-v0.0.0")
	})
	t.Run("corpus that is a file", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").Corpus = "fixtures/adapters/pi-v0.85.1/manifest.json"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeMissingPath, "is not a directory")
	})
	t.Run("ledger", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").Ledgers = []string{"research/pi-v0.0.0-mapping.md"}
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeMissingPath, "ledger research/pi-v0.0.0-mapping.md")
	})
}

func TestADigestNoLedgerRecordsFails(t *testing.T) {
	unrecorded := strings.Repeat("a", 64)
	t.Run("artifact", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").Artifacts[0].SHA256 = unrecorded
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnledgeredDigest, "pi-linux-x64.tar.gz sha256 "+unrecorded)
	})
	t.Run("source commit", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").Sources[0].Commit = unrecorded[:40]
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnledgeredDigest, "pi commit "+unrecorded[:40])
	})
	t.Run("recorded only in another version's ledger", func(t *testing.T) {
		catalog := loadCatalog(t)
		claude := harnessNamed(t, &catalog, "claude-code")
		floor := versionLabelled(t, claude, "2.1.263")
		floor.Artifacts[0].SHA256 = versionLabelled(t, claude, "2.1.280").Artifacts[0].SHA256
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnledgeredDigest, "@anthropic-ai/claude-code@2.1.263")
	})
}

func TestAGoAdapterRevisionOtherThanTheCurrentVersionsFails(t *testing.T) {
	catalog := loadCatalog(t)
	pins := pinsFor(catalog)
	pins["pi"] = AdapterPin{CapabilityRevision: "pi-v0.85.1-oap-v0", Corpus: pins["pi"].Corpus}
	assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeAdapterRevision, `CapabilityRevision is "pi-v0.85.1-oap-v0"`)
}

func TestAGoAdapterCorpusOtherThanTheCurrentVersionsFails(t *testing.T) {
	catalog := loadCatalog(t)
	pins := pinsFor(catalog)
	pins["claude-code"] = AdapterPin{CapabilityRevision: pins["claude-code"].CapabilityRevision, Corpus: "fixtures/adapters/claude-code-2.1.263"}
	assertOnly(t, Check(repositoryTree(t), catalog, pins), CodeAdapterCorpus, `CorpusDirectory is "fixtures/adapters/claude-code-2.1.263"`)
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
	for _, label := range []string{"dsh-v0.1.4", "dsh-v0.1.6-alpha.2"} {
		t.Run(label, func(t *testing.T) {
			catalog := loadCatalog(t)
			versionLabelled(t, harnessNamed(t, &catalog, "deepseek-harness"), "dsh-v0.1.6-alpha.2").CorpusFrom = label
			assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeCorpusFrom, label)
		})
	}
}

func TestAnArtifactOrSourceNamingAnUnlistedComponentFails(t *testing.T) {
	t.Run("artifact", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").Artifacts[0].Component = "pi-cli"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnknownComponent, `artifact pi-linux-x64.tar.gz names component "pi-cli"`)
	})
	t.Run("source", func(t *testing.T) {
		catalog := loadCatalog(t)
		versionLabelled(t, harnessNamed(t, &catalog, "pi"), "v0.85.1").Sources[0].Component = "pi-cli"
		assertOnly(t, Check(repositoryTree(t), catalog, pinsFor(catalog)), CodeUnknownComponent, `names component "pi-cli"`)
	})
}
