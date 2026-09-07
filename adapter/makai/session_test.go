package makai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/makai/internal/stdio"
	"github.com/lsm/open-agent-protocol/protocol"
)

type fakeClock struct {
	mu sync.Mutex
	n  int64
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return time.UnixMilli(c.n)
}

type fakeIDs struct {
	mu sync.Mutex
	n  int
}

func (g *fakeIDs) NewID(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	switch kind {
	case "makai-session":
		return fmt.Sprintf("%021d", g.n)
	case "makai-frame":
		return fmt.Sprintf("000000000000000000000%05d", g.n)
	default:
		return fmt.Sprintf("%s-%d", kind, g.n)
	}
}

type fakeClient struct {
	mu       sync.Mutex
	inbound  chan stdio.Inbound
	done     chan struct{}
	closed   bool
	sends    []native.Envelope
	callHook func(context.Context, native.Envelope, ...native.Type) (native.Envelope, error)
}

func newFakeClient() *fakeClient {
	return &fakeClient{inbound: make(chan stdio.Inbound, 256), done: make(chan struct{})}
}
func (f *fakeClient) Call(ctx context.Context, request native.Envelope, accepted ...native.Type) (native.Envelope, error) {
	if f.callHook != nil {
		return f.callHook(ctx, request, accepted...)
	}
	id := native.MessageID("01ARZ3NDEKTSV4RRFFQ69G5FAV")
	env, _ := native.NewEnvelope(native.TypeAgentStarted, "Abcdefghijklmnopqrstu", id, 1, 1, native.AgentStarted{SessionID: "Abcdefghijklmnopqrstu"})
	env.InReplyTo = &request.MessageID
	return env, nil
}
func (f *fakeClient) Send(_ context.Context, env native.Envelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, env)
	return nil
}
func (f *fakeClient) Inbound() <-chan stdio.Inbound { return f.inbound }
func (f *fakeClient) Done() <-chan struct{}         { return f.done }
func (f *fakeClient) Err() error                    { return errors.New("EOF") }
func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		close(f.done)
		f.closed = true
	}
	return nil
}
func (f *fakeClient) event(t *testing.T, sequence uint64, event any) {
	t.Helper()
	nested, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	id := native.MessageID(fmt.Sprintf("00000000000000000000%06d", sequence+100))
	env, err := native.NewEnvelope(native.TypeAgentEvent, "Abcdefghijklmnopqrstu", id, sequence, int64(sequence), native.AgentEvent{EventJSON: string(nested)})
	if err != nil {
		t.Fatal(err)
	}
	f.inbound <- stdio.Inbound{Envelope: &env}
}
func (f *fakeClient) result(t *testing.T, sequence uint64, result any) {
	t.Helper()
	nested, _ := json.Marshal(result)
	id := native.MessageID(fmt.Sprintf("00000000000000000000%06d", sequence+100))
	env, err := native.NewEnvelope(native.TypeAgentResult, "Abcdefghijklmnopqrstu", id, sequence, int64(sequence), native.AgentResult{ResultJSON: string(nested)})
	if err != nil {
		t.Fatal(err)
	}
	f.inbound <- stdio.Inbound{Envelope: &env}
}

func openTest(t *testing.T, capacity int) (base.Session, *fakeClient) {
	t.Helper()
	client := newFakeClient()
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), WorkingDirectory: "/workspace", AgentConfig: json.RawMessage(`{"model":"test"}`), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	return session, client
}
func submitTest(t *testing.T, session base.Session) (protocol.MessageSubmitResponse, base.EventStream) {
	t.Helper()
	response, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: "test", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err != nil {
		t.Fatal(err)
	}
	return response, stream
}
func collect(t *testing.T, stream base.EventStream) []protocol.Envelope {
	t.Helper()
	var events []protocol.Envelope
	for result := range stream {
		if result.Error != nil {
			t.Fatal(result.Error)
		}
		events = append(events, result.Envelope)
	}
	return events
}

func TestFirstMessageUsesNativeSequenceTwo(t *testing.T) {
	session, client := openTest(t, 32)
	_, _ = submitTest(t, session)
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.sends) != 1 || client.sends[0].Sequence != 2 {
		t.Fatalf("sent envelopes=%+v", client.sends)
	}
}

func TestCompletedTextWaitsForAgentEnd(t *testing.T) {
	session, client := openTest(t, 32)
	response, stream := submitTest(t, session)
	client.event(t, 2, map[string]any{"type": "message_update", "event": map[string]any{"type": "text_delta", "content_index": 0, "delta": "hello"}})
	client.result(t, 3, map[string]any{"type": "result", "stop_reason": "stop", "model": "test", "api": "messages", "provider": "test", "timestamp": 1, "input": 2, "output": 1, "cache_read": 0, "cache_write": 0, "content": []any{map[string]any{"type": "text", "text": "hello"}}})
	select {
	case result := <-stream:
		if result.Envelope.Type != protocol.TypeRunStarted {
			t.Fatalf("first=%s", result.Envelope.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("missing run.started")
	}
	select {
	case result := <-stream:
		if result.Envelope.Type != protocol.TypeContentDelta {
			t.Fatalf("second=%s", result.Envelope.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("missing content delta")
	}
	select {
	case result := <-stream:
		t.Fatalf("result settled early: %s", result.Envelope.Type)
	case <-time.After(20 * time.Millisecond):
	}
	client.event(t, 4, map[string]any{"type": "agent_end", "stop_reason": "stop", "provider_id": "test", "api": "messages"})
	events := collect(t, stream)
	if len(events) != 1 || events[0].Type != protocol.TypeRunCompleted {
		t.Fatalf("events=%v", types(events))
	}
	if response.RunID == "" {
		t.Fatal("missing run id")
	}
}

func TestToolSettlesBeforeParentTerminal(t *testing.T) {
	session, client := openTest(t, 32)
	_, stream := submitTest(t, session)
	client.event(t, 2, map[string]any{"type": "tool_execution_start", "tool_call_id": "tool-1", "tool_name": "demo", "args_json": "{}"})
	client.event(t, 3, map[string]any{"type": "agent_end", "stop_reason": "stop"})
	events := collect(t, stream)
	got := types(events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallFailed, protocol.TypeRunCompleted}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestCancellationRequiresAuthoritativeEnd(t *testing.T) {
	session, client := openTest(t, 32)
	response, stream := submitTest(t, session)
	client.callHook = func(_ context.Context, request native.Envelope, _ ...native.Type) (native.Envelope, error) {
		id := native.MessageID("01ARZ3NDEKTSV4RRFFQ69G5FAW")
		env, _ := native.NewEnvelope(native.TypeAgentStopped, "Abcdefghijklmnopqrstu", id, 2, 2, native.AgentStopped{SessionID: "Abcdefghijklmnopqrstu"})
		env.InReplyTo = &request.MessageID
		return env, nil
	}
	cancelled, err := session.Cancel(context.Background(), response.RunID)
	if err != nil || cancelled.Status != protocol.RunCancelled {
		t.Fatalf("cancel=%+v err=%v", cancelled, err)
	}
	events := collect(t, stream)
	if events[len(events)-1].Type != protocol.TypeRunCancelled {
		t.Fatalf("events=%v", types(events))
	}
}

func TestReplayGapAndTerminalReplay(t *testing.T) {
	session, client := openTest(t, 2)
	response, stream := submitTest(t, session)
	client.event(t, 2, map[string]any{"type": "message_update", "event": map[string]any{"type": "text_delta", "content_index": 0, "delta": "a"}})
	client.event(t, 3, map[string]any{"type": "message_update", "event": map[string]any{"type": "text_delta", "content_index": 0, "delta": "b"}})
	client.event(t, 4, map[string]any{"type": "agent_end", "stop_reason": "stop"})
	_ = collect(t, stream)
	_, gapStream, err := session.Resume(context.Background(), base.ResumeRequest{RunID: response.RunID, AfterSequence: 0})
	var gap *base.ReplayGap
	if !errors.As(err, &gap) {
		t.Fatalf("gap=%v", err)
	}
	if _, ok := <-gapStream; ok {
		t.Fatal("gap stream open")
	}
	recovery, replay, err := session.Resume(context.Background(), base.ResumeRequest{RunID: response.RunID, AfterSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	events := collect(t, replay)
	if recovery.ReplayedThrough != 4 || len(events) != 2 || events[1].Type != protocol.TypeRunCompleted {
		t.Fatalf("recovery=%+v events=%v", recovery, types(events))
	}
}

func types(events []protocol.Envelope) []protocol.EnvelopeType {
	out := make([]protocol.EnvelopeType, len(events))
	for i := range events {
		out[i] = events[i].Type
	}
	return out
}
