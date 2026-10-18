package adapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestMemoryAdmitsWhatItWouldOpenAndRefusesWhatOpenWouldRefuse(t *testing.T) {
	memory := adapter.NewMemory(adapter.Config{})
	ctx := context.Background()
	for _, testCase := range []struct {
		name    string
		request adapter.OpenRequest
		feature string
	}{
		{name: "a plain open", request: adapter.OpenRequest{SessionID: "s"}},
		{name: "a share of zero", request: adapter.OpenRequest{CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionShare}}, feature: protocol.FeatureCompactionPolicy},
		{name: "an attachment with no kind", request: adapter.OpenRequest{ToolSources: []protocol.ToolSourceAttachment{{ID: "x"}}}, feature: protocol.FeatureToolSourcesAttach},
		{name: "a provided tool with no name", request: adapter.OpenRequest{Tools: []protocol.ToolDefinition{{}}}, feature: protocol.FeatureToolsProvide},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			admitted := memory.Admit(ctx, testCase.request)
			request := testCase.request
			request.Participant.ID = "p"
			_, opened := memory.Open(ctx, request)
			if testCase.feature == "" {
				if admitted != nil || opened != nil {
					t.Fatalf("admit %v, open %v, want both to accept", admitted, opened)
				}
				return
			}
			var refused *adapter.UnsupportedControlError
			if !errors.As(admitted, &refused) || refused.Feature != testCase.feature {
				t.Fatalf("admit answered %v, want a refusal naming %s", admitted, testCase.feature)
			}
			if opened == nil || opened.Error() != admitted.Error() {
				t.Fatalf("open answered %v where admit answered %v", opened, admitted)
			}
		})
	}
}
