package servestdio

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestSessionEventFramingPreservesRunCursor(t *testing.T) {
	sequence := uint64(9)
	event := protocol.Envelope{Type: protocol.TypeSessionTaskStarted, SessionID: "s", Sequence: &sequence, Payload: []byte(`{}`)}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(envelopeLine{Event: signalEnvelope, ID: 1, SessionID: "s", Sequence: runEventSequence(event), Envelope: raw})
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(line, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["sequence"]; ok {
		t.Fatalf("outer sequence on session event: %s", line)
	}
	if !strings.Contains(string(parsed["envelope"]), `"sequence":9`) {
		t.Fatalf("missing session sequence: %s", line)
	}
	run, after := advanceCursor("r", 4, event)
	if run != "r" || after != 4 {
		t.Fatalf("cursor: %q %d", run, after)
	}
	event.RunID = "r"
	if got := runEventSequence(event); got == nil || *got != 9 {
		t.Fatalf("run sequence: %v", got)
	}
}

type sessionOnlyAdapter struct{ session *sessionOnlySource }
type sessionOnlySource struct {
	base.Session
	events chan base.Result
}

func (a sessionOnlyAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "events"}}, CapabilityRevision: "test"}, nil
}
func (a sessionOnlyAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return a.session, nil
}
func (s *sessionOnlySource) SessionEvents() base.EventStream { return s.events }
func (s *sessionOnlySource) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "s", Status: protocol.SessionIdle}, nil
}
func (s *sessionOnlySource) Close(context.Context) error { return nil }

func TestSessionEventStdioDeliveryAndUnframeableRecovery(t *testing.T) {
	source := &sessionOnlySource{events: make(chan base.Result, 2)}
	registry := serve.NewRegistry()
	if err := registry.Register("events", sessionOnlyAdapter{source}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	if _, _, err := hub.Open(context.Background(), "events", base.OpenRequest{SessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	defer hub.CloseSessions(context.Background())
	f := startFrontend(t, hub, Options{FrameLimit: 800})
	f.send(`{"id":1,"op":"events","session_id":"s"}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("subscribe: %+v", response.Error)
	}
	event, err := protocol.NewEnvelope(protocol.TypeSessionTaskStarted, "e1", map[string]any{"task_id": "t"})
	if err != nil {
		t.Fatal(err)
	}
	event.SessionID = "s"
	source.events <- base.Result{Envelope: event}
	first := f.expectSignal(1, signalEnvelope)
	if first.Sequence != nil {
		t.Fatalf("session event has outer cursor: %+v", first)
	}
	large, err := protocol.NewEnvelope(protocol.TypeSessionTaskEnded, "e2", map[string]any{"summary": strings.Repeat("x", 1200)})
	if err != nil {
		t.Fatal(err)
	}
	large.SessionID = "s"
	source.events <- base.Result{Envelope: large}
	var ending struct {
		Event    string `json:"event"`
		RunID    string `json:"run_id"`
		Sequence uint64 `json:"sequence"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(f.line()), &ending); err != nil {
		t.Fatal(err)
	}
	if ending.Event != signalStreamFailed || ending.RunID != "" || ending.Sequence != 0 || !strings.Contains(ending.Message, "read state") {
		t.Fatalf("ending: %+v", ending)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}
