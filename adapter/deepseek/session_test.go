package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/deepseek/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
)

type testClock struct {
	mu sync.Mutex
	n  int64
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return time.UnixMilli(c.n)
}

type testIDs struct {
	mu sync.Mutex
	n  int
}

func (i *testIDs) NewID(k string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return k + "-" + string(rune('a'+i.n-1))
}

type promptReply struct {
	id     string
	err    error
	before func()
}
type fakeClient struct {
	in      chan rpc.InboundMessage
	done    chan struct{}
	prompts chan promptReply
	started chan struct{}
	mu      sync.Mutex
	closed  bool
	calls   int
}

func newFake() *fakeClient {
	return &fakeClient{in: make(chan rpc.InboundMessage, 64), done: make(chan struct{}), prompts: make(chan promptReply, 8), started: make(chan struct{}, 8)}
}
func (f *fakeClient) Call(ctx context.Context, m string, p, r any) error {
	return f.CallStarted(ctx, m, p, r, nil)
}
func (f *fakeClient) CallStarted(_ context.Context, m string, _ any, r any, started chan<- error) error {
	if m != native.MethodSessionPrompt {
		return errors.New("unexpected call")
	}
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	f.started <- struct{}{}
	if started != nil {
		started <- nil
		close(started)
	}
	o := <-f.prompts
	if o.before != nil {
		o.before()
	}
	if o.err == nil {
		r.(*native.SessionPromptResult).MessageID = o.id
	}
	return o.err
}
func (f *fakeClient) Inbound() <-chan rpc.InboundMessage { return f.in }
func (f *fakeClient) Done() <-chan struct{}              { return f.done }
func (f *fakeClient) Err() error                         { return errors.New("EOF") }
func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		close(f.done)
		f.closed = true
	}
	return nil
}
func (f *fakeClient) notify(v any) {
	n := rpc.NotificationMessage{}
	switch x := v.(type) {
	case *native.SessionEventNotification:
		n.Method = native.NotifySessionEvent
		n.Value = x
	case *native.SessionStatusNotification:
		n.Method = native.NotifySessionStatus
		n.Value = x
	case *native.SubagentStartedNotification:
		n.Method = native.NotifySubagentStarted
		n.Value = x
	case *native.SubagentFinishedNotification:
		n.Method = native.NotifySubagentFinished
		n.Value = x
	}
	f.in <- rpc.InboundMessage{Notification: &n}
}
func event(seq int64, typ string, data any) native.Event {
	raw, _ := json.Marshal(data)
	return native.Event{Seq: seq, Time: seq, Type: typ, Data: raw}
}
func source(kind string) native.MessageSource { return native.MessageSource{Kind: kind} }
func (f *fakeClient) ev(seq int64, typ string, data any) {
	f.notify(&native.SessionEventNotification{SessionID: "session", Event: event(seq, typ, data)})
}
func openTest(t *testing.T) (base.Session, *fakeClient) {
	t.Helper()
	f := newFake()
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return f, "deepseek-chat", nil }), Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}
func request() protocol.MessageSubmitRequest {
	return protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}
}
func submitAsync(s base.Session) <-chan struct {
	r   protocol.MessageSubmitResponse
	st  base.EventStream
	err error
} {
	ch := make(chan struct {
		r   protocol.MessageSubmitResponse
		st  base.EventStream
		err error
	}, 1)
	go func() {
		r, st, e := s.Submit(context.Background(), request())
		ch <- struct {
			r   protocol.MessageSubmitResponse
			st  base.EventStream
			err error
		}{r, st, e}
	}()
	return ch
}
func admission(t *testing.T, s base.Session, f *fakeClient, id string) (protocol.MessageSubmitResponse, base.EventStream) {
	t.Helper()
	ch := submitAsync(s)
	<-f.started
	f.prompts <- promptReply{id: id}
	f.ev(1, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: id, Role: "user", Content: []native.ContentBlock{{Type: "text", Text: "hello"}}, Source: source("user")}}})
	f.ev(2, "turn/start", native.TurnStart{Turn: 1})
	f.ev(3, "step/start", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(4, "user/message", native.UserMessage{ID: id, Role: "user", Content: []native.ContentBlock{{Type: "text", Text: "hello"}}, Source: source("user")})
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		return got.r, got.st
	case <-time.After(time.Second):
		t.Fatal("admission timed out")
	}
	return protocol.MessageSubmitResponse{}, nil
}
func drain(t *testing.T, st base.EventStream) []protocol.Envelope {
	t.Helper()
	var out []protocol.Envelope
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case r, ok := <-st:
			if !ok {
				return out
			}
			if r.Error != nil {
				t.Fatal(r.Error)
			}
			out = append(out, r.Envelope)
		case <-timer.C:
			t.Fatal("stream did not close")
		}
	}
}

func TestDescriptorIsConservative(t *testing.T) {
	a, _ := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return newFake(), "m", nil })})
	d, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertDescriptor(t, d)
	if d.Capabilities.Features["run.cancel"].Level != protocol.SupportUnavailable || d.Capabilities.Features["run.resume"].Level != protocol.SupportUnavailable {
		t.Fatal("unsupported control advertised")
	}
}
func TestAdmissionWaitsForExactDirectUserProof(t *testing.T) {
	s, f := openTest(t)
	ch := submitAsync(s)
	<-f.started
	f.prompts <- promptReply{id: "receipt"}
	f.ev(1, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: "receipt", Role: "user", Content: []native.ContentBlock{}, Source: source("user")}}})
	f.ev(2, "turn/start", native.TurnStart{Turn: 1})
	f.ev(3, "step/start", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(4, "user/message", native.UserMessage{ID: "receipt", Role: "user", Content: []native.ContentBlock{}, Source: native.MessageSource{Kind: "plugin", Plugin: "x"}})
	select {
	case <-ch:
		t.Fatal("synthetic message admitted")
	case <-time.After(20 * time.Millisecond):
	}
	f.ev(5, "step/end", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(6, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"blocked"}`)})
	select {
	case got := <-ch:
		if got.err == nil {
			t.Fatal("expected pre-start failure")
		}
		if got.r.RunID != "" {
			t.Fatal("pre-start run exposed")
		}
	case <-time.After(time.Second):
		t.Fatal("failure timed out")
	}
}
func TestPromptResponseMayFollowEarlyNotifications(t *testing.T) {
	s, f := openTest(t)
	ch := submitAsync(s)
	<-f.started
	f.prompts <- promptReply{id: "receipt", before: func() {
		f.ev(1, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: "receipt", Role: "user", Content: []native.ContentBlock{}, Source: source("user")}}})
		f.ev(2, "turn/start", native.TurnStart{Turn: 1})
		f.ev(3, "step/start", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(4, "user/message", native.UserMessage{ID: "receipt", Role: "user", Content: []native.ContentBlock{}, Source: source("user")})
		bar := make(chan struct{})
		f.in <- rpc.InboundMessage{Barrier: bar}
		<-bar
	}}
	select {
	case got := <-ch:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.r.Admission != protocol.AdmissionStarted {
			t.Fatal(got.r.Admission)
		}
	case <-time.After(time.Second):
		t.Fatal("admission timed out")
	}
}
func TestCompletedRunMapsChunksUsageAndSettlement(t *testing.T) {
	s, f := openTest(t)
	a, st := admission(t, s, f, "receipt")
	f.ev(5, "assistant/chunk", native.AssistantChunk{Turn: 1, Step: 1, Chunk: json.RawMessage(`{"type":"text-delta","index":0,"text":"hi"}`)})
	f.ev(6, "assistant/message", native.AssistantMessageEvent{Turn: 1, Step: 1, Message: native.AssistantMessage{ID: "a", Role: "assistant", Content: []native.ContentBlock{{Type: "text", Text: "hi"}}, Source: native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}}, Usage: &native.TokenUsage{InputTokens: 2, OutputTokens: 1}})
	f.ev(7, "step/end", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(8, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
	time.Sleep(20 * time.Millisecond)
	state, err := s.State(context.Background())
	if err != nil || state.Status != protocol.SessionRunning {
		t.Fatalf("terminal before idle: state=%+v err=%v", state, err)
	}
	f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	events := drain(t, st)
	adaptertest.AssertRunTrace(t, a, CapabilityRevision, events)
	types := []protocol.EnvelopeType{}
	for _, e := range events {
		types = append(types, e.Type)
	}
	if len(types) != 3 || types[0] != protocol.TypeRunStarted || types[1] != protocol.TypeContentDelta || types[2] != protocol.TypeRunCompleted {
		t.Fatalf("types %v", types)
	}
}
func TestMaxTokensFails(t *testing.T) {
	s, f := openTest(t)
	_, st := admission(t, s, f, "r")
	f.ev(5, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"max-tokens"}`)})
	f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	ev := drain(t, st)
	if ev[len(ev)-1].Type != protocol.TypeRunFailed {
		t.Fatal(ev[len(ev)-1].Type)
	}
}
func TestChildDelaysParentTerminal(t *testing.T) {
	s, f := openTest(t)
	_, st := admission(t, s, f, "r")
	f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "child"})
	f.ev(5, "assistant/message", native.AssistantMessageEvent{Turn: 1, Step: 1, Message: native.AssistantMessage{ID: "a", Role: "assistant", Content: []native.ContentBlock{{Type: "text", Text: "ok"}}, Source: native.MessageSource{Kind: "model", Provider: "p", Model: "m"}}})
	f.ev(6, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
	f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	time.Sleep(20 * time.Millisecond)
	state, err := s.State(context.Background())
	if err != nil || state.Status != protocol.SessionRunning {
		t.Fatalf("settled before child: state=%+v err=%v", state, err)
	}
	f.notify(&native.SubagentFinishedNotification{Provider: "p", AgentID: "a", ParentSessionID: "session", ChildSessionID: "child", Status: "ok", StopReason: "completed"})
	ev := drain(t, st)
	if ev[len(ev)-1].Type != protocol.TypeRunCompleted {
		t.Fatal(ev[len(ev)-1].Type)
	}
}
func TestProcessLossBeforeAndAfterStart(t *testing.T) {
	t.Run("pre-start no event", func(t *testing.T) {
		s, f := openTest(t)
		ch := submitAsync(s)
		<-f.started
		f.prompts <- promptReply{id: "r"}
		close(f.done)
		got := <-ch
		if got.err == nil {
			t.Fatal("expected error")
		}
		if got.st != nil {
			for r := range got.st {
				if r.Envelope.Type != "" {
					t.Fatal("event before start")
				}
			}
		}
	})
	t.Run("started exactly one failure", func(t *testing.T) {
		s, f := openTest(t)
		_, st := admission(t, s, f, "r")
		close(f.done)
		ev := drain(t, st)
		if len(ev) != 2 || ev[0].Type != protocol.TypeRunStarted || ev[1].Type != protocol.TypeRunFailed {
			t.Fatalf("events %#v", ev)
		}
	})
}
func TestUnsupportedControlsHaveNoNativeSideEffects(t *testing.T) {
	s, f := openTest(t)
	if _, e := s.Cancel(context.Background(), "x"); !errors.Is(e, errUnavailable) {
		t.Fatal(e)
	}
	if e := s.Resolve(context.Background(), base.InteractionResolution{}); !errors.Is(e, errUnavailable) {
		t.Fatal(e)
	}
	if _, _, e := s.Resume(context.Background(), base.ResumeRequest{}); !errors.Is(e, errUnavailable) {
		t.Fatal(e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls != 0 {
		t.Fatal("native side effect")
	}
}
