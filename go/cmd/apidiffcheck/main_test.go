package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/publicset"
)

const testModule = "github.com/lsm/open-agent-protocol"

var allPublic = []string{"go/protocol", "go/providercatalog", "go/serve", "go/sdk", "go/binding", "go/client", "go/validation", "go/harness", "go/adapter", "harnesses", "providers", "schema"}

const protocolBreak = "\n# go/protocol\nIncompatible changes:\n- AuthProvider: old is comparable, new is not\nCompatible changes:\n- AuthProvider.AuthKinds: added\n"

const catalogAddition = "\n# go/providercatalog\nIncompatible changes:\n\nCompatible changes:\n- CredentialKind: added\n"

func TestAnIncompatibleBlockIsAChangeAndACompatibleOneIsNot(t *testing.T) {
	if got := incompatible(protocolBreak, testModule, allPublic); len(got) != 1 || got[0] != "go/protocol" {
		t.Fatalf("incompatible = %v, want go/protocol", got)
	}
	if got := incompatible(catalogAddition, testModule, allPublic); len(got) != 0 {
		t.Fatalf("incompatible = %v, want none: a member added is a compatible change", got)
	}
}

func TestAnInternalPackageIsNotAChange(t *testing.T) {
	report := "\n# go/internal/provider\nIncompatible changes:\n- Compat: removed\n"
	if got := incompatible(report, testModule, allPublic); len(got) != 0 {
		t.Fatalf("incompatible = %v, want none: go/internal is not a public package", got)
	}
}

func TestAChangedPackageIsMissingUnlessTheSectionNamesItInBackticks(t *testing.T) {
	changed := incompatible(protocolBreak, testModule, allPublic)
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
	if got := missingFrom(incompatible(protocolBreak, testModule, allPublic), recordedChanges(string(data))); len(got) != 0 {
		t.Fatalf("missing %v, want none: the description's section names the package", got)
	}
}

func TestAnUnreadableDescriptionIsAnErrorRatherThanAMissingRecord(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.md")
	if _, err := readDescription(absent); err == nil {
		t.Fatal("a description path that cannot be read was accepted")
	}
	notADescription := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(notADescription, []byte("## Breaking changes\n\n- `go/protocol` gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body, err := readDescription(notADescription)
	if err != nil {
		t.Fatal(err)
	}
	if got := missingFrom(incompatible(protocolBreak, testModule, allPublic), recordedChanges(body)); len(got) != 0 {
		t.Fatalf("missing %v, want none: the file's section is the record", got)
	}
}

func TestTheGateRefusesToRunWithoutADescription(t *testing.T) {
	t.Setenv("PR_BODY", "")
	t.Setenv("PR_BASE_ROOT", t.TempDir())
	err := run(nil, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "needs the pull request description") {
		t.Fatalf("err = %v, want one naming the description", err)
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
	description := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(description, []byte("## Breaking changes\n\n- none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run([]string{"-base-root", "", "-description", description}, os.Stdout)
	if err == nil || !strings.Contains(err.Error(), "needs a checkout of the merge base") {
		t.Fatalf("err = %v, want one naming the merge base", err)
	}
}

func TestTheBaseCheckoutComesFromTheEnvironment(t *testing.T) {
	description := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(description, []byte("## Breaking changes\n\n- none\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PR_BODY", description)
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
	head := []string{"go/adapter/newharness", "go/protocol"}
	added := setDifference(head, base)
	if len(added) != 1 {
		t.Fatalf("added %v, want one package", added)
	}
	report, err := apidiffReport(t.TempDir(), filepath.Join(t.TempDir(), "absent"), testModule, []string{"go/adapter/newharness"}, added)
	if err != nil {
		t.Fatalf("the added package was asked about, so the skip did not happen: %v", err)
	}
	if report != "" {
		t.Fatalf("report = %q, want empty", report)
	}
	if _, err := apidiffReport(t.TempDir(), filepath.Join(t.TempDir(), "absent"), testModule, []string{"go/adapter/newharness"}, nil); err == nil {
		t.Fatal("the same call without the added set was accepted, so the test proves nothing about the skip")
	}
}

func TestABaseExportFailureIsAnErrorRatherThanASkippedPackage(t *testing.T) {
	if _, err := apidiffReport(t.TempDir(), filepath.Join(t.TempDir(), "absent"), testModule, []string{"go/protocol"}, nil); err == nil {
		t.Fatal("a base checkout that cannot be loaded was accepted, which would skip the comparison and let a real break through")
	}
}

func TestAPackageThePublicSetDoesNotNameIsStillADirectoryTheBaseHad(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+testModule+"\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "gone")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "gone.go"), []byte("package gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if publicset.Public("gone") {
		t.Skip("this package is in the public set, so it does not exercise the difference")
	}
	public, err := publicPackages(root)
	if err != nil {
		if !strings.Contains(err.Error(), "no public package found") {
			t.Fatal(err)
		}
		public = nil
	}
	for _, name := range public {
		if name == "gone" {
			t.Fatal("publicPackages returned a package the public set does not name")
		}
	}
	dirs, err := publicDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, name := range dirs {
		if name == "gone" {
			found = true
		}
	}
	if !found {
		t.Fatal("publicDirectories did not return a package the public set does not name, so a removal would go unreported")
	}
	if removed, err := removalSet(root, root); err != nil {
		t.Fatal(err)
	} else if len(removed) != 0 {
		t.Fatalf("removed %v, want none: a package present in both trees is not removed", removed)
	}
	head := t.TempDir()
	if err := os.WriteFile(filepath.Join(head, "go.mod"), []byte("module "+testModule+"\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(head, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "other.go"), []byte("package other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	removed, err := removalSet(root, head)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "gone" {
		t.Fatalf("removed %v, want the package the head no longer has, even though the public set never named it", removed)
	}
	if got := missingFrom(removed, ""); len(got) != 1 {
		t.Fatalf("missing %v, want one: nothing records the removal", got)
	}
}

func TestAMainPackageIsNotAmongThePublicDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+testModule+"\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(root, "tool")
	if err := os.MkdirAll(command, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(command, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	library := filepath.Join(root, "lib")
	if err := os.MkdirAll(library, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(library, "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirs, err := publicDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range dirs {
		if name == "tool" {
			t.Fatal("a main package is among the public directories, so removing it would be charged as a break")
		}
	}
	found := false
	for _, name := range dirs {
		if name == "lib" {
			found = true
		}
	}
	if !found {
		t.Fatal("a library package is missing from the public directories, so removing one would go unreported")
	}
}

func TestAPackageTheWatchedSetDoesNotIncludeIsNotAChange(t *testing.T) {
	report := "\n# go/tools/nocomment\nIncompatible changes:\n- Run: signature changed\n"
	if got := incompatible(report, testModule, allPublic); len(got) != 0 {
		t.Fatalf("incompatible = %v, want none: go/tools/nocomment is not a public package, so its diff is not a break the gate reports", got)
	}
	if got := incompatible(report, testModule, append(allPublic, "go/tools/nocomment")); len(got) != 1 {
		t.Fatalf("incompatible = %v, want the package once it is watched", got)
	}
}

func TestAPackagePresentInBothTreesIsNotARemovalEvenWhenItIsNotPublic(t *testing.T) {
	dirs := []string{"go/cmd/goap", "go/protocol", "go/tools/nocomment"}
	public := []string{"go/protocol"}
	if removed := setDifference(dirs, public); len(removed) != 2 {
		t.Fatalf("removed %v against the public set, which is the bug: a package in both trees is not removed", removed)
	}
	if removed := setDifference(dirs, dirs); len(removed) != 0 {
		t.Fatalf("removed %v against the directories, want none", removed)
	}
}
