package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

type unencodableAdapter struct {
	mu      sync.Mutex
	session *unencodableSession
}

func (a *unencodableAdapter) opened(t *testing.T) *unencodableSession {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		t.Fatal("the adapter opened no session")
	}
	return a.session
}

func (*unencodableAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities:       protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "reference.unencodable"}},
		CapabilityRevision: "unencodable-v1",
	}, nil
}

func (a *unencodableAdapter) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	session := &unencodableSession{id: request.SessionID}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	return session, nil
}

type unencodableSession struct {
	id     protocol.SessionID
	mu     sync.Mutex
	closed bool
}

var _ base.Session = (*unencodableSession)(nil)

func (s *unencodableSession) State(context.Context) (protocol.SessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := protocol.SessionIdle
	if s.closed {
		status = protocol.SessionClosed
	}
	return protocol.SessionState{
		SessionID: s.id, Status: status,
		Metadata: map[string]json.RawMessage{"broken": json.RawMessage("{not json")},
	}, nil
}

func (s *unencodableSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
}
func (s *unencodableSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *unencodableSession) Cancel(_ context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: s.id, RunID: runID}, nil
}
func (s *unencodableSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, base.ErrRunNotFound
}
func (s *unencodableSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *unencodableSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func TestOpenRollsBackWhenItsResponseCannotEncode(t *testing.T) {
	t.Run("a minted id is rolled back", func(t *testing.T) {
		adapter := &unencodableAdapter{}
		registry := serve.NewRegistry()
		if err := registry.Register("broken", adapter); err != nil {
			t.Fatal(err)
		}
		_, server := newServer(t, registry, Options{})
		status, failure := postOpenTo(t, server, protocol.SessionOpenRequest{})
		if status != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500: %s", status, failure)
		}
		if !bytes.Contains(failure, []byte("rolled back")) {
			t.Fatalf("the refusal does not say what it left behind: %s", failure)
		}
		if !adapter.opened(t).isClosed() {
			t.Fatal("the failed open left a live session behind under an id the caller never saw")
		}
	})

	t.Run("an id the caller named is kept and said so", func(t *testing.T) {
		adapter := &unencodableAdapter{}
		registry := serve.NewRegistry()
		if err := registry.Register("broken", adapter); err != nil {
			t.Fatal(err)
		}
		_, server := newServer(t, registry, Options{})
		status, failure := postOpenTo(t, server, protocol.SessionOpenRequest{SessionID: "named-by-caller"})
		if status != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500: %s", status, failure)
		}
		if !bytes.Contains(failure, []byte("the session is open under the session_id the request supplied")) {
			t.Fatalf("the refusal hides the kept session: %s", failure)
		}
		if adapter.opened(t).isClosed() {
			t.Fatal("a session the caller named was rolled back; it is the one it could still close itself")
		}
	})
}

func postOpenTo(t *testing.T, server *httptest.Server, request protocol.SessionOpenRequest) (int, []byte) {
	t.Helper()
	envelope, err := protocol.NewEnvelope(protocol.TypeSessionOpenRequest, "open-broken", request)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = request.SessionID
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.Post(server.URL+"/adapters/broken/sessions", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, payload
}
