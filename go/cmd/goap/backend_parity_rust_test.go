package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackendsMatchRustHub(t *testing.T) {
	rust := os.Getenv("OAP_RUST_HUB_BIN")
	if rust == "" {
		t.Skip("set OAP_RUST_HUB_BIN to an oap-rust-hub binary to compare its served backends with goap's")
	}
	if !filepath.IsAbs(rust) {
		t.Fatalf("OAP_RUST_HUB_BIN must be absolute, got %q", rust)
	}
	goap := oapBinary(t)
	cases, err := filepath.Glob(filepath.Join("testdata", "parity", "*"))
	if err != nil {
		t.Fatal(err)
	}
	compared := 0
	for _, dir := range cases {
		if !servesOnlyMemory(t, dir) {
			continue
		}
		compared++
		backend := fixtureBackend(t, dir)
		t.Run(filepath.Base(dir), func(t *testing.T) {
			scenario := readLines(t, filepath.Join(dir, "scenario.jsonl"))
			wantOut, _ := exchangeWithChild(t, dir, backend, scenario, goap, "serve", "agent")
			gotOut, _ := exchangeWithChild(t, dir, backend, scenario, rust, "serve", "agent")
			if len(wantOut) < len(scenario) {
				t.Fatalf("goap wrote %d lines for %d requests; the fixture does not start", len(wantOut), len(scenario))
			}
			if missing, extra := lineDifference(wantOut, gotOut); len(missing)+len(extra) > 0 {
				t.Errorf("oap-rust-hub answers differently\n--- only goap\n%s\n--- only oap-rust-hub\n%s", strings.Join(missing, "\n"), strings.Join(extra, "\n"))
			}
			if where := runOrderDifference(t, wantOut, gotOut); where != "" {
				t.Errorf("oap-rust-hub orders a run differently: %s", where)
			}
		})
	}
	if compared == 0 {
		t.Fatal("no parity fixture serves only the memory adapter, so nothing was compared")
	}
}

func servesOnlyMemory(t *testing.T, dir string) bool {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry struct {
		Adapters map[string]struct {
			Type string `json:"type"`
		} `json:"adapters"`
	}
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatalf("parse %s/registry.json: %v", dir, err)
	}
	for name, entry := range registry.Adapters {
		kind := entry.Type
		if kind == "" {
			kind = name
		}
		if kind != "memory" {
			return false
		}
	}
	return len(registry.Adapters) > 0
}
