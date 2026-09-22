package native

import (
	"encoding/json"
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
