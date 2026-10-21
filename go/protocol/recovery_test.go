package protocol

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRecoveryRedeliveriesSurviveTypedRoundTrip(t *testing.T) {
	raw := []byte(`{"recovered":true,"redelivered_runs":[{"previous_run_id":"held-1","run_id":"new-1","root_run_id":"root-1"},{"previous_run_id":"held-2","root_run_id":"root-2","withdrawn":true}]}`)
	var metadata RecoveryMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal(raw, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("redelivery metadata lost in typed round trip: %s", encoded)
	}
}
