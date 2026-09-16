package opencode

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
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/httpapi"
	"github.com/lsm/open-agent-protocol/adapter/opencode/internal/native"
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
	promoted         bool
	promptErr        error
	foreignAdmission bool
	interrupts       int
	actives          int
	activeErr        error
	// activeFor reports the session as natively active for this many leading
	// polls, modelling an agent loop whose drain is still running.
	activeFor    int
	historyErr   error
	historyPage  native.HistoryPage
	lastPromptID native.MessageID
	subscribeCtx context.Context
	prompts      []native.PromptRequest
	closed       bool
	// idleGate, when set, reports the session as natively active until it is
	// closed, holding settlement until the corpus has delivered the whole
	// native stream. The real active set holds a session for one whole agent
	// loop drain, which implies every step event of the turn is already
	// durable; reporting idle immediately would let settlement race the
	// not-yet-delivered later steps.
	idleGate <-chan struct{}
	// model is the native session model CreateSession echoes to the adapter.
	model *native.ModelRef
}

func newFakeClient() *fakeClient {
	events := make(chan native.Event, 256)
	return &fakeClient{session: "ses_fake00000000000000", events: events, subscription: &fakeSubscription{events: events, done: make(chan struct{})}}
}

func (f *fakeClient) CreateSession(_ context.Context, _ httpapi.CreateSessionRequest) (native.SessionInfo, error) {
	return native.SessionInfo{ID: f.session, ProjectID: "prj_fake", Model: f.model, Time: struct {
		Created  int64  `json:"created"`
		Updated  int64  `json:"updated"`
		Archived *int64 `json:"archived,omitempty"`
	}{Created: 1, Updated: 1}, Location: json.RawMessage(`{"directory":"/w"}`)}, nil
}
func (f *fakeClient) Prompt(_ context.Context, session native.SessionID, request native.PromptRequest) (native.Admitted, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.promptErr != nil {
		return native.Admitted{}, f.promptErr
	}
	if f.foreignAdmission {
		return native.Admitted{AdmittedSeq: 1, ID: "msg_foreign", SessionID: session, Prompt: request.Prompt, Delivery: request.Delivery, TimeCreated: 1}, nil
	}
	f.lastPromptID = request.ID
	f.prompts = append(f.prompts, request)
	var promoted *int64
	if f.promoted {
		value := int64(len(f.prompts))
		promoted = &value
	}
	return native.Admitted{AdmittedSeq: int64(len(f.prompts)), ID: request.ID, SessionID: session, Prompt: request.Prompt, Delivery: request.Delivery, TimeCreated: 1, PromotedSeq: promoted}, nil
}
func (f *fakeClient) Interrupt(context.Context, native.SessionID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.interrupts++
	return nil
}
func (f *fakeClient) Active(ctx context.Context) (map[native.SessionID]bool, error) {
	f.mu.Lock()
	f.actives++
	gate, err := f.idleGate, f.activeErr
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
	if gate != nil {
		select {
		case <-gate:
		default:
			running = true
		}
	}
	if running {
		return map[native.SessionID]bool{f.session: true}, nil
	}
	return map[native.SessionID]bool{}, nil
}
func (f *fakeClient) History(_ context.Context, _ native.SessionID, after int64, _ int) (native.HistoryPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.historyErr != nil {
		return native.HistoryPage{}, f.historyErr
	}
	var page native.HistoryPage
	for _, event := range f.historyPage.Events {
		if event.Durable.Seq > after {
			page.Events = append(page.Events, event)
		}
	}
	return page, nil
}
func (f *fakeClient) Subscribe(ctx context.Context, _ native.SessionID, _ int64) (Subscription, error) {
	f.mu.Lock()
	f.subscribeCtx = ctx
	f.mu.Unlock()
	return f.subscription, nil
}
func (f *fakeClient) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeClient) emit(t *testing.T, seq int64, typ native.Type, payload any) {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	f.events <- native.Event{ID: native.EventID(fmt.Sprintf("evt_fake%04d", seq)), Type: typ, Durable: &native.DurablePosition{AggregateID: string(f.session), Seq: seq, Version: 1}, Data: data}
}

// The queue graduated on SessionInput.Admitted{delivery:"queue", promotedSeq},
// so it is advertised and applied; steer has no unit yet and is still refused
// under its own key. The queue's disclosure comes with it: advertising the key
// is a claim that some submission will be queued, and the bound is what makes
// the claim checkable.
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
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}, JournalCapacity: capacity, SettlePollMin: time.Millisecond, SettlePollMax: 2 * time.Millisecond})
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
	response, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	if err != nil {
		t.Fatal(err)
	}
	return response, stream
}

// None of the four run controls has a per-run native surface: the session's
// model is fixed at creation and the prompt request carries content only. Each
// must be refused under its own capability key with the typed
// unsupported-control error rather than a generic rejection that names none of
// them or an echoed effective model.
func TestSubmitRefusesEveryUnadvertisedControl(t *testing.T) {
	session, _ := openTest(t, newFakeClient(), 32)
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}
	for feature, request := range map[string]protocol.MessageSubmitRequest{
		protocol.FeatureInstructions:     {SessionID: "session", Delivery: protocol.DeliveryAuto, Instructions: protocol.ControlValue("be terse"), Messages: message},
		protocol.FeatureModelSelection:   {SessionID: "session", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("other-model"), Messages: message},
		protocol.FeatureStructuredOutput: {SessionID: "session", Delivery: protocol.DeliveryAuto, OutputSchema: json.RawMessage(`{"type":"object"}`), Messages: message},
		protocol.FeatureToolSelection:    {SessionID: "session", Delivery: protocol.DeliveryAuto, ToolChoice: json.RawMessage(`"none"`), Messages: message},
	} {
		_, _, err := session.Submit(context.Background(), request)
		var refusal *base.UnsupportedControlError
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: got %v, want an *adapter.UnsupportedControlError", feature, err)
		}
		if refusal.Feature != feature || refusal.Reason != base.ControlUnadvertised {
			t.Fatalf("%s: refused as %q/%q", feature, refusal.Feature, refusal.Reason)
		}
	}
}

// The validator accepts the zero-value delivery as auto, so the response must
// report the canonical value; an empty requested_delivery violates the schema
// enum and fails canonical trace validation.
func TestSubmitNormalizesOmittedDelivery(t *testing.T) {
	session, _ := openTest(t, newFakeClient(), 32)
	response, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session",
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.RequestedDelivery != protocol.DeliveryAuto {
		t.Fatalf("requested_delivery = %q, want %q", response.RequestedDelivery, protocol.DeliveryAuto)
	}
}

// A per-submit override is rejected, so the adapter must retain the native
// session model instead of clearing attribution with the empty request value.
func TestSessionRetainsNativeModel(t *testing.T) {
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "claude-sonnet", ProviderID: "anthropic"}
	session, _ := openTest(t, client, 32)
	response, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	})
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

func TestCompletedRunDerivesTerminalFromQuiescence(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_assistant1", Agent: "build", Model: native.ModelRef{ID: "m", ProviderID: "p"}})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_assistant1", TextID: "t1", Text: "done"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_assistant1", Finish: "stop", Tokens: tokenAccounting(2, 5)})
	events := adaptertest.Drain(t, stream, time.Second)
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
	// A loop that has already drained is observed idle on the first poll, so
	// settlement corroborates exactly once and never sleeps.
	client.mu.Lock()
	actives := client.actives
	client.mu.Unlock()
	if actives != 1 {
		t.Fatalf("active polls=%d", actives)
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
	client.promoted = true
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeToolCalled, native.ToolCalledData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", CallID: "call_1", Tool: "read", Input: map[string]any{"path": "/x"}})
	client.emit(t, 4, native.TypeToolProgress, native.ToolProgressData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a1", CallID: "call_1", Structured: map[string]any{}, Content: []native.ToolContent{{Type: "text", Text: "half"}}})
	client.emit(t, 5, native.TypeToolSuccess, native.ToolSuccessData{Timestamp: 5, SessionID: client.session, AssistantMessage: "msg_a1", CallID: "call_1", Structured: map[string]any{}, Content: []native.ToolContent{{Type: "text", Text: "done"}}})
	client.emit(t, 6, native.TypeTextEnded, native.TextEndedData{Timestamp: 6, SessionID: client.session, AssistantMessage: "msg_a1", TextID: "t1", Text: "answer"})
	client.emit(t, 7, native.TypeStepEnded, native.StepEndedData{Timestamp: 7, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use"})
	events := adaptertest.Drain(t, stream, time.Second)
	// The action lifecycle is validated against a capability-bearing
	// canonical trace in the corpus; here adapter-owned invariants suffice.
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
	client.promoted = true
	// Real wait-idle returns only once the run is quiescent, which implies the
	// second step is already durable. Hold the fake's wait-idle until the whole
	// stream is enqueued: an immediate return let the first step's settlement
	// fence before the producer had enqueued the second step, completing a
	// one-step run and dropping its text (issue #10).
	delivered := make(chan struct{})
	client.idleGate = delivered
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	messageID := native.MessageID(response.MessageIDs[0])
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: messageID, Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use"})
	// The second step is enqueued while the first step's settlement waits for
	// idle; the settlement drain must fold it in, not double-settle.
	client.emit(t, 4, native.TypeStepStarted, native.StepStartedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 5, native.TypeTextEnded, native.TextEndedData{Timestamp: 5, SessionID: client.session, AssistantMessage: "msg_a2", TextID: "t2", Text: "final"})
	client.emit(t, 6, native.TypeStepEnded, native.StepEndedData{Timestamp: 6, SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})
	close(delivered)
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
}

// The agent loop's drain is still running when the step ends, so settlement
// must poll the active set out rather than deriving a terminal from its first
// look. The pinned server has no blocking idle call to lean on: its wait route
// is declared but unimplemented and answers 503, so this poll is the whole
// corroboration.
func TestSettlementPollsActiveUntilLoopDrains(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	client.activeFor = 3
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", TextID: "t1", Text: "done"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
	client.mu.Lock()
	actives := client.actives
	client.mu.Unlock()
	if actives <= 3 {
		t.Fatalf("active polls=%d, want the run to outlast the three active replies", actives)
	}
}

// Settlement corroboration that cannot be read must fail the run rather than
// invent a terminal. This is the shape the adapter used to take on every turn:
// it settled through the unimplemented wait route, whose 503 failed each run
// and left the session unusable.
func TestSettlementFailsWhenQuiescenceUnreadable(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	client.activeErr = errors.New("HTTP 503 ServiceUnavailableError")
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunFailed {
		t.Fatalf("terminal=%s, want %s", last.Type, protocol.TypeRunFailed)
	}
	var failed protocol.RunFailedPayload
	if err := last.DecodePayload(&failed); err != nil {
		t.Fatal(err)
	}
	if failed.Error.Code != "opencode_quiescence_failed" {
		t.Fatalf("code=%q", failed.Error.Code)
	}
}

func TestHistoryFenceReducesEventsMissedByStream(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	tail := []native.Event{
		{ID: "evt_fake0004", Type: native.TypeTextEnded, Durable: &native.DurablePosition{AggregateID: string(client.session), Seq: 4, Version: 1}},
		{ID: "evt_fake0005", Type: native.TypeStepEnded, Durable: &native.DurablePosition{AggregateID: string(client.session), Seq: 5, Version: 1}},
	}
	textData, err := json.Marshal(native.TextEndedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a1", TextID: "t1", Text: "fenced"})
	if err != nil {
		t.Fatal(err)
	}
	stepData, err := json.Marshal(native.StepEndedData{Timestamp: 5, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	if err != nil {
		t.Fatal(err)
	}
	tail[0].Data, tail[1].Data = textData, stepData
	client.historyPage = native.HistoryPage{Events: tail}
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "tool_use"})
	events := adaptertest.Drain(t, stream, time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
}

func TestCancelAcknowledgesIntentAndSettlesCancelled(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	messageID := native.MessageID(response.MessageIDs[0])
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: messageID, Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})
	if started := adaptertest.Next(t, stream, time.Second); started.Type != protocol.TypeRunStarted {
		t.Fatalf("first=%s", started.Type)
	}
	cancelled, err := session.Cancel(context.Background(), response.RunID)
	if err != nil || !cancelled.Accepted || cancelled.Status != protocol.RunCancelling {
		t.Fatalf("cancel=%+v err=%v", cancelled, err)
	}
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "aborted"})
	events := adaptertest.Drain(t, stream, time.Second)
	want := []protocol.EnvelopeType{protocol.TypeRunStatusUpdated, protocol.TypeRunCancelled}
	if fmt.Sprint(types(events)) != fmt.Sprint(want) {
		t.Fatalf("events=%v", types(events))
	}
}

func TestQueuedAdmissionStartsOnPrompted(t *testing.T) {
	client := newFakeClient()
	client.promoted = false
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	if response.Admission != protocol.AdmissionQueued || response.Status != protocol.RunQueued {
		t.Fatalf("response=%+v", response)
	}
	select {
	case result := <-stream:
		t.Fatalf("premature event %s", result.Envelope.Type)
	case <-time.After(20 * time.Millisecond):
	}
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepEnded, native.StepEndedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	events := adaptertest.Drain(t, stream, time.Second)
	if events[0].Type != protocol.TypeRunStarted {
		t.Fatalf("events=%v", types(events))
	}
}

func TestForeignSessionEventFailsRun(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	data, _ := json.Marshal(native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.events <- native.Event{ID: "evt_foreign", Type: native.TypePrompted, Durable: &native.DurablePosition{AggregateID: "ses_other", Seq: 1, Version: 1}, Data: data}
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) != 1 || events[0].Type != protocol.TypeRunFailed {
		t.Fatalf("events=%v", types(events))
	}
	if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("second submit err=%v", err)
	}
}

// TestPreStartFailuresReportReservation pins the decision 0002 contract on
// every reserved-run failure path: Submit never pairs an error with a
// non-nil stream; the accepted queued reservation settles pre-start instead.
func TestPreStartFailuresReportReservation(t *testing.T) {
	cases := map[string]func(*fakeClient){
		"foreign admission": func(client *fakeClient) { client.foreignAdmission = true },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			client := newFakeClient()
			setup(client)
			session, _ := openTest(t, client, 32)
			response, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
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
			if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}); !errors.Is(err, base.ErrSessionClosed) {
				t.Fatalf("second submit err=%v", err)
			}
		})
	}
}

func TestAdmissionFailureRetiresSession(t *testing.T) {
	client := newFakeClient()
	client.promptErr = errors.New("HTTP 409")
	session, _ := openTest(t, client, 32)
	// Decision 0002: the reserved run settles pre-start on its stream and the
	// response reports the accepted queued reservation — no error return.
	response, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
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
	if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("again")}}}); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("second submit err=%v", err)
	}
}

func TestStreamFailureProjectsFailure(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
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
	client.promoted = true
	session, _ := openTest(t, client, 32)
	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})
	client.emit(t, 3, native.TypeStepFailed, native.StepFailedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Error: native.UnknownErrorBlock{Type: "unknown", Message: "provider exploded"}})
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

func TestReplayGapAndTerminalReplay(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	session, _ := openTest(t, client, 2)
	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeTextEnded, native.TextEndedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1", TextID: "t1", Text: "a"})
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", TextID: "t2", Text: "b"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
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

// TestOpenSubscriptionSurvivesOpenContextCancel pins that the durable
// subscription owns its context instead of borrowing the Open call's. A caller
// doing `ctx, cancel := context.WithTimeout(...); defer cancel()` must not tear
// the SSE request down when Open returns. Before the fix the subscription
// reused the Open context and the returned session became unusable.
func TestOpenSubscriptionSurvivesOpenContextCancel(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
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
	// The returned session is still usable end to end.
	response, stream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(response.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepEnded, native.StepEndedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) == 0 || events[0].Type != protocol.TypeRunStarted {
		t.Fatalf("events=%v", types(events))
	}
}

// gatedPromptClient blocks Prompt until release, letting a test deliver the
// prompted SSE event before the prompt HTTP response returns.
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

// TestPromptedEventBeforePromptResponseStartsRun pins that the pending mapping
// is installed before Prompt is issued. The server may publish
// session.next.prompted before the prompt HTTP response arrives; that event
// must still resolve to the reserved run and emit run.started. Before the fix
// the mapping was installed only after Prompt returned and the early event was
// discarded, so no run.started was ever emitted.
func TestPromptedEventBeforePromptResponseStartsRun(t *testing.T) {
	client := newFakeClient()
	client.promoted = false
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
		response, stream, err := sess.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
		submitted <- struct {
			response protocol.MessageSubmitResponse
			stream   base.EventStream
			err      error
		}{response, stream, err}
	}()
	<-gated.entered // Prompt is in flight; the response has not returned
	client.mu.Lock()
	messageID := client.lastPromptID
	client.mu.Unlock()
	// Deliver the prompted event, then a sentinel no-op event. If the prompted
	// event is handled by its own turn (before the fix) the sentinel's sequence
	// is reduced promptly; after the fix the handler blocks on admission, so
	// the sentinel is not reduced until the prompt response arrives.
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: messageID, Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeAgentSwitched, nil)
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
	client.emit(t, 3, native.TypeStepEnded, native.StepEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	events := adaptertest.Drain(t, result.stream, time.Second)
	if len(events) == 0 || events[0].Type != protocol.TypeRunStarted {
		t.Fatalf("events=%v", types(events))
	}
}

// queueRequest is an explicit queue submission on the shared test session.
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

// An explicit queue while the started run is live reserves the second run,
// publishes nothing for it, and promotes it only after the first run's derived
// settlement — so one run domain executes at a time, in admission order.
func TestExplicitQueueReservesAndPromotesAfterSettlement(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)
	first, firstStream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(first.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})

	queued, queuedStream, err := session.Submit(context.Background(), queueRequest("later"))
	if err != nil {
		t.Fatal(err)
	}
	if queued.Admission != protocol.AdmissionQueued || queued.EffectiveDelivery != protocol.EffectiveDeliveryQueue || queued.Status != protocol.RunQueued {
		t.Fatalf("reservation = %+v", queued)
	}
	if queued.RequestedDelivery != protocol.DeliveryQueue {
		t.Fatalf("requested_delivery = %q", queued.RequestedDelivery)
	}
	// Both nonterminal runs are listed in admission order, the reservation
	// with its 1-based position; active_run_id still names the started run.
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
	// A third submission exceeds the disclosed queue bound of one.
	if _, _, err := session.Submit(context.Background(), autoRequest("too much")); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("third submit = %v, want ErrRunActive", err)
	}

	// The server promotes the reservation while the first run is still live;
	// the promotion waits for that run's terminal.
	client.emit(t, 3, native.TypePrompted, native.PromptedData{Timestamp: 3, SessionID: client.session, MessageID: native.MessageID(queued.MessageIDs[0]), Prompt: native.Prompt{Text: "later"}, Delivery: native.DeliveryQueue})
	client.emit(t, 4, native.TypeTextEnded, native.TextEndedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a1", TextID: "t1", Text: "first"})
	client.emit(t, 5, native.TypeStepEnded, native.StepEndedData{Timestamp: 5, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)
	if len(firstEvents) == 0 || firstEvents[len(firstEvents)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("first run = %v", types(firstEvents))
	}
	// The reservation starts only now, and its own turn settles normally.
	client.emit(t, 6, native.TypeStepStarted, native.StepStartedData{Timestamp: 6, SessionID: client.session, AssistantMessage: "msg_a2"})
	client.emit(t, 7, native.TypeTextEnded, native.TextEndedData{Timestamp: 7, SessionID: client.session, AssistantMessage: "msg_a2", TextID: "t2", Text: "second"})
	client.emit(t, 8, native.TypeStepEnded, native.StepEndedData{Timestamp: 8, SessionID: client.session, AssistantMessage: "msg_a2", Finish: "stop"})
	queuedEvents := adaptertest.Drain(t, queuedStream, 2*time.Second)
	if len(queuedEvents) == 0 || queuedEvents[0].Type != protocol.TypeRunStarted {
		t.Fatalf("promoted run = %v", types(queuedEvents))
	}
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
		{Request: queueRequest("later"), Admission: queued},
	}, descriptor, append(append([]protocol.Envelope(nil), firstEvents...), queuedEvents...))
}

// A busy session turns an auto submission into a reservation and says so, and
// cancelling that reservation settles it pre-start: its first and only
// run-scoped event is the terminal, published while the started run is still
// nonterminal so the freed slot is observable at the moment it is released.
func TestBusyAutoReservesAndCancelsBeforePromotion(t *testing.T) {
	client := newFakeClient()
	client.promoted = true
	session, _ := openTest(t, client, 64)
	descriptor := testAdapterDescriptor(t)
	first, firstStream := submitTest(t, session)
	client.emit(t, 1, native.TypePrompted, native.PromptedData{Timestamp: 1, SessionID: client.session, MessageID: native.MessageID(first.MessageIDs[0]), Prompt: native.Prompt{Text: "hello"}, Delivery: native.DeliverySteer})
	client.emit(t, 2, native.TypeStepStarted, native.StepStartedData{Timestamp: 2, SessionID: client.session, AssistantMessage: "msg_a1"})

	queued, queuedStream, err := session.Submit(context.Background(), autoRequest("later"))
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
	// The slot is free again while the first run is still running.
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ActiveRuns) != 1 || state.ActiveRuns[0].RunID != first.RunID {
		t.Fatalf("active_runs after release = %+v", state.ActiveRuns)
	}
	client.emit(t, 3, native.TypeTextEnded, native.TextEndedData{Timestamp: 3, SessionID: client.session, AssistantMessage: "msg_a1", TextID: "t1", Text: "first"})
	client.emit(t, 4, native.TypeStepEnded, native.StepEndedData{Timestamp: 4, SessionID: client.session, AssistantMessage: "msg_a1", Finish: "stop"})
	firstEvents := adaptertest.Drain(t, firstStream, 2*time.Second)
	// The reservation's terminal is delivered where it happened: before the
	// earlier run's own terminal.
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: autoRequest("hello"), Admission: first},
		{Request: autoRequest("later"), Admission: queued, Cancelled: true},
	}, descriptor, append(append([]protocol.Envelope(nil), queuedEvents...), firstEvents...))
}
