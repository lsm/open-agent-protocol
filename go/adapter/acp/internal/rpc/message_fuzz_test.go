package rpc

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzTheJSONWalkAdmitsOnlyBytesJSONAdmits(f *testing.F) {
	for _, seed := range walkSeeds(f, "acp") {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if rejectDuplicateKeys([]byte(raw)) == nil && !json.Valid([]byte(raw)) {
			t.Fatalf("the walk admitted bytes json.Valid refuses: %q", raw)
		}
	})
}

func FuzzTheJSONWalkRefusesWellFormedJSONOnlyForADuplicateKey(f *testing.F) {
	for _, seed := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `{"a":1}`, `[1,2,3]`, `{}`, `null`, `true`, `1.5`, `"text"`, `[]`, `{"a":1}`, `{"ts":1e999}`, `[1e-999]`, `1e700`, huge, "1" + huge} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if !json.Valid([]byte(raw)) {
			return
		}
		err := rejectDuplicateKeys([]byte(raw))
		if err == nil {
			return
		}
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "UseNumber") {
			return
		}
		t.Fatalf("the walk refused well-formed JSON for another reason: %q: %v", raw, err)
	})
}

var huge = strings.Repeat("2", 400)

func walkSeeds(f *testing.F, id string) []string {
	bodies, err := fuzzseed.Corpus(id, fuzzseed.DefaultLimit)
	if err != nil {
		f.Fatal(err)
	}
	if len(bodies) == 0 {
		f.Fatalf("the catalog's corpus for %s is empty, so this target starts from its own literals only", id)
	}
	seeds := make([]string, 0, len(bodies))
	for _, body := range bodies {
		seeds = append(seeds, string(body))
	}
	return seeds
}
