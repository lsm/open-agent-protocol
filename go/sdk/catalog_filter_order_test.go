package sdk

import "testing"

func boolPtr(v bool) *bool { return &v }

func selectedList(t *testing.T, envs []string, request ListModelsRequest) (*ListModelsResponse, error) {
	t.Helper()
	client := newTestClient(t, scenarioOAP, envs...)
	defer client.Close()
	return client.Models.List(testContext(t), request)
}

// Each case names the filter that must skip the row and the env that puts the
// row in a state that filter will skip. The malformed member is separate, so a
// case cannot reach the assertion with its filter still having kept the row.
func TestAnInvalidMemberIsRefusedEvenWhenALocalFilterSkipsTheRow(t *testing.T) {
	cases := []struct {
		name    string
		skipEnv []string
		badEnv  []string
		member  string
		request ListModelsRequest
	}{
		{"deprecated/source", []string{"OAPX_TEST_CATALOG_LIFECYCLE=deprecated"},
			[]string{"OAPX_TEST_CATALOG_LIFECYCLE=deprecated", "OAPX_TEST_CATALOG_SOURCE=null"},
			"source", ListModelsRequest{IncludeDeprecated: boolPtr(false)}},
		{"api/lifecycle", []string{"OAPX_TEST_CATALOG_LIFECYCLE=stable"},
			[]string{"OAPX_TEST_CATALOG_LIFECYCLE=null"},
			"lifecycle", ListModelsRequest{IncludeDeprecated: boolPtr(false), API: "not-other"}},
		{"api/source", []string{"OAPX_TEST_CATALOG_LIFECYCLE=stable"},
			[]string{"OAPX_TEST_CATALOG_SOURCE=null"},
			"source", ListModelsRequest{IncludeDeprecated: boolPtr(false), API: "not-other"}},
		{"model/lifecycle", []string{"OAPX_TEST_CATALOG_LIFECYCLE=stable"},
			[]string{"OAPX_TEST_CATALOG_LIFECYCLE=null"},
			"lifecycle", ListModelsRequest{IncludeDeprecated: boolPtr(false), ModelID: "nope"}},
		{"model/source", []string{"OAPX_TEST_CATALOG_LIFECYCLE=stable"},
			[]string{"OAPX_TEST_CATALOG_SOURCE=null"},
			"source", ListModelsRequest{IncludeDeprecated: boolPtr(false), ModelID: "nope"}},
		{"auth/lifecycle", []string{"OAPX_TEST_CATALOG_AUTH=login_required"},
			[]string{"OAPX_TEST_CATALOG_AUTH=login_required", "OAPX_TEST_CATALOG_LIFECYCLE=null"},
			"lifecycle", ListModelsRequest{IncludeDeprecated: boolPtr(false), IncludeLoginRequired: boolPtr(false)}},
		{"auth/source", []string{"OAPX_TEST_CATALOG_AUTH=login_required"},
			[]string{"OAPX_TEST_CATALOG_AUTH=login_required", "OAPX_TEST_CATALOG_SOURCE=null"},
			"source", ListModelsRequest{IncludeDeprecated: boolPtr(false), IncludeLoginRequired: boolPtr(false)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			survivor, err := selectedList(t, c.skipEnv, c.request)
			if err != nil {
				t.Fatalf("the valid counterpart must list, got %v", err)
			}
			if len(survivor.Models) != 0 {
				t.Fatalf("this filter must actually skip the row, got %d models", len(survivor.Models))
			}
			_, err = selectedList(t, c.badEnv, c.request)
			if err == nil {
				t.Fatalf("a present null %s must be refused before the filter runs", c.member)
			}
			protocol, isProtocol := err.(*ProtocolError)
			if !isProtocol || protocol.Code != CodeMalformedResponse {
				t.Fatalf("want malformed_response, got %v", err)
			}
		})
	}
}

func TestAValidRowIsStillFilteredAfterValidationMovesFirst(t *testing.T) {
	dropped, err := selectedList(t, []string{"OAPX_TEST_CATALOG_LIFECYCLE=deprecated"},
		ListModelsRequest{IncludeDeprecated: boolPtr(false)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(dropped.Models) != 0 {
		t.Errorf("a stated deprecated model must still be filtered out, got %d", len(dropped.Models))
	}
	included, err := selectedList(t, []string{"OAPX_TEST_CATALOG_LIFECYCLE=deprecated"},
		ListModelsRequest{IncludeDeprecated: boolPtr(true)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(included.Models) != 1 {
		t.Errorf("got %d models, want 1 when deprecated models are requested", len(included.Models))
	}
	unknown, err := selectedList(t, []string{"OAPX_TEST_CATALOG_LIFECYCLE=absent"},
		ListModelsRequest{IncludeDeprecated: boolPtr(false)})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(unknown.Models) != 1 || unknown.Models[0].Lifecycle != nil {
		t.Errorf("an unknown lifecycle must stay unknown and stay in the listing, got %+v", unknown.Models)
	}
}
