package validation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestKnownGapsNameManifestFixturesOncePerTreeInOrder(t *testing.T) {
	root := repositoryRoot(t)
	manifest, err := LoadManifest(filepath.Join(root, "fixtures", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, entry := range manifest.Fixtures {
		ids[entry.ID] = true
	}
	raw, err := os.ReadFile(filepath.Join(root, "fixtures", "known-gaps.json"))
	if err != nil {
		t.Fatal(err)
	}
	var gaps struct {
		Version int      `json:"version"`
		Go      []string `json:"go"`
		Zig     []string `json:"zig"`
		Rust    []string `json:"rust"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&gaps); err != nil {
		t.Fatalf("fixtures/known-gaps.json: %v", err)
	}
	if gaps.Version != 1 {
		t.Fatalf("fixtures/known-gaps.json: version %d, want 1", gaps.Version)
	}
	for tree, listed := range map[string][]string{"go": gaps.Go, "zig": gaps.Zig, "rust": gaps.Rust} {
		if !slices.IsSorted(listed) {
			t.Errorf("%s: known gaps are not sorted", tree)
		}
		for at, id := range listed {
			if !ids[id] {
				t.Errorf("%s: %q is not a manifest fixture", tree, id)
			}
			if at > 0 && listed[at-1] == id {
				t.Errorf("%s: %q is listed twice", tree, id)
			}
		}
	}
}
