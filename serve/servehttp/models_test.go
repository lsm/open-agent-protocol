package servehttp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// The catalog route mirrors the capabilities route: a GET with no request
// envelope, answered by a models.response citing a daemon-minted correlation
// id. An unknown session is refused before any adapter is asked.
func TestModelsRoute(t *testing.T) {
	server := newMemoryServer(t, 0)
	openSession(t, server, "memory", "models-test")

	response, err := server.Client().Get(server.URL + "/sessions/models-test/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", response.StatusCode, data)
	}
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireEnvelopeType(t, envelope, protocol.TypeModelsResponse)
	if envelope.InReplyTo == "" {
		t.Fatal("models response lacks a correlation id")
	}
	var catalog protocol.ModelsResponse
	if err := envelope.DecodePayload(&catalog); err != nil {
		t.Fatal(err)
	}
	if catalog.SessionID != "models-test" || len(catalog.Models) != 2 {
		t.Fatalf("catalog: %+v", catalog)
	}
	if catalog.Models[0].ID != base.ModelPrimary || !catalog.Models[0].Default {
		t.Fatalf("first descriptor is not the default: %+v", catalog.Models[0])
	}
	requireEnvelopeSchema(t, envelope)

	missing, err := server.Client().Get(server.URL + "/sessions/ghost/models")
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(missing.Body)
	missing.Body.Close()
	missingEnvelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, missing.StatusCode, http.StatusNotFound, missingEnvelope, "unknown_session")
}

// The degraded opt-in is a wire-visible field, so it travels as a repeatable
// query parameter rather than a header: a GET carries no body, and a header
// would hide it from logs and curl. The daemon maps it onto the payload the
// adapter reads, unchanged and in order.
func TestModelsRouteCarriesTheDegradedOptin(t *testing.T) {
	recorder := &recordingLister{}
	registry := serve.NewRegistry()
	if err := registry.Register("recorder", recorder); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	openSession(t, server, "recorder", "optin")

	response, err := server.Client().Get(server.URL + "/sessions/optin/models?allow_degraded=models.list&allow_degraded=other.key")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", response.StatusCode, data)
	}
	if got := strings.Join(recorder.seen(), ","); got != "models.list,other.key" {
		t.Fatalf("adapter saw allow_degraded_features %q", got)
	}
}

// A session whose adapter serves no catalog is refused under the key rather
// than answered with an empty list: an endpoint that lists nothing and one
// that cannot list are different answers to the same question.
func TestModelsRouteRefusesAnAdapterWithoutACatalog(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("catalogless", catalogless{}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	openSession(t, server, "catalogless", "no-catalog")

	response, err := server.Client().Get(server.URL + "/sessions/no-catalog/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, response.StatusCode, http.StatusBadRequest, envelope, "unsupported_feature")
	var refusal protocol.ErrorResponse
	if err := envelope.DecodePayload(&refusal); err != nil {
		t.Fatal(err)
	}
	if refusal.Error.Details["feature"] != protocol.FeatureModelsList || refusal.Error.Details["reason"] != base.ControlUnadvertised {
		t.Fatalf("refusal does not say what to stop sending: %+v", refusal.Error.Details)
	}
}

// recordingLister is the reference adapter with one addition: it records the
// catalog request it was handed, so a test can see what crossed the boundary
// rather than only what came back.
type recordingLister struct {
	mu       sync.Mutex
	requests []protocol.ModelsRequest
}

func (a *recordingLister) Probe(ctx context.Context) (base.Descriptor, error) {
	return base.NewMemory(base.Config{}).Probe(ctx)
}

func (a *recordingLister) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := base.NewMemory(base.Config{}).Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return &recordingSession{Session: session, adapter: a}, nil
}

func (a *recordingLister) seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) != 1 {
		return nil
	}
	return a.requests[0].AllowDegradedFeatures
}

type recordingSession struct {
	base.Session
	adapter *recordingLister
}

func (s *recordingSession) Models(ctx context.Context, request protocol.ModelsRequest) (protocol.ModelsResponse, error) {
	s.adapter.mu.Lock()
	s.adapter.requests = append(s.adapter.requests, request)
	s.adapter.mu.Unlock()
	return s.Session.(base.ModelLister).Models(ctx, request)
}

// catalogless is an adapter whose sessions serve no catalog at all, which is
// what every session implementation written before this unit is.
type catalogless struct{}

func (catalogless) Probe(ctx context.Context) (base.Descriptor, error) {
	return base.NewMemory(base.Config{}).Probe(ctx)
}

func (catalogless) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := base.NewMemory(base.Config{}).Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return sessionWithoutCatalog{Session: session}, nil
}

// sessionWithoutCatalog hides the reference adapter's ModelLister behind a
// struct that does not promote it, so the type assertion the hub makes fails
// exactly as it does for an adapter that never implemented it.
type sessionWithoutCatalog struct{ base.Session }
