package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// Extension dialogs are emitted with the participant as the responder; an empty
// identity would produce schema-invalid events that no valid resolution could
// satisfy, so the open must be refused before any process is started.
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

// A requested model cannot be applied: the prompt carries no model selection
// and no set_model is issued, so the submission must be rejected rather than
// reporting the request as the effective model (which would misattribute the
// run to a model Pi never used).
func TestSubmitRejectsUnappliedModelID(t *testing.T) {
	s := openTest(t, newFakeClient(), 32)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := s.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: "glm-other",
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}); !errors.Is(err, ErrUnsupportedInput) {
		t.Fatalf("got %v, want ErrUnsupportedInput", err)
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

// An explicitly empty Environment is an empty allowlist, not "inherit". The
// adapter must forward it non-nil so rpc.Start installs an empty environment.
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
	_ = adaptertest.Drain(t, stream, time.Second)
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
	_, stream := submitTest(t, s)
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
