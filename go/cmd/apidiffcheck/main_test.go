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

func TestARemovedPublicPackageStillCounts(t *testing.T) {
	report := `# github.com/lsm/open-agent-protocol/go/client
## incompatible changes
package removed
`
	got := incompatiblePackages(report, module)
	if len(got) != 1 || got[0] != "go/client" {
		t.Fatalf("incompatible packages %v, want go/client: a package deleted at head is not in the head's set, and deleting one is the most incompatible change there is", got)
	}
}

func TestAnInternalPackageIsNotPublicApiSoItDoesNotCount(t *testing.T) {
	report := `# github.com/lsm/open-agent-protocol/go/internal/conformance
## incompatible changes
Runner: removed
`
	if got := incompatiblePackages(report, module); len(got) != 0 {
		t.Fatalf("incompatible packages %v, want none: an internal package is not public API", got)
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

func TestProseAndASubpackageDoNotRecordABreak(t *testing.T) {
	report := `# github.com/lsm/open-agent-protocol/schema
## incompatible changes
Pack: removed

# github.com/lsm/open-agent-protocol/go/serve
## incompatible changes
Hub.Subscribe: signature changed
`
	for _, section := range []string{
		"### Fixed\n\n- the table in `providers/catalog.schema.json` moves",
		"### Fixed\n\n- `schema/v0.1/envelope.schema.json` is unchanged",
		"### Fixed\n\n- `harnesses/claude-code.json` carries the pin",
		"### Fixed\n\n- `go/serve/servehttp` refuses a bad charset",
		"### Fixed\n\n- the go/serve daemon and go/serve/serveendpoint both",
	} {
		got := unrecorded(report, module, section)
		if len(got) != 2 {
			t.Fatalf("unrecorded %v for %q, want both packages: a word in prose or a longer path is not a record", got, section)
		}
	}
	if got := unrecorded(report, module, "### Fixed\n\n- `schema` and `go/serve` both changed"); len(got) != 0 {
		t.Fatalf("unrecorded %v, want none: both are named in backticks", got)
	}
}

func TestOnlyTheBreakingChangesSectionCounts(t *testing.T) {
	description := "## What changed and why\n\nProse that names `go/providercatalog` in passing.\n\n## Breaking changes\n\n- `schema` lost a field\n\n## Notes for reviewers\n\n- `go/serve` is fine\n"
	section := recordedChanges(description)
	if strings.Contains(section, "in passing") {
		t.Fatalf("the section reached above its own heading: %q", section)
	}
	if strings.Contains(section, "is fine") {
		t.Fatalf("the section reached into the next one: %q", section)
	}
	if !strings.Contains(section, "lost a field") {
		t.Fatalf("breaking changes = %q", section)
	}
	if strings.Contains(section, "go/serve") {
		t.Fatalf("a package named only in a later section is not recorded: %q", section)
	}
}

func TestADescriptionWithNoBreakingChangesSectionRecordsNothing(t *testing.T) {
	for _, description := range []string{
		"",
		"## What changed and why\n\n- `go/providercatalog` in a bullet list\n",
		"## Notes for reviewers\n\n## breaking changes\n\n- `go/providercatalog` is named in a lower-case heading\n",
		"### Breaking changes\n\n- `go/providercatalog` is named in a level-three heading\n",
		"##BREAKING CHANGES\n\n- `go/providercatalog` is named in an upper-case heading\n",
	} {
		if section := recordedChanges(description); strings.Contains(section, "go/providercatalog") {
			t.Fatalf("recorded %q from a description with no Breaking changes heading", section)
		}
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

func TestADescriptionFileIsReadAsTheRecord(t *testing.T) {
	report := `# github.com/lsm/open-agent-protocol/go/providercatalog
## incompatible changes
ModelsURL: removed
`
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte("## Breaking changes\n\n- `go/providercatalog` now answers Models().\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := unrecorded(report, module, recordedChanges(string(data))); len(got) != 0 {
		t.Fatalf("unrecorded %v, want none: the description's Breaking changes section names the package", got)
	}
	empty := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(empty, []byte("## What changed and why\n\n- `go/providercatalog` in prose only\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(empty)
	if err != nil {
		t.Fatal(err)
	}
	if got := unrecorded(report, module, recordedChanges(string(data))); len(got) != 1 {
		t.Fatalf("unrecorded %v, want go/providercatalog: a name in prose is not a record", got)
	}
}
