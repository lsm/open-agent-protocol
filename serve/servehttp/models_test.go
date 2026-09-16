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
	// The catalog is part of the capability snapshot, so the response names
	// the revision whose models.list promise governs it: without it a consumer
	// cannot bind the listing to a descriptor, and the validator's own gate
	// reads the catalog as citing a stale revision.
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

// The revision a catalog carries is the one its lister produced it under, not
// one the daemon read from a descriptor at some other moment. An adapter whose
// capabilities can update moves between the two reads, and a listing labelled
// with the older revision is one a client caches against the wrong models.list
// promise — the exact property the label exists to provide.
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

// A listing nothing can bind to a descriptor is worse than none: a consumer
// would cache it under no revision and never know when to discard it, and the
// validator's own gate rejects the envelope. The hub refuses rather than
// inventing a label.
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

// A catalog scoped to another session, or to none, is refused rather than
// published or relabelled. Publishing the adapter's value emits a
// cross-session or schema-invalid models.response — one the daemon's own
// clients reject — and rewriting it would show one session's models under
// another's id, which is the fault laundered into something that looks
// correct.
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

// An endpoint that lists no models answers with an empty list, never a null.
// An adapter that builds its listing by appending leaves the slice nil when it
// appends nothing, and Go marshals that as null where the schema requires an
// array — so the daemon would publish an invalid envelope for a state that is
// perfectly legal. The hub normalises it: unlike a missing revision or a
// foreign scope, the two spellings say the same thing and only one is on the
// wire, so this repairs the encoding without inventing an answer.
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
	// Read the payload as raw members: decoding into ModelsResponse would
	// erase the difference between the two encodings, which is the whole of
	// what this test is about.
	var members map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload, &members); err != nil {
		t.Fatal(err)
	}
	if got := string(members["models"]); got != "[]" {
		t.Fatalf("an empty catalog went on the wire as %s, want []", got)
	}
}

// movingLister probes as the reference adapter but serves its catalog under a
// revision — and optionally a session — of its own, standing in for an adapter
// whose descriptor moved between the two reads, or one that scopes its listing
// wrongly. An empty revision stands in for one that labels nothing at all, and
// empty for an adapter whose append-built listing gained no entries.
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
		// Nil, not an empty slice: exactly what an adapter that only ever
		// appends hands back when it has nothing to append.
		catalog.Models.Models = nil
	}
	return catalog, nil
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

func (s *recordingSession) Models(ctx context.Context, request protocol.ModelsRequest) (base.Catalog, error) {
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
