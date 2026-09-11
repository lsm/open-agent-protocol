package hermes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"
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

type reply struct {
	result any
	err    error
	before func()
}

// fakeClient scripts native responses per method and mirrors the production
// client's ordered inbound stream. The started channel signals each native
// call's entry so tests can sequence event deliveries after the reducer has
// reserved the submission (delivering earlier races the reservation and reads
// as an unsolicited turn).
type fakeClient struct {
	in      chan rpc.InboundMessage
	done    chan struct{}
	started chan string
	mu      sync.Mutex
	closed  bool
	replies map[string][]reply
	calls   []recordedCall
	callsMu sync.Mutex
}

type recordedCall struct {
	method string
	params any
}

func newFake() *fakeClient {
	return &fakeClient{in: make(chan rpc.InboundMessage, 64), done: make(chan struct{}), started: make(chan string, 8), replies: map[string][]reply{}}
}

func (f *fakeClient) queue(method string, r reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[method] = append(f.replies[method], r)
}

// awaitCall blocks until the reducer issued the given native call.
func (f *fakeClient) awaitCall(t *testing.T, method string) {
	t.Helper()
	for {
		select {
		case got := <-f.started:
			if got == method {
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("native call %q was not issued", method)
		}
	}
}

func (f *fakeClient) Call(ctx context.Context, method string, params, result any) error {
	f.callsMu.Lock()
	f.calls = append(f.calls, recordedCall{method, params})
	f.callsMu.Unlock()
	f.started <- method
	f.mu.Lock()
	queue := f.replies[method]
	var next reply
	if len(queue) > 0 {
		next, f.replies[method] = queue[0], queue[1:]
	}
	f.mu.Unlock()
	if next.before != nil {
		next.before()
	}
	if next.err != nil {
		return next.err
	}
	if next.result == nil {
		return errors.New("fake client: no scripted reply for " + method)
	}
	data, _ := json.Marshal(next.result)
	return json.Unmarshal(data, result)
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

func (f *fakeClient) callCount(method string) int {
	f.callsMu.Lock()
	defer f.callsMu.Unlock()
	count := 0
	for _, call := range f.calls {
		if call.method == method {
			count++
		}
	}
	return count
}

func (f *fakeClient) event(typ string, seq int64, payload string) {
	params := json.RawMessage(`{"type":"` + typ + `","session_id":"sess0001","seq":` + jsonInt(seq))
	if payload != "" {
		params = json.RawMessage(strings.TrimSuffix(string(params), "}") + `,"payload":` + payload + `}`)
	} else {
		params = json.RawMessage(strings.TrimSuffix(string(params), "}") + `}`)
	}
	value, err := native.DecodeNotification(native.NotifyEvent, params)
	if err != nil {
		panic(err)
	}
	f.in <- rpc.InboundMessage{Notification: &rpc.NotificationMessage{Method: native.NotifyEvent, Params: params, Value: value}}
	f.barrier()
}

func jsonInt(v int64) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func (f *fakeClient) barrier() {
	ack := make(chan struct{})
	f.in <- rpc.InboundMessage{Barrier: ack}
	select {
	case <-ack:
	case <-time.After(2 * time.Second):
		panic("reducer barrier timed out")
	}
}

func openTest(t *testing.T) (base.Session, *fakeClient) {
	t.Helper()
	f := newFake()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return f, "sess0001", nil }), Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	s, err := implementation.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

func request() protocol.MessageSubmitRequest {
	return protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}
}

type outcome struct {
	response protocol.MessageSubmitResponse
	stream   base.EventStream
	err      error
}

func submitAsync(s base.Session) <-chan outcome {
	ch := make(chan outcome, 1)
	go func() {
		response, stream, err := s.Submit(context.Background(), request())
		ch <- outcome{response, stream, err}
	}()
	return ch
}

func drain(t *testing.T, stream base.EventStream) []protocol.Envelope {
	t.Helper()
	var out []protocol.Envelope
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case result, ok := <-stream:
			if !ok {
				return out
			}
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			out = append(out, result.Envelope)
		case <-timer.C:
			t.Fatal("stream did not close")
		}
	}
}

const settlementUsage = `{"model":"m","input":3,"output":5,"reasoning":1,"prompt":3,"completion":5,"total":8,"calls":2}`

func settleFrame(status string, extra string) string {
	payload := `{"text":"done","usage":` + settlementUsage + `,"status":"` + status + `"` + extra + `}`
	return payload
}

// admit submits and drives the run to its opened state in the chosen wire
// order. Both orders must reduce identically: response-then-open and
// open-then-response converge on the same run.started emission.
func admit(t *testing.T, s base.Session, f *fakeClient, responseFirst bool) <-chan outcome {
	t.Helper()
	open := func() { f.event(native.EventMessageStart, 1, "") }
	// Script the reply before the submission goroutine can issue the call. The
	// fake has no reply until one is queued, so queueing after submitAsync races
	// the reducer and intermittently fails with "no scripted reply".
	if responseFirst {
		f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: native.SubmitStreaming}})
	} else {
		f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: native.SubmitStreaming}, before: open})
	}
	ch := submitAsync(s)
	f.awaitCall(t, native.MethodPromptSubmit)
	if responseFirst {
		open()
	}
	return ch
}

func TestAdmissionBothWireOrdersReduceIdentically(t *testing.T) {
	run := func(t *testing.T, responseFirst bool) []protocol.Envelope {
		s, f := openTest(t)
		ch := admit(t, s, f, responseFirst)
		got := <-ch
		if got.err != nil {
			t.Fatalf("responseFirst=%v: %v", responseFirst, got.err)
		}
		f.event(native.EventMessageComplete, 2, settleFrame("complete", ""))
		return drain(t, got.stream)
	}
	a := run(t, true)
	b := run(t, false)
	if len(a) != 2 || a[0].Type != protocol.TypeRunStarted || a[1].Type != protocol.TypeRunCompleted {
		t.Fatalf("events %v", a)
	}
	if len(b) != len(a) || b[0].Type != a[0].Type || b[1].Type != a[1].Type {
		t.Fatalf("orders diverge: %v vs %v", a, b)
	}
}

func TestCompletedRunMapsDeltasToolsAndUsage(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventMessageDelta, 2, `{"text":"Hel"}`)
	f.event(native.EventReasoningDelta, 3, `{"text":"hmm"}`)
	f.event(native.EventThinkingDelta, 4, `{"text":"wait"}`)
	f.event(native.EventToolStart, 5, `{"tool_id":"t1","name":"read_file","context":"read_file a.txt","args":{"path":"a.txt"}}`)
	f.event(native.EventToolComplete, 6, `{"tool_id":"t1","name":"read_file","args":{"path":"a.txt"},"result":"ok","summary":"ok"}`)
	f.event(native.EventMessageInterim, 7, `{"text":"note","already_streamed":true}`)
	f.event(native.EventMessageComplete, 8, settleFrame("complete", ""))
	got := <-ch
	if got.err != nil {
		t.Fatal(got.err)
	}
	events := drain(t, got.stream)
	validateWithCapabilities(t, got.response, events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeContentDelta, protocol.TypeContentDelta, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeRunCompleted}
	if len(events) != len(want) {
		t.Fatalf("events %v", events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want[i])
		}
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.Usage == nil || completed.Usage.InputTokens != 3 || completed.Usage.TotalTokens != 8 {
		t.Fatalf("usage %+v", completed.Usage)
	}
	if text, ok := completed.FinalResponse.Content.Text(); !ok || text != "done" {
		t.Fatalf("final response %+v", completed.FinalResponse)
	}
}

func TestSettlementStatusArbitration(t *testing.T) {
	cases := map[string]struct {
		status string
		extra  string
		want   protocol.EnvelopeType
		code   string
	}{
		"interrupted":   {"interrupted", "", protocol.TypeRunFailed, "hermes_interrupted"},
		"error":         {"error", `,"error":"provider down","recoverable":true`, protocol.TypeRunFailed, "hermes_error"},
		"error surface": {"error", `,"error":"x","recoverable":true,"error_surface":{"layer":"provider","code":"rate_limited","retryable":true}`, protocol.TypeRunFailed, "hermes_rate_limited"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			s, f := openTest(t)
			ch := admit(t, s, f, true)
			f.event(native.EventMessageComplete, 2, settleFrame(test.status, test.extra))
			got := <-ch
			if got.err != nil {
				t.Fatal(got.err)
			}
			events := drain(t, got.stream)
			if len(events) != 2 || events[1].Type != test.want {
				t.Fatalf("events %v", events)
			}
			var failed protocol.RunFailedPayload
			if err := events[1].DecodePayload(&failed); err != nil || failed.Error.Code != test.code {
				t.Fatalf("payload %+v err=%v", failed.Error, err)
			}
		})
	}
}

func TestChildMirrorSettlementNeverTerminatesParent(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventMessageComplete, 2, `{"text":"mirror summary"}`)
	got := <-ch
	events := drain(t, got.stream)
	if len(events) != 2 || events[1].Type != protocol.TypeRunFailed {
		t.Fatalf("events %v", events)
	}
	var failed protocol.RunFailedPayload
	if err := events[1].DecodePayload(&failed); err != nil || failed.Error.Code != "hermes_invalid_settlement" {
		t.Fatalf("payload %+v", failed.Error)
	}
}

func TestBusyStatusesAreAdmissionFailures(t *testing.T) {
	for _, status := range []string{native.SubmitSteered, native.SubmitRedirected, native.SubmitQueued} {
		s, f := openTest(t)
		f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: status}})
		ch := submitAsync(s)
		f.awaitCall(t, native.MethodPromptSubmit)
		got := <-ch
		if got.err == nil {
			t.Fatalf("status %s accepted", status)
		}
		if got.response.RunID != "" {
			t.Fatalf("status %s exposed a run", status)
		}
		if got.stream != nil {
			t.Fatalf("status %s exposed an event stream", status)
		}
		state, err := s.State(context.Background())
		if err != nil || state.Status != protocol.SessionIdle {
			t.Fatalf("status %s left state=%+v err=%v", status, state, err)
		}
	}
}

func TestForeignSessionAndSeqGapsFailClosed(t *testing.T) {
	t.Run("foreign session", func(t *testing.T) {
		s, f := openTest(t)
		ch := admit(t, s, f, true)
		params := json.RawMessage(`{"type":"message.delta","session_id":"other123","seq":1,"payload":{"text":"x"}}`)
		value, _ := native.DecodeNotification(native.NotifyEvent, params)
		f.in <- rpc.InboundMessage{Notification: &rpc.NotificationMessage{Method: native.NotifyEvent, Params: params, Value: value}}
		f.barrier()
		f.event(native.EventMessageComplete, 2, settleFrame("complete", ""))
		got := <-ch
		events := drain(t, got.stream)
		if len(events) != 2 || events[1].Type != protocol.TypeRunFailed {
			t.Fatalf("events %v", events)
		}
		if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrSessionClosed) {
			t.Fatalf("session not unusable: %v", err)
		}
	})
	t.Run("seq gap", func(t *testing.T) {
		s, f := openTest(t)
		ch := admit(t, s, f, true)
		f.event(native.EventMessageDelta, 5, `{"text":"skipped"}`)
		f.event(native.EventMessageComplete, 6, settleFrame("complete", ""))
		got := <-ch
		events := drain(t, got.stream)
		if len(events) != 2 || events[1].Type != protocol.TypeRunFailed {
			t.Fatalf("events %v", events)
		}
	})
}

func TestUnsolicitedTurnFailsClosed(t *testing.T) {
	s, f := openTest(t)
	f.event(native.EventMessageStart, 1, "")
	if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("session not unusable after unsolicited turn: %v", err)
	}
}

func TestApprovalGateRoundTrip(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventApprovalRequest, 2, `{"command":"rm -rf /tmp/x","choices":["once","deny"]}`)
	f.queue(native.MethodApprovalRespond, reply{result: native.ApprovalRespondResult{Resolved: true}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: lastInteraction(t, s), SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"once"}}}}}); err != nil {
		t.Fatal(err)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	got := <-ch
	events := drain(t, got.stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeUserInputRequested, protocol.TypeRunStatusUpdated, protocol.TypeUserInputResolved, protocol.TypeRunStatusUpdated, protocol.TypeRunCompleted}
	if len(events) != len(want) {
		t.Fatalf("events %v", events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want[i])
		}
	}
	if f.callCount(native.MethodApprovalRespond) != 1 {
		t.Fatal("approval.respond not written")
	}
	validateWithCapabilities(t, got.response, events)
}

func lastInteraction(t *testing.T, s base.Session) protocol.InteractionID {
	t.Helper()
	session := s.(*Session)
	session.mu.Lock()
	defer session.mu.Unlock()
	var last protocol.InteractionID
	for id := range session.interactions {
		last = id
	}
	return last
}

func TestExpireSiblingResolvesCancelled(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, `{"request_id":"abcd1234","question":"which?","choices":["a","b"]}`)
	f.event(native.EventClarifyExpire, 3, `{"request_id":"abcd1234"}`)
	f.event(native.EventMessageComplete, 4, settleFrame("complete", ""))
	got := <-ch
	events := drain(t, got.stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeUserInputRequested, protocol.TypeRunStatusUpdated, protocol.TypeUserInputResolved, protocol.TypeRunStatusUpdated, protocol.TypeRunCompleted}
	if len(events) != len(want) {
		t.Fatalf("events %v", events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want[i])
		}
	}
	var resolved protocol.UserInputResolvedPayload
	if err := events[3].DecodePayload(&resolved); err != nil || resolved.Status != protocol.InputCancelled {
		t.Fatalf("resolved %+v", resolved)
	}
}

func TestCancelIssuesInterruptAndSettlesFailed(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	got := <-ch
	if got.err != nil {
		t.Fatal(got.err)
	}
	f.queue(native.MethodSessionInterrupt, reply{result: native.InterruptResult{Status: "interrupted"}})
	if _, err := s.Cancel(context.Background(), got.response.RunID); err != nil {
		t.Fatal(err)
	}
	if f.callCount(native.MethodSessionInterrupt) != 1 {
		t.Fatal("session.interrupt not written")
	}
	if _, err := s.Cancel(context.Background(), "run-does-not-exist"); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("unknown run cancel err = %v", err)
	}
	f.event(native.EventMessageComplete, 2, settleFrame("interrupted", ""))
	events := drain(t, got.stream)
	if len(events) != 2 || events[1].Type != protocol.TypeRunFailed {
		t.Fatalf("events %v", events)
	}
}

func TestSubmitCancellationReleasesReservation(t *testing.T) {
	s, f := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := make(chan outcome, 1)
	go func() {
		response, stream, err := s.Submit(ctx, request())
		ch <- outcome{response, stream, err}
	}()
	// Accept the submission but never open the turn; then cancel.
	f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: native.SubmitStreaming}})
	f.awaitCall(t, native.MethodPromptSubmit)
	cancel()
	got := <-ch
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v", got.err)
	}
	if got.stream != nil {
		for range got.stream {
		}
	}
	state, err := s.State(context.Background())
	if err != nil || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("session wedged: %v", err)
	}
}

func TestOverlapRejectedBeforeNativeWrite(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	before := f.callCount(native.MethodPromptSubmit)
	if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("overlap err = %v", err)
	}
	if f.callCount(native.MethodPromptSubmit) != before {
		t.Fatal("rejected overlap wrote a native request")
	}
	f.event(native.EventMessageComplete, 2, settleFrame("complete", ""))
	got := <-ch
	drain(t, got.stream)
}

// validateWithCapabilities runs the executable schema over an optional-
// feature trace (tools, interactions) with a current capability descriptor
// pair prefixed, mirroring the corpus harness: the validator requires a
// descriptor before optional-feature events.
func validateWithCapabilities(t *testing.T, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return nil, "", errors.New("probe only") })})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	capReq, _ := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	capRes, _ := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	capRes.InReplyTo, capRes.CapabilityRevision = capReq.ID, descriptor.CapabilityRevision
	submitReq, _ := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, "submit-request", protocol.MessageSubmitRequest{SessionID: admission.SessionID, Delivery: admission.RequestedDelivery, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}})
	submitReq.SessionID = admission.SessionID
	submitRes, _ := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, "submit-response", admission)
	submitRes.SessionID, submitRes.InReplyTo = admission.SessionID, submitReq.ID
	trace := append([]protocol.Envelope{capReq, capRes, submitReq, submitRes}, events...)
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(data, "hermes-test"); !result.Valid() {
		t.Fatalf("adapter trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, data)
	}
}

func TestEventsBeforeConvergenceBufferAndReplay(t *testing.T) {
	// A piped burst can deliver the whole turn around the prompt.submit
	// response: the response barrier orders only wire-earlier events, so
	// run-scoped observations arriving before the convergence point buffer
	// and replay in wire order once the run starts.
	s, f := openTest(t)
	// Wire order: message.start, a delta, then the streaming response.
	open := func() {
		f.event(native.EventMessageStart, 1, "")
		f.event(native.EventMessageDelta, 2, `{"text":"Hi"}`)
	}
	f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: native.SubmitStreaming}, before: open})
	ch := submitAsync(s)
	f.awaitCall(t, native.MethodPromptSubmit)
	got := <-ch
	if got.err != nil {
		t.Fatal(got.err)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	events := drain(t, got.stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	if len(events) != len(want) {
		t.Fatalf("events %v", events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d = %s, want %s", i, events[i].Type, want[i])
		}
	}
	var delta protocol.ContentDeltaPayload
	if err := events[1].DecodePayload(&delta); err != nil || delta.Part.Text != "Hi" {
		t.Fatalf("buffered delta lost: %+v err=%v", delta.Part, err)
	}
	validateWithCapabilities(t, got.response, events)
}

func TestPostSettlementCorroborationKeepsSessionUsable(t *testing.T) {
	// The pin guarantees settled session.info after message.complete; the
	// terminal cleanup releases the run, so an idle session must accept the
	// corroboration frame instead of failing closed as foreign activity.
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventMessageComplete, 2, settleFrame("complete", ""))
	got := <-ch
	drain(t, got.stream)
	f.event(native.EventSessionInfo, 3, `{"model":"m","provider":"p","running":false,"title":"t","cwd":"/tmp","stored_session_id":"20260831_093000_ab12cd"}`)
	state, err := s.State(context.Background())
	if err != nil || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("session unusable after settled session.info: %v", err)
	}
}

func TestResolveValidatesAnswerShapes(t *testing.T) {
	// The schema's answer oneOf allows the text form; an approval answer
	// without a selected option must be rejected, not panic.
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventApprovalRequest, 2, `{"command":"rm -rf /tmp/x","choices":["once","deny"]}`)
	binding := lastInteraction(t, s)
	textForm := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "choice", Text: "once"}}}})
	if !errors.Is(textForm, base.ErrInvalidResolution) {
		t.Fatalf("text-form approval err = %v", textForm)
	}
	noAnswers := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session"}})
	if !errors.Is(noAnswers, base.ErrInvalidResolution) {
		t.Fatalf("empty answers err = %v", noAnswers)
	}
	// The gate is still resolvable after the rejections.
	f.queue(native.MethodApprovalRespond, reply{result: native.ApprovalRespondResult{Resolved: true}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"deny"}}}}}); err != nil {
		t.Fatal(err)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	drain(t, (<-ch).stream)
}

func TestBatchClarifyResolvesEveryQuestion(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, `{"request_id":"aaaa1111","questions":[{"qid":"q1","question":"first?","choices":["a","b"]},{"qid":"q2","question":"second?","choices":["c","d"]}]}`)
	binding := lastInteraction(t, s)
	// A partial answer set is invalid before any native write.
	partial := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "q1", SelectedOptionIDs: []string{"a"}}}}})
	if !errors.Is(partial, base.ErrInvalidResolution) {
		t.Fatalf("partial batch err = %v", partial)
	}
	if f.callCount(native.MethodClarifyRespond) != 0 {
		t.Fatal("rejected batch wrote a native respond")
	}
	f.queue(native.MethodClarifyRespond, reply{result: native.RespondResult{Status: "ok"}})
	f.queue(native.MethodClarifyRespond, reply{result: native.RespondResult{Status: "ok"}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "q1", SelectedOptionIDs: []string{"a"}}, {QuestionID: "q2", SelectedOptionIDs: []string{"d"}}}}}); err != nil {
		t.Fatal(err)
	}
	if got := f.callCount(native.MethodClarifyRespond); got != 2 {
		t.Fatalf("clarify.respond calls = %d", got)
	}
	f.callsMu.Lock()
	first, second := f.calls[1], f.calls[2]
	f.callsMu.Unlock()
	respondOne, ok := first.params.(native.RespondParams)
	if !ok || respondOne.QuestionID != "q1" || respondOne.Answer != "a" {
		t.Fatalf("first respond = %+v", first)
	}
	respondTwo, ok := second.params.(native.RespondParams)
	if !ok || respondTwo.QuestionID != "q2" || respondTwo.Answer != "d" {
		t.Fatalf("second respond = %+v", second)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	got := <-ch
	events := drain(t, got.stream)
	validateWithCapabilities(t, got.response, events)
}

func TestRespondFailureDoesNotProjectSubmitted(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, `{"request_id":"aaaa1111","question":"which?","choices":["a","b"]}`)
	binding := lastInteraction(t, s)
	f.queue(native.MethodClarifyRespond, reply{result: native.RespondResult{Status: "expired"}})
	err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "answer", SelectedOptionIDs: []string{"a"}}}}})
	if err == nil {
		t.Fatal("expired respond projected as submitted")
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	events := drain(t, (<-ch).stream)
	for _, envelope := range events {
		if envelope.Type == protocol.TypeUserInputResolved {
			t.Fatal("failed resolution emitted user.input.resolved")
		}
	}
}
