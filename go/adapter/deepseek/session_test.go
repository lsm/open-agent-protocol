package deepseek

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
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
	id       string
	err      error
	startErr error
	before   func()
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
	o := <-f.prompts
	if started != nil {
		started <- o.startErr
		close(started)
	}
	if o.startErr != nil {
		return o.startErr
	}
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

type assistantMessageWire struct {
	Turn    int64                   `json:"turn"`
	Step    int64                   `json:"step"`
	Message native.AssistantMessage `json:"message"`
	Stream  json.RawMessage         `json:"stream"`
	Usage   *native.TokenUsage      `json:"usage,omitempty"`
}

func assistantMessage(turn, step int64, id string, content []native.ContentBlock, model native.MessageSource, stream string, usage *native.TokenUsage) assistantMessageWire {
	return assistantMessageWire{
		Turn:    turn,
		Step:    step,
		Message: native.AssistantMessage{ID: id, Role: "assistant", Content: content, Source: model},
		Stream:  json.RawMessage(stream),
		Usage:   usage,
	}
}
func (f *fakeClient) ev(seq int64, typ string, data any) {
	f.notify(&native.SessionEventNotification{SessionID: "session", Event: event(seq, typ, data)})
}

func TestBlocksContentEmptyUsesEmptyText(t *testing.T) {
	session, _ := openTest(t)
	content, err := session.(*Session).blocksContent(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, ok := content.Text()
	if !ok || text != "" {
		t.Fatalf("content = %+v, want empty text", content)
	}
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

func TestSubmitRejectsUnappliedModelID(t *testing.T) {
	s, _ := openTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := request()
	req.ModelID = protocol.ControlValue("deepseek-other")
	if _, _, err := s.Submit(ctx, req); !errors.Is(err, base.ErrUnsupportedInput) {
		t.Fatalf("got %v, want ErrUnsupportedInput", err)
	}
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

	f.ev(5, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "hi"}}, native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}, `[{"type":"chunk","time":5,"chunk":{"type":"text-delta","index":0,"text":"hi"}}]`, &native.TokenUsage{InputTokens: 2, OutputTokens: 1}))
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
	f.ev(5, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "ok"}}, native.MessageSource{Kind: "model", Provider: "p", Model: "m"}, `[]`, nil))
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

func TestPreAdmissionChildNotificationsDefer(t *testing.T) {
	proof := func(f *fakeClient, receipt string) {
		f.ev(1, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: receipt, Role: "user", Content: []native.ContentBlock{}, Source: source("user")}}})
		f.ev(2, "turn/start", native.TurnStart{Turn: 1})
		f.ev(3, "step/start", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(4, "user/message", native.UserMessage{ID: receipt, Role: "user", Content: []native.ContentBlock{}, Source: source("user")})
	}

	reduced := func(f *fakeClient) {
		bar := make(chan struct{})
		f.in <- rpc.InboundMessage{Barrier: bar}
		<-bar
	}
	t.Run("matched child pair defers to admission", func(t *testing.T) {
		s, f := openTest(t)
		ch := submitAsync(s)
		<-f.started
		proof(f, "receipt")
		f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "child"})
		f.notify(&native.SubagentFinishedNotification{Provider: "p", AgentID: "agent", ParentSessionID: "session", ChildSessionID: "child", Status: "ok", StopReason: "completed"})
		reduced(f)
		f.prompts <- promptReply{id: "receipt"}
		select {
		case got := <-ch:
			if got.err != nil {
				t.Fatalf("pre-receipt child pair failed the submission: %v", got.err)
			}
			f.ev(5, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "ok"}}, native.MessageSource{Kind: "model", Provider: "p", Model: "m"}, `[]`, nil))
			f.ev(6, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
			f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
			events := drain(t, got.st)
			adaptertest.AssertRunTrace(t, got.r, CapabilityRevision, events)
			if len(events) != 2 || events[0].Type != protocol.TypeRunStarted || events[1].Type != protocol.TypeRunCompleted {
				t.Fatalf("events %v", events)
			}
		case <-time.After(time.Second):
			t.Fatal("admission timed out")
		}
	})
	t.Run("unmatched finish fails the admitted run coherently", func(t *testing.T) {
		s, f := openTest(t)
		ch := submitAsync(s)
		<-f.started
		proof(f, "receipt")
		f.notify(&native.SubagentFinishedNotification{Provider: "p", AgentID: "agent", ParentSessionID: "session", ChildSessionID: "ghost", Status: "ok", StopReason: "completed"})
		reduced(f)
		f.prompts <- promptReply{id: "receipt"}
		select {
		case got := <-ch:
			if got.err != nil {
				t.Fatalf("submission failed before ownership proof: %v", got.err)
			}
			events := drain(t, got.st)
			adaptertest.AssertRunTrace(t, got.r, CapabilityRevision, events)
			if len(events) != 2 || events[0].Type != protocol.TypeRunStarted || events[1].Type != protocol.TypeRunFailed {
				t.Fatalf("events %v", events)
			}
			var payload protocol.RunFailedPayload
			if err := events[1].DecodePayload(&payload); err != nil || payload.Error.Code != "deepseek_child_lifecycle" {
				t.Fatalf("failure payload %+v err=%v", payload.Error, err)
			}
		case <-time.After(time.Second):
			t.Fatal("admission timed out")
		}
		state, err := s.State(context.Background())
		if err != nil || state.Status != protocol.SessionIdle {
			t.Fatalf("session unusable after coherent failure: state=%+v err=%v", state, err)
		}
	})
}

func TestPromptReceiptWithoutMessageIDRetiresSession(t *testing.T) {
	s, f := openTest(t)
	settled := make(chan error, 1)
	go func() {
		_, _, err := s.Submit(context.Background(), request())
		settled <- err
	}()
	<-f.started
	f.prompts <- promptReply{id: ""}
	select {
	case err := <-settled:
		if !errors.Is(err, ErrNativeProtocol) {
			t.Fatalf("got %v, want ErrNativeProtocol", err)
		}
	case <-time.After(time.Second):
		t.Fatal("submit did not settle")
	}
	if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("retry: got %v, want ErrSessionClosed", err)
	}
}

func TestSubmitCancellationAfterReceiptKeepsReservation(t *testing.T) {
	s, f := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan struct {
		r   protocol.MessageSubmitResponse
		st  base.EventStream
		err error
	}, 1)
	go func() {
		r, st, err := s.Submit(ctx, request())
		ch <- struct {
			r   protocol.MessageSubmitResponse
			st  base.EventStream
			err error
		}{r, st, err}
	}()
	<-f.started
	cancel()
	f.prompts <- promptReply{id: "receipt"}
	var st base.EventStream
	select {
	case got := <-ch:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", got.err)
		}
		st = got.st
	case <-time.After(time.Second):
		t.Fatal("submit did not settle")
	}

	if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("overlapping submit: err=%v, want ErrRunActive", err)
	}

	f.ev(1, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: "receipt", Role: "user", Content: []native.ContentBlock{{Type: "text", Text: "hello"}}, Source: source("user")}}})
	f.ev(2, "turn/start", native.TurnStart{Turn: 1})
	f.ev(3, "step/start", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(4, "user/message", native.UserMessage{ID: "receipt", Role: "user", Content: []native.ContentBlock{{Type: "text", Text: "hello"}}, Source: source("user")})
	f.ev(5, "step/end", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(6, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
	f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	if st != nil {
		drain(t, st)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("session wedged after abandoned submission: %v", err)
	}
}

func TestEnteredMessageMayArriveInLaterStep(t *testing.T) {

	proof := func(f *fakeClient, receipt string) {
		f.ev(1, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: receipt, Role: "user", Content: []native.ContentBlock{}, Source: source("user")}}})
		f.ev(2, "turn/start", native.TurnStart{Turn: 1})
		f.ev(3, "step/start", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(4, "user/message", native.UserMessage{ID: "other", Role: "user", Content: []native.ContentBlock{}, Source: native.MessageSource{Kind: "plugin", Plugin: "watcher"}})
		f.ev(5, "step/end", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(6, "step/start", native.StepBoundary{Turn: 1, Step: 2})
		f.ev(7, "user/message", native.UserMessage{ID: receipt, Role: "user", Content: []native.ContentBlock{}, Source: source("user")})
	}
	settle := func(f *fakeClient) {
		f.ev(8, "assistant/message", assistantMessage(1, 2, "a", []native.ContentBlock{{Type: "text", Text: "hi"}}, native.MessageSource{Kind: "model", Provider: "p", Model: "m"}, `[{"type":"text-chunks","time0":0,"index":0,"dt":[],"texts":["hi"]}]`, &native.TokenUsage{InputTokens: 1, OutputTokens: 1}))
		f.ev(10, "step/end", native.StepBoundary{Turn: 1, Step: 2})
		f.ev(11, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
		f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	}
	runCase := func(t *testing.T, replyFirst bool) {
		s, f := openTest(t)
		ch := submitAsync(s)
		<-f.started
		if replyFirst {
			f.prompts <- promptReply{id: "receipt"}
			proof(f, "receipt")
			settle(f)
		} else {
			proof(f, "receipt")
			settle(f)
			f.prompts <- promptReply{id: "receipt"}
		}
		select {
		case got := <-ch:
			if got.err != nil {
				t.Fatalf("later-step entry rejected: %v", got.err)
			}
			events := drain(t, got.st)
			adaptertest.AssertRunTrace(t, got.r, CapabilityRevision, events)
			want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
			if len(events) != len(want) {
				t.Fatalf("events %v", events)
			}
			for i := range want {
				if events[i].Type != want[i] {
					t.Fatalf("events %v", events)
				}
			}
		case <-time.After(time.Second):
			t.Fatal("admission timed out")
		}
	}
	t.Run("retrospective proof at the reply", func(t *testing.T) { runCase(t, false) })
	t.Run("live proof during dispatch", func(t *testing.T) { runCase(t, true) })
}

func TestNativeSequenceStartsAtZero(t *testing.T) {
	s, f := openTest(t)
	ch := submitAsync(s)
	<-f.started

	f.ev(0, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: "receipt", Role: "user", Content: []native.ContentBlock{{Type: "text", Text: "hello"}}, Source: source("user")}}})
	f.prompts <- promptReply{id: "receipt"}
	f.ev(1, "turn/start", native.TurnStart{Turn: 1})
	f.ev(2, "step/start", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(3, "user/message", native.UserMessage{ID: "receipt", Role: "user", Content: []native.ContentBlock{{Type: "text", Text: "hello"}}, Source: source("user")})
	var got struct {
		r   protocol.MessageSubmitResponse
		st  base.EventStream
		err error
	}
	select {
	case got = <-ch:
	case <-time.After(time.Second):
		t.Fatal("admission timed out")
	}
	if got.err != nil {
		t.Fatalf("seq=0 admission failed: %v", got.err)
	}
	if got.r.Admission != protocol.AdmissionStarted {
		t.Fatalf("admission = %+v", got.r)
	}
	f.ev(4, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "hi"}}, native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}, `[]`, nil))
	f.ev(5, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
	f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	events := drain(t, got.st)
	adaptertest.AssertRunTrace(t, got.r, CapabilityRevision, events)
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s payload=%s", events[len(events)-1].Type, events[len(events)-1].Payload)
	}
}

func TestNativeSequenceRegressionStillRejected(t *testing.T) {
	s, f := openTest(t)
	a, st := admission(t, s, f, "receipt")

	f.ev(4, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "hi"}}, native.MessageSource{Kind: "model", Provider: "p", Model: "m"}, `[]`, nil))
	f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	events := drain(t, st)
	adaptertest.AssertRunTrace(t, a, CapabilityRevision, events)
	if events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("regressed sequence produced %s", events[len(events)-1].Type)
	}
}

func assertFailedWith(t *testing.T, events []protocol.Envelope, code, message string) {
	t.Helper()
	for _, e := range events {
		if e.Type != protocol.TypeRunFailed {
			continue
		}
		var p protocol.RunFailedPayload
		if err := e.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if p.Error.Code != code || p.Error.Message != message {
			t.Fatalf("failure=%q/%q want %q/%q", p.Error.Code, p.Error.Message, code, message)
		}
		return
	}
	var got []protocol.EnvelopeType
	for _, e := range events {
		got = append(got, e.Type)
	}
	t.Fatalf("no run.failed among %v", got)
}

func failingTurn(t *testing.T, drive func(f *fakeClient)) []protocol.Envelope {
	t.Helper()
	s, f := openTest(t)
	_, st := admission(t, s, f, "receipt")
	drive(f)
	return drain(t, st)
}

func TestSessionEventForAnotherSessionIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SessionEventNotification{SessionID: "someone-else", Event: event(5, "step/end", native.StepBoundary{Turn: 1, Step: 1})})
	})
	assertFailedWith(t, events, "deepseek_session_mismatch", "session.event for foreign session")
}

func TestSessionStatusForAnotherSessionIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SessionStatusNotification{SessionID: "someone-else", Status: "idle"})
	})
	assertFailedWith(t, events, "deepseek_session_mismatch", "session.status for foreign session")
}

func TestSubagentOfAnotherParentIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SubagentStartedNotification{ParentSessionID: "someone-else", ChildSessionID: "child-1"})
	})
	assertFailedWith(t, events, "deepseek_external_activity", "foreign or recursive subagent")
}

func TestSubagentThatIsItsOwnParentIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "session"})
	})
	assertFailedWith(t, events, "deepseek_external_activity", "foreign or recursive subagent")
}

func TestRepeatedSubagentStartIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "child-1"})
		f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "child-1"})
	})
	assertFailedWith(t, events, "deepseek_child_lifecycle", "duplicate child start")
}

func TestSubagentFinishFromAnotherParentIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SubagentFinishedNotification{ParentSessionID: "someone-else", ChildSessionID: "child-1", Status: "ok"})
	})
	assertFailedWith(t, events, "deepseek_external_activity", "foreign child finish")
}

func TestSubagentFinishWithoutItsStartIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SubagentFinishedNotification{ParentSessionID: "session", ChildSessionID: "never-started", Status: "ok"})
	})
	assertFailedWith(t, events, "deepseek_child_lifecycle", "unmatched child finish")
}

func TestSubagentThatFailedFailsTheParent(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "child-1"})
		f.ev(5, "step/end", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(6, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
		f.notify(&native.SubagentFinishedNotification{ParentSessionID: "session", ChildSessionID: "child-1", Status: "error"})
		f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	})
	assertFailedWith(t, events, "deepseek_child_failed", "subagent failed")
}

func TestATurnStartNamingAnotherTurnIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "turn/start", native.TurnStart{Turn: 9})
	})
	assertFailedWith(t, events, "deepseek_invalid_grammar", "overlapping foreign turn")
}

func TestAStepStartThatDoesNotAdvanceIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "step/start", native.StepBoundary{Turn: 1, Step: 1})
	})
	assertFailedWith(t, events, "deepseek_invalid_grammar", "invalid step start")
}

func TestAStepEndNamingAnotherStepIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "step/end", native.StepBoundary{Turn: 1, Step: 9})
	})
	assertFailedWith(t, events, "deepseek_invalid_grammar", "invalid step end")
}

func TestASecondTurnEndIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "step/end", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(6, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
		f.ev(7, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
	})
	assertFailedWith(t, events, "deepseek_invalid_grammar", "invalid turn end")
}

func TestAnEventTheGrammarDoesNotKnowIsRefusedByName(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "invented/later", struct{}{})
	})
	assertFailedWith(t, events, "deepseek_unknown_event", `unknown required event "invented/later"`)
}

func TestRepeatedToolCallIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		call := native.ToolCall{Turn: 1, Step: 1, CallID: "call-1", Name: "read", Arguments: `{}`}
		f.ev(5, "tool/call", call)
		f.ev(6, "tool/call", call)
	})
	assertFailedWith(t, events, "deepseek_tool_lifecycle", "duplicate tool call")
}

func TestToolFailureCarriesTheReasonRatherThanTheErrorClass(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "tool/call", native.ToolCall{Turn: 1, Step: 1, CallID: "call-1", Name: "read", Arguments: `{}`})
		f.ev(6, "tool/result", native.ToolResult{Turn: 1, Step: 1, Error: &native.ToolError{Name: "ToolError", Code: "ENOENT", Reason: json.RawMessage(`"a.txt is not there"`)}, Message: native.UserMessage{Source: native.MessageSource{Kind: "tool", CallID: "call-1"}}})
		f.ev(7, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "done"}}, native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}, `[]`, nil))
		f.ev(8, "step/end", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(9, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
		f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	})
	var failure *protocol.ProtocolError
	for _, e := range events {
		if e.Type != protocol.TypeActionCallFailed {
			continue
		}
		var p protocol.ActionCallPayload
		if err := e.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		failure = p.Error
	}
	if failure == nil {
		t.Fatal("no action.call.failed in the trace")
	}
	if failure.Message != "a.txt is not there" {
		t.Fatalf("message = %q, want the user-facing reason", failure.Message)
	}
	if failure.Code != "ENOENT" {
		t.Fatalf("code = %q", failure.Code)
	}
	if failure.Details["name"] != "ToolError" {
		t.Fatalf("details = %+v, want the error class kept beside the reason", failure.Details)
	}
}

func TestToolFailureFallsBackToTheErrorClassWithoutAReason(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "tool/call", native.ToolCall{Turn: 1, Step: 1, CallID: "call-1", Name: "read", Arguments: `{}`})
		f.ev(6, "tool/result", native.ToolResult{Turn: 1, Step: 1, Error: &native.ToolError{Name: "ToolError", Code: "ENOENT"}, Message: native.UserMessage{Source: native.MessageSource{Kind: "tool", CallID: "call-1"}}})
		f.ev(7, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "done"}}, native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}, `[]`, nil))
		f.ev(8, "step/end", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(9, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
		f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	})
	for _, e := range events {
		if e.Type != protocol.TypeActionCallFailed {
			continue
		}
		var p protocol.ActionCallPayload
		if err := e.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if p.Error == nil || p.Error.Message != "ToolError" {
			t.Fatalf("error = %+v, want the class as the fallback", p.Error)
		}
		if p.Error.Details != nil {
			t.Fatalf("details = %+v, want none when the class is already the message", p.Error.Details)
		}
		return
	}
	t.Fatal("no action.call.failed in the trace")
}

func TestToolResultWithoutItsCallIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "tool/result", native.ToolResult{Turn: 1, Step: 1, Message: native.UserMessage{Source: native.MessageSource{Kind: "tool", CallID: "never-called"}}})
	})
	assertFailedWith(t, events, "deepseek_tool_lifecycle", "unmatched tool result")
}

func TestEventOutsideTheOpenStepIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "tool/call", native.ToolCall{Turn: 9, Step: 1, CallID: "call-1", Name: "read", Arguments: `{}`})
	})
	assertFailedWith(t, events, "deepseek_invalid_grammar", "event outside open owned step")
}

func TestFinalMessageNamingAnUnknownToolIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "tool-call", ID: "never-called", Name: "read"}}, native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}, `[]`, nil))
		f.ev(6, "step/end", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(7, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
		f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	})
	assertFailedWith(t, events, "deepseek_invalid_final_message", `final message references unknown tool "never-called"`)
}

func TestNotificationTheAdapterDoesNotKnowIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		n := rpc.NotificationMessage{Method: "session/invented-later"}
		f.in <- rpc.InboundMessage{Notification: &n}
	})
	assertFailedWith(t, events, "deepseek_unknown_notification", "unknown notification")
}

func TestPromptRefusedAtStartAbortsWithoutRunEvents(t *testing.T) {
	s, f := openTest(t)
	refusal := errors.New("harness refused the prompt")
	ch := submitAsync(s)
	<-f.started
	f.prompts <- promptReply{startErr: refusal}
	got := <-ch
	if !errors.Is(got.err, refusal) {
		t.Fatalf("Submit err=%v, want the start refusal", got.err)
	}
	if events := drain(t, got.st); len(events) != 0 {
		t.Fatalf("a run that never started emitted %d envelopes", len(events))
	}
}

func TestReverseRequestIsExternalActivity(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		r := rpc.IncomingRequest{}
		f.in <- rpc.InboundMessage{Request: &r}
	})
	assertFailedWith(t, events, "deepseek_external_activity", "reverse request")
}

func TestDuplicateChildStartBeforeAdmissionIsRefusedOnReplay(t *testing.T) {
	s, f := openTest(t)
	ch := submitAsync(s)
	<-f.started
	f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "child"})
	f.notify(&native.SubagentStartedNotification{ParentSessionID: "session", ChildSessionID: "child"})
	bar := make(chan struct{})
	f.in <- rpc.InboundMessage{Barrier: bar}
	<-bar
	f.prompts <- promptReply{id: "receipt"}
	f.ev(1, "agent/inbox/spliced", native.InboxSpliced{Target: "next-turn", Start: 0, Inserted: []native.UserMessage{{ID: "receipt", Role: "user", Content: []native.ContentBlock{}, Source: source("user")}}})
	f.ev(2, "turn/start", native.TurnStart{Turn: 1})
	f.ev(3, "step/start", native.StepBoundary{Turn: 1, Step: 1})
	f.ev(4, "user/message", native.UserMessage{ID: "receipt", Role: "user", Content: []native.ContentBlock{}, Source: source("user")})
	got := <-ch
	if got.err != nil {
		t.Fatalf("admission failed: %v", got.err)
	}
	assertFailedWith(t, drain(t, got.st), "deepseek_child_lifecycle", "invalid buffered child start")
}

func TestCompletedTurnWithNoAssistantMessageIsRefused(t *testing.T) {
	events := failingTurn(t, func(f *fakeClient) {
		f.ev(5, "step/end", native.StepBoundary{Turn: 1, Step: 1})
		f.ev(6, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
		f.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	})
	assertFailedWith(t, events, "deepseek_missing_final_message", "completed turn omitted assistant message")
}

func descriptorFromProbe(t *testing.T) base.Descriptor {
	t.Helper()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return newFake(), "deepseek-chat", nil })})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func turnEndingWithAnOpenTool(t *testing.T, endKind string) []protocol.Envelope {
	t.Helper()
	descriptor := descriptorFromProbe(t)
	session, client := openTest(t)
	admitted, stream := admission(t, session, client, "receipt")
	client.ev(5, "tool/call", native.ToolCall{Turn: 1, Step: 1, CallID: "call-1", Name: "read", Arguments: `{}`})
	client.ev(6, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "done"}}, native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}, `[]`, nil))
	client.ev(7, "step/end", native.StepBoundary{Turn: 1, Step: 1})
	client.ev(8, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"` + endKind + `"}`)})
	client.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	events := drain(t, stream)
	adaptertest.AssertProtocolValidWithDescriptor(t, admitted, descriptor, events)
	return events
}

func assertToolSettledBeforeTerminal(t *testing.T, events []protocol.Envelope, terminal protocol.EnvelopeType) {
	t.Helper()
	settled, ended := -1, -1
	for i, envelope := range events {
		switch envelope.Type {
		case protocol.TypeActionCallFailed:
			if settled >= 0 {
				t.Fatal("the open tool was settled twice")
			}
			settled = i
			var payload protocol.ActionCallPayload
			if err := envelope.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error == nil || payload.Error.Code != "incomplete_tool" {
				t.Fatalf("settlement error = %+v", payload.Error)
			}
			if payload.Error.Message != "turn settled with an unfinished deepseek tool" {
				t.Fatalf("settlement message = %q", payload.Error.Message)
			}
		case terminal:
			ended = i
		}
	}
	if settled < 0 {
		t.Fatal("the open tool was never settled")
	}
	if ended < 0 {
		t.Fatalf("the run never reached %s", terminal)
	}
	if settled > ended {
		t.Fatalf("the tool settled after the terminal: settled=%d terminal=%d", settled, ended)
	}
}

func TestACompletedTurnSettlesItsOpenToolBeforeItsTerminal(t *testing.T) {
	assertToolSettledBeforeTerminal(t, turnEndingWithAnOpenTool(t, "completed"), protocol.TypeRunCompleted)
}

func TestAFailedTurnSettlesItsOpenToolBeforeItsTerminal(t *testing.T) {
	assertToolSettledBeforeTerminal(t, turnEndingWithAnOpenTool(t, "aborted"), protocol.TypeRunFailed)
}

func TestAToolThatFinishedIsNotSettledAgainAtTheTerminal(t *testing.T) {
	descriptor := descriptorFromProbe(t)
	session, client := openTest(t)
	admitted, stream := admission(t, session, client, "receipt")
	client.ev(5, "tool/call", native.ToolCall{Turn: 1, Step: 1, CallID: "call-1", Name: "read", Arguments: `{}`})
	client.ev(6, "tool/result", native.ToolResult{Turn: 1, Step: 1, Message: native.UserMessage{ID: "m", Role: "user", Source: native.MessageSource{Kind: "tool", CallID: "call-1"}, Content: []native.ContentBlock{{Type: "tool-result", ToolCallID: "call-1", Content: []native.ContentBlock{{Type: "text", Text: "ok"}}}}}})
	client.ev(7, "assistant/message", assistantMessage(1, 1, "a", []native.ContentBlock{{Type: "text", Text: "done"}}, native.MessageSource{Kind: "model", Provider: "deepseek", Model: "chat"}, `[]`, nil))
	client.ev(8, "step/end", native.StepBoundary{Turn: 1, Step: 1})
	client.ev(9, "turn/end", native.TurnEnd{Turn: 1, Reason: json.RawMessage(`{"kind":"completed"}`)})
	client.notify(&native.SessionStatusNotification{SessionID: "session", Status: "idle"})
	events := drain(t, stream)
	for _, envelope := range events {
		if envelope.Type == protocol.TypeActionCallFailed {
			t.Fatal("a tool that reported its result was settled as unfinished")
		}
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, admitted, descriptor, events)
}

func TestARunFailedOutsideSettlementStillSettlesItsOpenTool(t *testing.T) {
	descriptor := descriptorFromProbe(t)
	session, client := openTest(t)
	admitted, stream := admission(t, session, client, "receipt")
	call := native.ToolCall{Turn: 1, Step: 1, CallID: "call-1", Name: "read", Arguments: `{}`}
	client.ev(5, "tool/call", call)
	client.ev(6, "tool/call", call)
	events := drain(t, stream)
	assertFailedWith(t, events, "deepseek_tool_lifecycle", "duplicate tool call")
	assertToolSettledBeforeTerminal(t, events, protocol.TypeRunFailed)
	adaptertest.AssertProtocolValidWithDescriptor(t, admitted, descriptor, events)
}

func TestSettlingARunsToolsLeavesAnotherRunsAlone(t *testing.T) {
	reducer := openTestSession(t)
	mine := &runState{id: "run-mine", started: true}
	theirs := &runState{id: "run-theirs", started: true}
	ours := reducer.openToolFor(mine, "a")
	stranger := reducer.openToolFor(theirs, "b")

	reducer.settleOpenTools(mine)

	if !ours.terminal {
		t.Fatal("the run's own open tool was not settled")
	}
	if stranger.terminal {
		t.Fatal("a tool belonging to another run was settled")
	}
}

func openTestSession(t *testing.T) *Session {
	t.Helper()
	session, _ := openTest(t)
	return session.(*Session)
}

func (s *Session) openToolFor(run *runState, nativeID string) *toolState {
	tool := &toolState{nativeID: nativeID, id: protocol.ToolCallID("tool-call-" + nativeID), run: run, name: "read"}
	s.tools[toolKey(run, nativeID)] = tool
	return tool
}
