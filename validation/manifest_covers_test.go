package validation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, entries []FixtureEntry) string {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(FixtureManifest{Version: 1, Fixtures: entries})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func negative(id, unit string, covers ...Coverage) FixtureEntry {
	return FixtureEntry{ID: id, Path: "semantic-invalid/" + id + ".json", Kind: KindSemanticInvalid, Phase: PhaseSemantic, Codes: []string{CodeUnavailableCapability}, Units: []string{unit}, Covers: covers}
}

func positive(id, unit string) FixtureEntry {
	return FixtureEntry{ID: id, Path: "valid/" + id + ".json", Kind: KindPositive, Valid: true, Units: []string{unit}}
}

func withUnitKeys(t *testing.T, keys map[string][]string, deferred map[string]string) {
	t.Helper()
	prevKeys, prevDeferred := unitCapabilities, honourDeferred
	unitCapabilities, honourDeferred = keys, deferred
	t.Cleanup(func() { unitCapabilities, honourDeferred = prevKeys, prevDeferred })
}

func TestManifestCoversShape(t *testing.T) {
	cases := map[string]struct {
		entry FixtureEntry
		want  string
	}{
		"gate and honour are the only aspects": {
			entry: negative("x", "core", Coverage{Capability: "run.x", Aspect: "verify"}),
			want:  "unknown aspect",
		},
		"capability must be named": {
			entry: negative("x", "core", Coverage{Capability: " ", Aspect: AspectGate}),
			want:  "empty capability",
		},
		"a positive fixture cannot claim coverage": {
			entry: func() FixtureEntry {
				e := positive("x", "core")
				e.Covers = []Coverage{{Capability: "run.x", Aspect: AspectGate}}
				return e
			}(),
			want: "is positive",
		},
		"a schema-invalid fixture cannot claim coverage": {
			entry: func() FixtureEntry {
				e := negative("x", "core", Coverage{Capability: "run.x", Aspect: AspectGate})
				e.Kind, e.Phase, e.Codes = KindSchemaInvalid, PhaseSchema, []string{CodeSchemaInvalid}
				return e
			}(),
			want: "is " + string(KindSchemaInvalid),
		},
		"a load-invalid fixture cannot claim coverage": {
			entry: func() FixtureEntry {
				e := negative("x", "core", Coverage{Capability: "run.x", Aspect: AspectHonour})
				e.Kind, e.Phase, e.Codes = KindLoadInvalid, PhaseLoad, []string{LoadPackUnprefixedName}
				return e
			}(),
			want: "is " + string(KindLoadInvalid),
		},
		"a well-formed coverage loads": {
			entry: negative("x", "core", Coverage{Capability: "run.x", Aspect: AspectHonour}),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadManifest(writeManifest(t, []FixtureEntry{tc.entry}))
			if tc.want == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("error %v does not mention %q", err, tc.want)
			}
		})
	}
}

func TestManifestLoadInvalidKind(t *testing.T) {
	good := FixtureEntry{ID: "pack-bad", Path: "packs/bad", Kind: KindLoadInvalid, Phase: PhaseLoad, Codes: []string{LoadPackUnprefixedName}, Units: []string{"core"}}
	if _, err := LoadManifest(writeManifest(t, []FixtureEntry{good})); err != nil {
		t.Fatalf("well-formed load-invalid entry rejected: %v", err)
	}
	cases := map[string]struct {
		mutate func(e *FixtureEntry)
		want   string
	}{
		"phase must be load": {
			mutate: func(e *FixtureEntry) { e.Phase = PhaseSemantic },
			want:   "must have phase",
		},
		"codes must be load errors, not diagnostics": {
			mutate: func(e *FixtureEntry) { e.Codes = []string{CodeUnavailableCapability} },
			want:   "unknown load-error code",
		},
		"codes are required": {
			mutate: func(e *FixtureEntry) { e.Codes = nil },
			want:   "must have phase",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := good
			e.Codes = append([]string(nil), good.Codes...)
			tc.mutate(&e)
			_, err := LoadManifest(writeManifest(t, []FixtureEntry{e}))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v does not mention %q", err, tc.want)
			}
		})
	}

	crossed := negative("x", "core")
	crossed.Codes = []string{LoadPackUnprefixedName}
	if _, err := LoadManifest(writeManifest(t, []FixtureEntry{crossed})); err == nil || !strings.Contains(err.Error(), "unknown diagnostic code") {
		t.Fatalf("diagnostic fixture accepted a load-error code: %v", err)
	}

	v := MustNew()
	if _, err := v.ValidateManifest(writeManifest(t, []FixtureEntry{good})); err == nil || !strings.Contains(err.Error(), "pack-bad") {
		t.Fatalf("ValidateManifest did not run the load-invalid fixture: %v", err)
	}
}

func TestCorpusCompleteness(t *testing.T) {
	gate := Coverage{Capability: "run.model_selection", Aspect: AspectGate}
	honour := Coverage{Capability: "run.model_selection", Aspect: AspectHonour}

	t.Run("an unclaimed unit imposes nothing", func(t *testing.T) {
		withUnitKeys(t, map[string][]string{"run-controls": {"run.model_selection"}}, nil)
		if _, err := LoadManifest(writeManifest(t, []FixtureEntry{positive("core-ok", "core")})); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
	t.Run("a claimed key needs both aspects", func(t *testing.T) {
		withUnitKeys(t, map[string][]string{"run-controls": {"run.model_selection"}}, nil)
		_, err := LoadManifest(writeManifest(t, []FixtureEntry{positive("controls-ok", "run-controls")}))
		if err == nil || !strings.Contains(err.Error(), "no negative gate fixture") || !strings.Contains(err.Error(), "no negative honour fixture") {
			t.Fatalf("missing coverage not reported: %v", err)
		}
	})
	t.Run("gate alone is not enough", func(t *testing.T) {
		withUnitKeys(t, map[string][]string{"run-controls": {"run.model_selection"}}, nil)
		_, err := LoadManifest(writeManifest(t, []FixtureEntry{negative("controls-gate", "run-controls", gate)}))
		if err == nil || strings.Contains(err.Error(), "no negative gate") || !strings.Contains(err.Error(), "no negative honour fixture") {
			t.Fatalf("honour gap not reported alone: %v", err)
		}
	})
	t.Run("a gate fixture under an unrelated unit does not stand in", func(t *testing.T) {
		withUnitKeys(t, map[string][]string{"run-controls": {"run.model_selection"}}, nil)

		entries := []FixtureEntry{negative("tools-gate", "tools", gate), negative("controls-honour", "run-controls", honour)}
		_, err := LoadManifest(writeManifest(t, entries))
		if err == nil || !strings.Contains(err.Error(), "no negative gate fixture under unit run-controls") {
			t.Fatalf("gate under an unrelated unit was accepted: %v", err)
		}
	})
	t.Run("both aspects satisfy the check", func(t *testing.T) {
		withUnitKeys(t, map[string][]string{"run-controls": {"run.model_selection"}}, nil)
		entries := []FixtureEntry{negative("controls-gate", "run-controls", gate), negative("controls-honour", "run-controls", honour)}
		if _, err := LoadManifest(writeManifest(t, entries)); err != nil {
			t.Fatalf("complete corpus rejected: %v", err)
		}
	})
	t.Run("a deferred honour fixture must sit under the deferring unit", func(t *testing.T) {
		withUnitKeys(t, map[string][]string{"run-controls": {"run.model_selection"}}, map[string]string{"run.model_selection": "models"})

		entries := []FixtureEntry{negative("controls-gate", "run-controls", gate), negative("controls-honour", "run-controls", honour)}
		if _, err := LoadManifest(writeManifest(t, entries)); err == nil || !strings.Contains(err.Error(), "under unit models") {
			t.Fatalf("deferral not enforced: %v", err)
		}

		entries[1] = negative("models-false-miss", "models", honour)
		if _, err := LoadManifest(writeManifest(t, entries)); err != nil {
			t.Fatalf("deferred coverage rejected: %v", err)
		}
	})
	t.Run("the shipped corpus passes with the empty table", func(t *testing.T) {
		if len(unitCapabilities) != 0 {
			t.Skip("a unit has registered keys; the shipped-corpus check belongs to that unit's tests")
		}
		if _, err := LoadManifest(filepath.Join(repositoryRoot(t), "fixtures", "manifest.json")); err != nil {
			t.Fatalf("shipped manifest fails completeness with an empty key table: %v", err)
		}
	})
}

func TestProviderCorpusIsInvalidFirst(t *testing.T) {
	manifest, err := LoadManifest(filepath.Join("..", "fixtures", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	units := map[string]int{}
	var positive, invalid int
	for _, entry := range manifest.Fixtures {
		if entry.Profile != ProfileModelProvider {
			continue
		}
		if entry.Valid {
			positive++
		} else {
			invalid++
		}
		for _, unit := range entry.Units {
			units[unit]++
		}
	}
	if positive == 0 || invalid == 0 {
		t.Fatalf("the provider corpus has %d positive and %d invalid fixtures", positive, invalid)
	}
	if invalid <= positive {
		t.Fatalf("the provider corpus is not invalid-first: %d invalid against %d positive", invalid, positive)
	}
	for _, unit := range []string{"provider-core", "credentials", "carry"} {
		if units[unit] == 0 {
			t.Fatalf("no provider fixture claims the %q unit", unit)
		}
	}
}
