package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
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
	switch kind {
	case "opencode-message":
		return fmt.Sprintf("msg_fake%016d", g.n)
	default:
		return fmt.Sprintf("%s-%d", kind, g.n)
	}
}

type fakeSubscription struct {
	events chan native.Event
	done   chan struct{}
	once   sync.Once
	mu     sync.Mutex
	err    error
}

func (s *fakeSubscription) Events() <-chan native.Event { return s.events }
func (s *fakeSubscription) Done() <-chan struct{}       { return s.done }
func (s *fakeSubscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
func (s *fakeSubscription) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
func (s *fakeSubscription) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
	s.Close()
}

type fakeClient struct {
	mu               sync.Mutex
	session          native.SessionID
	events           chan native.Event
	subscription     *fakeSubscription
	promptErr        error
	foreignAdmission bool
	interrupts       int
	inboxCancels     []native.MessageID
	silentCancels    bool
	stalls           bool
	cancelSeq        int64
	actives          int
	activeErr        error
	activeFor        int
	lastPromptID     native.MessageID
	subscribeCtx     context.Context
	prompts          []native.PromptRequest
	closed           bool

	promptGate  <-chan struct{}
	promptEntry chan struct{}

	model       *native.ModelRef
	switches    []native.ModelRef
	keepVariant bool
	created     *native.ModelRef

	sessionErr error
	creates    int

	listed    []native.SessionInfo
	listErr   error
	listAsked []string
}

func newFakeClient() *fakeClient {
	events := make(chan native.Event, 256)
	return &fakeClient{session: "ses_fake00000000000000", events: events, subscription: &fakeSubscription{events: events, done: make(chan struct{})}, cancelSeq: 1000}
}

func fakeSessionInfo(id native.SessionID, model *native.ModelRef) native.SessionInfo {
	info := native.SessionInfo{ID: id, ProjectID: "prj_fake", Model: model, Location: json.RawMessage(`{"directory":"/w"}`)}
	info.Time.Created, info.Time.Updated = 1, 1
	return info
}

func (f *fakeClient) CreateSession(_ context.Context, request httpapi.CreateSessionRequest) (native.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	f.created = request.Model
	return fakeSessionInfo(f.session, f.model), nil
}
func (f *fakeClient) Session(_ context.Context, session native.SessionID) (native.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessionErr != nil {
		return native.SessionInfo{}, f.sessionErr
	}
	return fakeSessionInfo(session, f.model), nil
}
func (f *fakeClient) SwitchModel(_ context.Context, _ native.SessionID, model native.ModelRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switches = append(f.switches, model)
	if !f.keepVariant {
		f.model = &model
	}
	return nil
}
func (f *fakeClient) Prompt(_ context.Context, session native.SessionID, request native.PromptRequest) (native.Admitted, error) {
	f.mu.Lock()
	gate, entry := f.promptGate, f.promptEntry
	f.mu.Unlock()
	if entry != nil {
		entry <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.promptErr != nil {
		return native.Admitted{}, f.promptErr
	}
	id := request.ID
	if f.foreignAdmission {
		id = "msg_foreign"
	} else {
		f.lastPromptID = request.ID
		f.prompts = append(f.prompts, request)
	}
	admitted := native.Admitted{ID: id, SessionID: session, Type: "user", Payload: json.RawMessage(`{"text":"` + request.Text + `"}`), Delivery: request.Delivery}
	admitted.Time.Created = 1
	if admitted.Delivery == "" {
		admitted.Delivery = native.DeliverySteer
	}
	return admitted, nil
}
func (f *fakeClient) Interrupt(ctx context.Context, _ native.SessionID) (bool, error) {
	f.mu.Lock()
	f.interrupts++
	stalls := f.stalls
	f.mu.Unlock()
	if stalls {
		<-ctx.Done()
		return false, ctx.Err()
	}
	return true, nil
}
func (f *fakeClient) CancelInbox(_ context.Context, _ native.SessionID, inbox native.MessageID) error {
	f.mu.Lock()
	f.inboxCancels = append(f.inboxCancels, inbox)
	f.cancelSeq++
	seq, silent := f.cancelSeq, f.silentCancels
	f.mu.Unlock()
	if !silent {
		f.events <- f.event(seq, native.TypeInboxCancelled, native.InboxRefData{SessionID: f.session, InboxID: inbox})
	}
	return nil
}
func (f *fakeClient) Sessions(_ context.Context, directory string, limit int) ([]native.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listAsked = append(f.listAsked, fmt.Sprintf("%s|%d", directory, limit))
	return f.listed, f.listErr
}
func (f *fakeClient) Active(ctx context.Context) (map[native.SessionID]bool, error) {
	f.mu.Lock()
	f.actives++
	err := f.activeErr
	running := false
	if f.activeFor > 0 {
		f.activeFor--
		running = true
	}
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if running {
		return map[native.SessionID]bool{f.session: true}, nil
	}
	return map[native.SessionID]bool{}, nil
}
func (f *fakeClient) Subscribe(ctx context.Context, _ native.SessionID) (Subscription, error) {
	f.mu.Lock()
	f.subscribeCtx = ctx
	stalls := f.stalls
	f.mu.Unlock()
	if stalls {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return f.subscription, nil
}
func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeClient) event(seq int64, typ native.Type, payload any) native.Event {
	data, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	event := native.Event{ID: native.EventID(fmt.Sprintf("evt_fake%04d", seq)), Type: typ, Data: data, SessionID: f.session}
	if typ.Durable() {
		event.Durable = &native.DurablePosition{AggregateID: string(f.session), Seq: seq, Version: 1}
	}
	return event
}

func (f *fakeClient) emit(t *testing.T, seq int64, typ native.Type, payload any) {
	t.Helper()
	f.events <- f.event(seq, typ, payload)
}

func (f *fakeClient) deliver(t *testing.T, seq int64, inbox native.MessageID) {
	t.Helper()
	f.emit(t, seq, native.TypeInboxDelivered, native.InboxRefData{SessionID: f.session, InboxID: inbox})
}

func (f *fakeClient) succeed(t *testing.T, seq int64) {
	t.Helper()
	f.emit(t, seq, native.TypeExecutionSucceeded, native.ExecutionData{SessionID: f.session})
}

func (f *fakeClient) interrupted(t *testing.T, seq int64) {
	t.Helper()
	f.emit(t, seq, native.TypeExecutionInterrupted, native.ExecutionInterruptedData{SessionID: f.session, Reason: "user"})
}

func (f *fakeClient) toolCalled(t *testing.T, inputSeq, calledSeq int64, id, name string, input map[string]any) {
	t.Helper()
	f.emit(t, inputSeq, native.TypeToolInputStarted, native.ToolInputStartedData{SessionID: f.session, AssistantMessage: "msg_a1", ID: id, Name: name})
	f.emit(t, calledSeq, native.TypeToolCalled, native.ToolCalledData{SessionID: f.session, AssistantMessage: "msg_a1", ID: id, Input: input})
}

func TestTokenCountSaturates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value float64
		want  uint64
	}{
		{"zero", 0, 0},
		{"fraction", 2.9, 2},
		{"negative", -1, 0},
		{"nan", math.NaN(), 0},
		{"positive infinity", math.Inf(1), math.MaxUint64},
		{"beyond uint64", 1e300, math.MaxUint64},
		{"at the boundary", 18446744073709551616.0, math.MaxUint64},
		{"just below the boundary", 18446744073709549568.0, 18446744073709549568},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenCount(tc.value); got != tc.want {
				t.Fatalf("tokenCount(%v) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestProbeAdvertisesQueueAndRefusesSteer(t *testing.T) {
	a, err := New(Config{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := descriptor.Capabilities.Features["session.message.delivery.queue"].Level; got != protocol.SupportNative {
		t.Fatalf("queue = %s, want native", got)
	}
	if got := descriptor.Capabilities.Features["session.message.delivery.steer"].Level; got != protocol.SupportUnavailable {
		t.Fatalf("steer = %s, want unavailable", got)
	}
	limits := descriptor.Capabilities.Limits
	if limits == nil || limits.MaxQueuedRunsPerSession == nil || *limits.MaxQueuedRunsPerSession != 1 ||
		limits.MaxActiveRunsPerSession == nil || *limits.MaxActiveRunsPerSession != 2 {
		t.Fatalf("limits = %+v", limits)
	}
}

func openTest(t *testing.T, client *fakeClient, capacity int) (base.Session, *fakeSubscription) {
	t.Helper()
	subscription := client.subscription
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity})
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, subscription
}

func submitTest(t *testing.T, session base.Session) (protocol.MessageSubmitResponse, base.EventStream) {
	t.Helper()
	response, stream, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
	if err != nil {
		t.Fatal(err)
	}
	return response, stream
}

func TestSubmitRefusesEveryUnadvertisedControl(t *testing.T) {
	session, _ := openTest(t, newFakeClient(), 32)
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}
	for feature, request := range map[string]protocol.MessageSubmitRequest{
		protocol.FeatureInstructions:     {SessionID: "session", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse"), Messages: message},
		protocol.FeatureModelSelection:   {SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("other-model"), Messages: message},
		protocol.FeatureStructuredOutput: {SessionID: "session", Delivery: protocol.DeliveryAuto, OutputSchema: json.RawMessage(`{"type":"object"}`), Messages: message},
		protocol.FeatureToolSelection:    {SessionID: "session", Delivery: protocol.DeliveryAuto, ToolChoice: json.RawMessage(`"none"`), Messages: message},
	} {
		_, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: request})
		var refusal *base.UnsupportedControlError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: got %v, want an *adapter.UnsupportedControlError", feature, err)
		}
		if refusal.Feature != feature || refusal.Reason != base.ControlUnadvertised {
			t.Fatalf("%s: refused as %q/%q", feature, refusal.Feature, refusal.Reason)
		}
	}
}

func TestSubmitNormalizesOmittedDelivery(t *testing.T) {
	session, _ := openTest(t, newFakeClient(), 32)
	response, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "session",
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response.RequestedDelivery != protocol.DeliveryAuto {
		t.Fatalf("requested_delivery = %q, want %q", response.RequestedDelivery, protocol.DeliveryAuto)
	}
}

func TestSessionRetainsNativeModel(t *testing.T) {
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "claude-sonnet", ProviderID: "anthropic"}
	session, _ := openTest(t, client, 32)
	response, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if response.ModelID != "anthropic/claude-sonnet" {
		t.Fatalf("admission model = %q, want %q", response.ModelID, "anthropic/claude-sonnet")
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentModelID != "anthropic/claude-sonnet" {
		t.Fatalf("state model = %q, want %q", state.CurrentModelID, "anthropic/claude-sonnet")
	}
}

func types(events []protocol.Envelope) []protocol.EnvelopeType {
	out := make([]protocol.EnvelopeType, len(events))
	for i := range events {
		out[i] = events[i].Type
	}
	return out
}

func TestACompletedRunSettlesWhenTheExecutionSucceedsAndNotBefore(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_assistant1", Agent: "build", Model: native.ModelRef{ID: "m", ProviderID: "p"}})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_assistant1", Text: "done"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_assistant1", Finish: "stop", Tokens: tokenAccounting(2, 5)})
	events := []protocol.Envelope{adaptertest.Next(t, stream, time.Second), adaptertest.Next(t, stream, time.Second)}
	select {
	case result := <-stream:
		t.Fatalf("the run settled before its execution ended: %s", result.Envelope.Type)
	case <-time.After(30 * time.Millisecond):
	}
	client.succeed(t, 5)
	events = append(events, adaptertest.Drain(t, stream, time.Second)...)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.StopReason != "stop" || completed.Usage == nil || completed.Usage.InputTokens != 2 || completed.Usage.OutputTokens != 5 {
		t.Fatalf("completed=%+v", completed)
	}
}

func tokenAccounting(in, out float64) (tokens struct {
	Input     float64 `json:"input"`
	Output    float64 `json:"output"`
	Reasoning float64 `json:"reasoning"`
	Cache     struct {
		Read  float64 `json:"read"`
		Write float64 `json:"write"`
	} `json:"cache"`
}) {
	tokens.Input = in
	tokens.Output = out
	return tokens
}

func TestToolLifecycleSettlesBeforeTerminal(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.toolCalled(t, 25, 30, "call_1", "read", map[string]any{"path": "/x"})
	client.emit(t, 4, native.TypeToolProgress, native.ToolProgressData{SessionID: client.session, AssistantMessage: "msg_a1", ID: "call_1", Metadata: json.RawMessage(`{"text":"half"}`)})
	client.emit(t, 5, native.TypeToolSuccess, native.ToolSuccessData{SessionID: client.session, AssistantMessage: "msg_a1", ID: "call_1", Executed: true, Content: []native.ToolContent{{Type: "text", Text: "done"}}})
	client.emit(t, 6, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "answer"})
	client.emit(t, 7, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use"})
	client.succeed(t, 8)
	events := adaptertest.Drain(t, stream, time.Second)

	_ = response
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
	terminals := 0
	for _, event := range events {
		switch event.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminals=%d", terminals)
	}
}

func TestMultiStepTurnSettlesOnce(t *testing.T) {
	client := newFakeClient()

	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	messageID := native.MessageID(response.MessageIDs[0])
	client.deliver(t, 1, messageID)
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use"})

	client.emit(t, 4, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 5, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Text: "final"})
	client.emit(t, 6, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})
	client.succeed(t, 7)
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
}

func TestCancelAcknowledgesIntentAndSettlesCancelled(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	messageID := native.MessageID(response.MessageIDs[0])
	client.deliver(t, 1, messageID)
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	if started := adaptertest.Next(t, stream, time.Second); started.Type != protocol.TypeRunStarted {
		t.Fatalf("first=%s", started.Type)
	}
	cancelled, err := session.Cancel(context.Background(), response.RunID)
	if err != nil || !cancelled.Accepted || cancelled.Status != protocol.RunCancelling {
		t.Fatalf("cancel=%+v err=%v", cancelled, err)
	}
	client.emit(t, 3, native.TypeStepFailed, native.StepFailedData{SessionID: client.session, AssistantMessage: "msg_a1", Error: native.SessionError{Type: "aborted", Message: "Step interrupted"}})
	client.interrupted(t, 4)
	events := adaptertest.Drain(t, stream, time.Second)
	want := []protocol.EnvelopeType{protocol.TypeRunStatusUpdated, protocol.TypeRunCancelled}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
}

func TestAnAdmissionStaysQueuedUntilItsInputIsDelivered(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("hello")})
	if err != nil {
		t.Fatal(err)
	}
	if response.Admission != protocol.AdmissionQueued || response.Status != protocol.RunQueued {
		t.Fatalf("response=%+v", response)
	}
	select {
	case result := <-stream:
		t.Fatalf("premature event %s", result.Envelope.Type)
	case <-time.After(20 * time.Millisecond):
	}
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 3)
	events := adaptertest.Drain(t, stream, time.Second)
	if events[0].Type != protocol.TypeRunStarted {
		t.Fatalf("events=%v", types(events))
	}
}

func TestForeignSessionEventFailsRun(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	foreign := client.event(1, native.TypeInboxDelivered, native.InboxRefData{SessionID: "ses_other", InboxID: native.MessageID(response.MessageIDs[0])})
	foreign.SessionID = "ses_other"
	client.events <- foreign
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) != 1 || events[0].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", types(events))
	}
	if _, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}}); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("second submit err=%v", err)
	}
}

func TestPreStartFailuresReportReservation(t *testing.T) {
	cases := map[string]func(*fakeClient){
		"foreign admission": func(client *fakeClient) { client.foreignAdmission = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			client := newFakeClient()
			setup(client)
			session, _ := openTest(t, client, 32)
			response, stream, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
			if err != nil {
				t.Fatalf("submit paired an error with a stream: %v", err)
			}
			if !response.Accepted || response.Admission != protocol.AdmissionQueued || response.EffectiveDelivery != protocol.EffectiveDeliveryQueue || response.Status != protocol.RunQueued || response.RunID == "" {
				t.Fatalf("reservation = %+v", response)
			}
			events := adaptertest.Drain(t, stream, time.Second)
			if len(events) != 1 || events[0].Type != protocol.TypeRunFailed {
				t.Fatalf("events=%v", types(events))
			}
			var payload protocol.RunFailedPayload
			if err := events[0].DecodePayload(&payload); err != nil || payload.Error.Code != "opencode_foreign_admission" {
				t.Fatalf("payload = %+v err=%v", payload, err)
			}
			if _, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}}); !errors.Is(err, base.ErrSessionClosed) {
				t.Fatalf("second submit err=%v", err)
			}
		})
	}
}

func TestAdmissionFailureRetiresSession(t *testing.T) {
	client := newFakeClient()
	client.promptErr = errors.New("HTTP 409")
	session, _ := openTest(t, client, 32)

	response, stream, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Accepted || response.Admission != protocol.AdmissionQueued || response.EffectiveDelivery != protocol.EffectiveDeliveryQueue || response.RunID == "" {
		t.Fatalf("reservation = %+v", response)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) != 1 || events[0].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", types(events))
	}
	if _, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}}); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("second submit err=%v", err)
	}
}

func TestStreamFailureProjectsFailure(t *testing.T) {
	client := newFakeClient()
	session, subscription := openTest(t, client, 32)
	_, stream := submitTest(t, session)
	subscription.fail(errors.New("connection reset"))
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) != 1 || events[0].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", types(events))
	}
}

func TestStepFailureFailsRun(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepFailed, native.StepFailedData{SessionID: client.session, AssistantMessage: "msg_a1", Error: native.SessionError{Type: "unknown", Message: "provider exploded"}})
	client.succeed(t, 4)
	events := adaptertest.Drain(t, stream, time.Second)
	if last := events[len(events)-1]; last.Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", types(events))
	}
	var failed protocol.RunFailedPayload
	if err := events[len(events)-1].DecodePayload(&failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Code != "opencode_step_failed" || failed.Error.Message != "provider exploded" {
		t.Fatalf("failed=%+v", failed)
	}
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
}

func TestAFailedExecutionFailsTheRunWithItsError(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeExecutionFailed, native.ExecutionFailedData{SessionID: client.session, Error: native.SessionError{Type: "provider", Message: "the provider refused"}})
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	var failed protocol.RunFailedPayload
	if err := events[len(events)-1].DecodePayload(&failed); err != nil || events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v err=%v", types(events), err)
	}
	if failed.Error.Code != "opencode_execution_failed" || failed.Error.Message != "the provider refused" {
		t.Fatalf("failed=%+v", failed)
	}
}

func TestAnInterruptNobodyAskedForFailsTheRunNamingItsReason(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeExecutionInterrupted, native.ExecutionInterruptedData{SessionID: client.session, Reason: "shutdown"})
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	var failed protocol.RunFailedPayload
	if err := events[len(events)-1].DecodePayload(&failed); err != nil || events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v err=%v", types(events), err)
	}
	if failed.Error.Code != "opencode_execution_interrupted" || !strings.Contains(failed.Error.Message, "shutdown") {
		t.Fatalf("failed=%+v", failed)
	}
}

func TestReplayGapAndTerminalReplay(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 2)
	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "a"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "b"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 5)
	_ = adaptertest.Drain(t, stream, time.Second)
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
	events := adaptertest.Drain(t, replay, time.Second)
	if recovery.ReplayedThrough != 4 || len(events) != 2 || events[1].Type != protocol.TypeRunCompleted {
		t.Fatalf("recovery=%+v events=%v", recovery, types(events))
	}
}

func TestOpenSubscriptionSurvivesOpenContextCancel(t *testing.T) {
	client := newFakeClient()
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session, err := adapter.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	cancel()
	client.mu.Lock()
	subCtx := client.subscribeCtx
	client.mu.Unlock()
	if subCtx == nil {
		t.Fatal("subscription did not receive a context")
	}
	if subCtx.Err() != nil {
		t.Fatalf("subscription reused the cancelled Open context: %v", subCtx.Err())
	}

	response, stream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 3)
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) == 0 || events[0].Type != protocol.TypeRunStarted {
		t.Fatalf("events=%v", types(events))
	}
}

type gatedPromptClient struct {
	*fakeClient
	entered chan struct{}
	release chan struct{}
}

func (g *gatedPromptClient) Prompt(ctx context.Context, session native.SessionID, request native.PromptRequest) (native.Admitted, error) {
	g.mu.Lock()
	g.lastPromptID = request.ID
	g.mu.Unlock()
	close(g.entered)
	<-g.release
	return g.fakeClient.Prompt(ctx, session, request)
}

func TestADeliveryBeforeThePromptResponseStartsTheRun(t *testing.T) {
	client := newFakeClient()
	gated := &gatedPromptClient{fakeClient: client, entered: make(chan struct{}), release: make(chan struct{})}
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return gated, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := adapter.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })
	submitted := make(chan struct {
		response protocol.MessageSubmitResponse
		stream   base.EventStream
		err      error
	}, 1)
	go func() {
		response, stream, err := sess.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
		submitted <- struct {
			response protocol.MessageSubmitResponse
			stream   base.EventStream
			err      error
		}{response, stream, err}
	}()
	<-gated.entered
	client.mu.Lock()
	messageID := client.lastPromptID
	client.mu.Unlock()

	client.deliver(t, 1, messageID)
	client.emit(t, 2, native.TypeAgentSelected, map[string]any{"sessionID": client.session, "agent": "build"})
	concrete := sess.(*session)
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		concrete.mu.Lock()
		handled := concrete.reduced[2]
		concrete.mu.Unlock()
		if handled {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(gated.release)
	result := <-submitted
	if result.err != nil {
		t.Fatal(result.err)
	}
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 4)
	events := adaptertest.Drain(t, result.stream, time.Second)
	if len(events) == 0 || events[0].Type != protocol.TypeRunStarted {
		t.Fatalf("events=%v", types(events))
	}
}

func queueRequest(text string) protocol.MessageSubmitRequest {
	return protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryQueue, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}}}
}

func autoRequest(text string) protocol.MessageSubmitRequest {
	return protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}}}
}

func testAdapterDescriptor(t *testing.T) base.Descriptor {
	t.Helper()
	a, err := New(Config{Endpoint: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func TestExplicitQueueReservesAndPromotesAfterSettlement(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)
	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})

	queued, queuedStream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Admission != protocol.AdmissionQueued || queued.EffectiveDelivery != protocol.EffectiveDeliveryQueue || queued.Status != protocol.RunQueued {
		t.Fatalf("reservation = %+v", queued)
	}
	if queued.RequestedDelivery != protocol.DeliveryQueue {
		t.Fatalf("requested_delivery = %q", queued.RequestedDelivery)
	}

	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 2 || state.ActiveRuns[0].RunID != first.RunID || state.ActiveRuns[1].RunID != queued.RunID {
		t.Fatalf("active_runs = %+v", state.ActiveRuns)
	}
	if state.ActiveRuns[1].QueuePosition == nil || *state.ActiveRuns[1].QueuePosition != 1 || state.ActiveRuns[0].QueuePosition != nil {
		t.Fatalf("queue positions = %+v", state.ActiveRuns)
	}
	if state.ActiveRunID != first.RunID {
		t.Fatalf("active_run_id = %q", state.ActiveRunID)
	}

	if _, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: autoRequest("too much")}); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("third submit = %v, want ErrRunActive", err)
	}

	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "first"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.deliver(t, 5, native.MessageID(queued.MessageIDs[0]))
	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)
	if len(firstEvents) == 0 || firstEvents[len(firstEvents)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("first run = %v", types(firstEvents))
	}

	client.emit(t, 6, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 7, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Text: "second"})
	client.emit(t, 8, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})
	client.succeed(t, 9)
	queuedEvents := adaptertest.Drain(t, queuedStream, 2*time.Second)
	if len(queuedEvents) == 0 || queuedEvents[0].Type != protocol.TypeRunStarted {
		t.Fatalf("promoted run = %v", types(queuedEvents))
	}
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
		{Request: queueRequest("later"), Admission: queued},
	}, descriptor, append(append([]protocol.Envelope(nil), firstEvents...), queuedEvents...))
}

func TestBusyAutoReservesAndCancelsBeforePromotion(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)
	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})

	queued, queuedStream, err := session.Submit(context.Background(), base.SubmitRequest{Request: autoRequest("later")})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Admission != protocol.AdmissionQueued || queued.DeliveryResolution != "session_busy" {
		t.Fatalf("busy auto reservation = %+v", queued)
	}
	if _, err := session.Cancel(context.Background(), queued.RunID); err != nil {
		t.Fatal(err)
	}
	queuedEvents := adaptertest.Drain(t, queuedStream, 2*time.Second)
	if len(queuedEvents) != 1 || queuedEvents[0].Type != protocol.TypeRunCancelled {
		t.Fatalf("cancelled reservation = %v", types(queuedEvents))
	}

	var cancelled protocol.RunCancelledPayload
	if err := queuedEvents[0].DecodePayload(&cancelled); err != nil {
		t.Fatal(err)
	}
	if cancelled.SettledBy != "" {
		t.Fatalf("settled_by = %q, want the native evidence of session.inbox.cancelled", cancelled.SettledBy)
	}
	client.mu.Lock()
	cancels := append([]native.MessageID(nil), client.inboxCancels...)
	prompts := append([]native.PromptRequest(nil), client.prompts...)
	client.mu.Unlock()
	if len(cancels) != 1 || cancels[0] != native.MessageID(queued.MessageIDs[0]) {
		t.Fatalf("inbox cancels = %v, want the reservation's own input", cancels)
	}
	if len(prompts) != 2 || prompts[1].Delivery != native.DeliveryQueue {
		t.Fatalf("a busy auto was sent as %+v, want delivery=queue", prompts)
	}
	if queuedEvents[0].Extensions[costExtension] == nil {
		t.Fatalf("a reservation cancelled before promotion carried no cost: %v", queuedEvents[0].Extensions)
	}

	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 1 || state.ActiveRuns[0].RunID != first.RunID {
		t.Fatalf("active_runs after release = %+v", state.ActiveRuns)
	}
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "first"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 5)
	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)

	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
		{Request: autoRequest("later"), Admission: queued, Cancelled: true},
	}, descriptor, append(append([]protocol.Envelope(nil), queuedEvents...), firstEvents...))
}

func TestTheQueuedInputsDeliveryEndsThePreviousRunAndStartsItsOwn(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)

	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "first"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})

	queued, queuedStream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}

	client.deliver(t, 5, native.MessageID(queued.MessageIDs[0]))
	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)
	if fmt.Sprint(types(firstEvents)) != fmt.Sprint([]protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}) {
		t.Fatalf("first run = %v", types(firstEvents))
	}
	client.emit(t, 6, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 7, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Text: "second"})
	if started := adaptertest.Next(t, queuedStream, 2*time.Second); started.Type != protocol.TypeRunStarted {
		t.Fatalf("promoted run began with %s", started.Type)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 1 || state.ActiveRuns[0].RunID != queued.RunID || state.ActiveRuns[0].Status != protocol.RunRunning {
		t.Fatalf("active_runs after the delivery = %+v", state.ActiveRuns)
	}
	if text := deltaText(t, firstEvents[1]); text != "first" {
		t.Fatalf("first run took the promoted turn's text: %q", text)
	}
	client.emit(t, 8, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})
	client.succeed(t, 9)
	queuedEvents := append([]protocol.Envelope{{Type: protocol.TypeRunStarted}}, adaptertest.Drain(t, queuedStream, 2*time.Second)...)
	if fmt.Sprint(types(queuedEvents)) != fmt.Sprint([]protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}) {
		t.Fatalf("promoted run = %v", types(queuedEvents))
	}
	if text := deltaText(t, queuedEvents[1]); text != "second" {
		t.Fatalf("promoted run's text = %q", text)
	}
	_ = descriptor
}

func TestExplicitQueueOnIdleSessionStaysQueued(t *testing.T) {
	client := newFakeClient()
	session, stream := openTest(t, client, 64)
	_ = stream
	descriptor := testAdapterDescriptor(t)
	admission, events, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("go")})
	if err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionQueued || admission.EffectiveDelivery != protocol.EffectiveDeliveryQueue || admission.Status != protocol.RunQueued {
		t.Fatalf("explicit idle queue = %+v", admission)
	}
	client.deliver(t, 1, native.MessageID(admission.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "done"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 5)
	collected := adaptertest.Drain(t, events, 2*time.Second)
	if len(collected) == 0 || collected[0].Type != protocol.TypeRunStarted {
		t.Fatalf("promotion = %v", types(collected))
	}

	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 0 || state.Status != protocol.SessionIdle {
		t.Fatalf("settled state = %+v", state)
	}
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: queueRequest("go"), Admission: admission},
	}, descriptor, collected)
}

func deltaText(t *testing.T, envelope protocol.Envelope) string {
	t.Helper()
	var payload protocol.ContentDeltaPayload
	if err := envelope.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	return payload.Part.Text
}

func TestCloseRefusesWhileAReservationIsLive(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})

	queued, queuedStream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}

	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 4)
	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)
	if len(firstEvents) == 0 || firstEvents[len(firstEvents)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("first run = %v", types(firstEvents))
	}

	if err := session.Close(context.Background()); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("close with a live reservation = %v, want ErrRunActive", err)
	}

	if _, err := session.Cancel(context.Background(), queued.RunID); err != nil {
		t.Fatal(err)
	}
	queuedEvents := adaptertest.Drain(t, queuedStream, 2*time.Second)
	if len(queuedEvents) != 1 || queuedEvents[0].Type != protocol.TypeRunCancelled {
		t.Fatalf("cancelled reservation = %v", types(queuedEvents))
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("close after the reservation settled: %v", err)
	}
}

func TestProvisionalRunIsNotProjectedBeforeItsAdmission(t *testing.T) {
	client := newFakeClient()
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	release := func() { once.Do(func() { close(gate) }) }

	defer release()
	client.mu.Lock()
	client.promptGate, client.promptEntry = gate, entered
	client.mu.Unlock()
	session, _ := openTest(t, client, 64)

	admitted := make(chan protocol.MessageSubmitResponse, 1)
	go func() {
		defer close(admitted)
		response, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: autoRequest("hello")})
		if err == nil {
			admitted <- response
		}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the prompt never reached the client")
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle || state.ActiveRunID != "" || len(state.ActiveRuns) != 0 {
		t.Fatalf("a run with no admission response was projected: %s / %q / %+v", state.Status, state.ActiveRunID, state.ActiveRuns)
	}

	release()
	response, ok := <-admitted
	if !ok {
		t.Fatal("the submission failed")
	}
	state, err = session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 1 || state.ActiveRuns[0].RunID != response.RunID {
		t.Fatalf("the admitted run is not projected: %+v", state.ActiveRuns)
	}
}

func TestStateDuringARunValidates(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)

	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	started := adaptertest.Next(t, firstStream, 2*time.Second)
	if started.Type != protocol.TypeRunStarted {
		t.Fatalf("first envelope = %s", started.Type)
	}
	queued, queuedStream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != protocol.SessionRunning || snapshot.ActiveRunID != first.RunID || len(snapshot.ActiveRuns) != 2 {
		t.Fatalf("snapshot = %s / %q / %+v", snapshot.Status, snapshot.ActiveRunID, snapshot.ActiveRuns)
	}

	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "first"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.deliver(t, 5, native.MessageID(queued.MessageIDs[0]))
	firstEvents := append([]protocol.Envelope{started}, adaptertest.Drain(t, firstStream, 2*time.Second)...)
	client.emit(t, 6, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 7, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Text: "second"})
	client.emit(t, 8, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})
	client.succeed(t, 9)
	queuedEvents := adaptertest.Drain(t, queuedStream, 2*time.Second)

	exchange, err := adaptertest.StateExchange(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	events := append(append([]protocol.Envelope(nil), firstEvents...), queuedEvents...)
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
		{Request: queueRequest("later"), Admission: queued},
	}, descriptor, adaptertest.SpliceAfter(t, events, started.ID, exchange))
}

func TestHandedOutStateDoesNotAliasTheSession(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)

	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	if started := adaptertest.Next(t, firstStream, 2*time.Second); started.Type != protocol.TypeRunStarted {
		t.Fatalf("first envelope = %s", started.Type)
	}
	queued, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}

	recovery, _, err := session.Resume(context.Background(), base.ResumeRequest{RunID: first.RunID, AfterSequence: 0})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, given := range map[string]protocol.SessionState{"State": snapshot, "Resume": recovery.State} {
		if len(given.ActiveRuns) != 2 {
			t.Fatalf("%s: active_runs = %+v", name, given.ActiveRuns)
		}
		given.ActiveRuns[0].RunID = "tampered"
		given.ActiveRuns[1].Status = protocol.RunCompleted
		if position := given.ActiveRuns[1].QueuePosition; position != nil {
			*position = 99
		}
		if sequence := given.ActiveRuns[0].AsOfSequence; sequence != nil {
			*sequence = 99
		}
	}

	after, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.ActiveRuns) != 2 || after.ActiveRuns[0].RunID != first.RunID || after.ActiveRuns[1].RunID != queued.RunID {
		t.Fatalf("the session's own runs were edited through a snapshot: %+v", after.ActiveRuns)
	}
	if after.ActiveRuns[1].Status != protocol.RunQueued {
		t.Fatalf("reservation status = %s, edited through a snapshot", after.ActiveRuns[1].Status)
	}
	if position := after.ActiveRuns[1].QueuePosition; position == nil || *position != 1 {
		t.Fatalf("queue position = %s, edited through a snapshot", describeSequence(nil))
	}
	if sequence := after.ActiveRuns[0].AsOfSequence; sequence == nil || *sequence != 1 {
		t.Fatalf("capture position = %s, edited through a snapshot", describeSequence(sequence))
	}
}

func TestIdleExplicitQueueIsProjectedAsAReservation(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)

	queued, stream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}
	if queued.Admission != protocol.AdmissionQueued || queued.EffectiveDelivery != protocol.EffectiveDeliveryQueue || queued.Status != protocol.RunQueued {
		t.Fatalf("idle explicit queue = %+v", queued)
	}
	if queued.DeliveryResolution != "" {
		t.Fatalf("nothing was running, so nothing resolved: %q", queued.DeliveryResolution)
	}

	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionQueued || state.ActiveRunID != "" {
		t.Fatalf("session holding only a reservation = %s / %q", state.Status, state.ActiveRunID)
	}
	if len(state.ActiveRuns) != 1 {
		t.Fatalf("active_runs = %+v", state.ActiveRuns)
	}
	entry := state.ActiveRuns[0]
	if entry.RunID != queued.RunID || entry.Status != protocol.RunQueued {
		t.Fatalf("entry = %+v, want the reservation the response reported", entry)
	}
	if entry.QueuePosition == nil || *entry.QueuePosition != 1 {
		t.Fatalf("a reservation holds its place in the queue: %+v", entry)
	}

	if _, _, err := session.Submit(context.Background(), base.SubmitRequest{Request: autoRequest("second")}); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("second submission behind a reservation = %v, want run_active", err)
	}

	client.deliver(t, 1, native.MessageID(queued.MessageIDs[0]))
	started := adaptertest.Next(t, stream, 2*time.Second)
	if started.Type != protocol.TypeRunStarted {
		t.Fatalf("first envelope = %s, want run.started", started.Type)
	}
	state, err = session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionRunning || state.ActiveRunID != queued.RunID {
		t.Fatalf("session after the promotion = %s / %q", state.Status, state.ActiveRunID)
	}
	if len(state.ActiveRuns) != 1 || state.ActiveRuns[0].Status != protocol.RunRunning || state.ActiveRuns[0].QueuePosition != nil {
		t.Fatalf("promoted entry = %+v", state.ActiveRuns)
	}

	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "later"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 5)
	rest := adaptertest.Drain(t, stream, 2*time.Second)
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: queueRequest("later"), Admission: queued},
	}, descriptor, append([]protocol.Envelope{started}, rest...))
}

func TestUnusableSessionSettlesTheReservationToo(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)

	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})

	queued, queuedStream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}

	client.events <- native.Event{
		ID:      "evt_fake0003",
		Type:    native.TypeStepEnded,
		Durable: &native.DurablePosition{AggregateID: "ses_other000000000000", Seq: 3, Version: 1},
		Data:    json.RawMessage(`{"timestamp":3,"sessionID":"ses_other000000000000","assistantMessage":"msg_x","finish":"stop"}`),
	}

	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)
	if len(firstEvents) == 0 || firstEvents[len(firstEvents)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("started run = %v", types(firstEvents))
	}
	queuedEvents := adaptertest.Drain(t, queuedStream, 2*time.Second)
	if len(queuedEvents) != 1 || queuedEvents[0].Type != protocol.TypeRunFailed {
		t.Fatalf("reservation = %v, want one pre-start terminal", types(queuedEvents))
	}
	var failure protocol.RunFailedPayload
	if err := queuedEvents[0].DecodePayload(&failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "queue_dropped" {
		t.Fatalf("reservation failure = %q, want the code for a slot lost before promotion", failure.Error.Code)
	}

	if err := session.Close(context.Background()); err != nil {
		t.Fatalf("close after an unusable session settled its runs: %v", err)
	}
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
		{Request: queueRequest("later"), Admission: queued},
	}, descriptor, append(append([]protocol.Envelope(nil), firstEvents...), queuedEvents...))
}

func TestActiveRunsFollowThePublishedStart(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)

	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	started := adaptertest.Next(t, firstStream, 2*time.Second)
	if started.Type != protocol.TypeRunStarted {
		t.Fatalf("first envelope = %s, want run.started", started.Type)
	}

	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionRunning || state.ActiveRunID != first.RunID {
		t.Fatalf("session state after the start = %s / %q", state.Status, state.ActiveRunID)
	}
	if len(state.ActiveRuns) != 1 {
		t.Fatalf("active_runs = %+v", state.ActiveRuns)
	}
	entry := state.ActiveRuns[0]
	if entry.Status != protocol.RunRunning {
		t.Fatalf("entry status = %s, want running at a published start", entry.Status)
	}
	if entry.AsOfSequence == nil || *entry.AsOfSequence != 1 {
		t.Fatalf("entry as_of_sequence = %s, want the start's own sequence", describeSequence(entry.AsOfSequence))
	}
	if entry.QueuePosition != nil {
		t.Fatalf("a started run holds no queue position: %+v", entry)
	}

	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "first"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 5)
	rest := adaptertest.Drain(t, firstStream, 2*time.Second)
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
	}, descriptor, append([]protocol.Envelope{started}, rest...))
}

func TestStateAnchorsASettledRunItHasNotDelivered(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)

	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	started := adaptertest.Next(t, firstStream, 2*time.Second)
	if started.Type != protocol.TypeRunStarted {
		t.Fatalf("first envelope = %s", started.Type)
	}
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	client.succeed(t, 4)

	snapshot := waitIdle(t, session)
	if snapshot.ActiveRunID != "" || len(snapshot.ActiveRuns) != 0 {
		t.Fatalf("snapshot still holds the run: %+v", snapshot)
	}
	if snapshot.AsOf == nil || len(snapshot.AsOf.Settled) != 1 {
		t.Fatalf("snapshot dropped the run without saying so: %+v", snapshot.AsOf)
	}

	events := append([]protocol.Envelope{started}, adaptertest.Drain(t, firstStream, 2*time.Second)...)
	terminal := events[len(events)-1]
	if terminal.Type != protocol.TypeRunCompleted || terminal.Sequence == nil {
		t.Fatalf("last envelope = %s", terminal.Type)
	}
	if claim := snapshot.AsOf.Settled[0]; claim.RunID != first.RunID || claim.Sequence != *terminal.Sequence {
		t.Fatalf("settlement anchor = %+v, want %s at sequence %d", claim, first.RunID, *terminal.Sequence)
	}

	exchange, err := adaptertest.StateExchange(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
	}, descriptor, adaptertest.SpliceAfter(t, events, started.ID, exchange))
}

func waitIdle(t *testing.T, session base.Session) protocol.SessionState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, err := session.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if state.ActiveRunID == "" && len(state.ActiveRuns) == 0 {
			return state
		}
		if time.Now().After(deadline) {
			t.Fatalf("the run never left the projection: %+v", state)
		}
		time.Sleep(time.Millisecond)
	}
}

func describeSequence(value *uint64) string {
	if value == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *value)
}

func TestACancelledReservationsLateDeliveryEndsTheRunAndItsTurnIsQuarantined(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)

	first, firstStream := submitTest(t, session)
	client.deliver(t, 1, native.MessageID(first.MessageIDs[0]))
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Text: "first"})

	queued, queuedStream, err := session.Submit(context.Background(), base.SubmitRequest{Request: queueRequest("later")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Cancel(context.Background(), queued.RunID); err != nil {
		t.Fatal(err)
	}
	cancelled := adaptertest.Drain(t, queuedStream, 2*time.Second)
	if len(cancelled) != 1 || cancelled[0].Type != protocol.TypeRunCancelled {
		t.Fatalf("cancelled reservation = %v", types(cancelled))
	}

	client.deliver(t, 4, native.MessageID(queued.MessageIDs[0]))
	client.emit(t, 5, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 6, native.TypeTextEnded, native.TextEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Text: "abandoned"})
	client.emit(t, 7, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})

	client.subscription.fail(errors.New("stream gone"))
	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)
	if fmt.Sprint(types(firstEvents)) != fmt.Sprint([]protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}) {
		t.Fatalf("first run = %v", types(firstEvents))
	}
	if text := deltaText(t, firstEvents[1]); text != "first" {
		t.Fatalf("first run took the quarantined turn's text: %q", text)
	}
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
		{Request: queueRequest("later"), Admission: queued, Cancelled: true},
	}, descriptor, append(append([]protocol.Envelope(nil), cancelled...), firstEvents...))
}

func TestCancelSettlesAnOpenToolAsCancelled(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	messageID := native.MessageID(response.MessageIDs[0])
	client.deliver(t, 1, messageID)
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	started := adaptertest.Next(t, stream, time.Second)
	if started.Type != protocol.TypeRunStarted {
		t.Fatalf("first=%s", started.Type)
	}
	client.toolCalled(t, 25, 30, "call_1", "read", map[string]any{"path": "/x"})
	requested := adaptertest.Next(t, stream, time.Second)
	toolStarted := adaptertest.Next(t, stream, time.Second)
	if requested.Type != protocol.TypeActionCallRequested || toolStarted.Type != protocol.TypeActionCallStarted {
		t.Fatalf("tool events = %s %s", requested.Type, toolStarted.Type)
	}

	if _, err := session.Cancel(context.Background(), response.RunID); err != nil {
		t.Fatal(err)
	}
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "aborted"})
	client.interrupted(t, 5)
	rest := adaptertest.Drain(t, stream, time.Second)

	events := append([]protocol.Envelope{started, requested, toolStarted}, rest...)
	settled, terminal := -1, -1
	for i, envelope := range events {
		switch envelope.Type {
		case protocol.TypeActionCallCancelled:
			settled = i
		case protocol.TypeRunCancelled:
			terminal = i
		case protocol.TypeActionCallFailed:
			t.Fatalf("a cancelled run settled its tool as failed: %v", types(events))
		}
	}
	if settled < 0 || terminal < 0 || settled > terminal {
		t.Fatalf("settled=%d terminal=%d events=%v", settled, terminal, types(events))
	}
	adaptertest.AssertProtocolValidWithCancellation(t, response, testAdapterDescriptor(t), events)
}

func TestATerminalCarriesTheCostTheStepsReported(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	messageID := native.MessageID(response.MessageIDs[0])
	client.deliver(t, 1, messageID)
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use", Cost: 0.25})
	client.emit(t, 4, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 5, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop", Cost: 0.75})
	client.succeed(t, 6)
	events := adaptertest.Drain(t, stream, time.Second)

	terminal := events[len(events)-1]
	if terminal.Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal = %s (%v)", terminal.Type, types(events))
	}
	carried := terminal.Extensions[costExtension]
	if carried == nil {
		t.Fatalf("the terminal carried no cost: %v", terminal.Extensions)
	}
	var reported struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(carried, &reported); err != nil {
		t.Fatal(err)
	}
	if reported.TotalCostUSD != 1.0 {
		t.Fatalf("total_cost_usd = %v, want the two steps summed", reported.TotalCostUSD)
	}
}

func TestAFailedTerminalCarriesTheCostTheStepsReported(t *testing.T) {
	client := newFakeClient()
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	messageID := native.MessageID(response.MessageIDs[0])
	client.deliver(t, 1, messageID)
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use", Cost: 0.25})
	client.emit(t, 4, native.TypeStepStarted, native.StepStartedData{SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 5, native.TypeStepFailed, native.StepFailedData{SessionID: client.session, AssistantMessage: "msg_a2", Error: native.SessionError{Type: "ProviderError", Message: "upstream refused"}})
	client.succeed(t, 6)
	events := adaptertest.Drain(t, stream, time.Second)

	terminal := events[len(events)-1]
	if terminal.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal = %s (%v)", terminal.Type, types(events))
	}
	carried := terminal.Extensions[costExtension]
	if carried == nil {
		t.Fatalf("a run that failed after a paid step carried no cost: %v", terminal.Extensions)
	}
	var reported struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	if err := json.Unmarshal(carried, &reported); err != nil {
		t.Fatal(err)
	}
	if reported.TotalCostUSD != 0.25 {
		t.Fatalf("total_cost_usd = %v, want the step that completed before the failure", reported.TotalCostUSD)
	}
}

func TestAnOpenCreatesTheSessionWithTheLevelAsItsModelsVariantAndRefusesOneNotRecorded(t *testing.T) {
	configured := &native.ModelRef{ID: "model", ProviderID: "fixture"}
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "model", ProviderID: "fixture", Variant: "high"}
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Model: configured, Clock: &fakeClock{}, IDs: &fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := adapter.Open(context.Background(), base.OpenRequest{SessionID: "s1", Participant: protocol.Participant{ID: "user"}, ReasoningLevel: protocol.ReasoningHigh})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	client.mu.Lock()
	sent := client.created
	client.mu.Unlock()
	if sent == nil || sent.Variant != "high" || configured.Variant != "" {
		t.Fatalf("create session sent %+v, want the configured model with variant high and the configuration untouched", sent)
	}
	state, err := opened.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("state reports %q, want high", state.ReasoningLevel)
	}

	ignoring := newFakeClient()
	ignoring.model = &native.ModelRef{ID: "model", ProviderID: "fixture"}
	strict, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return ignoring, nil }), Model: configured, Clock: &fakeClock{}, IDs: &fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = strict.Open(context.Background(), base.OpenRequest{SessionID: "s2", Participant: protocol.Participant{ID: "user"}, ReasoningLevel: protocol.ReasoningMax})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning {
		t.Fatalf("open answered %v, want a variant the server did not record refused", err)
	}

	bare, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return newFakeClient(), nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bare.Open(context.Background(), base.OpenRequest{SessionID: "s3", Participant: protocol.Participant{ID: "user"}, ReasoningLevel: protocol.ReasoningLow}); !errors.As(err, &refusal) {
		t.Fatalf("open with no configured model answered %v, want unsupported_feature", err)
	}
	if _, err := bare.Open(context.Background(), base.OpenRequest{SessionID: "s4", Participant: protocol.Participant{ID: "user"}, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureCompactionPolicy {
		t.Fatalf("open with a compaction policy answered %v, want it refused as unadvertised", err)
	}
}

func TestALiveLevelSwitchesTheSessionToItsModelWithTheNewVariant(t *testing.T) {
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "model", ProviderID: "fixture", Variant: "low"}
	session, _ := openTest(t, client, 32)
	response, state, err := session.(base.SettingsUpdater).UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningHigh})
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	switches := append([]native.ModelRef(nil), client.switches...)
	client.mu.Unlock()
	if len(switches) != 1 || switches[0].ID != "model" || switches[0].ProviderID != "fixture" || switches[0].Variant != "high" {
		t.Fatalf("the update switched %+v, want the session's model with variant high", switches)
	}
	if response.ReasoningLevel != protocol.ReasoningHigh || state.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("response %+v and state %q, want high in force", response, state.ReasoningLevel)
	}
}

func TestALiveUpdateIsRefusedWhatOpenCodeCannotTakeBetweenRuns(t *testing.T) {
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "model", ProviderID: "fixture", Variant: "low"}
	client.keepVariant = true
	session, _ := openTest(t, client, 32)
	updater := session.(base.SettingsUpdater)
	var refusal *base.UnsupportedControlError
	if _, _, err := updater.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningMax}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("a variant OpenCode did not record answered %v, want it unsatisfiable", err)
	}
	if state, _ := session.State(context.Background()); state.ReasoningLevel != "" {
		t.Fatalf("a refused level is reported as %q", state.ReasoningLevel)
	}
	if _, _, err := updater.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}}); !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureCompactionPolicy || refusal.Reason != base.ControlUnadvertised {
		t.Fatalf("a live policy answered %v, want it unadvertised", err)
	}
	if _, _, err := updater.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "other", ReasoningLevel: protocol.ReasoningHigh}); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("another session answered %v, want run_not_found", err)
	}
	submitTest(t, session)
	if _, _, err := updater.UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningHigh}); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("a busy session answered %v, want run_active", err)
	}

	modelless := newFakeClient()
	bare, _ := openTest(t, modelless, 32)
	if _, _, err := bare.(base.SettingsUpdater).UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningHigh}); !errors.As(err, &refusal) || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("a session with no recorded model answered %v, want it unsatisfiable", err)
	}
	modelless.mu.Lock()
	defer modelless.mu.Unlock()
	if len(modelless.switches) != 0 {
		t.Fatal("a session with no model still switched")
	}
}

func TestOpenGivesUpOnAServerThatNeverSendsServerConnected(t *testing.T) {
	client := newFakeClient()
	client.stalls = true
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, RequestTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := adapter.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no server.connected within the request timeout") {
			t.Fatalf("Open answered %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Open waited past the request timeout for server.connected")
	}
}

func TestALateInterruptTheServerStallsAbandonsTheRunWithinTheRequestTimeout(t *testing.T) {
	client := newFakeClient()
	client.silentCancels = true
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, RequestTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	session, err := adapter.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	response, stream := submitTest(t, session)
	if _, err := session.Cancel(context.Background(), response.RunID); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.stalls = true
	client.mu.Unlock()
	client.deliver(t, 1, native.MessageID(response.MessageIDs[0]))
	events := adaptertest.Drain(t, stream, 2*time.Second)
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", types(events))
	}
	var failed protocol.RunFailedPayload
	if err := json.Unmarshal(last.Payload, &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Code != "opencode_cancellation_ambiguous" || !strings.Contains(failed.Error.Message, context.DeadlineExceeded.Error()) {
		t.Fatalf("run failed with %+v", failed.Error)
	}
}
