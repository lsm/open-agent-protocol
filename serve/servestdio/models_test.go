package servestdio

import (
	"encoding/json"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
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
