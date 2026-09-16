package servestdio

import (
	"context"
	"encoding/json"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// The models op mirrors the HTTP route one-to-one: the same envelope, the same
// daemon-minted correlation, and the same refusal for an unknown session. The
// degraded opt-in is a field of the line rather than a query parameter,
// because that is the only difference the framing makes.
func TestModelsOpServesTheCatalog(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "models")
	f := startFrontend(t, hub, Options{})

	f.send(`{"id":1,"op":"models","session_id":"models"}`)
	response := f.expectResponse(1)
	requireOK(t, response)
	var envelope protocol.Envelope
	if err := json.Unmarshal(response.Result, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != protocol.TypeModelsResponse || envelope.InReplyTo == "" || envelope.SessionID != "models" {
		t.Fatalf("models envelope: %+v", envelope)
	}
	// The same revision the HTTP route stamps: the two transports name one
	// catalog under one descriptor, or a host reading both sees two.
	if envelope.CapabilityRevision != base.CapabilityRevision {
		t.Fatalf("models response cites revision %q, want %q", envelope.CapabilityRevision, base.CapabilityRevision)
	}
	var catalog protocol.ModelsResponse
	if err := envelope.DecodePayload(&catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Models) != 2 || catalog.Models[0].ID != base.ModelPrimary || !catalog.Models[0].Default {
		t.Fatalf("catalog: %+v", catalog)
	}

	// The opt-in is accepted on this op and on no other, so a line carrying it
	// elsewhere is a request error the host can correct rather than a framing
	// defect that fails the frontend closed.
	f.send(`{"id":2,"op":"models","session_id":"models","allow_degraded_features":["models.list"]}`)
	requireOK(t, f.expectResponse(2))
	f.send(`{"id":3,"op":"state","session_id":"models","allow_degraded_features":["models.list"]}`)
	requireCode(t, f.expectResponse(3), "invalid_request")
	f.send(`{"id":4,"op":"models","session_id":"nope"}`)
	requireCode(t, f.expectResponse(4), "unknown_session")

	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

// A catalog scoped to another session, or to none, is refused on this
// transport as on the other: the hub checks what the adapter handed back
// before either codec can label an envelope with it, so neither can publish a
// cross-session or schema-invalid models.response.
func TestModelsOpRefusesAMisscopedCatalog(t *testing.T) {
	for _, row := range []struct {
		name    string
		session protocol.SessionID
	}{
		{"another session", "somewhere-else"},
		{"no session at all", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			registry := serve.NewRegistry()
			if err := registry.Register("misscoped", &misscopedLister{session: row.session}); err != nil {
				t.Fatal(err)
			}
			hub := serve.New(registry, serve.Options{})
			if _, _, err := hub.Open(context.Background(), "misscoped", base.OpenRequest{
				SessionID: "misscoped", Participant: protocol.Participant{ID: serve.DefaultParticipant},
			}); err != nil {
				t.Fatal(err)
			}
			f := startFrontend(t, hub, Options{})
			f.send(`{"id":1,"op":"models","session_id":"misscoped"}`)
			requireCode(t, f.expectResponse(1), "internal")
			if err := f.finish(); err != nil {
				t.Fatalf("finish: %v", err)
			}
		})
	}
}

// misscopedLister is the reference adapter with its catalog relabelled to
// another session — the third-party adapter bug the hub has to catch.
type misscopedLister struct{ session protocol.SessionID }

func (a *misscopedLister) Probe(ctx context.Context) (base.Descriptor, error) {
	return base.NewMemory(base.Config{}).Probe(ctx)
}

func (a *misscopedLister) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := base.NewMemory(base.Config{}).Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return &misscopedSession{Session: session, session: a.session}, nil
}

type misscopedSession struct {
	base.Session
	session protocol.SessionID
}

func (s *misscopedSession) Models(ctx context.Context, request protocol.ModelsRequest) (base.Catalog, error) {
	catalog, err := s.Session.(base.ModelLister).Models(ctx, request)
	if err != nil {
		return base.Catalog{}, err
	}
	catalog.Models.SessionID = s.session
	return catalog, nil
}
