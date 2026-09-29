package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testModule = "github.com/lsm/open-agent-protocol"

const protocolBreak = "\n# go/protocol\nIncompatible changes:\n- AuthProvider: old is comparable, new is not\nCompatible changes:\n- AuthProvider.AuthKinds: added\n"

const catalogAddition = "\n# go/providercatalog\nIncompatible changes:\n\nCompatible changes:\n- CredentialKind: added\n"

func TestAnIncompatibleBlockIsAChangeAndACompatibleOneIsNot(t *testing.T) {
	if got := incompatible(protocolBreak, testModule); len(got) != 1 || got[0] != "go/protocol" {
		t.Fatalf("incompatible = %v, want go/protocol", got)
	}
	if got := incompatible(catalogAddition, testModule); len(got) != 0 {
		t.Fatalf("incompatible = %v, want none: a member added is a compatible change", got)
	}
}

func TestAnInternalPackageIsNotAChange(t *testing.T) {
	report := "\n# go/internal/provider\nIncompatible changes:\n- Compat: removed\n"
	if got := incompatible(report, testModule); len(got) != 0 {
		t.Fatalf("incompatible = %v, want none: go/internal is not a public package", got)
	}
}

func TestAnUnrecordedChangeFailsAndARecordedOneDoesNot(t *testing.T) {
	packages := []string{"go/protocol", "go/providercatalog"}
	if got := unrecorded(protocolBreak, testModule, packages, "### Fixed\n\n- nothing here"); len(got) != 1 || got[0] != "go/protocol" {
		t.Fatalf("unrecorded %v, want go/protocol", got)
	}
	if got := unrecorded(protocolBreak, testModule, packages, "## Breaking changes\n\n- `go/protocol` no longer comparable"); len(got) != 0 {
		t.Fatalf("unrecorded %v, want none: the section names it", got)
	}
}

func TestProseAndAPackageTheGateDoesNotWatchAreNotARecord(t *testing.T) {
	packages := []string{"go/protocol", "go/providercatalog"}
	if got := unrecorded(protocolBreak, testModule, packages, "## Breaking changes\n\n- the protocol package changed"); len(got) != 1 {
		t.Fatalf("unrecorded %v, want one: a package named in prose is not a record", got)
	}
	report := "\n# go/serve\nIncompatible changes:\n- Hub: removed\n"
	if got := unrecorded(report, testModule, packages, "## Breaking changes\n\n- `go/serve` gone"); len(got) != 0 {
		t.Fatalf("unrecorded %v, want none: go/serve is not in the watched set", got)
	}
}

func TestOnlyTheBreakingChangesSectionCounts(t *testing.T) {
	description := "## What changed and why\n\nProse naming `go/protocol` in passing.\n\n## Breaking changes\n\n- `go/protocol` no longer comparable\n\n## Notes for reviewers\n\n- `go/serve` is fine\n"
	section := recordedChanges(description)
	if strings.Contains(section, "in passing") {
		t.Fatalf("the section reached above its own heading: %q", section)
	}
	if strings.Contains(section, "is fine") {
		t.Fatalf("the section reached into the next one: %q", section)
	}
	if !strings.Contains(section, "no longer comparable") {
		t.Fatalf("breaking changes = %q", section)
	}
}

func TestADescriptionWithNoBreakingChangesSectionRecordsNothing(t *testing.T) {
	for _, description := range []string{
		"",
		"## What changed and why\n\n- `go/protocol` in a bullet list\n",
		"## Notes for reviewers\n\n## breaking changes\n\n- `go/protocol` lower case\n",
		"### Breaking changes\n\n- `go/protocol` level three\n",
		"##BREAKING CHANGES\n\n- `go/protocol` upper case\n",
	} {
		if section := recordedChanges(description); strings.Contains(section, "go/protocol") {
			t.Fatalf("recorded %q from a description with no Breaking changes heading", section)
		}
	}
}

func TestTheRecordedSectionIsReadFromTheFileTheWorkflowWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte("## Breaking changes\n\n- `go/protocol` no longer comparable\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := unrecorded(protocolBreak, testModule, []string{"go/protocol"}, recordedChanges(string(data))); len(got) != 0 {
		t.Fatalf("unrecorded %v, want none: the description's section names the package", got)
	}
}

func TestAnUnreadableDescriptionRecordsNothingRatherThanFailing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.md")
	if got := unrecorded(protocolBreak, testModule, []string{"go/protocol"}, recordedChanges(readDescription(missing))); len(got) != 1 {
		t.Fatalf("unrecorded %v, want one: an absent description is not a record", got)
	}
}

func TestTheModuleNameComesFromGoMod(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/thing\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	module, err := modulePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if module != "example.com/thing" {
		t.Fatalf("module = %q", module)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := modulePath(root); err == nil {
		t.Fatal("a go.mod naming no module was accepted")
	}
}

func TestABaseCheckoutWithoutItsCommitIsAnError(t *testing.T) {
	err := run([]string{"-base-root", t.TempDir()}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "needs the base commit") {
		t.Fatalf("err = %v, want one naming the base commit", err)
	}
}

func TestPackageDirsListTheTreeTheyArePointedAt(t *testing.T) {
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dirs, err := packageDirs(here)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatal("packageDirs found no packages in the module it was pointed at")
	}
	if !strings.HasPrefix(dirs[0], here) {
		t.Fatalf("packageDirs listed %s, which is not under the root %s it was given", dirs[0], here)
	}
}
