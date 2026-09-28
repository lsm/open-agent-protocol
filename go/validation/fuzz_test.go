package validation

import (
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzTheValidatorReachesAVerdictForAnyBytesAtAll(f *testing.F) {
	validator := MustNew()
	for _, seed := range validatorSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{"", "\n", "{", "{}", "[]", "not json", strings.Repeat("{\"a\":1}\n", 64), "\x00\xff"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		result := validator.Validate(strings.NewReader(raw), "fuzz")
		for _, diagnostic := range result.Diagnostics {
			if diagnostic.Code == "" {
				t.Fatalf("a diagnostic carries no code for %q", raw)
			}
			if !knownPhase(diagnostic.Phase) {
				t.Fatalf("a diagnostic carries the phase %q for %q", diagnostic.Phase, raw)
			}
			if diagnostic.Fixture != "fuzz" {
				t.Fatalf("a diagnostic names the fixture %q, so the caller's own name was lost for %q", diagnostic.Fixture, raw)
			}
		}
	})
}

func FuzzTheValidatorIsTotalOverARepeatedEnvelopeWithoutGrowingItsVerdictWithoutEnd(f *testing.F) {
	validator := MustNew()
	for _, seed := range validatorSeeds(f) {
		f.Add(seed, 1, 512)
	}
	f.Fuzz(func(t *testing.T, raw string, count, size int) {
		if count < 1 {
			count = 1
		}
		if count > 64 {
			count = 64
		}
		if size < 0 {
			size = 0
		}
		if size > 4096 {
			size = 4096
		}
		trace := strings.Repeat(raw, count)
		if len(trace) > size {
			trace = trace[:size]
		}
		once := validator.Validate(strings.NewReader(trace), "fuzz")
		twice := validator.Validate(strings.NewReader(trace), "fuzz")
		if once.Valid() != twice.Valid() || len(once.Diagnostics) != len(twice.Diagnostics) {
			t.Fatalf("two verdicts over the same bytes disagree: %+v and %+v", once, twice)
		}
	})
}

func knownPhase(phase Phase) bool {
	switch phase {
	case PhaseDecode, PhaseSchema, PhaseSemantic, PhaseLoad, "":
		return true
	default:
		return false
	}
}

func validatorSeeds(f *testing.F) []string {
	root, err := fuzzseed.RepositoryRoot()
	if err != nil {
		f.Fatal(err)
	}
	bodies, err := fuzzseed.Manifest(root, 24)
	if err != nil {
		f.Fatal(err)
	}
	if len(bodies) == 0 {
		f.Fatal("the manifest corpus is empty, so this target starts from its own literals only")
	}
	seeds := make([]string, 0, len(bodies))
	for _, body := range bodies {
		seeds = append(seeds, string(body))
	}
	return seeds
}
