package native

import (
	"encoding/json"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzANotificationIsRefusedForAMethodItDoesNotServeWhateverTheDataSays(f *testing.F) {
	for _, method := range []string{NotifySessionEvent, NotifySessionStatus, NotifySubagentStarted, NotifySubagentFinished, "session.other", "", "session.event "} {
		for _, data := range []string{`{}`, `not json`, `{"a":1}`, `null`} {
			f.Add(method, data)
		}
	}
	f.Fuzz(func(t *testing.T, method, data string) {
		value, err := DecodeNotification(method, []byte(data))
		if err != nil {
			if value != nil {
				t.Fatalf("a refused notification returned the value %+v: %q", value, data)
			}
			return
		}
		switch method {
		case NotifySessionEvent, NotifySessionStatus, NotifySubagentStarted, NotifySubagentFinished:
		default:
			t.Fatalf("the unserved method %q was admitted: %q", method, data)
		}
	})
}

func FuzzAStrictDecodeAdmitsOnlyOneWholeJSONDocument(f *testing.F) {
	for _, seed := range deepseekSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{}`, `{"a":1}`, `{"a":1} {"b":2}`, `{"a":1,"a":2}`, `{"unknown":1}`, `null`, `[]`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var value map[string]any
		if err := DecodeStrict([]byte(raw), &value); err == nil && !json.Valid([]byte(raw)) {
			t.Fatalf("the strict decode admitted bytes that are not one whole JSON document: %q", raw)
		}
	})
}

func deepseekSeeds(f *testing.F) []string {
	{
		bodies, err := fuzzseed.Corpus("deepseek-harness", fuzzseed.DefaultLimit)
		if err != nil {
			{
				f.Fatal(err)
			}
		}
		if len(bodies) == 0 {
			{
				f.Fatalf("the catalog's corpus for %s is empty, so this target starts from its own literals only", "deepseek-harness")
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
