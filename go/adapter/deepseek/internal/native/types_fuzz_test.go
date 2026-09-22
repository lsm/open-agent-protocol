package native

import (
	"encoding/json"
	"strings"
	"testing"
)

func FuzzRejectDuplicateKeysAdmitsOnlyValidJSON(f *testing.F) {
	for _, seed := range []string{`{}`, `{"a":1}`, `[1,2]`, `not json`, `{}x`, `123abc`, `{"a":1,}`, `[1,]`, `{"a"}`, `nul`, `"\u00"`, `{"a":}`, `1.2.3`, `[}`, `{"a":1 "b":2}`, `0000`, `0 0`, `{} {}`, `[][]`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if rejectDuplicateKeys([]byte(raw)) == nil && !json.Valid([]byte(raw)) {
			t.Fatalf("walk admitted bytes json.Valid refuses: %q", raw)
		}
	})
}

func FuzzRejectDuplicateKeysRefusesOnlyDuplicatesAndMalformedJSON(f *testing.F) {
	for _, seed := range []string{`{"a":1,"a":2}`, `{"a":1}`, `{"ts":1e999}`, `{"ts":-1e999}`, `{"ts":1e-999}`, `[1e999]`, `{"a":{"b":1,"b":2}}`, `123456789012345678901234567890`, `not json`, `{"a":1e400,"a":2}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		if !json.Valid([]byte(raw)) {
			return
		}
		err := rejectDuplicateKeys([]byte(raw))
		if err != nil && !strings.Contains(err.Error(), "duplicate object key") {
			t.Fatalf("walk refused well-formed JSON for another reason: %q: %v", raw, err)
		}
	})
}
