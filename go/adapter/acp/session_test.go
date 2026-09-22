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

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
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

	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "text", Text: "hi"}, MessageID: "m1"})
	waitCursor(t, s, "2")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	assertValidTrace(t, admission, collect(t, stream))
}

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
	resolution.Permission.RequestedBy = endpointID
	if err := s.Resolve(context.Background(), resolution); err != nil {
		t.Fatalf("valid requester rejected after foreign one: %v", err)
	}
}

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

	impl := s.(*session)
	impl.mu.Lock()
	recorded := impl.interactions[requested.InteractionID].requestEventID
	impl.mu.Unlock()
	if recorded != gate.ID {
		t.Fatalf("gate observed with request event id %q, want %q", recorded, gate.ID)
	}

	if err := s.Resolve(context.Background(), base.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: admission.SessionID, RunID: admission.RunID, ChoiceID: "allow", Granted: true}}); err != nil {
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

func countingFactory(f *fakeClient, started *int) ClientFactoryFunc {
	return func(context.Context) (Client, rpc.InitializeResponse, error) {
		*started++
		return f, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
	}
}

func TestAttachRefusesACollidingSourceID(t *testing.T) {
	files := protocol.ToolSourceAttachment{ID: "files", Kind: protocol.ToolSourceProcess, Command: "/usr/local/bin/mcp-filesystem"}
	for _, testCase := range []struct {
		name       string
		configured []native.MCPServer
		attach     []protocol.ToolSourceAttachment
	}{
		{"a configured server", []native.MCPServer{{Name: "files", Command: "/opt/mcp-files"}}, []protocol.ToolSourceAttachment{files}},
		{"another attachment", nil, []protocol.ToolSourceAttachment{files, files}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := 0
			a, err := New(Config{
				Factory: countingFactory(newFake(), &started), WorkingDirectory: "/workspace",
				Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32, MCPServers: testCase.configured,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = a.Open(context.Background(), base.OpenRequest{
				SessionID: "session", Participant: protocol.Participant{ID: "user"}, ToolSources: testCase.attach,
			})
			var refusal *base.UnsupportedControlError
			if !errors.As(err, &refusal) {
				t.Fatalf("got %v, want an UnsupportedControlError", err)
			}
			if refusal.Feature != protocol.FeatureToolSourcesAttach || refusal.Reason != base.ControlUnsatisfiable || refusal.Source != "files" {
				t.Fatalf("refusal = %+v", refusal)
			}
			if started != 0 {
				t.Fatalf("a refused attachment started %d children", started)
			}
		})
	}
}

func TestAttachmentIsAdmittedBeforeTheChildStarts(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		attach protocol.ToolSourceAttachment
	}{
		{"an unsupported transport", protocol.ToolSourceAttachment{ID: "hosted-tools", Kind: protocol.ToolSourceRemote, Endpoint: "https://tools.example"}},
		{"a process source with no command", protocol.ToolSourceAttachment{ID: "files", Kind: protocol.ToolSourceProcess}},
		{"a source with no id", protocol.ToolSourceAttachment{Kind: protocol.ToolSourceProcess, Command: "/usr/local/bin/mcp-filesystem"}},

		{"one environment variable named twice", protocol.ToolSourceAttachment{
			ID: "files", Kind: protocol.ToolSourceProcess, Command: "/usr/local/bin/mcp-filesystem",
			Environment: []string{"TOKEN=first", "TOKEN=second"},
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			started := 0
			a, err := New(Config{
				Factory: countingFactory(newFake(), &started), WorkingDirectory: "/workspace",
				Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32,
			})
			if err != nil {
				t.Fatal(err)
			}
			session, err := a.Open(context.Background(), base.OpenRequest{
				SessionID: "session", Participant: protocol.Participant{ID: "user"},
				ToolSources: []protocol.ToolSourceAttachment{testCase.attach},
			})
			if err == nil {
				_ = session.Close(context.Background())
				t.Fatal("the open was admitted")
			}
			if started != 0 {
				t.Fatalf("a refused attachment started %d children", started)
			}
		})
	}
}

func TestConfiguredServersAreDeclaredBeforeTheyAreReserved(t *testing.T) {
	configured := []native.MCPServer{{Name: "files", Command: "/opt/mcp-files"}}
	a, err := New(Config{
		Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
			return newFake(), rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
		}),
		WorkingDirectory: "/workspace", Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32,
		MCPServers: configured,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	declared := descriptor.Capabilities.Sources
	if len(declared) != 1 || declared[0].ID != "files" || declared[0].Kind != protocol.ToolSourceProcess {
		t.Fatalf("descriptor declares %+v, want the configured server as a process source", declared)
	}

	if declared[0].Endpoint != "" {
		t.Fatalf("descriptor invented an endpoint for a configured server: %+v", declared[0])
	}

	session, err := a.Open(context.Background(), base.OpenRequest{
		SessionID: "session", Participant: protocol.Participant{ID: "user"},
		ToolSources: []protocol.ToolSourceAttachment{{ID: "docs", Kind: protocol.ToolSourceProcess, Command: "/usr/local/bin/mcp-docs"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	published := map[string]bool{}
	for _, source := range state.Sources {
		published[source.ID] = true
	}
	if !published["files"] || !published["docs"] {
		t.Fatalf("session publishes %+v, want the configured server beside the attachment", state.Sources)
	}
}

func TestNewRefusesAnUnusableConfiguredServer(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		configured []native.MCPServer
		want       string
	}{
		{"no name", []native.MCPServer{{Command: "/opt/mcp-files"}}, "needs a name"},
		{"one name twice", []native.MCPServer{{Name: "files", Command: "/opt/a"}, {Name: "files", Command: "/opt/b"}}, "configured twice"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := New(Config{
				Factory: countingFactory(newFake(), new(int)), WorkingDirectory: "/workspace",
				Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32, MCPServers: testCase.configured,
			})
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("New error = %v, want one reporting %q", err, testCase.want)
			}
		})
	}
}

func TestAttachRefusesASourceWithNoID(t *testing.T) {
	started := 0
	a, err := New(Config{
		Factory: countingFactory(newFake(), &started), WorkingDirectory: "/workspace",
		Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := a.Open(context.Background(), base.OpenRequest{
		SessionID: "session", Participant: protocol.Participant{ID: "user"},
		ToolSources: []protocol.ToolSourceAttachment{{Kind: protocol.ToolSourceProcess, Command: "/usr/local/bin/mcp-filesystem"}},
	})
	if err == nil {
		_ = session.Close(context.Background())
		t.Fatal("an attachment with no id was admitted")
	}
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) {
		t.Fatalf("got %v, want an UnsupportedControlError", err)
	}
	if refusal.Feature != protocol.FeatureToolSourcesAttach || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("refusal = %+v", refusal)
	}
	if started != 0 {
		t.Fatalf("a refused attachment started %d children", started)
	}
}

func TestAdmittedAttachmentReachesSessionNew(t *testing.T) {
	f := newFake()
	started := 0
	a, err := New(Config{
		Factory: countingFactory(f, &started), WorkingDirectory: "/workspace",
		Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), base.OpenRequest{
		SessionID: "session", Participant: protocol.Participant{ID: "user"},
		ToolSources: []protocol.ToolSourceAttachment{{ID: "files", Kind: protocol.ToolSourceProcess, Command: "/usr/local/bin/mcp-filesystem", Args: []string{"--root", "/workspace"}}},
	}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	params := f.sessionNew
	f.mu.Unlock()
	if started != 1 {
		t.Fatalf("an admitted open started %d children", started)
	}
	if len(params.MCPServers) != 1 || params.MCPServers[0].Name != "files" || params.MCPServers[0].Command != "/usr/local/bin/mcp-filesystem" {
		t.Fatalf("session/new carried %+v", params.MCPServers)
	}
}

func (f *fakeClient) rawUpdate(t *testing.T, sessionID string, update string) {
	t.Helper()
	p, err := json.Marshal(native.SessionUpdateParams{SessionID: sessionID, Update: json.RawMessage(update)})
	if err != nil {
		t.Fatal(err)
	}
	n := rpc.NotificationMessage{Method: native.MethodSessionUpdate, Params: p}
	f.inbound <- rpc.InboundMessage{Notification: &n}
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
	t.Fatalf("no run.failed among %v", types(events))
}

func failingPrompt(t *testing.T, drive func(t *testing.T, f *fakeClient)) []protocol.Envelope {
	t.Helper()
	s, f := openTest(t, 64)
	_, stream := submit(t, s)
	<-f.promptStarted
	drive(t, f)
	return adaptertest.Drain(t, stream, 2*time.Second)
}

func TestUpdateForAnotherSessionIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.rawUpdate(t, "someone-elses-session", `{"sessionUpdate":"agent_message_chunk"}`)
	})
	assertFailedWith(t, events, "acp_invalid_update", "malformed or foreign session/update")
}

func TestUpdateThatIsNotAnObjectIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.rawUpdate(t, "native-session", `7`)
	})
	assertFailedWith(t, events, "acp_invalid_update", "malformed session update")
}

func TestAssistantChunkThatIsNotTextIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.update(t, native.AgentMessageChunk{SessionUpdate: "agent_message_chunk", Content: native.ContentBlock{Type: "image"}})
	})
	assertFailedWith(t, events, "acp_invalid_message_chunk", "unsupported assistant chunk")
}

func TestMalformedToolCallIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.rawUpdate(t, "native-session", `{"sessionUpdate":"tool_call","toolCallId":7}`)
	})
	assertFailedWith(t, events, "acp_invalid_tool_call", "malformed tool call")
}

func TestToolCallMissingIdentityIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Status: "pending"})
	})
	assertFailedWith(t, events, "acp_invalid_tool_call", "tool id and title are required")
}

func TestMalformedToolUpdateIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.rawUpdate(t, "native-session", `{"sessionUpdate":"tool_call_update","toolCallId":7}`)
	})
	assertFailedWith(t, events, "acp_invalid_tool_update", "malformed tool update")
}

func TestToolPatchBeforeItsCallIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		status := "in_progress"
		f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "never-created", Status: &status})
	})
	assertFailedWith(t, events, "acp_tool_patch_without_call", "tool patch before creation")
}

func TestToolStatusTheProtocolDoesNotDefineIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "Read", Status: "pending"})
		status := "invented_later"
		f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Status: &status})
	})
	assertFailedWith(t, events, "acp_invalid_tool_status", "unknown tool status")
}

func TestToolPatchedAfterItSettledIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "Read", Status: "pending"})
		done := "completed"
		f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Status: &done})
		again := "in_progress"
		f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Status: &again})
	})
	assertFailedWith(t, events, "acp_tool_after_terminal", "tool updated after terminal")
}

func TestToolRecreatedAfterItSettledIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "Read", Status: "pending"})
		done := "completed"
		f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "call-1", Status: &done})
		f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "Read again", Status: "pending"})
	})
	assertFailedWith(t, events, "acp_tool_after_terminal", "tool updated after terminal")
}

func TestMalformedPermissionRequestIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		params, err := json.Marshal(native.PermissionRequest{SessionID: "native-session", ToolCall: native.ToolCall{Title: "Read"}, Options: []native.PermissionOption{{OptionID: "allow", Name: "Allow"}}})
		if err != nil {
			t.Fatal(err)
		}
		f.inbound <- rpc.InboundMessage{Request: corpusIncomingRequest(t, rpc.Request(rpc.StringID("perm-1"), native.MethodSessionRequestPermission, params))}
	})
	assertFailedWith(t, events, "acp_invalid_permission", "malformed permission request")
}

func TestPermissionOfferingNoUsableOptionIsRefused(t *testing.T) {
	events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
		params, err := json.Marshal(native.PermissionRequest{SessionID: "native-session", ToolCall: native.ToolCall{ToolCallID: "call-1", Title: "Read"}, Options: []native.PermissionOption{{OptionID: "", Name: "Nameless"}}})
		if err != nil {
			t.Fatal(err)
		}
		f.inbound <- rpc.InboundMessage{Request: corpusIncomingRequest(t, rpc.Request(rpc.StringID("perm-1"), native.MethodSessionRequestPermission, params))}
	})
	assertFailedWith(t, events, "acp_invalid_permission", "empty permission options")
}

func TestToolIdReusedByALaterPromptIsRefused(t *testing.T) {
	s, f := openTest(t, 64)
	_, first := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "Read", Status: "pending"})
	waitCursor(t, s, "2")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	adaptertest.Drain(t, first, 2*time.Second)

	_, second := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "Read", Status: "pending"})
	assertFailedWith(t, adaptertest.Drain(t, second, 2*time.Second), "acp_tool_id_reuse", "tool id reused across prompts")
}

func acpPermission(t *testing.T, s base.Session, f *fakeClient, admission protocol.MessageSubmitResponse, stream base.EventStream, options string) (protocol.PermissionRequestedPayload, []protocol.Envelope) {
	t.Helper()
	params := json.RawMessage(`{"sessionId":"native-session","toolCall":{"toolCallId":"call-1","title":"Act"},"options":` + options + `}`)
	f.inbound <- rpc.InboundMessage{Request: corpusIncomingRequest(t, rpc.Request(rpc.StringID("perm-1"), native.MethodSessionRequestPermission, params))}
	var requested protocol.PermissionRequestedPayload
	var seen []protocol.Envelope
	for requested.InteractionID == "" {
		envelope := adaptertest.Next(t, stream, 2*time.Second)
		seen = append(seen, envelope)
		if envelope.Type == protocol.TypeRunFailed {
			return requested, seen
		}
		if envelope.Type == protocol.TypeActionPermissionRequested {
			if err := envelope.DecodePayload(&requested); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = admission
	return requested, seen
}

func TestEmptyPatchedTitleKeepsTheCallNamed(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Read", Status: "pending"})
	empty := ""
	status := "completed"
	f.update(t, native.ToolCallUpdate{SessionUpdate: "tool_call_update", ToolCallID: "tool", Title: &empty, Status: &status, RawOutput: json.RawMessage(`{"ok":true}`)})
	waitCursor(t, s, "4")
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	events := collect(t, stream)
	assertValidTrace(t, admission, events)

	named := 0
	for _, e := range events {
		if e.Type != protocol.TypeActionCallStarted && e.Type != protocol.TypeActionCallRequested {
			continue
		}
		var payload protocol.ActionCallPayload
		if err := e.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Name != "Read" {
			t.Fatalf("%s name = %q, want Read", e.Type, payload.Name)
		}
		named++
	}
	if named != 2 {
		t.Fatalf("named envelopes = %d, want 2", named)
	}
}

func TestOnlyOptionsTheSchemaAndACPBothAcceptAreOffered(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	requested, prefix := acpPermission(t, s, f, admission, stream,
		`[{"optionId":"","name":"Nameless","kind":"allow_once"},{"optionId":"blank","name":"","kind":"allow_once"},{"optionId":"future","name":"Future","kind":"allow_for_this_repository"},{"optionId":"no","name":"Reject","kind":"reject_always"}]`)

	if len(requested.Choices) != 1 || requested.Choices[0].ID != "no" {
		t.Fatalf("choices = %+v, want only the reject_always option", requested.Choices)
	}
	reject := base.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: admission.SessionID, RunID: admission.RunID, ChoiceID: "future", Granted: false}}
	if err := s.Resolve(context.Background(), reject); !errors.Is(err, base.ErrInvalidResolution) {
		t.Fatalf("resolving an unoffered option: got %v, want ErrInvalidResolution", err)
	}
	reject.Permission.ChoiceID = "no"
	if err := s.Resolve(context.Background(), reject); err != nil {
		t.Fatalf("resolving the offered option: %v", err)
	}
	prefix = append(prefix, adaptertest.Next(t, stream, 2*time.Second))
	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	assertValidTrace(t, admission, append(prefix, collect(t, stream)...))
}

func TestARequestWithNoUsableOptionIsRefused(t *testing.T) {
	for _, options := range []string{
		`[{"optionId":"future","name":"Future","kind":"allow_for_this_repository"}]`,
		`[{"optionId":"blank","name":"","kind":"allow_once"}]`,
	} {
		events := failingPrompt(t, func(t *testing.T, f *fakeClient) {
			params := json.RawMessage(`{"sessionId":"native-session","toolCall":{"toolCallId":"call-1","title":"Act"},"options":` + options + `}`)
			f.inbound <- rpc.InboundMessage{Request: corpusIncomingRequest(t, rpc.Request(rpc.StringID("perm-1"), native.MethodSessionRequestPermission, params))}
		})
		assertFailedWith(t, events, "acp_invalid_permission", "empty permission options")
	}
}

func TestAToolThatNeverStartedIsStartedOnlyWhereTheValidatorNeedsIt(t *testing.T) {
	for _, settlement := range []struct {
		stop string
		want []protocol.EnvelopeType
	}{
		{"end_turn", []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallFailed, protocol.TypeRunCompleted}},
		{"cancelled", []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallCancelled, protocol.TypeRunCancelled}},
	} {
		s, f := openTest(t, 64)
		admission, stream := submit(t, s)
		<-f.promptStarted
		f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "tool", Title: "Act", Status: "pending"})
		waitCursor(t, s, "2")
		f.prompt <- promptOutcome{result: native.PromptResult{StopReason: settlement.stop}}
		events := collect(t, stream)
		if settlement.stop == "cancelled" {
			assertCancelledTrace(t, admission, events)
		} else {
			assertValidTrace(t, admission, events)
		}

		var kinds []protocol.EnvelopeType
		for _, e := range events {
			kinds = append(kinds, e.Type)
		}
		if len(kinds) != len(settlement.want) {
			t.Fatalf("%s produced %v, want %v", settlement.stop, kinds, settlement.want)
		}
		for i, want := range settlement.want {
			if kinds[i] != want {
				t.Fatalf("%s produced %v, want %v", settlement.stop, kinds, settlement.want)
			}
		}
	}
}

func TestSettlingChildrenOfATerminalRunClaimsNothing(t *testing.T) {
	s, f := openTest(t, 64)
	admission, stream := submit(t, s)
	<-f.promptStarted
	f.update(t, native.ToolCall{SessionUpdate: "tool_call", ToolCallID: "call-1", Title: "Read", Status: "pending"})
	waitCursor(t, s, "2")
	acpPermission(t, s, f, admission, stream, `[{"optionId":"allow","name":"Allow","kind":"allow_once"}]`)

	sess := s.(*session)
	sess.mu.Lock()
	run := sess.active
	sess.mu.Unlock()

	f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
	adaptertest.Drain(t, stream, 2*time.Second)

	sess.mu.Lock()
	if !run.terminal {
		sess.mu.Unlock()
		t.Fatal("the run did not terminate")
	}
	for _, tool := range sess.tools {
		if tool.run == run {
			tool.terminal = false
		}
	}
	gates := 0
	for _, gate := range sess.interactions {
		if gate.run == run {
			gate.resolved = false
			gates++
		}
	}
	sess.mu.Unlock()
	if gates == 0 {
		t.Fatal("the run carried no permission gate to settle")
	}

	sess.settleChildren(run, true)

	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, tool := range sess.tools {
		if tool.run != run {
			continue
		}
		if tool.terminal {
			t.Fatal("a tool was marked settled by a settlement nothing emitted")
		}
	}
	for _, gate := range sess.interactions {
		if gate.run != run {
			continue
		}
		if gate.resolved {
			t.Fatal("a gate was marked resolved by a settlement nothing emitted")
		}
	}
}
