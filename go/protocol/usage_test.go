package protocol_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestUsagePreservesReportedCounts(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"input_tokens":0}`,
		`{"output_tokens":0}`,
		`{"total_tokens":0}`,
		`{"input_tokens":0,"output_tokens":0,"total_tokens":0}`,
		`{"input_tokens":0,"cache_read_input_tokens":0,"cache_write_input_tokens":0,"output_tokens":0,"reasoning_tokens":0,"total_tokens":0}`,
		`{"input_tokens":7,"output_tokens":0}`,
		`{"total_tokens":18446744073709551615}`,
	} {
		t.Run(raw, func(t *testing.T) {
			var usage protocol.Usage
			if err := json.Unmarshal([]byte(raw), &usage); err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(usage)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]json.RawMessage
			if err := json.Unmarshal(wire, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(raw), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("usage round trip = %s, want %s", wire, raw)
			}
		})
	}
}
