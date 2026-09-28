package native

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

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

func FuzzAStrictDecodeAdmitsOnlyOneWholeJSONDocument(f *testing.F) {
	for _, seed := range walkSeeds(f, "pi") {
		f.Add(seed)
	}
	for _, seed := range []string{`{}`, `{"a":1}`, `{"a":1} {"b":2}`, `{"a":1,"a":2}`, `{"unknown":1}`, `null`, `[]`, `1e700`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var value map[string]any
		if err := DecodeStrict([]byte(raw), &value); err == nil && !json.Valid([]byte(raw)) {
			t.Fatalf("the strict decode admitted bytes that are not one whole JSON document: %q", raw)
		}
	})
}
