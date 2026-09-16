package serve

import (
	"context"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// listerSession answers the catalog with whatever a test hands it, standing in
// for an adapter this repository does not own. The boundary's whole purpose is
// adapters nobody here reviews, so the stand-in is a session that simply says
// something wrong rather than a real adapter made to misbehave.
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

// TestHubRefusesAMisScopedToolCatalog is the hub-side half of a rule the
// adapters also keep. Fixing an adapter that answers in the wrong scope is
// necessary and not sufficient: the codec labels the envelope with the session
// it addressed, so an unchecked payload scope survives into a response whose
// envelope and payload disagree — which this project's own Go and TypeScript
// clients refuse, and which no later rule can catch, because every lifetime
// check keys off the session the payload names.
//
// The rule runs in both directions. A scoped request answered with an
// endpoint-level catalog is the answer to a different question, and it is the
// direction that leaks: an unscoped answer drops exactly the sources the
// session attached while still being a schema-valid catalog.
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

// TestHubRefusesACatalogItCannotBindToADescriptor is the revision half. A
// listing nothing can bind to a descriptor snapshot is worse than no listing:
// a consumer caches it under no revision and never learns when to discard it.
// The schema requires the field on the response, so serving one without it
// would also put a document on the wire that the validator rejects.
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

// TestHubRepairsAnAbsentToolList is the one fault of the three that is
// repaired rather than refused, and it is repaired for the reason the models
// catalog gives: an adapter that builds its listing by appending hands back a
// nil slice where it meant an empty one, Go marshals that as null, and the
// schema requires an array. An absent list and an empty one say the same
// thing; only one of the two spellings is legal on the wire, so the hub writes
// the legal one instead of refusing an adapter that meant the right thing.
//
// The request reaches the adapter unchanged, because the hub adds no protocol
// semantics: the degraded opt-in is applied by the adapter to exactly what the
// wire said.
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
