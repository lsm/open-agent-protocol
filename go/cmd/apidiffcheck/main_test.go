package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const module = "github.com/lsm/open-agent-protocol"

func TestOnlyTheIncompatibleSectionCounts(t *testing.T) {
	report := `# github.com/lsm/open-agent-protocol/go/providercatalog
## incompatible changes
ModelsURL: removed
## compatible changes
RequestURL: added

# github.com/lsm/open-agent-protocol/go/client
## compatible changes
New: added

# github.com/lsm/open-agent-protocol/go/serve
## incompatible changes
Hub.Subscribe: signature changed
Options.After: removed
`
	got := incompatiblePackages(report, module)
	want := []string{"go/providercatalog", "go/serve"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incompatible packages %v, want %v: an addition is compatible and must never count", got, want)
	}
}

func TestAnEmptyReportNamesNothing(t *testing.T) {
	if got := incompatiblePackages("# summary\nSuggested version: v0.1.0\n", module); len(got) != 0 {
		t.Fatalf("incompatible packages %v, want none", got)
	}
}

func TestAPackageOutsideTheModuleIsNotCounted(t *testing.T) {
	report := `# golang.org/x/exp/typeparams
## incompatible changes
TypeParam: removed
`
	if got := incompatiblePackages(report, module); len(got) != 0 {
		t.Fatalf("incompatible packages %v, want none: the report is about this module", got)
	}
}

func TestAnUnrecordedBreakFailsAndARecordedOneDoesNot(t *testing.T) {
	report := `# github.com/lsm/open-agent-protocol/go/providercatalog
## incompatible changes
ModelsURL: removed
`
	if got := unrecorded(report, module, "### Fixed\n\n- `go/providercatalog` now answers (url, found)."); len(got) != 0 {
		t.Fatalf("unrecorded %v, want none: the Unreleased section names the package", got)
	}
	got := unrecorded(report, module, "### Fixed\n\n- something else entirely")
	if len(got) != 1 || got[0] != "go/providercatalog" {
		t.Fatalf("unrecorded %v, want go/providercatalog", got)
	}
}

func TestOnlyTheUnreleasedSectionCounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	changelog := "## Unreleased\n\n### Fixed\n\n- nothing here\n\n## [0.2.0] - 2026-09-23\n\n### Fixed\n\n- `go/providercatalog` changed its signature\n"
	if err := os.WriteFile(path, []byte(changelog), 0o644); err != nil {
		t.Fatal(err)
	}
	section, err := unreleasedSection(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(section, "changed its signature") {
		t.Fatalf("the Unreleased section reached into a released one: %q", section)
	}
	if !strings.Contains(section, "nothing here") {
		t.Fatalf("unreleased section = %q", section)
	}
}

func TestAChangelogWithNoUnreleasedSectionIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte("## [0.2.0] - 2026-09-23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := unreleasedSection(path); err == nil {
		t.Fatal("a changelog with no Unreleased section was accepted")
	}
}

func TestTheModuleNameComesFromGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/thing\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := modulePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != "example.com/thing" {
		t.Fatalf("module path %q", got)
	}
}
