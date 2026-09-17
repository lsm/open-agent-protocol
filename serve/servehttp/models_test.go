package servehttp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

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

	if envelope.CapabilityRevision != base.CapabilityRevision {
		t.Fatalf("models response cites revision %q, want %q", envelope.CapabilityRevision, base.CapabilityRevision)
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

func TestModelsRouteStampsTheListersRevision(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("moving", &movingLister{revision: "moved-past-the-probe"}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	openSession(t, server, "moving", "moving")

	response, err := server.Client().Get(server.URL + "/sessions/moving/models")
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
	if envelope.CapabilityRevision != "moved-past-the-probe" {
		t.Fatalf("catalog cites revision %q, want the one its lister produced it under", envelope.CapabilityRevision)
	}
	if envelope.CapabilityRevision == base.CapabilityRevision {
		t.Fatal("catalog cites the probed descriptor's revision rather than the listing's")
	}
}

func TestModelsRouteRefusesAnUnlabelledCatalog(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("unlabelled", &movingLister{}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	openSession(t, server, "unlabelled", "unlabelled")

	response, err := server.Client().Get(server.URL + "/sessions/unlabelled/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireErrorResponse(t, response.StatusCode, http.StatusInternalServerError, envelope, "internal")
}

func TestModelsRouteRefusesAMisscopedCatalog(t *testing.T) {
	for _, row := range []struct {
		name    string
		session protocol.SessionID
	}{
		{"another session", "somewhere-else"},
		{"no session at all", ""},
	} {
		t.Run(row.name, func(t *testing.T) {
			registry := serve.NewRegistry()
			if err := registry.Register("misscoped", &movingLister{revision: base.CapabilityRevision, session: &row.session}); err != nil {
				t.Fatal(err)
			}
			_, server := newServer(t, registry, Options{})
			openSession(t, server, "misscoped", "misscoped")

			response, err := server.Client().Get(server.URL + "/sessions/misscoped/models")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			data, _ := io.ReadAll(response.Body)
			envelope, err := protocol.ParseEnvelope(data)
			if err != nil {
				t.Fatal(err)
			}
			requireErrorResponse(t, response.StatusCode, http.StatusInternalServerError, envelope, "internal")
		})
	}
}

func TestModelsRouteServesAnEmptyCatalogAsAnEmptyList(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("empty", &movingLister{revision: base.CapabilityRevision, empty: true}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	openSession(t, server, "empty", "empty")

	response, err := server.Client().Get(server.URL + "/sessions/empty/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("an empty catalog was refused with %d", response.StatusCode)
	}
	data, _ := io.ReadAll(response.Body)
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}

	var members map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload, &members); err != nil {
		t.Fatal(err)
	}
	if got := string(members["models"]); got != "[]" {
		t.Fatalf("an empty catalog went on the wire as %s, want []", got)
	}
}

type movingLister struct {
	revision string
	session  *protocol.SessionID
	empty    bool
}

func (a *movingLister) Probe(ctx context.Context) (base.Descriptor, error) {
	return base.NewMemory(base.Config{}).Probe(ctx)
}

func (a *movingLister) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := base.NewMemory(base.Config{}).Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return &movingSession{Session: session, revision: a.revision, session: a.session, empty: a.empty}, nil
}

type movingSession struct {
	base.Session
	revision string
	session  *protocol.SessionID
	empty    bool
}

func (s *movingSession) Models(ctx context.Context, request protocol.ModelsRequest) (base.Catalog, error) {
	catalog, err := s.Session.(base.ModelLister).Models(ctx, request)
	if err != nil {
		return base.Catalog{}, err
	}
	catalog.Revision = s.revision
	if s.session != nil {
		catalog.Models.SessionID = *s.session
	}
	if s.empty {

		catalog.Models.Models = nil
	}
	return catalog, nil
}

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

func (s *recordingSession) Models(ctx context.Context, request protocol.ModelsRequest) (base.Catalog, error) {
	s.adapter.mu.Lock()
	s.adapter.requests = append(s.adapter.requests, request)
	s.adapter.mu.Unlock()
	return s.Session.(base.ModelLister).Models(ctx, request)
}

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

type sessionWithoutCatalog struct{ base.Session }
