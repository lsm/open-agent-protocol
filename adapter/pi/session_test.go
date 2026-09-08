package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/pi/internal/rpc"
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
	return fmt.Sprintf("%s-%d", kind, g.n)
}

type fakeClient struct {
	mu        sync.Mutex
	inbound   chan rpc.Inbound
	done      chan struct{}
	onCall    func(native.Command)
	calls     []native.Command
	responses []native.ExtensionUIResponse
	state     native.SessionState
	err       error
	closed    bool
}

func newFakeClient() *fakeClient {
	return &fakeClient{inbound: make(chan rpc.Inbound, 256), done: make(chan struct{}), state: validState(false)}
}
func (f *fakeClient) Call(_ context.Context, c native.Command, result any) error {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	cb := f.onCall
	state := f.state
	err := f.err
	f.mu.Unlock()
	if cb != nil {
		cb(c)
	}
	if err != nil {
		return err
	}
	if result != nil {
		raw, ok := result.(*json.RawMessage)
		if !ok {
			return errors.New("expected raw result")
		}
		*raw, _ = json.Marshal(state)
	}
	return nil
}
func (f *fakeClient) Respond(_ context.Context, r native.ExtensionUIResponse) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses = append(f.responses, r)
	return f.err
}
func (f *fakeClient) Inbound() <-chan rpc.Inbound { return f.inbound }
func (f *fakeClient) Done() <-chan struct{}       { return f.done }
func (f *fakeClient) Err() error                  { f.mu.Lock(); defer f.mu.Unlock(); return f.err }
func (f *fakeClient) Close() error                { f.mu.Lock(); defer f.mu.Unlock(); f.closed = true; return nil }
func (f *fakeClient) emit(t *testing.T, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var header eventHeader
	if err := json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	f.inbound <- rpc.Inbound{Event: &native.Event{Type: header.Type, Raw: raw}}
}
func (f *fakeClient) extension(r native.ExtensionUIRequest) {
	f.inbound <- rpc.Inbound{ExtensionRequest: &r}
}
func validState(streaming bool) native.SessionState {
	return native.SessionState{SessionID: "native-session", ThinkingLevel: native.ThinkingMedium, SteeringMode: native.QueueAll, FollowUpMode: native.QueueAll, IsStreaming: streaming}
}

func openTest(t *testing.T, client *fakeClient, capacity int) *Session {
	t.Helper()
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) { return client, client.state, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	got, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	return got.(*Session)
}
func submitTest(t *testing.T, s *Session) (protocol.MessageSubmitResponse, base.EventStream) {
	t.Helper()
	response, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err != nil {
		t.Fatal(err)
	}
	return response, stream
}
func eventTypes(events []protocol.Envelope) []protocol.EnvelopeType {
	out := make([]protocol.EnvelopeType, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}
func assistant(text, reason string) map[string]any {
	return map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}, "api": "messages", "provider": "fake", "model": "m", "usage": map[string]any{"input": 1, "output": 2}, "stopReason": reason, "timestamp": 1}
}
func TestProbeIsConservative(t *testing.T) {
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) {
		return newFakeClient(), validState(false), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	d, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Capabilities.Features["session.message.delivery.queue"].Level != protocol.SupportUnavailable || d.Capabilities.Features["session.message.delivery.steer"].Level != protocol.SupportUnavailable || d.Capabilities.Features["action.permissions"].Level != protocol.SupportUnavailable {
		t.Fatalf("descriptor overclaims: %+v", d.Capabilities.Features)
	}
	if d.CapabilityRevision != CapabilityRevision || d.MaxActiveRunsPerSession != 1 || !d.InteractiveGates {
		t.Fatalf("descriptor=%+v", d)
	}
}

func TestPromptAdmissionDoesNotSynthesizeStartAndPreservesEarlyEvents(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt {
			client.emit(t, map[string]any{"type": "agent_start"})
			client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "early"}})
		}
	}
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	events := []protocol.Envelope{adaptertest.Next(t, stream, time.Second), adaptertest.Next(t, stream, time.Second)}
	if fmt.Sprint(eventTypes(events)) != fmt.Sprint([]protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta}) {
		t.Fatalf("events=%v", eventTypes(events))
	}
	if response.Admission != protocol.AdmissionStarted || response.EffectiveDelivery != protocol.DeliveryStart {
		t.Fatalf("response=%+v", response)
	}
	client.mu.Lock()
	command := client.calls[0]
	client.mu.Unlock()
	if command.Type != native.CommandPrompt || command.StreamingBehavior != native.StreamingSteer {
		t.Fatalf("command=%+v", command)
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("early", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, stream, time.Second)
}

func TestPromptSuccessBeforeAgentStartEmitsNothing(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	select {
	case got := <-stream:
		t.Fatalf("premature=%s", got.Envelope.Type)
	case <-time.After(20 * time.Millisecond):
	}
	client.emit(t, map[string]any{"type": "agent_start"})
	if got := adaptertest.Next(t, stream, time.Second); got.Type != protocol.TypeRunStarted {
		t.Fatalf("got=%s", got.Type)
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("ok", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, stream, time.Second)
}

func TestFinalMessageAuthoritativeAndRetryEndNonterminal(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": "why"}})
	client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 1, "delta": "draft"}})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("ignored", "error")}, "willRetry": true})
	select {
	case e := <-stream:
		if e.Envelope.Type == protocol.TypeRunCompleted || e.Envelope.Type == protocol.TypeRunFailed {
			t.Fatal("retry candidate settled")
		}
	default:
	}
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("final", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("final", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunCompleted {
		t.Fatalf("events=%v", eventTypes(events))
	}
	var p protocol.RunCompletedPayload
	if err := last.DecodePayload(&p); err != nil {
		t.Fatal(err)
	}
	parts, ok := p.FinalResponse.Content.Parts()
	if !ok || len(parts) != 1 || parts[0].Text != "final" {
		t.Fatalf("final=%s", p.FinalResponse.Content)
	}
}

func TestToolsKeyedByIDAndSettleBeforeParent(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "a", "toolName": "read", "args": map[string]any{"path": "a"}})
	client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "b", "toolName": "read", "args": map[string]any{"path": "b"}})
	client.emit(t, map[string]any{"type": "tool_execution_update", "toolCallId": "a", "toolName": "read", "args": map[string]any{"path": "a"}, "partialResult": map[string]any{"text": "half"}})
	client.emit(t, map[string]any{"type": "tool_execution_end", "toolCallId": "b", "toolName": "read", "result": map[string]any{"text": "b"}, "isError": false})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	types := eventTypes(events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeRunCompleted}
	if fmt.Sprint(types) != fmt.Sprint(want) {
		t.Fatalf("types=%v", types)
	}
	if events[1].ToolCallID == "a" || events[3].ToolCallID == "b" || events[1].ToolCallID == events[3].ToolCallID {
		t.Fatalf("mapped ids=%s,%s", events[1].ToolCallID, events[3].ToolCallID)
	}
}

func TestExtensionConfirmIsGenericInput(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	_ = adaptertest.Next(t, stream, time.Second)
	client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "ui-1", Method: native.ExtensionConfirm, Title: "Proceed?", Message: "Continue"})
	requested := adaptertest.Next(t, stream, time.Second)
	status := adaptertest.Next(t, stream, time.Second)
	if requested.Type != protocol.TypeUserInputRequested || status.Type != protocol.TypeRunStatusUpdated {
		t.Fatalf("types=%s,%s", requested.Type, status.Type)
	}
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	err := s.Resolve(context.Background(), base.InteractionResolution{RunID: response.RunID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session", RunID: response.RunID, Answers: []protocol.InputAnswer{{QuestionID: "value", SelectedOptionIDs: []string{"yes"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if adaptertest.Next(t, stream, time.Second).Type != protocol.TypeUserInputResolved {
		t.Fatal("missing resolution")
	}
	client.mu.Lock()
	nativeResponse := client.responses[0]
	client.mu.Unlock()
	if nativeResponse.Confirmed == nil || !*nativeResponse.Confirmed {
		t.Fatalf("response=%+v", nativeResponse)
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("ok", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, stream, time.Second)
}

func TestAbortIntentNaturalCompletionCanWin(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	_ = adaptertest.Next(t, stream, time.Second)
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandAbort {
			client.emit(t, map[string]any{"type": "message_end", "message": assistant("naturally done", "stop")})
			client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("naturally done", "stop")}, "willRetry": false})
			client.emit(t, map[string]any{"type": "agent_settled"})
		}
	}
	cancel, err := s.Cancel(context.Background(), response.RunID)
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("events=%v cancel=%+v", eventTypes(events), cancel)
	}
}

func TestAbortSettlementCancelsWhenNoNaturalCandidate(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	_ = adaptertest.Next(t, stream, time.Second)
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandAbort {
			client.emit(t, map[string]any{"type": "agent_settled"})
		}
	}
	cancel, err := s.Cancel(context.Background(), response.RunID)
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if events[len(events)-1].Type != protocol.TypeRunCancelled || (cancel.Status != protocol.RunCancelled && cancel.Status != protocol.RunCancelling) {
		t.Fatalf("events=%v cancel=%+v", eventTypes(events), cancel)
	}
}

func TestProcessExitFailsActiveOnce(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	client.mu.Lock()
	client.err = errors.New("exit 9")
	client.mu.Unlock()
	close(client.done)
	events := adaptertest.Drain(t, stream, time.Second)
	terminals := 0
	for _, e := range events {
		if e.Type == protocol.TypeRunFailed || e.Type == protocol.TypeRunCompleted || e.Type == protocol.TypeRunCancelled {
			terminals++
		}
	}
	if terminals != 1 || events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", eventTypes(events))
	}
	_ = s
}

func TestReplayGapAndOverflow(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 2)
	response, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "a"}})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("a", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, stream, time.Second)
	_, gapStream, err := s.Resume(context.Background(), base.ResumeRequest{RunID: response.RunID, AfterSequence: 0})
	var gap *base.ReplayGap
	if !errors.As(err, &gap) {
		t.Fatalf("gap=%v", err)
	}
	if _, ok := <-gapStream; ok {
		t.Fatal("gap stream open")
	}
	recovery, replay, err := s.Resume(context.Background(), base.ResumeRequest{RunID: response.RunID, AfterSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, replay, time.Second)
	if recovery.ReplayedThrough != 3 || len(events) != 2 || events[1].Type != protocol.TypeRunCompleted {
		t.Fatalf("recovery=%+v events=%v", recovery, eventTypes(events))
	}
}

func TestPinnedMessageUpdateShapesAreDiscriminated(t *testing.T) {
	text, emit, err := decodeProviderEvent(json.RawMessage(`{"type":"text_delta","contentIndex":0,"delta":"x"}`))
	if err != nil || !emit || text.Type != protocol.ContentText || text.Text != "x" {
		t.Fatalf("text=%+v emit=%v err=%v", text, emit, err)
	}
	if _, _, err := decodeProviderEvent(json.RawMessage(`{"type":"text_delta","contentIndex":0,"delta":"x","partial":{}}`)); err == nil {
		t.Fatal("cross-variant member accepted")
	}
	if _, _, err := decodeProviderEvent(json.RawMessage(`{"type":"future_delta","contentIndex":0,"delta":"x"}`)); err == nil {
		t.Fatal("unknown variant accepted")
	}
	if _, emit, err := decodeProviderEvent(json.RawMessage(`{"type":"toolcall_delta","contentIndex":2,"delta":"{}","partial":{"type":"toolCall","id":"call","name":"read","arguments":{}}}`)); err != nil || emit {
		t.Fatalf("tool delta emit=%v err=%v", emit, err)
	}
}

func TestPreStartObservationsBufferBehindRunStarted(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt {
			client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 0, "delta": "early"}})
			client.emit(t, map[string]any{"type": "agent_start"})
		}
	}
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	first, second := adaptertest.Next(t, stream, time.Second), adaptertest.Next(t, stream, time.Second)
	if first.Type != protocol.TypeRunStarted || second.Type != protocol.TypeContentDelta {
		t.Fatalf("types=%s,%s", first.Type, second.Type)
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("early", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, stream, time.Second)
}

func TestInitialStreamingRejected(t *testing.T) {
	client := newFakeClient()
	client.state = validState(true)
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) { return client, client.state, nil })})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), base.OpenRequest{}); !errors.Is(err, ErrNativeProtocol) {
		t.Fatalf("open err=%v", err)
	}
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if !closed {
		t.Fatal("client not closed")
	}
}

func TestAbortedFinalWithIntentCancels(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	_ = adaptertest.Next(t, stream, time.Second)
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandAbort {
			client.emit(t, map[string]any{"type": "message_end", "message": assistant("", "aborted")})
			client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("", "aborted")}, "willRetry": false})
			client.emit(t, map[string]any{"type": "agent_settled"})
		}
	}
	if _, err := s.Cancel(context.Background(), response.RunID); err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if events[len(events)-1].Type != protocol.TypeRunCancelled {
		t.Fatalf("events=%v", eventTypes(events))
	}
}

func TestFinalToolCallUsesMappedIdentity(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "agent_start"})
	client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "native", "toolName": "read", "args": map[string]any{"path": "x"}})
	client.emit(t, map[string]any{"type": "tool_execution_end", "toolCallId": "native", "toolName": "read", "result": map[string]any{"text": "x"}, "isError": false})
	final := assistant("done", "stop")
	final["content"] = []any{map[string]any{"type": "toolCall", "id": "native", "name": "read", "arguments": map[string]any{"path": "x"}}, map[string]any{"type": "text", "text": "done"}}
	client.emit(t, map[string]any{"type": "message_end", "message": final})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{final}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	var done protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&done); err != nil {
		t.Fatal(err)
	}
	parts, ok := done.FinalResponse.Content.Parts()
	if !ok || len(parts) != 2 || parts[0].ToolCallID == "native" || parts[0].ToolCallID == "" {
		t.Fatalf("parts=%+v", parts)
	}
}

func TestStateStrictReconciliationAndDeliveryRejection(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	if _, _, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryQueue, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}}}); !errors.Is(err, base.ErrInvalidSubmission) {
		t.Fatalf("queue err=%v", err)
	}
	state, err := s.State(context.Background())
	if err != nil || state.SessionID != "session" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	client.mu.Lock()
	client.state.SessionID = "other"
	client.mu.Unlock()
	if _, err := s.State(context.Background()); !errors.Is(err, ErrNativeProtocol) {
		t.Fatalf("foreign state err=%v", err)
	}
}
