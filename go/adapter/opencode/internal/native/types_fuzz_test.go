package native

import (
	"encoding/json"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzASessionEventIsAdmittedOnlyWithItsSessionAndTheDurabilityItsTypeDeclares(f *testing.F) {
	for _, seed := range corpusSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{}`, `{"id":"evt_1","type":"session.execution.started","durable":{"aggregateID":"ses_1","seq":1,"version":1},"data":{"sessionID":"ses_1"}}`, `{"id":"evt_2","type":"session.text.delta","data":{"sessionID":"ses_1","assistantMessageID":"msg_1","ordinal":0,"delta":"x"}}`} {
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
		if !event.Type.SessionScoped() {
			return
		}
		if !event.Type.Supported() || !event.SessionID.Valid() {
			t.Fatalf("an admitted session event carries the type %q and session %q: %q", event.Type, event.SessionID, raw)
		}
		if event.Type.Durable() != (event.Durable != nil) {
			t.Fatalf("an admitted %s carries the durable position %+v: %q", event.Type, event.Durable, raw)
		}
		if event.Durable != nil && (event.Durable.AggregateID != string(event.SessionID) || event.Durable.Seq < 0) {
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
