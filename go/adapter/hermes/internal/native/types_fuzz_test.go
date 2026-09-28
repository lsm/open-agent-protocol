package native

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/internal/fuzzseed"
)

func FuzzANotificationIsRefusedForAMethodItDoesNotServeWhateverTheDataSays(f *testing.F) {
	for _, method := range []string{NotifyEvent, "event.other", "", " EVENT", "event "} {
		for _, data := range []string{`{}`, `not json`, `{"a":1}`, `null`, `[]`} {
			f.Add(method, data)
		}
	}
	f.Fuzz(func(t *testing.T, method, data string) {
		value, err := DecodeNotification(method, []byte(data))
		if method != NotifyEvent {
			if err == nil {
				t.Fatalf("the unserved method %q was admitted: %q", method, data)
			}
			if value != nil {
				t.Fatalf("the unserved method %q returned the value %+v", method, value)
			}
			if !strings.Contains(err.Error(), "unknown notification method") {
				t.Fatalf("the unserved method %q was refused for another reason: %q: %v", method, data, err)
			}
			return
		}
		if err != nil {
			if value != nil {
				t.Fatalf("a refused notification returned the value %+v: %q", value, data)
			}
			if strings.Contains(err.Error(), "unknown notification method") {
				t.Fatalf("the served method %q was refused as unknown: %q", method, data)
			}
		}
	})
}

func FuzzAServerRequestIsEitherAValueOrAnErrorAndNeverBothAtOnce(f *testing.F) {
	for _, method := range []string{RequestApproval, RequestClarify, RequestSudo, RequestSecret, MethodSessionCreate, "session.other", ""} {
		for _, params := range []string{`{}`, `not json`, `{"session_id":"s","request_id":"r","command":"ls","choices":["once"]}`} {
			f.Add(method, params)
		}
	}
	f.Fuzz(func(t *testing.T, method, params string) {
		value, err := DecodeServerRequest(method, []byte(params))
		if err != nil {
			if value != nil {
				t.Fatalf("a refused server request returned the value %+v: %q", value, params)
			}
			return
		}
		if value == nil {
			return
		}
		if jsonTypeOf(value) == "invalid" {
			t.Fatalf("an admitted server request is not a struct: %q: %T", params, value)
		}
	})
}

func FuzzAStrictDecodeAdmitsOnlyOneWholeJSONDocument(f *testing.F) {
	for _, seed := range hermesSeeds(f) {
		f.Add(seed)
	}
	for _, seed := range []string{`{}`, `{"a":1}`, `{"a":1} {"b":2}`, `{"a":1,"a":2}`, `{"unknown":1}`, `null`, `[]`, `{"a":1,}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		var value map[string]any
		if err := DecodeStrict([]byte(raw), &value); err == nil && !json.Valid([]byte(raw)) {
			t.Fatalf("the strict decode admitted bytes that are not one whole JSON document: %q", raw)
		}
	})
}

func jsonTypeOf(value any) string {
	switch value.(type) {
	case *ApprovalRequestParams, *ClarifyRequestParams, *SudoRequestParams, *SecretRequestParams:
		return "struct"
	default:
		return "invalid"
	}
}

func hermesSeeds(f *testing.F) []string {
	{
		bodies, err := fuzzseed.Corpus("hermes", fuzzseed.DefaultLimit)
		if err != nil {
			{
				f.Fatal(err)
			}
		}
		if len(bodies) == 0 {
			{
				f.Fatalf("the catalog's corpus for %s is empty, so this target starts from its own literals only", "hermes")
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
