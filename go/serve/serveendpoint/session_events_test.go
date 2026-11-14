package serveendpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	base "github.com/lsm/oxap/go/adapter"
	"github.com/lsm/oxap/go/protocol"
	"github.com/lsm/oxap/go/serve"
)

type eventAdapter struct {
	*base.Memory
	events chan base.Result
}
type eventSession struct {
	base.Session
	events chan base.Result
}

func (a *eventAdapter) Open(ctx context.Context, request base.OpenRequest) (base.Session, error) {
	session, err := a.Memory.Open(ctx, request)
	if err != nil {
		return nil, err
	}
	return &eventSession{session, a.events}, nil
}
func (s *eventSession) SessionEvents() base.EventStream { return s.events }

func (s *eventSession) UpdateSettings(ctx context.Context, request protocol.SessionSettingsUpdateRequest) (protocol.SessionSettingsUpdateResponse, protocol.SessionState, error) {
	return s.Session.(base.SettingsUpdater).UpdateSettings(ctx, request)
}

func TestSessionEventsReachEndpointBetweenRuns(t *testing.T) {
	adapter := &eventAdapter{Memory: base.NewMemory(base.Config{}), events: make(chan base.Result, 2)}
	registry := serve.NewRegistry()
	if err := registry.Register("events", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	defer hub.CloseSessions(context.Background())
	server, err := New(hub, Options{Adapter: "events", Shutdown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	reader, output := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), input, output); output.Close() }()
	scan := bufio.NewScanner(reader)
	next := func() protocol.Envelope {
		t.Helper()
		if !scan.Scan() {
			t.Fatalf("missing output: %v", scan.Err())
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(scan.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}
	event, err := protocol.NewEnvelope(protocol.TypeSessionTaskStarted, "task", map[string]any{"task": map[string]any{"task_id": "t"}})
	if err != nil {
		t.Fatal(err)
	}
	event.SessionID = "s"
	adapter.events <- base.Result{Envelope: event}
	_, err = io.WriteString(writer, requestLine(t, protocol.TypeSessionOpenRequest, "open", protocol.SessionOpenRequest{SessionID: "s"}, "s")+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := next(); got.Type != protocol.TypeSessionOpenResponse {
		t.Fatalf("open: %+v", got)
	}
	if got := next(); got.Type != protocol.TypeSessionTaskStarted || got.Sequence == nil || *got.Sequence != 1 || got.RunID != "" {
		t.Fatalf("task: %+v", got)
	}
	policy := &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 9}
	_, err = io.WriteString(writer, requestLine(t, protocol.TypeSessionSettingsUpdateRequest, "settings", protocol.SessionSettingsUpdateRequest{SessionID: "s", CompactionPolicy: policy}, "s")+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := next(); got.Type != protocol.TypeSessionSettingsUpdateResponse {
		t.Fatalf("response: %+v", got)
	}
	if got := next(); got.Type != protocol.TypeSessionStateUpdated || got.Sequence == nil || *got.Sequence != 2 {
		t.Fatalf("state: %+v", got)
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionEventSourceFailureEndsEndpoint(t *testing.T) {
	adapter := &eventAdapter{Memory: base.NewMemory(base.Config{}), events: make(chan base.Result)}
	registry := serve.NewRegistry()
	if err := registry.Register("events", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	defer hub.CloseSessions(context.Background())
	server, err := New(hub, Options{Adapter: "events", Shutdown: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	input, writer := io.Pipe()
	defer writer.Close()
	reader, output := io.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), input, output); output.Close() }()
	if _, err := io.WriteString(writer, requestLine(t, protocol.TypeSessionOpenRequest, "open", protocol.SessionOpenRequest{SessionID: "s"}, "s")+"\n"); err != nil {
		t.Fatal(err)
	}
	if !bufio.NewScanner(reader).Scan() {
		t.Fatal("missing open response")
	}
	close(adapter.events)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("session stream failure was hidden")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("endpoint did not stop on session stream failure")
	}
}
