package validation

import (
	"encoding/json"
	"testing"
)

func TestToolResultImageNamesTheFirstSiblingKeyInSortedOrder(t *testing.T) {
	image := `{"type":"tool_result","tool_call_id":"t","result":[{"type":"image","image":{"data":"aGk=","media_type":"image/png"}}]}`
	var payload any
	if err := json.Unmarshal([]byte(`{"zeta":`+image+`,"alpha":`+image+`,"mid":`+image+`}`), &payload); err != nil {
		t.Fatal(err)
	}
	for range 50 {
		pointer, found := toolResultImage(payload, "/payload")
		if !found || pointer != "/payload/alpha/result" {
			t.Fatalf("got %q, %v; want /payload/alpha/result", pointer, found)
		}
	}
}
