package serve

import (
	"context"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

type listerSession struct {
	stubSession
	catalog base.ToolCatalog
	request protocol.ToolsListRequest
}

func (l *listerSession) Tools(_ context.Context, request protocol.ToolsListRequest) (base.ToolCatalog, error) {
	l.request = request
	return l.catalog, nil
}

var _ base.ToolLister = (*listerSession)(nil)

func TestHubRefusesAMisScopedToolCatalog(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		ask    protocol.SessionID
		answer protocol.SessionID
		want   string
	}{
		{"a foreign scope", "listing", "someone-else", `scoped to session "someone-else", want "listing"`},
		{"no scope at all", "listing", "", `scoped to session "", want "listing"`},
		{"a scope nobody asked for", "", "listing", `scoped to session "listing", want ""`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			lister := &listerSession{catalog: base.ToolCatalog{
				Revision: "stub-v1",
				Tools:    protocol.ToolsListResponse{SessionID: testCase.answer, Tools: []protocol.ToolDefinition{}},
			}}
			entry := newSession("listing", "stub", lister)
			_, err := entry.Tools(context.Background(), protocol.ToolsListRequest{SessionID: testCase.ask})
			if err == nil {
				t.Fatal("a mis-scoped catalog reached the binding")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not report the scope defect %q", err, testCase.want)
			}
		})
	}
}

func TestHubRefusesACatalogItCannotBindToADescriptor(t *testing.T) {
	lister := &listerSession{catalog: base.ToolCatalog{
		Tools: protocol.ToolsListResponse{SessionID: "listing", Tools: []protocol.ToolDefinition{}},
	}}
	entry := newSession("listing", "stub", lister)
	_, err := entry.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "listing"})
	if err == nil {
		t.Fatal("an unbindable catalog reached the binding")
	}
	if !strings.Contains(err.Error(), "no capability revision") {
		t.Fatalf("error %q does not report the missing revision", err)
	}
}

func TestHubRepairsAnAbsentToolList(t *testing.T) {
	lister := &listerSession{catalog: base.ToolCatalog{
		Revision: "stub-v1",
		Tools:    protocol.ToolsListResponse{SessionID: "listing"},
	}}
	entry := newSession("listing", "stub", lister)
	catalog, err := entry.Tools(context.Background(), protocol.ToolsListRequest{
		SessionID: "listing", AllowDegradedFeatures: []string{protocol.FeatureToolsList},
	})
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Tools.Tools == nil {
		t.Fatal("an absent tool list reached the binding, where it marshals as null")
	}
	if catalog.Revision != "stub-v1" {
		t.Fatalf("catalog revision %q, want the adapter's", catalog.Revision)
	}
	if strings.Join(lister.request.AllowDegradedFeatures, ",") != protocol.FeatureToolsList {
		t.Fatalf("the adapter saw allow_degraded_features %v", lister.request.AllowDegradedFeatures)
	}
}
