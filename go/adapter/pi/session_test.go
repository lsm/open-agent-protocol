package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/validation"
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
	replies   map[native.CommandType]json.RawMessage
	refusals  map[native.CommandType]error
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
	f.mu.Lock()
	refusal, reply := f.refusals[c.Type], f.replies[c.Type]
	f.mu.Unlock()
	if refusal != nil {
		return refusal
	}
	if result != nil {
		raw, ok := result.(*json.RawMessage)
		if !ok {
			return errors.New("expected raw result")
		}
		if reply != nil {
			*raw = reply
		} else {
			*raw, _ = json.Marshal(state)
		}
	}
	return nil
}
func (f *fakeClient) reduced() {
	ack := make(chan struct{})
	f.inbound <- rpc.Inbound{Barrier: ack}
	<-ack
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
	response, stream, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
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
		_, _, err := s.Submit(ctx, base.SubmitRequest{Request: request})
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
	if d.Capabilities.Features["session.message.delivery.queue"].Level != protocol.SupportUnavailable || d.Capabilities.Features["session.message.delivery.steer"].Level != protocol.SupportEmulated || d.Capabilities.Features["action.permissions"].Level != protocol.SupportUnavailable {
		t.Fatalf("descriptor overclaims: %+v", d.Capabilities.Features)
	}
	if reason := d.Capabilities.Features["session.message.delivery.steer"].Reason; reason == "" {
		t.Fatalf("steer is advertised without a reason: %+v", d.Capabilities.Features["session.message.delivery.steer"])
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
	_, stream, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("/extension-command argument")}}}})
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
		r, stream, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
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
		response, stream, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
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
	response, stream, err := s.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
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
	response, stream, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
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
	response, stream, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
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
	if events[len(events)-1].Type != protocol.TypeRunCancelled {
		t.Fatalf("events=%v cancel=%+v", eventTypes(events), cancel)
	}
	if cancel.Status != protocol.RunCancelling {
		t.Fatalf("cancel status %q, want %q: the answer is the acceptance, and the run settled on the stream behind it", cancel.Status, protocol.RunCancelling)
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

func TestSystemWireMessageIsAcceptedAndNotAFinalMessage(t *testing.T) {
	accepted := []string{
		`{"role":"system","content":"","sections":{"preamble":"p","gone":null},"toolsAdded":[{"name":"read"}],"toolsRemoved":[{"name":"bash"}],"timestamp":1}`,
		`{"role":"system","content":[{"type":"text","text":"more"}],"timestamp":2}`,
	}
	for _, raw := range accepted {
		if message, err := decodeWireMessage(json.RawMessage(raw)); err != nil || message != nil {
			t.Fatalf("%s: message=%+v err=%v", raw, message, err)
		}
	}
	rejected := []string{
		`{"role":"system","timestamp":1}`,
		`{"role":"system","content":""}`,
		`{"role":"system","content":[{"type":"image","data":"AA==","mimeType":"image/png"}],"timestamp":1}`,
		`{"role":"system","content":"","sections":{"a":1},"timestamp":1}`,
		`{"role":"system","content":[{"type":"text"}],"timestamp":1}`,
		`{"role":"system","content":"","timestamp":1,"api":"a"}`,
	}
	for _, raw := range rejected {
		if _, err := decodeWireMessage(json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted", raw)
		}
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
	started := adaptertest.Next(t, stream, time.Second)
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
	assertCancelledTrace(t, response, append([]protocol.Envelope{started}, events...))
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
	if _, _, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryQueue, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}}}}); !isUnadvertised(err, protocol.FeatureDeliveryQueue) {
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

func TestAToolStartedWithoutANameIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "native", "toolName": "", "args": map[string]any{}})
	})
	assertRunFailedWith(t, events, "pi_invalid_tool_lifecycle", "invalid tool start")
}

func TestFinalMessageRenamingItsToolIsRefused(t *testing.T) {
	for _, final := range []struct {
		name  string
		named string
	}{
		{"an empty name", ""},
		{"a different name", "write"},
	} {
		t.Run(final.name, func(t *testing.T) {
			client := newFakeClient()
			session := openTest(t, client, 32)
			admitted, stream := submitTest(t, session)
			client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": "native", "toolName": "read", "args": map[string]any{"path": "x"}})
			client.emit(t, map[string]any{"type": "tool_execution_end", "toolCallId": "native", "toolName": "read", "result": map[string]any{"text": "x"}, "isError": false})
			message := assistant("done", "stop")
			message["content"] = []any{map[string]any{"type": "toolCall", "id": "native", "name": final.named, "arguments": map[string]any{"path": "x"}}}
			client.emit(t, map[string]any{"type": "message_end", "message": message})
			client.emit(t, map[string]any{"type": "agent_end", "messages": []any{message}, "willRetry": false})
			client.emit(t, map[string]any{"type": "agent_settled"})
			events := adaptertest.Drain(t, stream, time.Second)
			assertRunFailedWith(t, events, "pi_invalid_final_message", `final message calls tool "native" "`+final.named+`", which was started as "read"`)
			adaptertest.AssertProtocolValidWithDescriptor(t, admitted, testDescriptor(t), events)
		})
	}
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
	_, _, err := s.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
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

func resolveInput(t *testing.T, s *Session, run protocol.RunID, requested protocol.Envelope) {
	t.Helper()
	var payload protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	answer := protocol.InputAnswer{QuestionID: payload.Questions[0].ID, SelectedOptionIDs: []string{payload.Questions[0].Options[0].ID}}
	resolution := base.InteractionResolution{RunID: run, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: payload.InteractionID, RequestedBy: endpointID, RespondedBy: "user", SessionID: "session", RunID: run, Answers: []protocol.InputAnswer{answer}}}
	if err := s.Resolve(context.Background(), resolution); err != nil {
		t.Fatalf("resolve %s: %v", payload.InteractionID, err)
	}
}

func TestAResolvedGateReportsTheOldestStillOutstanding(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admitted, stream := submitTest(t, s)
	if event := adaptertest.Next(t, stream, time.Second); event.Type != protocol.TypeRunStarted {
		t.Fatalf("event=%s", event.Type)
	}
	for _, id := range []string{"ui-1", "ui-2", "ui-3"} {
		client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: id, Method: native.ExtensionSelect, Title: id, Options: []string{"a", "b"}})
	}
	var requested []protocol.Envelope
	var opened []protocol.InteractionID
	for i := 0; i < 6; i++ {
		event := adaptertest.Next(t, stream, time.Second)
		if event.Type == protocol.TypeUserInputRequested {
			requested = append(requested, event)
			var payload protocol.UserInputRequestedPayload
			if err := event.DecodePayload(&payload); err != nil {
				t.Fatal(err)
			}
			opened = append(opened, payload.InteractionID)
			continue
		}
		if event.Type != protocol.TypeRunStatusUpdated {
			t.Fatalf("event=%s", event.Type)
		}
	}
	if len(requested) != 3 {
		t.Fatalf("opened %d gates, want 3", len(requested))
	}

	for at, gate := range requested {
		resolveInput(t, s, admitted.RunID, gate)
		if event := adaptertest.Next(t, stream, time.Second); event.Type != protocol.TypeUserInputResolved {
			t.Fatalf("event=%s, want user.input.resolved", event.Type)
		}
		update := adaptertest.Next(t, stream, time.Second)
		var status protocol.RunStatusUpdatedPayload
		if err := update.DecodePayload(&status); err != nil {
			t.Fatal(err)
		}
		if at == len(requested)-1 {
			if status.Status != protocol.RunRunning || status.PendingUserInputID != "" {
				t.Fatalf("after the last gate: status=%s pending=%q, want running and none", status.Status, status.PendingUserInputID)
			}
			continue
		}
		if status.Status != protocol.RunWaitingForInput {
			t.Fatalf("after gate %d: status=%s, want waiting_for_input", at+1, status.Status)
		}
		if status.PendingUserInputID != opened[at+1] {
			t.Fatalf("after gate %d: pending=%q, want the oldest still open %q", at+1, status.PendingUserInputID, opened[at+1])
		}
	}
}

func TestTheOldestOutstandingGateIsTheRunsOwn(t *testing.T) {
	session := openTest(t, newFakeClient(), 32)
	mine := &runState{id: "run-mine"}
	theirs := &runState{id: "run-theirs"}
	session.interactions["theirs"] = &inputState{id: "theirs", run: theirs, opened: 1}
	session.interactions["mine"] = &inputState{id: "mine", run: mine, opened: 2}

	oldest := session.oldestPendingInput(mine)
	if oldest == nil || oldest.id != "mine" {
		t.Fatalf("oldest = %+v, want this run's own gate", oldest)
	}
	session.interactions["mine"].phase = interactionResolved
	if settled := session.oldestPendingInput(mine); settled != nil {
		t.Fatalf("oldest = %+v, want none once this run has nothing open", settled)
	}
}

func isUnadvertised(err error, key string) bool {
	var refused *base.UnsupportedControlError
	return errors.As(err, &refused) && refused.Feature == key && refused.Reason == base.ControlUnadvertised
}

func TestPendingToolsAreSettledInTheOrderTheyStarted(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	response, stream := submitTest(t, s)
	for _, id := range []string{"a", "b", "c"} {
		client.emit(t, map[string]any{"type": "tool_execution_start", "toolCallId": id, "toolName": "read", "args": map[string]any{"path": id}})
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	assertValidTrace(t, response, events)

	var started, settled []protocol.ToolCallID
	for _, event := range events {
		if event.ToolCallID == "" {
			continue
		}
		switch event.Type {
		case protocol.TypeActionCallRequested:
			started = append(started, event.ToolCallID)
		case protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled:
			settled = append(settled, event.ToolCallID)
		}
	}
	if len(started) != 3 || len(settled) != 3 {
		t.Fatalf("started %v and settled %v, want three of each", started, settled)
	}
	for i := range started {
		if settled[i] != started[i] {
			t.Fatalf("settled %v, want the start order %v", settled, started)
		}
	}
}

func TestPendingPromptsAreSettledInTheOrderTheyStarted(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	trace := []protocol.Envelope{adaptertest.Next(t, stream, time.Second)}
	for i, id := range []string{"ui-1", "ui-2"} {
		client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: id, Method: native.ExtensionInput, Title: "Name " + id})
		requested := adaptertest.Next(t, stream, time.Second)
		if requested.Type != protocol.TypeUserInputRequested {
			t.Fatalf("envelope %d = %s, want the prompt", i, requested.Type)
		}
		trace = append(trace, requested, adaptertest.Next(t, stream, time.Second))
	}
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := append(trace, adaptertest.Drain(t, stream, time.Second)...)

	var asked, settled []protocol.InteractionID
	for _, event := range events {
		if event.Type != protocol.TypeUserInputRequested && event.Type != protocol.TypeUserInputResolved {
			continue
		}
		var payload struct {
			InteractionID protocol.InteractionID `json:"interaction_id"`
		}
		if err := event.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		if event.Type == protocol.TypeUserInputRequested {
			asked = append(asked, payload.InteractionID)
		} else {
			settled = append(settled, payload.InteractionID)
		}
	}
	if len(asked) != 2 || len(settled) != 2 {
		t.Fatalf("asked %v and settled %v, want two of each", asked, settled)
	}
	for i := range asked {
		if settled[i] != asked[i] {
			t.Fatalf("settled %v, want the order they were asked in %v", settled, asked)
		}
	}
}

func TestAnOpenSetsPisThinkingLevelAndCompactionAndReportsWhatPiConfirms(t *testing.T) {
	client := newFakeClient()
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandSetThinkingLevel {
			client.mu.Lock()
			client.state.ThinkingLevel = c.Level
			client.mu.Unlock()
		}
	}
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) { return client, client.state, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	policy := &protocol.CompactionPolicy{Kind: protocol.CompactionOff}
	s, err := a.Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "user"}, ReasoningLevel: protocol.ReasoningXHigh, CompactionPolicy: policy})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	client.mu.Lock()
	calls := append([]native.Command(nil), client.calls...)
	client.mu.Unlock()
	if len(calls) < 2 || calls[0].Type != native.CommandSetAutoCompaction || calls[0].Enabled == nil || *calls[0].Enabled || calls[1].Type != native.CommandSetThinkingLevel || calls[1].Level != native.ThinkingXHigh {
		t.Fatalf("open sent %+v, want set_auto_compaction false then set_thinking_level xhigh", calls)
	}
	state, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.ReasoningLevel != protocol.ReasoningXHigh || state.CompactionPolicy == nil || state.CompactionPolicy.Kind != protocol.CompactionOff {
		t.Fatalf("state reports %q and %+v, want the confirmed settings", state.ReasoningLevel, state.CompactionPolicy)
	}
}

func TestALevelPiDoesNotConfirmIsRefusedAndAThresholdBeforeThePiStarts(t *testing.T) {
	client := newFakeClient()
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) { return client, client.state, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "user"}, ReasoningLevel: protocol.ReasoningMax})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning {
		t.Fatalf("open answered %v, want a level Pi kept at %s refused", err, client.state.ThinkingLevel)
	}
	started := false
	b, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) {
		started = true
		return client, client.state, nil
	}), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 16})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.Open(context.Background(), base.OpenRequest{SessionID: "s2", Participant: protocol.Participant{ID: "user"}, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 1000}})
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureCompactionPolicy || started {
		t.Fatalf("open answered %v (started %v), want a token threshold refused before Pi starts", err, started)
	}
}

func steerRequest(requestID protocol.EnvelopeID, target protocol.RunID) base.SubmitRequest {
	return base.SubmitRequest{
		EnvelopeID: requestID,
		Request: protocol.MessageSubmitRequest{
			SessionID: "session", Delivery: protocol.DeliverySteer, TargetRunID: target,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("adjust")}},
		},
	}
}

func sentCommand(client *fakeClient, typ native.CommandType) bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	for _, call := range client.calls {
		if call.Type == typ {
			return true
		}
	}
	return false
}

func TestSteerAdmitsAgainstTheStartedRunAndSettlesAtTheTurnBoundary(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)

	steered, steerStream, err := s.Submit(context.Background(), steerRequest("steer-request", ""))
	if err != nil {
		t.Fatal(err)
	}
	if steerStream != nil {
		t.Fatal("a steer exposed a stream")
	}
	if steered.Admission != protocol.AdmissionSteered || steered.EffectiveDelivery != protocol.EffectiveDeliverySteer || steered.RunID != admission.RunID {
		t.Fatalf("steer admission = %+v", steered)
	}
	if steered.TargetSequence == nil || *steered.TargetSequence != 1 || steered.Status != protocol.RunRunning {
		t.Fatalf("steer admission position = %+v", steered)
	}
	if len(steered.MessageIDs) != 1 || steered.MessageIDs[0] == "" || steered.SubmissionID == "" {
		t.Fatalf("steer admission identity = %+v", steered)
	}
	if !sentCommand(client, native.CommandSteer) {
		t.Fatal("the steer never reached Pi's native steer command")
	}

	state, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 1 || len(state.ActiveRuns[0].PendingSteers) != 1 {
		t.Fatalf("state = %+v, want one pending steer", state.ActiveRuns)
	}
	pending := state.ActiveRuns[0].PendingSteers[0]
	if pending.SubmissionID != steered.SubmissionID || pending.RequestID != "steer-request" {
		t.Fatalf("pending steer = %+v", pending)
	}
	if state.ActiveRuns[0].AsOfSequence == nil || *state.ActiveRuns[0].AsOfSequence != 1 {
		t.Fatalf("pending steer capture cursor = %+v", state.ActiveRuns[0].AsOfSequence)
	}
	if len(state.ActiveRuns[0].AdmittedSubmitRequests) != 1 || state.ActiveRuns[0].AdmittedSubmitRequests[0] != "steer-request" {
		t.Fatalf("pending steer anchors = %+v", state.ActiveRuns[0].AdmittedSubmitRequests)
	}

	client.emit(t, map[string]any{"type": "turn_end", "message": assistant("mid", "stop"), "toolResults": []any{}})
	applied := adaptertest.Next(t, stream, time.Second)
	if applied.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("settlement type = %s", applied.Type)
	}
	var payload protocol.RunSteerAppliedPayload
	if err := applied.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.SubmissionID != steered.SubmissionID || payload.RequestID != "steer-request" || payload.Boundary != protocol.SteerTurn || payload.RunID != admission.RunID {
		t.Fatalf("settlement = %+v", payload)
	}
	if len(payload.MessageIDs) != 1 || payload.MessageIDs[0] != steered.MessageIDs[0] {
		t.Fatalf("settlement messages = %+v", payload.MessageIDs)
	}

	settled, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(settled.ActiveRuns) != 0 {
		t.Fatalf("a settled steer stayed pending: %+v", settled.ActiveRuns)
	}
	client.emit(t, map[string]any{"type": "turn_end", "message": assistant("mid", "stop"), "toolResults": []any{}})
	select {
	case extra := <-stream:
		t.Fatalf("a settled steer settled twice: %s", extra.Envelope.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestASteerSettlesAtATurnEndReducedBeforeItIsRecorded(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)

	early := make(chan struct{})
	client.inbound <- rpc.Inbound{Barrier: early}
	<-early
	client.emit(t, map[string]any{"type": "turn_end", "message": assistant("before", "stop"), "toolResults": []any{}})
	client.mu.Lock()
	client.onCall = func(c native.Command) {
		if c.Type != native.CommandSteer {
			return
		}
		answered := make(chan struct{})
		client.inbound <- rpc.Inbound{Barrier: answered}
		raw, _ := json.Marshal(map[string]any{"type": "turn_end", "message": assistant("mid", "stop"), "toolResults": []any{}})
		client.inbound <- rpc.Inbound{Event: &native.Event{Type: native.EventTurnEnd, Raw: raw}}
		reduced := make(chan struct{})
		client.inbound <- rpc.Inbound{Barrier: reduced}
		<-answered
		<-reduced
	}
	client.mu.Unlock()

	steered, _, err := s.Submit(context.Background(), steerRequest("steer-request", admission.RunID))
	if err != nil {
		t.Fatal(err)
	}
	applied := adaptertest.Next(t, stream, time.Second)
	if applied.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("settlement type = %s, want the steer applied at the turn end that followed its response", applied.Type)
	}
	var payload protocol.RunSteerAppliedPayload
	if err := applied.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.SubmissionID != steered.SubmissionID || payload.Boundary != protocol.SteerTurn {
		t.Fatalf("settlement = %+v", payload)
	}
	state, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 0 {
		t.Fatalf("state = %+v, want the steer settled", state.ActiveRuns)
	}
}

func TestASteerAnsweredAfterATurnEndWaitsForTheNextOne(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)

	client.mu.Lock()
	client.onCall = func(c native.Command) {
		if c.Type != native.CommandSteer {
			return
		}
		raw, _ := json.Marshal(map[string]any{"type": "turn_end", "message": assistant("before", "stop"), "toolResults": []any{}})
		client.inbound <- rpc.Inbound{Event: &native.Event{Type: native.EventTurnEnd, Raw: raw}}
		answered := make(chan struct{})
		client.inbound <- rpc.Inbound{Barrier: answered}
		<-answered
	}
	client.mu.Unlock()

	if _, _, err := s.Submit(context.Background(), steerRequest("steer-request", admission.RunID)); err != nil {
		t.Fatal(err)
	}
	select {
	case early := <-stream:
		t.Fatalf("a turn end before the steer's response settled it: %s", early.Envelope.Type)
	case <-time.After(100 * time.Millisecond):
	}
	client.emit(t, map[string]any{"type": "turn_end", "message": assistant("mid", "stop"), "toolResults": []any{}})
	if applied := adaptertest.Next(t, stream, time.Second); applied.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("settlement type = %s", applied.Type)
	}
}

func TestResumeAfterASteerSettlesReportsNoPendingSteer(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)
	if _, _, err := s.Submit(context.Background(), steerRequest("steer-request", admission.RunID)); err != nil {
		t.Fatal(err)
	}
	pending, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending.ActiveRuns) != 1 || len(pending.ActiveRuns[0].PendingSteers) != 1 {
		t.Fatalf("state = %+v, want the steer pending", pending.ActiveRuns)
	}
	client.emit(t, map[string]any{"type": "turn_end", "message": assistant("mid", "stop"), "toolResults": []any{}})
	if applied := adaptertest.Next(t, stream, time.Second); applied.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("settlement type = %s", applied.Type)
	}
	recovery, _, err := s.Resume(context.Background(), base.ResumeRequest{RunID: admission.RunID, AfterSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovery.State.ActiveRuns) != 0 {
		t.Fatalf("recovery state = %+v, want the settled steer gone", recovery.State.ActiveRuns)
	}
}

func TestSteerDropsAtTheTerminalBeforeTheRunSettles(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)
	if _, _, err := s.Submit(context.Background(), steerRequest("steer-request", admission.RunID)); err != nil {
		t.Fatal(err)
	}
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("done", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})

	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) != 2 || events[0].Type != protocol.TypeRunSteerDropped || events[1].Type != protocol.TypeRunCompleted {
		t.Fatalf("events = %v, want the drop before the terminal", eventTypes(events))
	}
	var dropped protocol.RunSteerDroppedPayload
	if err := events[0].DecodePayload(&dropped); err != nil {
		t.Fatal(err)
	}
	if dropped.SubmissionID == "" || dropped.RequestID != "steer-request" || dropped.Reason.Code != "run_terminated" {
		t.Fatalf("drop = %+v", dropped)
	}
	if events[0].Sequence == nil || events[1].Sequence == nil || *events[0].Sequence >= *events[1].Sequence {
		t.Fatalf("the drop is not in the run's sequence before the terminal: %v", events)
	}
}

func TestSteerTargetRefusals(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)

	_, _, err := s.Submit(context.Background(), steerRequest("steer-idle", ""))
	var refusal *base.InvalidSteerTargetError
	if !errors.As(err, &refusal) || refusal.Reason != base.SteerReasonNoActiveRun {
		t.Fatalf("idle steer = %v, want no_active_run", err)
	}

	admission, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)
	_, _, err = s.Submit(context.Background(), steerRequest("steer-unknown", "run-elsewhere"))
	if !errors.As(err, &refusal) || refusal.Reason != base.SteerReasonUnknownTarget || refusal.TargetSequence != nil {
		t.Fatalf("unknown target = %v, want unknown_target without a position", err)
	}

	if _, err := s.Cancel(context.Background(), admission.RunID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.Submit(context.Background(), steerRequest("steer-cancelling", ""))
	if !errors.As(err, &refusal) || refusal.Reason != base.SteerReasonNotSteerable {
		t.Fatalf("cancelling target = %v, want not_steerable", err)
	}
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("done", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	adaptertest.Drain(t, stream, time.Second)

	_, _, err = s.Submit(context.Background(), steerRequest("steer-terminal", admission.RunID))
	if !errors.As(err, &refusal) || refusal.Reason != base.SteerReasonTerminal || refusal.TargetSequence == nil {
		t.Fatalf("terminal target = %v, want terminal with a position", err)
	}
}

func TestSteerRefusesRunControlsAsUnadvertised(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	_, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)

	request := steerRequest("steer-controls", "")
	model := "model"
	request.Request.ModelID = &model
	_, _, err := s.Submit(context.Background(), request)
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureModelSelection || refusal.Reason != base.ControlUnadvertised {
		t.Fatalf("steer with a model = %v, want unadvertised model_selection", err)
	}
	if sentCommand(client, native.CommandSteer) {
		t.Fatal("a refused steer reached Pi")
	}
}

func TestASteerDuringAnExtensionDialogCapturesTheOpenInteraction(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	started := adaptertest.Next(t, stream, time.Second)

	client.extension(native.ExtensionUIRequest{Type: "extension_ui_request", ID: "ui-1", Method: native.ExtensionInput, Title: "Name"})
	requested := adaptertest.Next(t, stream, time.Second)
	status := adaptertest.Next(t, stream, time.Second)
	var open protocol.UserInputRequestedPayload
	if err := requested.DecodePayload(&open); err != nil {
		t.Fatal(err)
	}

	steer := steerRequest("steer-request", "")
	steered, _, err := s.Submit(context.Background(), steer)
	if err != nil {
		t.Fatal(err)
	}
	if steered.Status != protocol.RunWaitingForInput {
		t.Fatalf("steer status = %s, want the waiting status", steered.Status)
	}
	state, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 1 {
		t.Fatalf("state = %+v, want one active run", state.ActiveRuns)
	}
	entry := state.ActiveRuns[0]
	if len(entry.PendingInteractions) != 1 || entry.PendingInteractions[0] != open.InteractionID {
		t.Fatalf("pending interactions = %v, want the open %s", entry.PendingInteractions, open.InteractionID)
	}

	client.emit(t, map[string]any{"type": "turn_end", "message": assistant("mid", "stop"), "toolResults": []any{}})
	applied := adaptertest.Next(t, stream, time.Second)
	if applied.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("settlement = %s", applied.Type)
	}
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("done", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	tail := append([]protocol.Envelope{applied}, adaptertest.Drain(t, stream, time.Second)...)

	exchange, err := adaptertest.StateExchange(state)
	if err != nil {
		t.Fatal(err)
	}
	head := []protocol.Envelope{started, requested, status}
	trace, err := steeredDialogTrace(admission, steer, steered, head, exchange, tail)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(trace, "pi-steer-dialog"); !result.Valid() {
		t.Fatalf("the steered capture failed OAP validation: %v\ntrace: %s", result.Diagnostics, trace)
	}

	blind := state
	blind.ActiveRuns = append([]protocol.ActiveRun(nil), state.ActiveRuns...)
	blind.ActiveRuns[0].PendingInteractions = nil
	blindExchange, err := adaptertest.StateExchange(blind)
	if err != nil {
		t.Fatal(err)
	}
	blindTrace, err := steeredDialogTrace(admission, steer, steered, head, blindExchange, tail)
	if err != nil {
		t.Fatal(err)
	}
	result := validation.MustNew().ValidateBytes(blindTrace, "pi-steer-dialog-blind")
	if result.Valid() {
		t.Fatal("a capture that omits the open interaction passed OAP validation")
	}
	found := false
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Code == validation.CodeSessionStateMismatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("the blind capture failed without session_state_mismatch: %v", result.Diagnostics)
	}
}

func steeredDialogTrace(admission protocol.MessageSubmitResponse, steer base.SubmitRequest, steered protocol.MessageSubmitResponse, head, exchange, tail []protocol.Envelope) ([]byte, error) {
	descriptor, err := dialogDescriptor()
	if err != nil {
		return nil, err
	}
	capabilities, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	if err != nil {
		return nil, err
	}
	capabilityResponse, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	if err != nil {
		return nil, err
	}
	capabilityResponse.InReplyTo, capabilityResponse.CapabilityRevision = capabilities.ID, descriptor.CapabilityRevision

	start, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, "start-request", protocol.MessageSubmitRequest{
		SessionID: admission.SessionID, Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	})
	if err != nil {
		return nil, err
	}
	start.SessionID, start.CapabilityRevision = admission.SessionID, descriptor.CapabilityRevision
	startResponse, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, "start-response", admission)
	if err != nil {
		return nil, err
	}
	startResponse.SessionID, startResponse.InReplyTo, startResponse.RunID = admission.SessionID, start.ID, admission.RunID
	startResponse.CapabilityRevision = descriptor.CapabilityRevision

	steerRequest, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, steer.EnvelopeID, steer.Request)
	if err != nil {
		return nil, err
	}
	steerRequest.SessionID, steerRequest.CapabilityRevision = admission.SessionID, descriptor.CapabilityRevision
	steerResponse, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, "steer-response", steered)
	if err != nil {
		return nil, err
	}
	steerResponse.SessionID, steerResponse.InReplyTo, steerResponse.RunID = admission.SessionID, steerRequest.ID, steered.RunID
	steerResponse.CapabilityRevision = descriptor.CapabilityRevision

	trace := append([]protocol.Envelope{capabilities, capabilityResponse, start, startResponse}, head...)
	trace = append(trace, steerRequest, steerResponse)
	trace = append(trace, exchange...)
	trace = append(trace, tail...)
	return json.Marshal(trace)
}

func dialogDescriptor() (base.Descriptor, error) {
	descriptor, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) {
		return nil, native.SessionState{}, errors.New("probe only")
	}), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: 64})
	if err != nil {
		return base.Descriptor{}, err
	}
	probed, err := descriptor.Probe(context.Background())
	if err != nil {
		return base.Descriptor{}, err
	}
	features := make(map[string]protocol.FeatureSupport, len(probed.Capabilities.Features)+1)
	for key, support := range probed.Capabilities.Features {
		features[key] = support
	}
	features["user_input"] = protocol.FeatureSupport{Level: protocol.SupportEmulated, Reason: "an extension dialog is projected as generic user input"}
	probed.Capabilities.Features = features
	return probed, nil
}

func TestASteerReadsItsTargetUnderTheReducerLock(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	adaptertest.Next(t, stream, time.Second)

	s.reduceMu.Lock()
	submitted := make(chan error, 1)
	go func() {
		_, _, err := s.Submit(context.Background(), steerRequest("steer-request", admission.RunID))
		submitted <- err
	}()
	select {
	case err := <-submitted:
		t.Fatalf("the steer read the run without the reducer lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	s.reduceMu.Unlock()
	select {
	case err := <-submitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("the steer never returned after the reducer lock was released")
	}
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("done", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	adaptertest.Drain(t, stream, time.Second)
}

func TestWireMessagesAdmitPiOneMembers(t *testing.T) {
	assistant := `{"role":"assistant","content":"hi","api":"a","provider":"p","model":"m","usage":{},"stopReason":"stop","timestamp":1,"thinkingLevel":"off"}`
	if _, err := decodeWireMessage(json.RawMessage(assistant)); err != nil {
		t.Fatalf("assistant with thinkingLevel: %v", err)
	}
	result := `{"role":"toolResult","toolCallId":"t1","toolName":"grep","content":"ok","isError":false,"timestamp":1,"nestedCalls":{"calls":[],"complete":true}}`
	if _, err := decodeWireMessage(json.RawMessage(result)); err != nil {
		t.Fatalf("tool result with nestedCalls: %v", err)
	}
}

func TestUnknownCompactionReasonIsRefusedByName(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "compaction_start", "reason": "invented"})
	})
	assertRunFailedWith(t, events, "pi_invalid_compaction", `unknown compaction reason "invented"`)
}

func TestCompactionEndWithoutItsStartIsRefused(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "compaction_end", "reason": "threshold", "aborted": false, "willRetry": false})
	})
	assertRunFailedWith(t, events, "pi_invalid_compaction", "compaction_end without compaction_start")
}

func TestSecondCompactionStartIsRefusedAndTheOpenOneEndsFailed(t *testing.T) {
	events := failingRun(t, func(client *fakeClient) {
		client.emit(t, map[string]any{"type": "compaction_start", "reason": "threshold"})
		client.emit(t, map[string]any{"type": "compaction_start", "reason": "overflow"})
	})
	assertRunFailedWith(t, events, "pi_invalid_compaction", "compaction_start while a compaction is open")
	ended := compactionEnded(t, events)
	if len(ended) != 1 || ended[0].Outcome != protocol.CompactionFailed || ended[0].Error == nil || ended[0].Error.Code != "pi_compaction_unfinished" {
		t.Fatalf("compaction ends = %+v, want the open one failed as unfinished", ended)
	}
}

func compactionEnded(t *testing.T, events []protocol.Envelope) []protocol.RunCompactionEndedPayload {
	t.Helper()
	var ended []protocol.RunCompactionEndedPayload
	for _, e := range events {
		if e.Type != protocol.TypeRunCompactionEnded {
			continue
		}
		var p protocol.RunCompactionEndedPayload
		if err := e.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		ended = append(ended, p)
	}
	return ended
}

func TestAnAbortedCompactionEndsCancelledAndTheRunCarriesOn(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "compaction_start", "reason": "overflow"})
	client.emit(t, map[string]any{"type": "compaction_end", "reason": "overflow", "aborted": true, "willRetry": false})
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("done", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	ended := compactionEnded(t, events)
	if len(ended) != 1 || ended[0].Outcome != protocol.CompactionCancelled || ended[0].Error != nil {
		t.Fatalf("compaction ends = %+v, want one cancelled", ended)
	}
	if last := events[len(events)-1]; last.Type != protocol.TypeRunCompleted {
		t.Fatalf("events=%v", eventTypes(events))
	}
	assertValidTrace(t, admission, events)
}

func TestARunSettlingWithAnOpenCompactionEndsItFailedFirst(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	admission, stream := submitTest(t, s)
	client.emit(t, map[string]any{"type": "compaction_start", "reason": "threshold"})
	client.emit(t, map[string]any{"type": "message_end", "message": assistant("done", "stop")})
	client.emit(t, map[string]any{"type": "agent_end", "messages": []any{assistant("done", "stop")}, "willRetry": false})
	client.emit(t, map[string]any{"type": "agent_settled"})
	events := adaptertest.Drain(t, stream, time.Second)
	types := eventTypes(events)
	if len(types) < 2 || types[len(types)-2] != protocol.TypeRunCompactionEnded || types[len(types)-1] != protocol.TypeRunCompleted {
		t.Fatalf("events=%v, want the compaction ended just before the terminal", types)
	}
	if ended := compactionEnded(t, events); ended[0].Outcome != protocol.CompactionFailed || ended[0].Error.Code != "pi_compaction_unfinished" {
		t.Fatalf("compaction end = %+v", ended[0])
	}
	assertValidTrace(t, admission, events)
}

func compactTrace(t *testing.T, request protocol.SessionCompactRequest, admission protocol.SessionCompactResponse, events []protocol.Envelope, cancelled bool) {
	t.Helper()
	descriptor := testDescriptor(t)
	envelope := func(typ protocol.EnvelopeType, id string, payload any, reply protocol.EnvelopeID) protocol.Envelope {
		e, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
		if err != nil {
			t.Fatal(err)
		}
		e.InReplyTo, e.SessionID = reply, "session"
		if typ != protocol.TypeCapabilitiesRequest {
			e.CapabilityRevision = descriptor.CapabilityRevision
		}
		return e
	}
	trace := []protocol.Envelope{
		envelope(protocol.TypeCapabilitiesRequest, "caps", protocol.CapabilitiesRequest{}, ""),
		envelope(protocol.TypeCapabilitiesResponse, "caps-r", descriptor.Capabilities, "caps"),
		envelope(protocol.TypeSessionCompactRequest, "compact", request, ""),
		envelope(protocol.TypeSessionCompactResponse, "compact-r", admission, "compact"),
	}
	trace[0].SessionID, trace[1].SessionID = "", ""
	if cancelled {
		trace = append(trace,
			envelope(protocol.TypeRunCancelRequest, "cancel", protocol.RunCancelRequest{SessionID: "session", RunID: admission.RunID}, ""),
			envelope(protocol.TypeRunCancelResponse, "cancel-r", protocol.RunCancelResponse{SessionID: "session", RunID: admission.RunID, Accepted: true, Status: protocol.RunCancelling}, "cancel"))
	}
	for i := range trace {
		if trace[i].Type == protocol.TypeRunCancelRequest || trace[i].Type == protocol.TypeRunCancelResponse {
			trace[i].RunID = admission.RunID
		}
	}
	encoded, err := json.Marshal(append(trace, events...))
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(encoded, "pi-compaction"); !result.Valid() {
		t.Fatalf("compaction trace failed OAP validation: %v\n%s", result.Diagnostics, encoded)
	}
}

func compactSummary() json.RawMessage {
	return json.RawMessage(`{"summary":"the summary","firstKeptEntryId":"e1","tokensBefore":20,"estimatedTokensAfter":5,"usage":{},"details":{}}`)
}

func TestACompactionRequestRunsPiCompactAsARunOfItsOwn(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	client.replies = map[native.CommandType]json.RawMessage{native.CommandCompact: compactSummary()}
	client.onCall = func(c native.Command) {
		if c.Type != native.CommandCompact {
			return
		}
		client.emit(t, map[string]any{"type": "compaction_start", "reason": "manual"})
		client.emit(t, map[string]any{"type": "compaction_end", "reason": "manual", "aborted": false, "willRetry": false, "result": compactSummary()})
		client.reduced()
	}
	focus := "keep the plan"
	request := protocol.SessionCompactRequest{SessionID: "session", Focus: &focus}
	admission, stream, err := s.Compact(context.Background(), base.CompactRequest{Request: request, EnvelopeID: "compact"})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeRunCompactionStarted, protocol.TypeRunCompactionEnded, protocol.TypeRunCompleted}
	if got := eventTypes(events); !slices.Equal(got, want) {
		t.Fatalf("events=%v want %v", got, want)
	}
	var started protocol.RunCompactionStartedPayload
	if err := events[1].DecodePayload(&started); err != nil || started.Reason != protocol.CompactionRequested {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	ended := compactionEnded(t, events)[0]
	var completed protocol.RunCompletedPayload
	if err := events[3].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.StopReason != "compacted" || ended.Summary == nil || completed.FinalResponse.ID != ended.Summary.ID {
		t.Fatalf("completed=%+v ended=%+v, want the summary as the final response", completed, ended)
	}
	if !sentCommand(client, native.CommandCompact) || client.calls[len(client.calls)-1].CustomInstructions == nil || *client.calls[len(client.calls)-1].CustomInstructions != focus {
		t.Fatalf("compact command = %+v, want the focus as custom instructions", client.calls[len(client.calls)-1])
	}
	compactTrace(t, request, admission, events, false)
}

func TestACompactionPiRefusesFailsTheRunWithPisReason(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	client.refusals = map[native.CommandType]error{native.CommandCompact: &rpc.RemoteError{ID: "c", Command: native.CommandCompact, Message: "Nothing to compact (session too small)"}}
	client.onCall = func(c native.Command) {
		if c.Type != native.CommandCompact {
			return
		}
		client.emit(t, map[string]any{"type": "compaction_start", "reason": "manual"})
		client.emit(t, map[string]any{"type": "compaction_end", "reason": "manual", "aborted": false, "willRetry": false, "errorMessage": "Compaction failed: Nothing to compact (session too small)"})
		client.reduced()
	}
	request := protocol.SessionCompactRequest{SessionID: "session"}
	admission, stream, err := s.Compact(context.Background(), base.CompactRequest{Request: request})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	assertRunFailedWith(t, events, "pi_compaction_failed", "Nothing to compact")
	if ended := compactionEnded(t, events); len(ended) != 1 || ended[0].Outcome != protocol.CompactionFailed {
		t.Fatalf("compaction ends=%+v", ended)
	}
	compactTrace(t, request, admission, events, false)
}

func TestACancelledCompactionSettlesCancelled(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	aborted := make(chan struct{})
	client.refusals = map[native.CommandType]error{native.CommandCompact: &rpc.RemoteError{ID: "c", Command: native.CommandCompact, Message: "Compaction cancelled"}}
	client.onCall = func(c native.Command) {
		switch c.Type {
		case native.CommandAbort:
			close(aborted)
		case native.CommandCompact:
			client.emit(t, map[string]any{"type": "compaction_start", "reason": "manual"})
			<-aborted
			client.emit(t, map[string]any{"type": "compaction_end", "reason": "manual", "aborted": true, "willRetry": false})
			client.reduced()
		}
	}
	request := protocol.SessionCompactRequest{SessionID: "session"}
	admission, stream, err := s.Compact(context.Background(), base.CompactRequest{Request: request})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Cancel(context.Background(), admission.RunID); err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if last := events[len(events)-1]; last.Type != protocol.TypeRunCancelled {
		t.Fatalf("events=%v", eventTypes(events))
	}
	if ended := compactionEnded(t, events); len(ended) != 1 || ended[0].Outcome != protocol.CompactionCancelled {
		t.Fatalf("compaction ends=%+v", ended)
	}
	compactTrace(t, request, admission, events, true)
}

func TestACompactionRequestIsRefusedWhatPiCannotDo(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	var refusal *base.UnsupportedControlError
	if _, _, err := s.Compact(context.Background(), base.CompactRequest{Request: protocol.SessionCompactRequest{SessionID: "session", Continue: true}}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionCompact || refusal.Field != "continue" {
		t.Fatalf("continue answered %v, want unsupported_feature naming continue", err)
	}
	if _, _, err := s.Compact(context.Background(), base.CompactRequest{Request: protocol.SessionCompactRequest{SessionID: "session", Delivery: protocol.DeliveryQueue}}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureDeliveryQueue {
		t.Fatalf("queue answered %v, want unsupported_feature naming the queue key", err)
	}
	if _, _, err := s.Compact(context.Background(), base.CompactRequest{Request: protocol.SessionCompactRequest{SessionID: "session", Delivery: protocol.DeliverySteer}}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureDeliverySteer {
		t.Fatalf("steer answered %v, want unsupported_feature naming the steer key", err)
	}
	submitTest(t, s)
	if _, _, err := s.Compact(context.Background(), base.CompactRequest{Request: protocol.SessionCompactRequest{SessionID: "session"}}); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("a busy session answered %v, want run_active", err)
	}
}

func followLevels(client *fakeClient, refused native.ThinkingLevel) {
	client.onCall = func(c native.Command) {
		if c.Type == native.CommandSetThinkingLevel && c.Level != refused {
			client.mu.Lock()
			client.state.ThinkingLevel = c.Level
			client.mu.Unlock()
		}
	}
}

func TestALiveUpdateSetsPisLevelAndCompactionBetweenRunsAndReportsThem(t *testing.T) {
	client := newFakeClient()
	followLevels(client, "")
	s := openTest(t, client, 32)
	client.mu.Lock()
	client.calls = nil
	client.mu.Unlock()
	response, state, err := s.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningHigh, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}})
	if err != nil {
		t.Fatal(err)
	}
	if !sentCommand(client, native.CommandSetThinkingLevel) || !sentCommand(client, native.CommandSetAutoCompaction) {
		t.Fatalf("the update sent %+v, want set_thinking_level and set_auto_compaction", client.calls)
	}
	client.mu.Lock()
	running := client.state.ThinkingLevel
	var enabled *bool
	for _, c := range client.calls {
		if c.Type == native.CommandSetAutoCompaction {
			enabled = c.Enabled
		}
	}
	client.mu.Unlock()
	if running != native.ThinkingHigh || enabled == nil || *enabled {
		t.Fatalf("Pi runs at %s with auto compaction %v, want high and off", running, enabled)
	}
	if response.PreviousReasoningLevel != protocol.ReasoningMedium || response.ReasoningLevel != protocol.ReasoningHigh || response.CompactionPolicy == nil || response.CompactionPolicy.Kind != protocol.CompactionOff {
		t.Fatalf("response %+v, want medium to high and an off policy", response)
	}
	if state.ReasoningLevel != protocol.ReasoningHigh || state.CompactionPolicy == nil || state.CompactionPolicy.Kind != protocol.CompactionOff {
		t.Fatalf("state reports %q and %+v, want the updated settings", state.ReasoningLevel, state.CompactionPolicy)
	}
	polled, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if polled.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("a later state reports %q, want the level Pi confirmed", polled.ReasoningLevel)
	}
}

func TestALiveLevelPiDoesNotConfirmIsRefusedAndPiIsPutBack(t *testing.T) {
	client := newFakeClient()
	followLevels(client, native.ThinkingMax)
	s := openTest(t, client, 32)
	_, _, err := s.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningMax, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("the update answered %v, want an unsatisfiable reasoning level", err)
	}
	client.mu.Lock()
	running := client.state.ThinkingLevel
	last := client.calls[len(client.calls)-1]
	client.mu.Unlock()
	if running != native.ThinkingMedium || last.Type != native.CommandSetThinkingLevel || last.Level != native.ThinkingMedium {
		t.Fatalf("Pi runs at %s after %+v, want the medium level it replaced restored", running, last)
	}
	if sentCommand(client, native.CommandSetAutoCompaction) {
		t.Fatal("a refused update still switched Pi's compaction")
	}
}

func TestALiveUpdateIsRefusedWhatPiCannotDo(t *testing.T) {
	client := newFakeClient()
	s := openTest(t, client, 32)
	var refusal *base.UnsupportedControlError
	if _, _, err := s.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 1000}}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureCompactionPolicy || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("a token threshold answered %v, want it unsatisfiable", err)
	}
	if _, _, err := s.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "other", ReasoningLevel: protocol.ReasoningHigh}); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("another session answered %v, want run_not_found", err)
	}
	submitTest(t, s)
	if _, _, err := s.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningHigh}); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("a busy session answered %v, want run_active", err)
	}
	if sentCommand(client, native.CommandSetThinkingLevel) || sentCommand(client, native.CommandSetAutoCompaction) {
		t.Fatal("a refused update reached Pi")
	}
}
