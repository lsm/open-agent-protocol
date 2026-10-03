package servehttp

import (
	"context"
	"net/http"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestCompactionIsAdmittedOnTheSubmitRouteAndItsRunReachesSubscribers(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{})); err != nil {
		t.Fatal(err)
	}
	hub, server := newServer(t, registry, Options{})
	opened := openSession(t, server, "memory", "compact-http")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	subscription, err := hub.Subscribe(ctx, "compact-http")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	focus := "the release plan"
	request := requestEnvelope(t, protocol.TypeSessionCompactRequest, "compact-1", protocol.SessionCompactRequest{
		SessionID: "compact-http", Focus: &focus,
	}, "compact-http", "", opened.CapabilityRevision)
	status, response := postEnvelope(t, server, "/sessions/compact-http/submit", request)
	if status != http.StatusOK {
		t.Fatalf("compact status %d: %s", status, response.Payload)
	}
	requireEnvelopeType(t, response, protocol.TypeSessionCompactResponse)
	requireEnvelopeSchema(t, response)
	if response.InReplyTo != request.ID || response.CapabilityRevision != opened.CapabilityRevision {
		t.Fatalf("compact response correlation = %q, revision %q", response.InReplyTo, response.CapabilityRevision)
	}
	var admission protocol.SessionCompactResponse
	if err := response.DecodePayload(&admission); err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionStarted || admission.RunID == "" || response.RunID != admission.RunID {
		t.Fatalf("compact admission = %+v on run %q", admission, response.RunID)
	}

	var seen []protocol.EnvelopeType
	for {
		envelope, err := subscription.Next()
		if err != nil {
			t.Fatalf("draining the compaction run after %v: %v", seen, err)
		}
		if envelope.RunID != admission.RunID {
			t.Fatalf("event %s on run %q, want %q", envelope.Type, envelope.RunID, admission.RunID)
		}
		seen = append(seen, envelope.Type)
		if envelope.Type == protocol.TypeRunCompleted || envelope.Type == protocol.TypeRunFailed {
			break
		}
	}
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeRunCompactionStarted, protocol.TypeRunCompactionEnded, protocol.TypeRunCompleted}
	if len(seen) != len(want) {
		t.Fatalf("compaction run events = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("compaction run events = %v, want %v", seen, want)
		}
	}
}

func TestACompactionTheAdapterRefusesAnswersWithTheSubmitRoutesRefusal(t *testing.T) {
	server := newMemoryServer(t, 0)
	opened := openSession(t, server, "memory", "compact-refused")
	submitRun(t, server, "compact-refused", "submit-busy")

	request := requestEnvelope(t, protocol.TypeSessionCompactRequest, "compact-steer", protocol.SessionCompactRequest{
		SessionID: "compact-refused", Delivery: protocol.DeliverySteer,
	}, "compact-refused", "", opened.CapabilityRevision)
	status, refusal := postEnvelope(t, server, "/sessions/compact-refused/submit", request)
	requireErrorResponse(t, status, http.StatusBadRequest, refusal, "unsupported_feature")
	if refusal.InReplyTo != request.ID {
		t.Fatalf("refusal correlation = %q, want %q", refusal.InReplyTo, request.ID)
	}

	scoped := requestEnvelope(t, protocol.TypeSessionCompactRequest, "compact-scope", protocol.SessionCompactRequest{
		SessionID: "elsewhere",
	}, "compact-refused", "", opened.CapabilityRevision)
	status, refusal = postEnvelope(t, server, "/sessions/compact-refused/submit", scoped)
	requireErrorResponse(t, status, http.StatusBadRequest, refusal, "scope_mismatch")
}

func TestASessionThatCannotCompactRefusesTheRequestNamingTheFeature(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("memory", noControlsAdapter{base.NewMemory(base.Config{})}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	opened := openSession(t, server, "memory", "no-compact")

	request := requestEnvelope(t, protocol.TypeSessionCompactRequest, "compact-unadvertised", protocol.SessionCompactRequest{
		SessionID: "no-compact",
	}, "no-compact", "", opened.CapabilityRevision)
	status, refusal := postEnvelope(t, server, "/sessions/no-compact/submit", request)
	requireErrorResponse(t, status, http.StatusBadRequest, refusal, "unsupported_feature")
	var payload protocol.ErrorResponse
	if err := refusal.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Details["feature"] != protocol.FeatureSessionCompact {
		t.Fatalf("refusal details = %+v", payload.Error.Details)
	}
}
