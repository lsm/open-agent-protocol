package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/rpc"
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

func TestOpenRejectsEmptyParticipant(t *testing.T) {
	started := 0
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) {
		started++
		return newFakeClient(), validState(false), nil
	}), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 32})
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

func submitTest(t *testing.T, s *Session) (protocol.MessageSubmitResponse, base.EventStream) {
	t.Helper()
	s.client.(*fakeClient).mu.Lock()
	hook := s.client.(*fakeClient).onCall
	s.client.(*fakeClient).mu.Unlock()
	if hook == nil {
		s.client.(*fakeClient).mu.Lock()
		s.client.(*fakeClient).onCall = func(c native.Command) {
			if c.Type == native.CommandPrompt {
				s.client.(*fakeClient).emit(t, map[string]any{"type": "agent_start"})
			}
		}
		s.client.(*fakeClient).mu.Unlock()
	}
	response, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err != nil {
		t.Fatal(err)
	}
	return response, stream
}

func testDescriptor(t *testing.T) base.Descriptor {
	t.Helper()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) {
		return nil, native.SessionState{}, errors.New("probe only")
	}), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 64})
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

func TestSubmitRefusesEveryUnadvertisedControl(t *testing.T) {
	s := openTest(t, newFakeClient(), 32)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}
	for feature, request := range map[string]protocol.MessageSubmitRequest{
		protocol.FeatureInstructions:     {SessionID: "session", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse"), Messages: message},
		protocol.FeatureModelSelection:   {SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("glm-other"), Messages: message},
		protocol.FeatureStructuredOutput: {SessionID: "session", Delivery: protocol.DeliveryAuto, OutputSchema: json.RawMessage(`{"type":"object"}`), Messages: message},
		protocol.FeatureToolSelection:    {SessionID: "session", Delivery: protocol.DeliveryAuto, ToolChoice: json.RawMessage(`"none"`), Messages: message},
	} {
		_, _, err := s.Submit(ctx, request)
		var refusal *base.UnsupportedControlError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: got %v, want an *adapter.UnsupportedControlError", feature, err)
		}
		if refusal.Feature != feature || refusal.Reason != base.ControlUnadvertised {
			t.Fatalf("%s: refused as %q/%q", feature, refusal.Feature, refusal.Reason)
		}
	}
}

func TestRunAttributesNativeModel(t *testing.T) {
	client := newFakeClient()
	client.state.Model = json.RawMessage(`{"id":"claude-x","name":"Claude X","provider":"anthropic"}`)
	s := openTest(t, client, 32)
	response, _ := submitTest(t, s)
	if response.ModelID != "anthropic/claude-x" {
		t.Fatalf("admission model = %q, want %q", response.ModelID, "anthropic/claude-x")
	}
	state, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentModelID != "anthropic/claude-x" {
		t.Fatalf("state model = %q, want %q", state.CurrentModelID, "anthropic/claude-x")
	}
}

func eventTypes(events []protocol.Envelope) []protocol.EnvelopeType {
	out := make([]protocol.EnvelopeType, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}
func assistant(text, reason string) map[string]any {
	return map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}, "api": "messages", "provider": "fake", "model": "m", "usage": map[string]any{"input": 1, "output": 2, "cacheRead": 0, "cacheWrite": 0, "totalTokens": 3, "cost": map[string]any{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0, "total": 0}}, "stopReason": reason, "timestamp": 1}
}
func TestProductionProcessForcesExtensionsDisabled(t *testing.T) {
	var got rpc.ProcessConfig
	factory := ProcessFactoryFunc(func(_ context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
		got = config
		return nil, errors.New("stop after config capture")
	})
	a, err := New(Config{ProcessFactory: factory, Executable: "/usr/bin/pi", WorkingDirectory: "/tmp", Args: []string{"--no-extensions"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}}); err == nil {
		t.Fatal("open unexpectedly succeeded")
	}
	if len(got.Args) != 2 || got.Args[0] != "--no-extensions" || got.Args[1] != "--no-extensions" {
		t.Fatalf("args=%q", got.Args)
	}
	if _, err := New(Config{ProcessFactory: factory, Executable: "/usr/bin/pi", WorkingDirectory: "/tmp", Args: []string{"--extension", "plugin.ts"}}); err == nil {
		t.Fatal("explicit extension accepted")
	}
}

func TestProductionProcessKeepsEmptyEnvironmentNonNil(t *testing.T) {
	var got rpc.ProcessConfig
	factory := ProcessFactoryFunc(func(_ context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
		got = config
		return nil, errors.New("stop after config capture")
	})
	a, err := New(Config{ProcessFactory: factory, Executable: "/usr/bin/pi", WorkingDirectory: "/tmp", Environment: []string{}, Args: []string{"--no-extensions"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}}); err == nil {
		t.Fatal("open unexpectedly succeeded")
	}
	if got.Env == nil || len(got.Env) != 0 {
		t.Fatalf("empty environment did not stay non-nil: %#v", got.Env)
	}
}

func TestProductionProcessRejectsStandaloneFlagTerminatorWithoutSideEffects(t *testing.T) {
	starts := 0
	factory := ProcessFactoryFunc(func(context.Context, rpc.ProcessConfig) (ProcessBridge, error) {
		starts++
		return nil, errors.New("unexpected process start")
	})
	args := []string{"--model", "test", "--", "prompt"}
	wantArgs := append([]string(nil), args...)

	if _, err := New(Config{ProcessFactory: factory, Executable: "/usr/bin/pi", WorkingDirectory: "/tmp", Args: args}); err == nil {
		t.Fatal("standalone -- accepted")
	}
	if starts != 0 {
		t.Fatalf("process starts=%d", starts)
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args mutated: got %q want %q", args, wantArgs)
	}
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
	if d.CapabilityRevision != CapabilityRevision || d.MaxActiveRunsPerSession != 1 || d.InteractiveGates {
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
	settled := adaptertest.Drain(t, stream, time.Second)
	assertValidTrace(t, response, append(events, settled...))
}

func TestSlashCommandRejectedWithoutNativeSideEffect(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("/extension-command argument")}}})
	if !errors.Is(err, ErrUnsupportedInput) || stream != nil {
		t.Fatalf("stream=%v err=%v", stream, err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 0 {
		t.Fatalf("native calls=%+v", client.calls)
	}
}

func TestSubmitWaitsForDelayedAgentStart(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(native.Command) {}
	s := openTest(t, client, 32)
	type result struct {
		response protocol.MessageSubmitResponse
		stream   base.EventStream
		err      error
	}
	done := make(chan result, 1)
	go func() {
		r, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
		done <- result{r, stream, err}
	}()
	select {
	case got := <-done:
		t.Fatalf("submit returned before start: %+v", got)
	case <-time.After(20 * time.Millisecond):
	}
	client.emit(t, map[string]any{"type": "agent_start"})
	got := <-done
	if got.err != nil || got.response.Admission != protocol.AdmissionStarted {
		t.Fatalf("result=%+v", got)
	}
	if event := adaptertest.Next(t, got.stream, time.Second); event.Type != protocol.TypeRunStarted {
		t.Fatalf("event=%s", event.Type)
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("ok", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, got.stream, time.Second)
}

func TestCancelBeforeAgentStartPreservesCanonicalOrdering(t *testing.T) {
	client := newFakeClient()
	prompted := make(chan struct{})
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt {
			close(prompted)
		}
	}
	s := openTest(t, client, 32)
	type submitResult struct {
		response protocol.MessageSubmitResponse
		stream   base.EventStream
		err      error
	}
	done := make(chan submitResult, 1)
	go func() {
		response, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
		done <- submitResult{response: response, stream: stream, err: err}
	}()
	<-prompted
	state, err := s.State(context.Background())
	if err != nil || state.ActiveRunID == "" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	cancel, err := s.Cancel(context.Background(), state.ActiveRunID)
	if err != nil || cancel.Status != protocol.RunCancelling {
		t.Fatalf("cancel=%+v err=%v", cancel, err)
	}
	client.emit(t, map[string]any{"type": "agent_start"})
	result := <-done
	if result.err != nil || result.response.RunID != state.ActiveRunID {
		t.Fatalf("submit=%+v", result)
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("", "aborted")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, result.stream, time.Second)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeRunStatusUpdated, protocol.TypeRunCancelled}
	if got := eventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("events=%v want=%v", got, want)
	}
	for i, event := range events {
		if event.Sequence == nil || *event.Sequence != uint64(i+1) {
			t.Fatalf("event %d sequence=%v", i, event.Sequence)
		}
	}
	assertCancelledTrace(t, result.response, events)
}

func TestSubmitContextBeforeStartDoesNotMisreportAdmission(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(native.Command) {}
	s := openTest(t, client, 32)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	response, stream, err := s.Submit(ctx, protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if !errors.Is(err, context.DeadlineExceeded) || response.Accepted {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	client.emit(t, map[string]any{"type": "agent_start"})
	if event := adaptertest.Next(t, stream, time.Second); event.Type != protocol.TypeRunStarted {
		t.Fatalf("event=%s", event.Type)
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("ok", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, stream, time.Second)
}

func TestSubmitTerminalBeforeStartReturnsErrorWithoutRunEvents(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt {
			client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("never", "stop")}, "willRetry": false})
			client.emit(t, map[string]any{"type": "agent_settled"})
		}
	}
	s := openTest(t, client, 32)
	response, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err == nil || response.Accepted {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, ok := <-stream; ok {
		t.Fatal("pre-start terminal emitted run event")
	}
}

func TestSubmitTransportFailureBeforeStartReturnsError(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt {
			client.mu.Lock()
			client.err = errors.New("process exited")
			client.mu.Unlock()
			close(client.done)
		}
	}
	s := openTest(t, client, 32)
	response, stream, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err == nil || response.Accepted {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	if _, ok := <-stream; ok {
		t.Fatal("pre-start transport failure emitted run event")
	}
}

func TestFinalMessageAuthoritativeAndRetryEndNonterminal(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "thinking_delta", "contentIndex": 0, "delta": "why"}})
	client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "text_delta", "contentIndex": 1, "delta": "draft"}})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("ignored", "error")}, "willRetry": true})
	var trace []protocol.Envelope
	select {
	case e := <-stream:
		if e.Envelope.Type == protocol.TypeRunCompleted || e.Envelope.Type == protocol.TypeRunFailed {
			t.Fatal("retry candidate settled")
		}
		trace = append(trace, e.Envelope)
	default:
	}
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("final", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("final", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := append(trace, adaptertest.Drain(t, stream, time.Second)...)
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
	assertValidTrace(t, admission, events)
}

func TestToolsKeyedByIDAndSettleBeforeParent(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
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
	assertValidTrace(t, admission, events)
}

func TestExtensionConfirmIsGenericInput(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
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
	err := s.Resolve(context.Background(), base.InteractionResolution{RunID: response.RunID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session", RunID: response.RunID, Answers: []protocol.InputAnswer{{QuestionID: "value", SelectedOptionIDs: []string{"yes"}}}}})
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

func TestExtensionInputRejectsMalformedTextAnswers(t *testing.T) {

	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	_ = adaptertest.Next(t, stream, time.Second)
	client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "ui-2", Method: native.ExtensionInput, Title: "Name"})
	requested := adaptertest.Next(t, stream, time.Second)
	if adaptertest.Next(t, stream, time.Second).Type != protocol.TypeRunStatusUpdated {
		t.Fatal("missing waiting status")
	}
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	resolve := func(a protocol.InputAnswer) error {
		return s.Resolve(context.Background(), base.InteractionResolution{RunID: response.RunID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session", RunID: response.RunID, Answers: []protocol.InputAnswer{a}}})
	}
	if err := resolve(protocol.InputAnswer{QuestionID: "value", Text: ""}); !errors.Is(err, base.ErrInvalidResolution) {
		t.Fatalf("empty text err = %v", err)
	}
	if err := resolve(protocol.InputAnswer{QuestionID: "value", SelectedOptionIDs: []string{"yes"}}); !errors.Is(err, base.ErrInvalidResolution) {
		t.Fatalf("choice-form text err = %v", err)
	}
	client.mu.Lock()
	wrote := len(client.responses)
	client.mu.Unlock()
	if wrote != 0 {
		t.Fatalf("rejected answers wrote %d native responses", wrote)
	}
	if err := resolve(protocol.InputAnswer{QuestionID: "value", Text: "Ada"}); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	nativeResponse := client.responses[0]
	client.mu.Unlock()
	if nativeResponse.Value == nil || *nativeResponse.Value != "Ada" {
		t.Fatalf("response=%+v", nativeResponse)
	}
	if adaptertest.Next(t, stream, time.Second).Type != protocol.TypeUserInputResolved {
		t.Fatal("missing resolution")
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("ok", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	_ = adaptertest.Drain(t, stream, time.Second)
}

func TestExtensionChoiceRejectsAttachedText(t *testing.T) {

	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	_ = adaptertest.Next(t, stream, time.Second)
	client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "ui-3", Method: native.ExtensionConfirm, Title: "Proceed?", Message: "Continue"})
	requested := adaptertest.Next(t, stream, time.Second)
	_ = adaptertest.Next(t, stream, time.Second)
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	resolve := func(a protocol.InputAnswer) error {
		return s.Resolve(context.Background(), base.InteractionResolution{RunID: response.RunID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session", RunID: response.RunID, Answers: []protocol.InputAnswer{a}}})
	}
	if err := resolve(protocol.InputAnswer{QuestionID: "value", SelectedOptionIDs: []string{"yes"}, Text: "yes"}); !errors.Is(err, base.ErrInvalidResolution) {
		t.Fatalf("mixed-form err = %v", err)
	}
	client.mu.Lock()
	wrote := len(client.responses)
	client.mu.Unlock()
	if wrote != 0 {
		t.Fatalf("rejected answer wrote %d native responses", wrote)
	}
	if err := resolve(protocol.InputAnswer{QuestionID: "value", SelectedOptionIDs: []string{"yes"}}); err != nil {
		t.Fatal(err)
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

func TestSelectExtensionRejectsEmptyOptions(t *testing.T) {

	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	_ = adaptertest.Next(t, stream, time.Second)
	client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "ui-9", Method: native.ExtensionSelect, Title: "Pick"})
	events := adaptertest.Drain(t, stream, time.Second)
	failed, surfaced := false, false
	for _, envelope := range events {
		switch envelope.Type {
		case protocol.TypeRunFailed:
			failed = true
		case protocol.TypeUserInputRequested:
			surfaced = true
		}
	}
	if !failed || surfaced {
		t.Fatalf("failed=%v surfaced=%v events=%v", failed, surfaced, events)
	}
}

func TestFallbackContentEmptyUsesEmptyText(t *testing.T) {
	content := fallbackContent(&runState{})
	text, ok := content.Text()
	if !ok || text != "" {
		t.Fatalf("content = %+v, want empty text", content)
	}
}

func TestAbortIntentNaturalCompletionCanWin(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	started := adaptertest.Next(t, stream, time.Second)
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
	assertCancelledTrace(t, response, append([]protocol.Envelope{started}, events...))
}

func TestAbortSettlementCancelsWhenNoNaturalCandidate(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	started := adaptertest.Next(t, stream, time.Second)
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
	assertCancelledTrace(t, response, append([]protocol.Envelope{started}, events...))
}

func TestProcessExitFailsActiveOnce(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
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
	assertValidTrace(t, admission, events)
}

func TestReplayGapAndOverflow(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 2)
	response, stream := submitTest(t, s)
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
	valid := []string{
		`{"type":"start"}`,
		`{"type":"text_start","contentIndex":0}`,
		`{"type":"text_end","contentIndex":0,"content":"x"}`,
		`{"type":"thinking_start","contentIndex":1}`,
		`{"type":"thinking_end","contentIndex":1,"content":"why"}`,
		`{"type":"toolcall_start","contentIndex":2,"id":"call","toolName":"read"}`,
		`{"type":"toolcall_delta","contentIndex":2,"delta":"{\"path\":"}`,
		`{"type":"toolcall_end","contentIndex":2,"toolCall":{"type":"toolCall","id":"call","name":"read","arguments":{"path":"x"},"namespace":"fs"}}`,
	}
	for _, raw := range valid {
		if _, emit, err := decodeProviderEvent(json.RawMessage(raw)); err != nil || emit {
			t.Fatalf("shape=%s emit=%v err=%v", raw, emit, err)
		}
	}
	if _, _, err := decodeProviderEvent(json.RawMessage(`{"type":"toolcall_delta","contentIndex":2,"delta":"{}","partial":{}}`)); err == nil {
		t.Fatal("serialized-out partial accepted")
	}
}

func TestPinnedMessageRolesAcceptFullOptionalShape(t *testing.T) {
	full := assistant("ok", "stop")
	full["responseModel"] = "resolved"
	full["responseId"] = "resp"
	full["providerThinkingLevel"] = "high"
	full["diagnostics"] = []any{map[string]any{"type": "warning", "message": "notice"}}
	full["deferred"] = map[string]any{"provider": "fake", "modelId": "m", "api": "messages", "id": "deferred", "expiresAt": 2, "pollAfterMs": 1, "data": map[string]any{"x": true}}
	full["rawStopReason"] = "native_stop"
	full["endTurn"] = true
	raw, _ := json.Marshal(full)
	message, err := decodeWireMessage(raw)
	if err != nil || message == nil {
		t.Fatalf("message=%+v err=%v", message, err)
	}
	tool := map[string]any{"role": "toolResult", "toolCallId": "call", "toolName": "read", "content": []any{map[string]any{"type": "image", "data": "AA==", "mimeType": "image/png"}}, "details": map[string]any{"x": 1}, "usage": map[string]any{"input": 1}, "addedToolNames": []string{"new"}, "isError": false, "timestamp": 2}
	raw, _ = json.Marshal(tool)
	if message, err := decodeWireMessage(raw); err != nil || message != nil {
		t.Fatalf("tool message=%+v err=%v", message, err)
	}
}

func TestProviderEventsRejectMissingAndNegativeFields(t *testing.T) {
	invalid := []string{
		`{"type":"text_delta","contentIndex":0}`,
		`{"type":"text_delta","delta":"x"}`,
		`{"type":"text_delta","contentIndex":-1,"delta":"x"}`,
		`{"type":"text_end","contentIndex":0}`,
		`{"type":"toolcall_start","contentIndex":0,"toolName":"read"}`,
		`{"type":"toolcall_end","contentIndex":0}`,
		`{"type":"done","reason":"stop"}`,
		`{"type":"error","error":{}}`,
	}
	for _, raw := range invalid {
		if _, _, err := decodeProviderEvent(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestPreStartExtensionBufferedBehindRunStarted(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt {
			client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "early-ui", Method: native.ExtensionInput, Title: "Name"})
			client.emit(t, map[string]any{"type": "agent_start"})
		}
	}
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	first, second := adaptertest.Next(t, stream, time.Second), adaptertest.Next(t, stream, time.Second)
	if first.Type != protocol.TypeRunStarted || second.Type != protocol.TypeUserInputRequested {
		t.Fatalf("types=%s,%s", first.Type, second.Type)
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
	if _, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}}); !errors.Is(err, ErrNativeProtocol) {
		t.Fatalf("open err=%v", err)
	}
	client.mu.Lock()
	closed := client.closed
	client.mu.Unlock()
	if !closed {
		t.Fatal("client not closed")
	}
}

func TestRetryCandidateDoesNotReuseStaleMessageEnd(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("stale", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("stale", "stop")}, "willRetry": true})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	if events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", eventTypes(events))
	}
}

func TestAbortedFinalCancelsOpenToolsBeforeParent(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	_ = adaptertest.Next(t, stream, time.Second)
	client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "open", "toolName": "read", "args": map[string]any{}})
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandAbort {
			client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("", "aborted")}, "willRetry": false})
			client.emit(t, map[string]any{"type": "agent_settled"})
		}
	}
	if _, err := s.Cancel(context.Background(), response.RunID); err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) < 2 || events[len(events)-2].Type != protocol.TypeActionCallCancelled || events[len(events)-1].Type != protocol.TypeRunCancelled {
		t.Fatalf("events=%v", eventTypes(events))
	}
}

func TestAbortedFinalWithIntentCancels(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
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

func TestRepeatedIdleSnapshotsRemainProvisionalUntilSettled(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	if event := adaptertest.Next(t, stream, time.Second); event.Type != protocol.TypeRunStarted {
		t.Fatalf("event=%s", event.Type)
	}
	for i := 0; i < 10; i++ {
		state, err := s.State(context.Background())
		if err != nil {
			t.Fatalf("state %d: %v", i, err)
		}
		if state.Status != protocol.SessionRunning {
			t.Fatalf("state %d prematurely changed: %+v", i, state)
		}
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("eventually", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("events=%v", eventTypes(events))
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

func assertRunFailedWith(t *testing.T, events []protocol.Envelope, code, message string) {
	t.Helper()
	for _, e := range events {
		if e.Type != protocol.TypeRunFailed {
			continue
		}
		var p protocol.RunFailedPayload
		if err := e.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if p.Error.Code != code {
			t.Fatalf("code=%q want %q (message %q)", p.Error.Code, code, p.Error.Message)
		}
		if message != "" && !strings.Contains(p.Error.Message, message) {
			t.Fatalf("message=%q want it to contain %q", p.Error.Message, message)
		}
		return
	}
	t.Fatalf("no run.failed among %v", eventTypes(events))
}

func failingRun(t *testing.T, emit func(client *fakeClient)) []protocol.Envelope {
	t.Helper()
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	emit(client)
	return adaptertest.Drain(t, stream, time.Second)
}

func TestSecondAgentStartRefusedAsDuplicate(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "agent_start"})
	})
	assertRunFailedWith(t, events, "pi_invalid_lifecycle", "duplicate agent_start")
}

func TestEventCarryingAnUndeclaredFieldIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "message_end", "message": assistant("hi", "stop"), "invented": 1})
	})
	assertRunFailedWith(t, events, "pi_invalid_event", "")
}

func TestUnknownEventTypeIsRefusedByName(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "invented_later"})
	})
	assertRunFailedWith(t, events, "pi_unknown_event", `unknown event "invented_later"`)
}

func TestUnknownAssistantMessageEventIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "message_update", "usage": map[string]any{}, "assistantMessageEvent": map[string]any{"type": "invented_later"}})
	})
	assertRunFailedWith(t, events, "pi_invalid_message_update", "unknown assistant message event")
}

func TestMalformedTerminalMessageIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "message_end", "message": "not-a-message"})
	})
	assertRunFailedWith(t, events, "pi_invalid_message_end", "")
}

func TestToolStartMissingItsIdentityIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "", "toolName": "read", "args": map[string]any{}})
	})
	assertRunFailedWith(t, events, "pi_invalid_tool_lifecycle", "invalid tool start")
}

func TestRepeatedToolStartIsRefusedAsDuplicate(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "a", "toolName": "read", "args": map[string]any{}})
		client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "a", "toolName": "read", "args": map[string]any{}})
	})
	assertRunFailedWith(t, events, "pi_invalid_tool_lifecycle", "duplicate tool start")
}

func TestToolProgressWithoutItsStartIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "tool_execution_update", "toolCallId": "nobody", "toolName": "read", "args": map[string]any{}, "partialResult": map[string]any{}})
	})
	assertRunFailedWith(t, events, "pi_invalid_tool_lifecycle", "tool update without matching active start")
}

func TestToolEndWithoutItsStartIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "tool_execution_end", "toolCallId": "nobody", "toolName": "read", "result": map[string]any{}, "isError": false})
	})
	assertRunFailedWith(t, events, "pi_invalid_tool_lifecycle", "tool end without matching active start")
}

func TestToolProgressNamingAnotherToolIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "a", "toolName": "read", "args": map[string]any{}})
		client.emit(t, map[string]any{"type": "tool_execution_update", "toolCallId": "a", "toolName": "write", "args": map[string]any{}, "partialResult": map[string]any{}})
	})
	assertRunFailedWith(t, events, "pi_invalid_tool_lifecycle", "tool update without matching active start")
}

func TestSettlementWithoutAgentEndIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "agent_settled"})
	})
	assertRunFailedWith(t, events, "pi_missing_agent_end", "agent_settled arrived without terminal agent_end")
}

func TestMalformedCandidateMessageIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "agent_end", "messages": []any{"not-a-message"}, "willRetry": false})
		client.emit(t, map[string]any{"type": "agent_settled"})
	})
	assertRunFailedWith(t, events, "pi_invalid_final_message", "")
}

func TestSelectExtensionWithAnEmptyLabelIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "ui-9", Method: native.ExtensionSelect, Title: "pick", Options: []string{"one", ""}})
	})
	assertRunFailedWith(t, events, "pi_invalid_extension", "select extension offered an empty option label")
}

func TestFinalMessageNamingAnUnknownToolIsRefused(t *testing.T) {
	message := assistant("ignored", "stop")
	message["content"] = []any{map[string]any{"type": "toolCall", "id": "nobody", "name": "read", "arguments": map[string]any{}}}
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "agent_end", "messages": []any{message}, "willRetry": false})
		client.emit(t, map[string]any{"type": "agent_settled"})
	})
	assertRunFailedWith(t, events, "pi_invalid_final_message", `final message references unknown tool "nobody"`)
}

func TestCandidateMessageOfAnUnknownRoleIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "agent_end", "messages": []any{map[string]any{"role": "invented"}}, "willRetry": false})
		client.emit(t, map[string]any{"type": "agent_settled"})
	})
	assertRunFailedWith(t, events, "pi_invalid_final_message", `unknown message role "invented"`)
}

func TestInteractionAnswerTheHarnessRefusesFailsTheRun(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	_ = adaptertest.Next(t, stream, time.Second)
	client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "ui-8", Method: native.ExtensionConfirm, Title: "Proceed?", Message: "Continue"})
	requested := adaptertest.Next(t, stream, time.Second)
	_ = adaptertest.Next(t, stream, time.Second)
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}

	refusal := errors.New("pi refused the response")
	client.mu.Lock()
	client.err = refusal
	client.mu.Unlock()

	err := s.Resolve(context.Background(), base.InteractionResolution{RunID: response.RunID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session", RunID: response.RunID, Answers: []protocol.InputAnswer{{QuestionID: "value", SelectedOptionIDs: []string{"yes"}}}}})
	if !errors.Is(err, refusal) {
		t.Fatalf("Resolve err=%v, want the harness refusal", err)
	}
	assertRunFailedWith(t, adaptertest.Drain(t, stream, time.Second), "pi_interaction_response_failed", "pi refused the response")
}

func abortRefusedBy(t *testing.T, refusal error) protocol.RunFailedPayload {
	t.Helper()
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	_ = adaptertest.Next(t, stream, time.Second)

	client.mu.Lock()
	client.err = refusal
	client.mu.Unlock()

	if _, err := s.Cancel(context.Background(), response.RunID); !errors.Is(err, refusal) {
		t.Fatalf("Cancel err=%v, want the harness refusal", err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	for _, e := range events {
		if e.Type != protocol.TypeRunFailed {
			continue
		}
		var p protocol.RunFailedPayload
		if err := e.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if p.Error.Code != "pi_abort_failed" {
			t.Fatalf("code=%q want pi_abort_failed", p.Error.Code)
		}
		return p
	}
	t.Fatalf("no run.failed among %v", eventTypes(events))
	return protocol.RunFailedPayload{}
}

func TestAbortTheHarnessRefusesFailsTheRunAndSaysWhoSettledIt(t *testing.T) {
	silent := abortRefusedBy(t, errors.New("transport gave up"))
	if silent.SettledBy != protocol.SettledByInferred {
		t.Fatalf("settled_by=%q want %q when the harness never answered", silent.SettledBy, protocol.SettledByInferred)
	}

	answered := abortRefusedBy(t, &rpc.RemoteError{ID: "1", Command: native.CommandAbort, Message: "cannot abort"})
	if answered.SettledBy != "" {
		t.Fatalf("settled_by=%q want it unset when the harness answered with a refusal", answered.SettledBy)
	}
}

func TestSettlementBeforeAnyStartAbortsTheAdmission(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	client.mu.Lock()
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandPrompt {
			client.emit(t, map[string]any{"type": "agent_settled"})
		}
	}
	client.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := s.Submit(ctx, protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("Submit hung: a settlement before any start was neither admitted nor refused")
	}
	if !errors.Is(err, ErrNativeProtocol) {
		t.Fatalf("Submit err=%v, want the native protocol refusal", err)
	}
	if !strings.Contains(err.Error(), "agent_settled arrived without agent_start") {
		t.Fatalf("Submit err=%v, want it to name the missing start", err)
	}
}

func TestSettlementWithAnEmptyCandidateIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "agent_end", "messages": []any{}, "willRetry": false})
		client.emit(t, map[string]any{"type": "agent_settled"})
	})
	assertRunFailedWith(t, events, "pi_missing_final_message", "agent settlement omitted assistant message")
}
