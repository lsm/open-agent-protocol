package jsonwalk

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

var corpusHarnesses = []string{
	"acp", "claude-code", "codex-app-server", "deepseek-harness",
	"hermes", "opencode", "pi",
}

func corpusSeeds(f *testing.F) []string {
	var seeds []string
	for _, id := range corpusHarnesses {
		bodies, err := fuzzseed.Corpus(id, 8)
		if err != nil {
			f.Fatal(err)
		}
		for _, body := range bodies {
			seeds = append(seeds, string(body))
		}
	}
	if len(seeds) == 0 {
		f.Fatal("no harness corpus was found, so these targets start from their own literals only")
	}
	return seeds
}

func FuzzTheWalkAdmitsOnlyBytesJSONAdmits(f *testing.F) {
	for _, seed := range corpusSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{}`, `{"a":1}`, `[1,2]`, `not json`, `{}x`, `123abc`, `{"a":1,}`, `[1,]`, `{"a"}`, `nul`, `"\u00"`, `{"a":}`, `1.2.3`, `[}`, `{"a":1 "b":2}`, `0000`, `0 0`, `{} {}`, `[][]`, `{"a":{"b":{"c":1}}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if RejectDuplicateKeys([]byte(raw)) == nil && !json.Valid([]byte(raw)) {
			t.Fatalf("the walk admitted bytes json.Valid refuses: %q", raw)
		}
	})
}

func FuzzTheWalkRefusesWellFormedJSONOnlyForADuplicateKey(f *testing.F) {
	for _, seed := range corpusSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `{"a":1}`, `[1,2,3]`, `{}`, `null`, `true`, `1.5`, `"text"`, `[]`, `{"a":1}`, `{"ts":1e999}`, `[1e-999]`, `1e700`, `{"a":[[{"b":1,"b":2}]]}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if !json.Valid([]byte(raw)) {
			return
		}
		err := RejectDuplicateKeys([]byte(raw))
		if err == nil {
			return
		}
		if strings.Contains(err.Error(), "duplicate object key") {
			return
		}
		t.Fatalf("the walk refused well-formed JSON for another reason: %q: %v", raw, err)
	})
}

func TestTheWalkRefusesADuplicateAtEveryDepthAndAdmitsTheSameBytesOnceOverwritten(t *testing.T) {
	for _, raw := range []string{
		`{"a":1,"a":2}`,
		`{"a":{"b":1,"b":2}}`,
		`[{"a":1,"a":2}]`,
		`{"a":[{"b":1,"b":2}]}`,
		`{"a":1,"a":2,"a":3}`,
	} {
		if err := RejectDuplicateKeys([]byte(raw)); err == nil {
			t.Fatalf("the walk admitted %q, whose key is written twice", raw)
		} else if !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("the walk refused %q for another reason: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"a":1}`,
		`{"a":{"b":[{"c":1}]}}`,
		`[1,{"a":[2,3]}]`,
		`{}`,
		`[]`,
		`null`,
	} {
		if err := RejectDuplicateKeys([]byte(raw)); err != nil {
			t.Fatalf("the walk refused %q: %v", raw, err)
		}
	}
}

func TestANumberAFloat64CannotHoldIsStillWalkedRatherThanRefused(t *testing.T) {
	for _, raw := range []string{`{"ts":1e999}`, `{"ts":-1e999}`, `{"ts":1e-999}`, `[1e999]`, `1e700`, "1" + strings.Repeat("2", 400)} {
		if !json.Valid([]byte(raw)) {
			t.Fatalf("%q is not well-formed JSON, so this case proves nothing", raw)
		}
		if err := RejectDuplicateKeys([]byte(raw)); err != nil {
			t.Fatalf("the walk refused %q, a number it is not being asked to hold: %v", raw, err)
		}
	}
}
