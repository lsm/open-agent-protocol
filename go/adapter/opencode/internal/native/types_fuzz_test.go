package native

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzAnEventIsAdmittedOnlyWithAValidIDTypeAndDurablePosition(f *testing.F) {
	for _, seed := range corpusSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{}`, `{"id":"evt_1","type":"session.next.prompted","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{}}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		event, err := DecodeEvent([]byte(raw))
		if err != nil {
			return
		}
		if !event.ID.Valid() {
			t.Fatalf("an admitted event carries the id %q: %q", event.ID, raw)
		}
		if !event.Type.Supported() {
			t.Fatalf("an admitted event carries the unsupported type %q: %q", event.Type, raw)
		}
		if event.Durable == nil || event.Durable.AggregateID == "" || event.Durable.Seq < 0 {
			t.Fatalf("an admitted event carries the durable position %+v: %q", event.Durable, raw)
		}
	})
}

func FuzzAnAdmittedEventsDataIsAWholeJSONValueOrNothing(f *testing.F) {
	for _, seed := range corpusSeeds(f) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		event, err := DecodeEvent([]byte(raw))
		if err != nil {
			return
		}
		if len(event.Data) == 0 {
			return
		}
		if !json.Valid(event.Data) {
			t.Fatalf("an admitted event carries data that is not a whole JSON value: %q in %q", event.Data, raw)
		}
	})
}

func corpusSeeds(f *testing.F) []string {
	{
		bodies, err := fuzzseed.Corpus("opencode", fuzzseed.DefaultLimit)
		if err != nil {
			{
				f.Fatal(err)
			}
		}
		if len(bodies) == 0 {
			{
				f.Fatalf("the catalog's corpus for %s is empty, so this target starts from its own literals only", "opencode")
			}
		}
		seeds := make([]string, 0, len(bodies))
		for _, body := range bodies {
			{
				seeds = append(seeds, string(body))
			}
		}
		return seeds
	}
}

func FuzzTheJSONWalkRefusesWellFormedJSONOnlyForADuplicateKey(f *testing.F) {
	for _, seed := range corpusSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{"a":1,"a":2}`, `{"a":{"b":1,"b":2}}`, `{"a":1}`, `{}`, `[]`, `null`, `1e700`, `{"ts":1e999}`} {
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
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "UseNumber") {
			return
		}
		t.Fatalf("the walk refused well-formed JSON for another reason: %q: %v", raw, err)
	})
}
