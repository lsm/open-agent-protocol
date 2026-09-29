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

func TestAChangedPackageIsMissingUnlessTheSectionNamesItInBackticks(t *testing.T) {
	changed := incompatible(protocolBreak, testModule)
	if got := missingFrom(changed, "### Fixed\n\n- nothing here"); len(got) != 1 || got[0] != "go/protocol" {
		t.Fatalf("missing %v, want go/protocol", got)
	}
	if got := missingFrom(changed, "## Breaking changes\n\n- `go/protocol` no longer comparable"); len(got) != 0 {
		t.Fatalf("missing %v, want none: the section names it in backticks", got)
	}
	if got := missingFrom(changed, "## Breaking changes\n\n- the protocol package changed"); len(got) != 1 {
		t.Fatalf("missing %v, want one: a package named in prose is not a record", got)
	}
}

func TestARemovedPublicPackageNeedsARecordEvenWithNothingToDiff(t *testing.T) {
	base := []string{"go/binding", "go/protocol"}
	head := []string{"go/protocol"}
	removed := setDifference(base, head)
	if len(removed) != 1 || removed[0] != "go/binding" {
		t.Fatalf("removed %v, want go/binding: a deleted public package is the most incompatible change there is", removed)
	}
	if got := missingFrom(removed, ""); len(got) != 1 {
		t.Fatalf("missing %v, want one: nothing in an empty description names it", got)
	}
	if got := missingFrom(removed, "## Breaking changes\n\n- `go/binding` is gone"); len(got) != 0 {
		t.Fatalf("missing %v, want none: the removal is recorded", got)
	}
}

func TestAPackageOnlyTheHeadHasIsAnAdditionAndNeedsNoRecord(t *testing.T) {
	base := []string{"go/protocol"}
	head := []string{"go/protocol", "go/adapter/newharness"}
	if added := setDifference(head, base); len(added) != 1 || added[0] != "go/adapter/newharness" {
		t.Fatalf("added %v, want go/adapter/newharness", added)
	}
	if removed := setDifference(base, head); len(removed) != 0 {
		t.Fatalf("removed %v, want none", removed)
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
	if got := missingFrom(incompatible(protocolBreak, testModule), recordedChanges(string(data))); len(got) != 0 {
		t.Fatalf("missing %v, want none: the description's section names the package", got)
	}
}

func TestAnUnreadableDescriptionRecordsNothingRatherThanFailing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.md")
	if got := missingFrom(incompatible(protocolBreak, testModule), recordedChanges(readDescription(missing))); len(got) != 1 {
		t.Fatalf("missing %v, want one: an absent description is not a record", got)
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

func TestTheGateRefusesToRunWithoutABaseCheckout(t *testing.T) {
	err := run([]string{"-base-root", ""}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "needs a checkout of the merge base") {
		t.Fatalf("err = %v, want one naming the merge base", err)
	}
}

func TestTheBaseCheckoutComesFromTheEnvironment(t *testing.T) {
	t.Setenv("PR_BASE_ROOT", "")
	if err := run(nil, os.Stdout); err == nil || !strings.Contains(err.Error(), "needs a checkout of the merge base") {
		t.Fatalf("err = %v, want one naming the merge base", err)
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

func TestAnAddedPackageIsSkippedRatherThanDiffedAgainstAStaleExport(t *testing.T) {
	base := []string{"go/protocol"}
	head := []string{"go/protocol", "go/adapter/newharness"}
	added := setDifference(head, base)
	if len(added) != 1 {
		t.Fatalf("added %v, want one package", added)
	}
	baseDir := t.TempDir()
	if _, err := apidiffReport(baseDir, filepath.Join(t.TempDir(), "absent"), testModule, head, added); err == nil {
		t.Fatal("a base that cannot be loaded was accepted for go/protocol")
	} else if !strings.Contains(err.Error(), "go/protocol") {
		t.Fatalf("err = %v, want one naming the package", err)
	}
}

func TestABaseExportFailureIsAnErrorRatherThanASkippedPackage(t *testing.T) {
	if _, err := apidiffReport(t.TempDir(), filepath.Join(t.TempDir(), "absent"), testModule, []string{"go/protocol"}, nil); err == nil {
		t.Fatal("a base checkout that cannot be loaded was accepted, which would skip the comparison and let a real break through")
	}
}
