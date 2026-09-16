package client

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// The catalog a client reads is the one the endpoint's model gate enforces, so
// a picker built on it offers exactly the ids a submission may select.
func TestSessionModels(t *testing.T) {
	server := newDaemon(t, memoryRegistry(0))
	session := openMemorySession(t, dial(t, server), "models")
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	listing, err := session.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if listing.Models.SessionID != session.ID() {
		t.Fatalf("catalog names session %q, want %q", listing.Models.SessionID, session.ID())
	}
	// The catalog is valid for exactly one descriptor snapshot, so the caller
	// is handed the revision to cache it against; without it a client cannot
	// tell which models.list promise it just read.
	if listing.Revision != base.CapabilityRevision {
		t.Fatalf("catalog cites revision %q, want %q", listing.Revision, base.CapabilityRevision)
	}
	ids := make([]string, 0, len(listing.Models.Models))
	defaults := 0
	for _, descriptor := range listing.Models.Models {
		ids = append(ids, descriptor.ID)
		if descriptor.Default {
			defaults++
		}
	}
	if len(ids) != 2 || ids[0] != base.ModelPrimary || ids[1] != base.ModelSecondary || defaults != 1 {
		t.Fatalf("catalog: %+v", listing.Models.Models)
	}

	// Every listed id is selectable, which is the promise a catalog makes.
	for _, id := range ids {
		admission, err := session.Submit(ctx, protocol.MessageSubmitRequest{
			SessionID: session.ID(), Delivery: protocol.DeliveryAuto,
			ModelID:  protocol.ControlValue(id),
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		})
		if err != nil {
			t.Fatalf("selecting the listed model %q was refused: %v", id, err)
		}
		if admission.ModelID != id {
			t.Fatalf("admission model %q, want %q", admission.ModelID, id)
		}
		if _, err := session.Cancel(ctx, admission.RunID); err != nil {
			t.Fatal(err)
		}
	}

	// And an id the catalog omits is refused under the code that names it,
	// never as an unsupported feature: the capability is advertised and the
	// request was understood.
	_, err = session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: session.ID(), Delivery: protocol.DeliveryAuto,
		ModelID:  protocol.ControlValue("absent-model"),
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	})
	var refusal *ServerError
	if !errors.As(err, &refusal) || refusal.Code != "model_not_found" {
		t.Fatalf("an unlisted model was refused as %v", err)
	}
	if refusal.Details["model_id"] != "absent-model" {
		t.Fatalf("refusal does not name the id to stop sending: %+v", refusal.Details)
	}
}

// A catalog with no revision is refused rather than returned with an empty
// one. The schema requires the field and the daemon refuses to serve a listing
// without it, but a client validates envelopes only on request, so a
// third-party endpoint that honours neither reaches an unvalidating client
// intact — and a listing nothing can bind to a descriptor cannot be cached
// against one or invalidated when it moves.
func TestModelsRejectsAnUnlabelledCatalog(t *testing.T) {
	response, err := protocol.NewEnvelope(protocol.TypeModelsResponse, protocol.EnvelopeID("resp-1"), protocol.ModelsResponse{
		SessionID: "wire", Models: []protocol.ModelDescriptor{{ID: "m1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	response.InReplyTo = "req-1"
	response.SessionID = "wire"
	body, err := response.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	if _, err := session.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "no capability revision") {
		t.Fatalf("an unlabelled catalog was accepted: %v", err)
	}
}

// The option is what makes a degraded catalog readable, and a call without it
// sends nothing extra: the parameter appears only when it is asked for.
func TestModelsOptionAddsTheDegradedOptin(t *testing.T) {
	plain := &Session{id: "s1"}
	if got := plain.modelsPath(); got != "/sessions/s1/models" {
		t.Fatalf("an unmodified call built %q", got)
	}
	if got := plain.modelsPath(AllowDegraded(protocol.FeatureModelsList, "other.key")); got != "/sessions/s1/models?allow_degraded=models.list&allow_degraded=other.key" {
		t.Fatalf("the opt-in built %q", got)
	}
}
