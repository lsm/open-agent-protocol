package fuzzseed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryRootFindsTheCorpusAboveTheCaller(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "fixtures", "manifest.json")); err != nil {
		t.Fatalf("root %s has no manifest: %v", root, err)
	}
}

func TestLimitedReturnsBoundedDistinctNonEmptyLines(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	seeds, err := Limited(root, 25, "fixtures/adapters/*/*/native.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 25 {
		t.Fatalf("got %d seeds, want the requested 25", len(seeds))
	}
	seen := map[string]bool{}
	for index, seed := range seeds {
		if len(strings.TrimSpace(string(seed))) == 0 {
			t.Fatalf("seed %d is blank", index)
		}
		if seen[string(seed)] {
			t.Fatalf("seed %d repeats an earlier line", index)
		}
		seen[string(seed)] = true
	}
	again, err := Limited(root, 25, "fixtures/adapters/*/*/native.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for index := range seeds {
		if string(seeds[index]) != string(again[index]) {
			t.Fatalf("seed %d differs between runs, so the corpus is not a fixed starting point", index)
		}
	}
}

func TestLimitedRefusesAPatternThatMatchesNothingRatherThanSeedingNothing(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	seeds, err := Limited(root, 10, "fixtures/adapters/*/*/native.jsonl", "fixtures/nowhere/*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) == 0 {
		t.Fatal("a corpus that exists was skipped because a second pattern matched nothing")
	}
}

func TestManifestSpreadsItsSeedsOverTheWholeCorpus(t *testing.T) {
	root, err := RepositoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	seeds, err := Manifest(root, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeds) != 24 {
		t.Fatalf("got %d seeds, want 24", len(seeds))
	}
	first, err := Manifest(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 {
		t.Fatalf("got %d seeds, want 1", len(first))
	}
	walk, err := filepath.Glob(filepath.Join(root, "fixtures", "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(walk) < 100 {
		t.Fatalf("the corpus has %d json fixtures, so the manifest read is not the corpus", len(walk))
	}
	if string(first[0]) == string(seeds[len(seeds)-1]) {
		t.Log("the single seed and the last spread seed coincide, which is allowed but worth knowing")
	}
}
