package fuzzseed

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var matrixEntry = regexp.MustCompile(`- package: (\S+)\s+target: (\S+)`)

var fuzzTarget = regexp.MustCompile(`(?m)^func (Fuzz[A-Za-z0-9_]+)\(f \*testing\.F\)`)

func TestEveryFuzzTargetInTheTreeIsInTheWeeklyMatrix(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	found := targetsInTree(t, root)
	if len(found) == 0 {
		t.Fatal("no fuzz target was found in the tree, so this gate is vacuous")
	}
	listed := matrixEntries(t, root)
	for target, where := range found {
		if _, ok := listed[target]; !ok {
			t.Errorf("%s declares %s, and .github/workflows/fuzz.yml does not run it: a target nothing schedules is a target nobody reads", where, target)
		}
	}
}

func TestEveryTargetInTheWeeklyMatrixExistsInTheTree(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	found := targetsInTree(t, root)
	listed := matrixEntries(t, root)
	if len(listed) == 0 {
		t.Fatal("the matrix has no entries, so this gate is vacuous")
	}
	for target, where := range listed {
		if _, ok := found[target]; !ok {
			t.Errorf(".github/workflows/fuzz.yml runs %s at %s, and no such target exists: that leg of the job fuzzes nothing", target, where)
		}
	}
}

func targetsInTree(t *testing.T, root string) map[string]string {
	t.Helper()
	found := map[string]string{}
	err := filepath.WalkDir(filepath.Join(root, "go"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range fuzzTarget.FindAllStringSubmatch(string(body), -1) {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				relative = path
			}
			found[match[1]] = relative
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func matrixEntries(t *testing.T, root string) map[string]string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "fuzz.yml"))
	if err != nil {
		t.Fatal(err)
	}
	matches := matrixEntry.FindAllStringSubmatch(string(body), -1)
	if len(matches) == 0 {
		return map[string]string{}
	}
	entries := make(map[string]string, len(matches))
	for _, match := range matches {
		packagePath, err := filepath.Rel(root, filepath.Join(root, strings.TrimPrefix(match[1], "./")))
		if err != nil {
			packagePath = match[1]
		}
		entries[match[2]] = packagePath
	}
	return entries
}

func TestTheMatrixNamesAPackageThatHoldsTheTargetItRuns(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	listed := matrixEntries(t, root)
	found := targetsInTree(t, root)
	for target, where := range listed {
		claimed, ok := found[target]
		if !ok {
			continue
		}
		if filepath.Dir(claimed) != where {
			t.Errorf("the matrix runs %s under %s, and it is declared in %s: the leg would fuzz the wrong package", target, where, filepath.Dir(claimed))
		}
	}
}

func TestEveryLegOfTheJobIsDistinguishable(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "fuzz.yml"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, match := range matrixEntry.FindAllStringSubmatch(string(body), -1) {
		names[match[1]+" "+match[2]]++
	}
	if len(names) == 0 {
		t.Fatal("the matrix has no entries, so this gate is vacuous")
	}
	for name, count := range names {
		if count > 1 {
			t.Errorf("%d legs are named %q, so a failing job is not identifiable from its name", count, name)
		}
	}
}
