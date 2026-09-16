package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
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

func (i *fakeIDs) NewID(kind string) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.n++
	return kind + "-" + string(rune('a'+i.n-1))
}

type promptOutcome struct {
	result native.PromptResult
	err    error
}
type fakeClient struct {
	mu            sync.Mutex
	notifications chan rpc.NotificationMessage
	requests      chan *rpc.IncomingRequest
	inbound       chan rpc.InboundMessage
	done          chan struct{}
	promptStarted chan struct{}
	prompt        chan promptOutcome
	notifies      []string
	notifyErr     error
	closed        bool
	sessionNew    native.SessionNewParams
}

func newFake() *fakeClient {
	return &fakeClient{notifications: make(chan rpc.NotificationMessage, 32), requests: make(chan *rpc.IncomingRequest, 8), inbound: make(chan rpc.InboundMessage, 40), done: make(chan struct{}), promptStarted: make(chan struct{}, 1), prompt: make(chan promptOutcome, 4)}
}
func (f *fakeClient) Call(_ context.Context, m string, p, r any) error {
	switch m {
	case native.MethodSessionNew:
		f.mu.Lock()
		f.sessionNew, _ = p.(native.SessionNewParams)
		f.mu.Unlock()
		*r.(*native.SessionNewResult) = native.SessionNewResult{SessionID: "native-session"}
		return nil
	case native.MethodSessionPrompt:
		f.promptStarted <- struct{}{}
		o := <-f.prompt
		if o.err == nil {
			*r.(*native.PromptResult) = o.result
		}
		return o.err
	default:
		return errors.New("unexpected call")
	}
}
func (f *fakeClient) CallStarted(ctx context.Context, m string, p, r any, started chan<- error) error {
	started <- nil
	close(started)
	return f.Call(ctx, m, p, r)
}
func (f *fakeClient) Notify(_ context.Context, m string, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notifyErr != nil {
		return f.notifyErr
	}
	f.notifies = append(f.notifies, m)
	return nil
}
func (f *fakeClient) Requests() <-chan *rpc.IncomingRequest         { return f.requests }
func (f *fakeClient) Notifications() <-chan rpc.NotificationMessage { return f.notifications }
func (f *fakeClient) Inbound() <-chan rpc.InboundMessage            { return f.inbound }
func (f *fakeClient) Done() <-chan struct{}                         { return f.done }
func (f *fakeClient) Err() error                                    { return errors.New("EOF") }
func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		close(f.done)
		f.closed = true
	}
	return nil
}
func (f *fakeClient) update(t *testing.T, value any) {
	t.Helper()
	u, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	p, err := json.Marshal(native.SessionUpdateParams{SessionID: "native-session", Update: u})
	if err != nil {
		t.Fatal(err)
	}
	n := rpc.NotificationMessage{Method: native.MethodSessionUpdate, Params: p}
	f.inbound <- rpc.InboundMessage{Notification: &n}
}
func openTest(t *testing.T, capacity int) (base.Session, *fakeClient) {
	return openTestSession(t, capacity, "session")
}
func openTestSession(t *testing.T, capacity int, sessionID protocol.SessionID) (base.Session, *fakeClient) {
	t.Helper()
	f := newFake()
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return f, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
	}), WorkingDirectory: "/workspace", Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Open(context.Background(), base.OpenRequest{SessionID: sessionID, Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

// The participant is the recorded responder for every permission gate; an empty
// identity would emit schema-invalid events that no valid resolution could
// satisfy, so the open must be refused before any process is started.
func TestOpenRejectsEmptyParticipant(t *testing.T) {
	f := newFake()
	started := 0
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		started++
		return f, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
	}), WorkingDirectory: "/workspace", Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session"}); !errors.Is(err, base.ErrInvalidParticipant) {
		t.Fatalf("got %v, want ErrInvalidParticipant", err)
	}
	if started != 0 {
		t.Fatalf("factory started for an invalid request: %d", started)
	}
}
func submit(t *testing.T, s base.Session) (protocol.MessageSubmitResponse, base.EventStream) {
	t.Helper()
	r, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err != nil {
		t.Fatal(err)
	}
	return r, stream
}
func waitCursor(t *testing.T, s base.Session, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state, err := s.State(context.Background())
		if err == nil && state.TranscriptCursor == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("transcript cursor did not reach %s", want)
}

func collect(t *testing.T, stream base.EventStream) []protocol.Envelope {
	t.Helper()
	var out []protocol.Envelope
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case v, ok := <-stream:
			if !ok {
				return out
			}
			if v.Error != nil {
				t.Fatal(v.Error)
			}
			out = append(out, v.Envelope)
		case <-timer.C:
			t.Fatal("stream did not close")
		}
	}
}

func testDescriptor(t *testing.T) base.Descriptor {
	t.Helper()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return nil, rpc.InitializeResponse{}, errors.New("probe only")
	}), WorkingDirectory: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

// assertValidTrace runs the shared protocol assertion with the adapter's live
// descriptor. assertCancelledTrace is the variant for runs the test itself
// cancelled: it splices the harness-side exchange the cancellation implies.
func assertValidTrace(t *testing.T, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, testDescriptor(t), events)
}

func assertCancelledTrace(t *testing.T, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	adaptertest.AssertProtocolValidWithCancellation(t, admission, testDescriptor(t), events)
}

func TestCompletedPromptMapsChunksToolsAndSequence(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "hello "}, MessageID: "m1"})
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "world"}, MessageID: "m1"})
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Status: "pending", RawInput: json.RawMessage(`{"path":"x"}`)})
	status := "in_progress"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status, Content: json.RawMessage(`[{"type":"content","content":{"type":"text","text":"working"}}]`)})
	status = "completed"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status, RawOutput: json.RawMessage(`{"ok":true}`)})
	waitCursor(t, s, "6")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeContentDelta, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeRunCompleted}
	if len(events) != len(want) {
		t.Fatalf("events=%v", types(events))
	}
	for i, e := range events {
		if e.Type != want[i] {
			t.Fatalf("event %d=%s want %s", i, e.Type, want[i])
		}
		if e.Sequence == nil || *e.Sequence != uint64(i+1) {
			t.Fatalf("sequence %d: %v", i, e.Sequence)
		}
		if e.RunID != admission.RunID {
			t.Fatalf("run identity changed")
		}
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if text, _ := completed.FinalResponse.Content.Text(); text != "hello world" {
		t.Fatalf("final text=%q", text)
	}
	var lastChunk protocol.ContentDeltaPayload
	if err := events[2].DecodePayload(&lastChunk); err != nil {
		t.Fatal(err)
	}
	if completed.FinalResponse.ID != lastChunk.MessageID {
		t.Fatalf("final message id %q != streamed id %q", completed.FinalResponse.ID, lastChunk.MessageID)
	}
	assertValidTrace(t, admission, events)
	state, err := s.State(context.Background())
	if err != nil || state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestOneActivePromptAndCancellationRaces(t *testing.T) {
	tests := []struct {
		name, stop string
		want       protocol.EnvelopeType
	}{{"confirmed", "cancelled", protocol.TypeRunCancelled}, {"completion wins", "end_turn", protocol.TypeRunCompleted}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, f := openTest(t, 64)
			admission, stream := submit(t, s)
			<-f.promptStarted
			if _, _, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("second")}}}); !errors.Is(err, base.ErrRunActive) {
				t.Fatalf("second prompt err=%v", err)
			}
			ack, err := s.Cancel(context.Background(), admission.RunID)
			if err != nil || ack.Status != protocol.RunCancelling {
				t.Fatalf("cancel=%+v %v", ack, err)
			}
			f.prompt <- promptOutcome{result: native.PromptResult{StopReason: tc.stop}}
			events := collect(t, stream)
			if events[len(events)-1].Type != tc.want {
				t.Fatalf("terminal=%s", events[len(events)-1].Type)
			}
			if tc.want == protocol.TypeRunCancelled {
				assertCancelledTrace(t, admission, events)
			} else {
				assertValidTrace(t, admission, events)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.notifies) != 1 || f.notifies[0] != native.MethodSessionCancel {
				t.Fatalf("notifications=%v", f.notifies)
			}
		})
	}
}
func TestArbitraryPostCancelErrorIsFailure(t *testing.T) {
	s, f := openTest(t, 64)
	a, stream := submit(t, s)
	<-f.promptStarted
	if _, err := s.Cancel(context.Background(), a.RunID); err != nil {
		t.Fatal(err)
	}
	f.prompt <- promptOutcome{err: errors.New("network error")}
	events := collect(t, stream)
	if got := events[len(events)-1].Type; got != protocol.TypeRunFailed {
		t.Fatalf("terminal=%s", got)
	}
	assertValidTrace(t, a, events)
}
func TestTransportFailureSettlesRunOnce(t *testing.T) {
	s, f := openTest(t, 64)
	_, stream := submit(t, s)
	<-f.promptStarted
	close(f.done)
	events := collect(t, stream)
	if got := events[len(events)-1].Type; got != protocol.TypeRunFailed {
		t.Fatalf("terminal=%s", got)
	}
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	time.Sleep(10 * time.Millisecond)
	state, _ := s.State(context.Background())
	if state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}
}
func TestResumeReplayAndGap(t *testing.T) {
	s, f := openTest(t, 2)
	a, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "a"}})
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "b"}})
	waitCursor(t, s, "3")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	if len(events) != 4 {
		t.Fatalf("events=%v", types(events))
	}
	recovery, replay, err := s.Resume(context.Background(), base.ResumeRequest{RunID: a.RunID, AfterSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	replayed := collect(t, replay)
	if len(replayed) != 2 || recovery.ReplayedFrom != 3 || recovery.ReplayedThrough != 4 {
		t.Fatalf("recovery=%+v replay=%v", recovery, types(replayed))
	}
	recovery, replay, err = s.Resume(context.Background(), base.ResumeRequest{RunID: a.RunID, AfterSequence: 0})
	var gap *base.ReplayGap
	if !errors.As(err, &gap) || recovery.ReplayGap == nil {
		t.Fatalf("gap recovery=%+v err=%v", recovery, err)
	}
	if _, ok := <-replay; ok {
		t.Fatal("gap stream should close")
	}
}
func TestCancelWriteFailureRemainsRetriable(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.mu.Lock()
	f.notifyErr = errors.New("not written")
	f.mu.Unlock()
	if _, err := s.Cancel(context.Background(), admission.RunID); err == nil {
		t.Fatal("expected notify failure")
	}
	f.mu.Lock()
	f.notifyErr = nil
	f.mu.Unlock()
	if ack, err := s.Cancel(context.Background(), admission.RunID); err != nil || ack.Status != protocol.RunCancelling {
		t.Fatalf("retry cancel=%+v err=%v", ack, err)
	}
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "cancelled"}}
	if events := collect(t, stream); events[len(events)-1].Type != protocol.TypeRunCancelled {
		t.Fatalf("events=%v", types(events))
	}
}

func TestSlowSubscriberDetachesWithOverflow(t *testing.T) {
	sessionAPI, f := openTest(t, 256)
	admission, stream := submit(t, sessionAPI)
	<-f.promptStarted
	s := sessionAPI.(*session)
	s.mu.Lock()
	run := s.runs[admission.RunID]
	s.mu.Unlock()
	for i := 0; i < streamCapacity+1; i++ {
		if err := s.emit(run, protocol.TypeContentDelta, protocol.ContentDeltaPayload{SessionID: "session", RunID: run.id, MessageID: run.messageID, Part: protocol.ContentPart{Type: protocol.ContentText, Text: "x"}}, false); err != nil {
			t.Fatal(err)
		}
	}
	var sequences []uint64
	var sawOverflow bool
	for result := range stream {
		if errors.Is(result.Error, base.ErrEventStreamOverflow) {
			sawOverflow = true
			continue
		}
		if result.Envelope.Sequence == nil {
			t.Fatal("event missing sequence")
		}
		sequences = append(sequences, *result.Envelope.Sequence)
	}
	if !sawOverflow {
		t.Fatal("slow stream closed without explicit overflow")
	}
	for i, sequence := range sequences {
		if sequence != uint64(i+1) {
			t.Fatalf("non-contiguous overflow prefix: %v", sequences)
		}
	}
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
}

func TestFirstExplicitMessageIDContinuesUnlabelledChunks(t *testing.T) {
	sessionAPI, f := openTest(t, 64)
	_, stream := submit(t, sessionAPI)
	<-f.promptStarted
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "hello "}})
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", MessageID: "m1", Content: native.ContentBlock{Type: "text", Text: "world"}})
	waitCursor(t, sessionAPI, "3")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	var first, second protocol.ContentDeltaPayload
	if err := events[1].DecodePayload(&first); err != nil {
		t.Fatal(err)
	}
	if err := events[2].DecodePayload(&second); err != nil {
		t.Fatal(err)
	}
	if first.MessageID != second.MessageID {
		t.Fatalf("unlabelled and first labelled chunks split: %q != %q", first.MessageID, second.MessageID)
	}
	var terminal protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&terminal); err != nil {
		t.Fatal(err)
	}
	text, ok := terminal.FinalResponse.Content.Text()
	if terminal.FinalResponse.ID != first.MessageID || !ok || text != "hello world" {
		t.Fatalf("final response=%+v", terminal.FinalResponse)
	}
}

func TestNativeMessageIDsArePortableAndSessionScoped(t *testing.T) {
	first, f1 := openTest(t, 64)
	_, stream1 := submit(t, first)
	<-f1.promptStarted
	f1.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", MessageID: "same", Content: native.ContentBlock{Type: "text", Text: "one"}})
	waitCursor(t, first, "2")
	f1.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events1 := collect(t, stream1)
	var one protocol.ContentDeltaPayload
	_ = events1[1].DecodePayload(&one)

	second, f2 := openTestSession(t, 64, "session-two")
	r2, stream2, err := second.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session-two", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err != nil || !r2.Accepted {
		t.Fatalf("second submit=%+v err=%v", r2, err)
	}
	<-f2.promptStarted
	f2.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", MessageID: "same", Content: native.ContentBlock{Type: "text", Text: "two"}})
	waitCursor(t, second, "2")
	f2.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events2 := collect(t, stream2)
	var two protocol.ContentDeltaPayload
	_ = events2[1].DecodePayload(&two)
	if one.MessageID == two.MessageID {
		t.Fatalf("native id leaked across sessions: %q", one.MessageID)
	}
	var terminal protocol.RunCompletedPayload
	_ = events2[len(events2)-1].DecodePayload(&terminal)
	if terminal.FinalResponse.ID != two.MessageID {
		t.Fatalf("terminal id=%q chunk id=%q", terminal.FinalResponse.ID, two.MessageID)
	}
}

func TestUnsupportedInputHasNoPromptSideEffect(t *testing.T) {
	s, f := openTest(t, 64)
	_, _, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleAssistant, Content: protocol.TextContent("bad")}}})
	if !errors.Is(err, ErrUnsupportedInput) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-f.promptStarted:
		t.Fatal("prompt started")
	default:
	}
}
func TestProbeIsConservative(t *testing.T) {
	s, _ := openTest(t, 64)
	_ = s
	f := newFake()
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) { return f, rpc.InitializeResponse{}, nil }), WorkingDirectory: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"filesystem", "terminal", "models.list", "action.tools.list"} {
		if _, ok := d.Capabilities.Features[feature]; ok {
			t.Fatalf("unexpected capability %s", feature)
		}
	}
	if d.MaxActiveRunsPerSession != 1 || d.Journal.Persistence != "process_memory" {
		t.Fatalf("descriptor=%+v", d)
	}
}

// A sparse tool patch (no status) that arrives while the call is still pending
// must not emit action.call.progress: no started event has occurred, and the
// progress payload requires a progress member. The patch is retained and
// surfaces on action.call.started.
func TestSparseToolUpdateBeforeStartEmitsNoProgress(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Status: "pending"})
	title := "Read v2"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Title: &title})
	status := "in_progress"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status})
	status = "completed"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status, RawOutput: json.RawMessage(`{"ok":true}`)})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
	var started protocol.ActionCallPayload
	if err := events[2].DecodePayload(&started); err != nil {
		t.Fatal(err)
	}
	if started.Name != "Read v2" {
		t.Fatalf("started name = %q, want the retained patch", started.Name)
	}
	assertValidTrace(t, admission, events)
}

// ACP may omit rawInput; the requested event must still carry arguments_json,
// normalized to the JSON null value.
func TestToolCallWithoutInputCarriesNullArguments(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Status: "pending"})
	status := "in_progress"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status})
	status = "completed"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status, RawOutput: json.RawMessage(`{"ok":true}`)})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	observed := false
	for _, envelope := range events {
		if envelope.Type != protocol.TypeActionCallRequested {
			continue
		}
		observed = true
		var requested protocol.ActionCallPayload
		if err := envelope.DecodePayload(&requested); err != nil {
			t.Fatal(err)
		}
		if string(requested.ArgumentsJSON) != "null" {
			t.Fatalf("arguments_json = %q, want null", requested.ArgumentsJSON)
		}
	}
	if !observed {
		t.Fatal("no action.call.requested observed")
	}
	assertValidTrace(t, admission, events)
}

// A native snapshot may first report a tool terminal. The adapter must still
// emit an action.call.started boundary, which the validator requires.
func TestToolTerminalWithoutProgressSynthesizesStart(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Status: "completed", RawInput: json.RawMessage(`{"path":"x"}`), RawOutput: json.RawMessage(`{"ok":true}`)})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
	assertValidTrace(t, admission, events)
}

// action.call.completed requires result; a completion without native output is
// normalized to the JSON null value.
func TestToolCompletionWithoutOutputCarriesNullResult(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Status: "pending"})
	status := "in_progress"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status})
	status = "completed"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	observed := false
	for _, envelope := range events {
		if envelope.Type != protocol.TypeActionCallCompleted {
			continue
		}
		observed = true
		var completed protocol.ActionCallPayload
		if err := envelope.DecodePayload(&completed); err != nil {
			t.Fatal(err)
		}
		if string(completed.Result) != "null" {
			t.Fatalf("result = %q, want null", completed.Result)
		}
	}
	if !observed {
		t.Fatal("no action.call.completed observed")
	}
	assertValidTrace(t, admission, events)
}

// A permission request whose tool is rejected (empty title) must settle the run
// without dereferencing the missing tool entry.
// A sparse patch that arrives while a tool is executing emits progress; the
// callProgress schema forbids the request-only and terminal-only members, so
// the projection must strip them even though the retained tool state carries
// both the input and any output observed so far.
func TestSparsePatchInProgressEmitsBareProgress(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Status: "pending", RawInput: json.RawMessage(`{"path":"x"}`), RawOutput: json.RawMessage(`{"early":true}`)})
	status := "in_progress"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Status: &status})
	title := "Read v2"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Title: &title})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	var progress *protocol.ActionCallPayload
	for index, envelope := range events {
		if envelope.Type == protocol.TypeActionCallProgress {
			if progress != nil {
				t.Fatal("more than one progress event")
			}
			var payload protocol.ActionCallPayload
			if err := envelope.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			progress = &payload
			_ = index
		}
	}
	if progress == nil {
		t.Fatalf("no progress event: %v", types(events))
	}
	if progress.ArgumentsJSON != nil || progress.Result != nil || progress.Error != nil {
		t.Fatalf("progress carried forbidden members: %+v", progress)
	}
	assertValidTrace(t, admission, events)
}

func TestPermissionRequestWithMalformedToolSettles(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	params, err := json.Marshal(native.PermissionRequest{SessionID: "native-session", ToolCall: native.ToolCall{ToolCallID: "call-1"}, Options: []native.PermissionOption{{OptionID: "allow", Name: "Allow", Kind: "allow_once"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.inbound <- rpc.InboundMessage{Request: corpusIncomingRequest(t, rpc.Request(rpc.StringID("perm-1"), native.MethodSessionRequestPermission, params))}
	events := adaptertest.Drain(t, stream, 2*time.Second)
	surfaced := false
	for _, envelope := range events {
		if envelope.Type == protocol.TypeActionPermissionRequested {
			surfaced = true
		}
	}
	if surfaced {
		t.Fatal("malformed tool surfaced a permission interaction")
	}
	assertValidTrace(t, admission, events)
}

func types(events []protocol.Envelope) []protocol.EnvelopeType {
	out := make([]protocol.EnvelopeType, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

// None of the four run controls has a native mapping in ACP's session/prompt,
// and this adapter performs no session configuration mutation. Each must be
// refused under its own capability key with the typed unsupported-control
// error, so the caller learns which control to stop sending; a generic invalid
// submission names none of them, and echoing a requested model back as
// effective would attribute the run to a model the agent never used.
func TestSubmitRefusesEveryUnadvertisedControl(t *testing.T) {
	s, f := openTest(t, 64)
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hi")}}
	for feature, request := range map[string]protocol.MessageSubmitRequest{
		protocol.FeatureInstructions:     {SessionID: "session", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse"), Messages: message},
		protocol.FeatureModelSelection:   {SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("another-model"), Messages: message},
		protocol.FeatureStructuredOutput: {SessionID: "session", Delivery: protocol.DeliveryAuto, OutputSchema: json.RawMessage(`{"type":"object"}`), Messages: message},
		protocol.FeatureToolSelection:    {SessionID: "session", Delivery: protocol.DeliveryAuto, ToolChoice: json.RawMessage(`"none"`), Messages: message},
	} {
		_, _, err := s.Submit(context.Background(), request)
		var refusal *base.UnsupportedControlError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: got %v, want an *adapter.UnsupportedControlError", feature, err)
		}
		if refusal.Feature != feature || refusal.Reason != base.ControlUnadvertised {
			t.Fatalf("%s: refused as %q/%q", feature, refusal.Feature, refusal.Reason)
		}
		if !errors.Is(err, base.ErrUnsupportedInput) {
			t.Fatalf("%s: the refusal does not unwrap to the shared sentinel: %v", feature, err)
		}
	}
	// The ladder ranks a capability refusal above ordinary validation, so the
	// gate runs first: a request that is malformed and carries an unadvertised
	// control is answered with the control, the same ordering every adapter
	// here uses. Answering "invalid submission" would send the caller round
	// again to be refused for a control it was never told about.
	for name, request := range map[string]protocol.MessageSubmitRequest{
		"no messages":      {SessionID: "session", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse")},
		"no session":       {Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse"), Messages: message},
		"unsupported mode": {SessionID: "session", Delivery: protocol.DeliveryQueue, Instructions: protocol.ControlValue("be terse"), Messages: message},
	} {
		_, _, err := s.Submit(context.Background(), request)
		var refusal *base.UnsupportedControlError
		if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureInstructions {
			t.Fatalf("%s: got %v, want the unadvertised control named ahead of the ordinary refusal", name, err)
		}
	}

	// Every refusal precedes admission, so no run was reserved: a clean
	// submission still runs and still validates as a complete protocol trace.
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "hi"}, MessageID: "m1"})
	waitCursor(t, s, "2")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	assertValidTrace(t, admission, collect(t, stream))
}

// A completed prompt write is authoritative even when the caller's context
// expires at the same instant: the run start boundary must not be discarded in
// favour of the cancellation arm.
func TestAwaitAdmissionPrefersCompletedWrite(t *testing.T) {
	for i := 0; i < 200; i++ {
		started := make(chan error, 1)
		started <- nil
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err, done := awaitAdmission(started, ctx)
		if !done || err != nil {
			t.Fatalf("iteration %d: completed write discarded (done=%v err=%v)", i, done, err)
		}
	}
}

// The pending permission was emitted with requester "agent"; a resolution that
// names a different requester is ownership-inconsistent and must not resolve
// the native gate.
func TestPermissionResolveRejectsForeignRequester(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	params, err := json.Marshal(native.PermissionRequest{SessionID: "native-session", ToolCall: native.ToolCall{ToolCallID: "call-1", Title: "Act"}, Options: []native.PermissionOption{{OptionID: "allow", Name: "Allow", Kind: "allow_once"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.inbound <- rpc.InboundMessage{Request: corpusIncomingRequest(t, rpc.Request(rpc.StringID("perm-1"), native.MethodSessionRequestPermission, params))}
	var requested protocol.PermissionRequestedPayload
	for requested.InteractionID == "" {
		envelope := adaptertest.Next(t, stream, 2*time.Second)
		if envelope.Type == protocol.TypeActionPermissionRequested {
			if err := envelope.DecodePayload(&requested); err != nil {
				t.Fatal(err)
			}
		}
	}
	resolution := base.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, RequestedBy: "intruder", RespondedBy: "user", SessionID: admission.SessionID, RunID: admission.RunID, ChoiceID: "allow", Granted: true}}
	if err := s.Resolve(context.Background(), resolution); !errors.Is(err, base.ErrInvalidResolution) {
		t.Fatalf("foreign requester: got %v, want ErrInvalidResolution", err)
	}
	resolution.Permission.RequestedBy = "agent"
	if err := s.Resolve(context.Background(), resolution); err != nil {
		t.Fatalf("valid requester rejected after foreign one: %v", err)
	}
}

// ACP v1 declares session/new's mcpServers as a required array. A nil Go slice
// marshals to null, which the official client SDK's own param validator rejects
// ("mcpServers is required") with -32602 Invalid params; the real docker/cagent
// server did exactly that. Assert the encoded wire shape, not the Go value.
func TestSessionNewSendsRequiredMCPServersArray(t *testing.T) {
	_, f := openTest(t, 64)
	f.mu.Lock()
	params := f.sessionNew
	f.mu.Unlock()
	if params.MCPServers == nil {
		t.Fatal("session/new sent a nil mcpServers slice; ACP requires the field as an array")
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"mcpServers":[]`) {
		t.Fatalf("session/new params did not encode mcpServers as an empty array: %s", encoded)
	}
}

// ACP defines more stable session updates than carry run lifecycle. A
// conforming agent may interleave the presentation-affordance variants with an
// active run; they must be observed-only, not fatal. docker/cagent emits
// available_commands_update immediately after session/prompt is written.
func TestDefinedNonLifecycleUpdatesAreObservedOnly(t *testing.T) {
	updates := []map[string]any{
		{"sessionUpdate": "available_commands_update", "availableCommands": []any{}},
		{"sessionUpdate": "session_info_update", "title": "fixture"},
		{"sessionUpdate": "plan", "entries": []any{}},
		{"sessionUpdate": "plan_update", "entries": []any{}},
		{"sessionUpdate": "plan_removed", "ids": []any{}},
		{"sessionUpdate": "current_mode_update", "currentModeId": "default"},
		{"sessionUpdate": "config_option_update", "options": []any{}},
		{"sessionUpdate": "usage_update", "size": 0, "used": 0},
		{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "thinking"}},
		{"sessionUpdate": "user_message_chunk", "content": map[string]any{"type": "text", "text": "echo"}},
	}
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	for _, update := range updates {
		f.update(t, update)
	}
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "hello"}})
	// Only run.started and one content delta are OAP-visible, so reaching cursor
	// 2 proves every observed-only update was already reduced.
	waitCursor(t, s, "2")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if len(events) != len(want) {
		t.Fatalf("events=%v", types(events))
	}
	for i, e := range events {
		if e.Type != want[i] {
			t.Fatalf("event %d=%s want %s", i, e.Type, want[i])
		}
		if e.RunID != admission.RunID {
			t.Fatal("run identity changed")
		}
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if text, _ := completed.FinalResponse.Content.Text(); text != "hello" {
		t.Fatalf("final text=%q", text)
	}
}

// A consumer may resolve a permission gate the instant it observes the gate on
// its stream, and the resolution correlates to the gate's request event through
// in_reply_to. The correlation must therefore exist before the gate is visible:
// recording it after publication let a fast Resolve read an empty correlation
// and emit an uncorrelated action.permission.resolved, the
// tool-lifecycle-permission corpus flake in issue #10.
func TestPermissionGateIsCorrelatedWhenPublished(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	params, err := json.Marshal(native.PermissionRequest{SessionID: "native-session", ToolCall: native.ToolCall{ToolCallID: "call-1", Title: "Act"}, Options: []native.PermissionOption{{OptionID: "allow", Name: "Allow", Kind: "allow_once"}}})
	if err != nil {
		t.Fatal(err)
	}
	f.inbound <- rpc.InboundMessage{Request: corpusIncomingRequest(t, rpc.Request(rpc.StringID("perm-1"), native.MethodSessionRequestPermission, params))}
	var events []protocol.Envelope
	var gate protocol.Envelope
	var requested protocol.PermissionRequestedPayload
	for requested.InteractionID == "" {
		gate = adaptertest.Next(t, stream, 2*time.Second)
		events = append(events, gate)
		if gate.Type == protocol.TypeActionPermissionRequested {
			if err := gate.DecodePayload(&requested); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The correlation is recorded by the time the gate can be observed.
	impl := s.(*session)
	impl.mu.Lock()
	recorded := impl.interactions[requested.InteractionID].requestEventID
	impl.mu.Unlock()
	if recorded != gate.ID {
		t.Fatalf("gate observed with request event id %q, want %q", recorded, gate.ID)
	}
	// Resolving immediately carries it.
	if err := s.Resolve(context.Background(), base.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: admission.SessionID, RunID: admission.RunID, ChoiceID: "allow", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	resolved := adaptertest.Next(t, stream, 2*time.Second)
	events = append(events, resolved)
	if resolved.Type != protocol.TypeActionPermissionResolved || resolved.InReplyTo != gate.ID {
		t.Fatalf("got %s in reply to %q, want action.permission.resolved in reply to %q", resolved.Type, resolved.InReplyTo, gate.ID)
	}
	status := "completed"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Status: &status})
	waitCursor(t, s, "6")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events = append(events, collect(t, stream)...)
	assertValidTrace(t, admission, events)
}
