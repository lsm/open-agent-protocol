package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
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

type reply struct {
	result any
	err    error
	before func()
}

type fakeClient struct {
	in      chan rpc.InboundMessage
	done    chan struct{}
	started chan string
	mu      sync.Mutex
	closed  bool
	dead    bool
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
func (f *fakeClient) ReadDone() <-chan struct{}          { return f.done }
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
	return openWithJournal(t, 64)
}

func openWithJournal(t *testing.T, capacity int) (base.Session, *fakeClient) {
	t.Helper()
	f := newFake()
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) { return f, "sess0001", nil }), Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: capacity})
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

func TestSubmitRejectsUnappliedModelID(t *testing.T) {
	s, _ := openTest(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req := request()
	req.ModelID = protocol.ControlValue("hermes-other")
	if _, _, err := s.Submit(ctx, req); !errors.Is(err, base.ErrUnsupportedInput) {
		t.Fatalf("got %v, want ErrUnsupportedInput", err)
	}
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

func admit(t *testing.T, s base.Session, f *fakeClient, responseFirst bool) <-chan outcome {
	t.Helper()
	open := func() { f.event(native.EventMessageStart, 1, "") }

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

	waitStarted(t, s)
	return ch
}

func waitStarted(t *testing.T, s base.Session) {
	t.Helper()
	session := s.(*Session)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		run := session.pending
		if run == nil {
			run = session.active
		}
		started := run != nil && run.started
		session.mu.Unlock()
		if started {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("run did not reach started after admission")
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

func TestToolCompletionWithoutResultCarriesNull(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventToolStart, 2, `{"tool_id":"t1","name":"read","context":"read"}`)
	f.event(native.EventToolComplete, 3, `{"tool_id":"t1","name":"read"}`)
	f.event(native.EventMessageComplete, 4, settleFrame("complete", ""))
	got := <-ch
	events := drain(t, got.stream)
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
	validateWithCapabilities(t, got.response, events)
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

func TestResolveRejectsForeignOwnership(t *testing.T) {
	s, f := openTest(t)
	_ = admit(t, s, f, true)
	f.event(native.EventApprovalRequest, 2, `{"command":"rm","choices":["once","deny"]}`)
	id := lastInteraction(t, s)
	if id == "" {
		t.Fatal("approval gate was not registered")
	}
	answer := []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"once"}}}
	cases := map[string]base.InteractionResolution{
		"foreign responded_by": {RespondedBy: "someone-else", Input: &protocol.UserInputResolveRequest{InteractionID: id, SessionID: "session", Answers: answer}},
		"foreign session":      {Input: &protocol.UserInputResolveRequest{InteractionID: id, SessionID: "other-session", Answers: answer}},
		"foreign run":          {Input: &protocol.UserInputResolveRequest{InteractionID: id, SessionID: "session", RunID: "run-other", Answers: answer}},
		"foreign requester":    {Input: &protocol.UserInputResolveRequest{InteractionID: id, SessionID: "session", RequestedBy: "someone-else", Answers: answer}},
	}
	for name, resolution := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Resolve(context.Background(), resolution); !errors.Is(err, base.ErrInvalidResolution) {
				t.Fatalf("got %v, want ErrInvalidResolution", err)
			}
		})
	}
	if f.callCount(native.MethodApprovalRespond) != 0 {
		t.Fatal("a rejected resolution must not reach the native gate")
	}
}

func lastInteraction(t *testing.T, s base.Session) protocol.InteractionID {
	t.Helper()
	session := s.(*Session)

	session.reduceMu.Lock()
	defer session.reduceMu.Unlock()
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

func TestSubmitCancellationAfterAcceptanceKeepsReservation(t *testing.T) {
	s, f := openTest(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: native.SubmitStreaming}})
	ch := make(chan outcome, 1)
	go func() {
		response, stream, err := s.Submit(ctx, request())
		ch <- outcome{response, stream, err}
	}()
	f.awaitCall(t, native.MethodPromptSubmit)
	cancel()
	got := <-ch
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v", got.err)
	}
	if got.stream != nil {
		go func() {
			for range got.stream {
			}
		}()
	}

	if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("overlapping submit: err=%v, want ErrRunActive", err)
	}

	f.event(native.EventMessageStart, 1, "")
	f.event(native.EventMessageComplete, 2, settleFrame("complete", ""))
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
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
}

func TestEventsBeforeConvergenceBufferAndReplay(t *testing.T) {

	s, f := openTest(t)

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

func TestResolveRejectsUnofferedApprovalAnswer(t *testing.T) {

	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventApprovalRequest, 2, `{"command":"rm -rf /tmp/x","choices":["once","deny"]}`)
	binding := lastInteraction(t, s)
	for name, answer := range map[string]protocol.InputAnswer{
		"unknown question": {QuestionID: "other", SelectedOptionIDs: []string{"once"}},
		"unoffered option": {QuestionID: "choice", SelectedOptionIDs: []string{"always"}},
		"empty option":     {QuestionID: "choice", SelectedOptionIDs: []string{""}},
	} {
		if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{answer}}}); !errors.Is(err, base.ErrInvalidResolution) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if f.callCount(native.MethodApprovalRespond) != 0 {
		t.Fatal("rejected answer reached approval.respond")
	}

	f.queue(native.MethodApprovalRespond, reply{result: native.ApprovalRespondResult{Resolved: true}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"deny"}}}}}); err != nil {
		t.Fatal(err)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	drain(t, (<-ch).stream)
}

func TestResolveParksSettlementUntilGateResolved(t *testing.T) {

	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventApprovalRequest, 2, `{"command":"rm -rf /tmp/x","choices":["once","deny"]}`)
	binding := lastInteraction(t, s)
	f.queue(native.MethodApprovalRespond, reply{
		result: native.ApprovalRespondResult{Resolved: true},
		before: func() { f.event(native.EventMessageComplete, 3, settleFrame("complete", "")) },
	})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"once"}}}}}); err != nil {
		t.Fatal(err)
	}
	got := <-ch
	events := drain(t, got.stream)
	validateWithCapabilities(t, got.response, events)
	resolvedIdx, terminalIdx := -1, -1
	for index, envelope := range events {
		switch envelope.Type {
		case protocol.TypeUserInputResolved:
			if resolvedIdx == -1 {
				resolvedIdx = index
			}
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			if terminalIdx == -1 {
				terminalIdx = index
			}
		}
	}
	if resolvedIdx == -1 {
		t.Fatalf("gate resolution dropped at terminality (%d events)", len(events))
	}
	if terminalIdx == -1 || resolvedIdx > terminalIdx {
		t.Fatalf("resolution index %d, terminal index %d", resolvedIdx, terminalIdx)
	}
}

func TestSudoSecretAnswersMustNameTheSurfacedQuestion(t *testing.T) {

	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventSecretRequest, 2, `{"request_id":"aaaa1111","prompt":"token?","env_var":"TOKEN"}`)
	binding := lastInteraction(t, s)
	for name, answer := range map[string]protocol.InputAnswer{
		"foreign question": {QuestionID: "other", Text: "hunter2"},
		"mixed form":       {QuestionID: "value", SelectedOptionIDs: []string{"hunter2"}, Text: "hunter2"},
	} {
		if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{answer}}}); !errors.Is(err, base.ErrInvalidResolution) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if f.callCount(native.MethodSecretRespond) != 0 {
		t.Fatal("rejected answer reached secret.respond")
	}
	f.queue(native.MethodSecretRespond, reply{result: native.RespondResult{Status: "ok"}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "value", Text: "hunter2"}}}}); err != nil {
		t.Fatal(err)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	drain(t, (<-ch).stream)
}

func TestChoiceLessClarifySurfacesAsText(t *testing.T) {

	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, `{"request_id":"aaaa1111","question":"why?","choices":[]}`)
	binding := lastInteraction(t, s)
	f.queue(native.MethodClarifyRespond, reply{result: native.RespondResult{Status: "ok"}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "answer", Text: "because"}}}}); err != nil {
		t.Fatal(err)
	}
	var respond native.RespondParams
	f.callsMu.Lock()
	for _, call := range f.calls {
		if call.method == native.MethodClarifyRespond {
			respond, _ = call.params.(native.RespondParams)
		}
	}
	f.callsMu.Unlock()
	if respond.Answer != "because" {
		t.Fatalf("native answer = %q", respond.Answer)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	got := <-ch
	events := drain(t, got.stream)
	var kind protocol.InputQuestionKind
	for _, envelope := range events {
		if envelope.Type != protocol.TypeUserInputRequested {
			continue
		}
		var p protocol.UserInputRequestedPayload
		if err := envelope.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if len(p.Questions) == 1 {
			kind = p.Questions[0].Kind
		}
	}
	if kind != protocol.InputText {
		t.Fatalf("advertised kind = %q, want %q", kind, protocol.InputText)
	}
	validateWithCapabilities(t, got.response, events)
}

func TestClarifyRejectsUnofferedSelection(t *testing.T) {

	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, `{"request_id":"aaaa1111","questions":[{"qid":"q1","question":"pick","choices":["a","b"]},{"qid":"q2","question":"many","choices":["x","y"],"multi_select":true}]}`)
	binding := lastInteraction(t, s)
	single := func(id string) protocol.InputAnswer {
		return protocol.InputAnswer{QuestionID: "q1", SelectedOptionIDs: []string{id}}
	}
	multi := func(ids ...string) protocol.InputAnswer {
		return protocol.InputAnswer{QuestionID: "q2", SelectedOptionIDs: ids}
	}
	cases := map[string][]protocol.InputAnswer{
		"single unoffered":   {single("bogus"), multi("x")},
		"single text":        {{QuestionID: "q1", Text: "a"}, multi("x")},
		"single option+text": {{QuestionID: "q1", SelectedOptionIDs: []string{"a"}, Text: "a"}, multi("x")},
		"multi unoffered":    {single("a"), multi("x", "bogus")},
		"multi text":         {single("a"), {QuestionID: "q2", SelectedOptionIDs: []string{"x"}, Text: "x"}},
		"multi duplicate":    {single("a"), multi("x", "x")},
	}
	for name, answers := range cases {
		if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: answers}}); !errors.Is(err, base.ErrInvalidResolution) {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
	if f.callCount(native.MethodClarifyRespond) != 0 {
		t.Fatal("rejected selection reached clarify.respond")
	}
	f.queue(native.MethodClarifyRespond, reply{result: native.RespondResult{Status: "ok"}})
	f.queue(native.MethodClarifyRespond, reply{result: native.RespondResult{Status: "ok"}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{single("b"), multi("x", "y")}}}); err != nil {
		t.Fatal(err)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	drain(t, (<-ch).stream)
}

func TestMultiSelectClarifyPreservesEverySelection(t *testing.T) {

	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, `{"request_id":"aaaa1111","question":"which?","choices":["a","b","c"],"multi_select":true}`)
	binding := lastInteraction(t, s)
	f.queue(native.MethodClarifyRespond, reply{result: native.RespondResult{Status: "ok"}})
	if err := s.Resolve(context.Background(), base.InteractionResolution{Input: &protocol.UserInputResolveRequest{InteractionID: binding, SessionID: "session", Answers: []protocol.InputAnswer{{QuestionID: "answer", SelectedOptionIDs: []string{"a", "c"}}}}}); err != nil {
		t.Fatal(err)
	}
	if got := f.callCount(native.MethodClarifyRespond); got != 1 {
		t.Fatalf("clarify.respond calls = %d", got)
	}
	var respond native.RespondParams
	found := false
	f.callsMu.Lock()
	for _, call := range f.calls {
		if call.method == native.MethodClarifyRespond {
			respond, found = call.params.(native.RespondParams)
		}
	}
	f.callsMu.Unlock()
	if !found || respond.Answer != `["a","c"]` {
		t.Fatalf("multi-select respond = %+v (found=%v)", respond, found)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	got := <-ch
	events := drain(t, got.stream)
	var kind protocol.InputQuestionKind
	for _, envelope := range events {
		if envelope.Type != protocol.TypeUserInputRequested {
			continue
		}
		var p protocol.UserInputRequestedPayload
		if err := envelope.DecodePayload(&p); err != nil {
			t.Fatal(err)
		}
		if len(p.Questions) == 1 {
			kind = p.Questions[0].Kind
		}
	}
	if kind != protocol.InputMultiChoice {
		t.Fatalf("advertised question kind = %q, want %q", kind, protocol.InputMultiChoice)
	}
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
	admitted := <-ch
	events := drain(t, admitted.stream)
	for _, envelope := range events {
		if envelope.Type != protocol.TypeUserInputResolved {
			continue
		}
		var resolved protocol.UserInputResolvedPayload
		if err := envelope.DecodePayload(&resolved); err != nil {
			t.Fatal(err)
		}
		if resolved.Status == protocol.InputSubmitted {
			t.Fatal("failed resolution projected as submitted")
		}
		if resolved.Status != protocol.InputCancelled {
			t.Fatalf("resolution status = %q, want %q", resolved.Status, protocol.InputCancelled)
		}
	}
	validateWithCapabilities(t, admitted.response, events)
}

func (f *fakeClient) transportClose() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead {
		return
	}
	f.dead = true
	if !f.closed {
		close(f.done)
		f.closed = true
	}
	close(f.in)
}

func waitUnusable(t *testing.T, s base.Session) {
	t.Helper()
	session := s.(*Session)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		session.mu.Lock()
		unusable := session.unusable
		session.mu.Unlock()
		if unusable {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("session did not become unusable after the transport closed")
}

func TestTransportDeathAfterAcceptanceProjectsTheOpenedTurn(t *testing.T) {
	run := func(t *testing.T, closeBeforeReply bool) {
		s, f := openTest(t)
		release := make(chan struct{})
		f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: native.SubmitStreaming}, before: func() { <-release }})
		ch := submitAsync(s)
		f.awaitCall(t, native.MethodPromptSubmit)

		f.event(native.EventMessageStart, 1, "")
		f.event(native.EventMessageDelta, 2, `{"text":"Hi"}`)
		if closeBeforeReply {
			f.transportClose()
			waitUnusable(t, s)
		}
		close(release)
		got := <-ch
		if got.err != nil {
			t.Fatalf("submit failed instead of projecting the opened turn: %v", got.err)
		}
		if got.stream == nil || got.response.Admission != protocol.AdmissionStarted {
			t.Fatalf("admission %+v with stream %v, want a started admission with a stream", got.response, got.stream)
		}
		if !closeBeforeReply {
			f.transportClose()
		}
		events := drain(t, got.stream)
		want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeRunFailed}
		if len(events) != len(want) {
			t.Fatalf("events %v", events)
		}
		for i := range want {
			if events[i].Type != want[i] {
				t.Fatalf("event %d = %s, want %s", i, events[i].Type, want[i])
			}
		}
		var delta protocol.ContentDeltaPayload
		if err := events[1].DecodePayload(&delta); err != nil || delta.Part.Type != protocol.ContentText || delta.Part.Text != "Hi" {
			t.Fatalf("buffered delta lost: %+v err=%v", delta.Part, err)
		}
		var failed protocol.RunFailedPayload
		if err := events[2].DecodePayload(&failed); err != nil || failed.Error.Code != "hermes_process_exit" || failed.Error.Message != "EOF" {
			t.Fatalf("terminal %+v err=%v, want hermes_process_exit carrying the transport error", failed.Error, err)
		}
		validateWithCapabilities(t, got.response, events)

		if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrSessionClosed) {
			t.Fatalf("session usable after transport death: %v", err)
		}
	}
	t.Run("reducer settles the failure before the reply lands", func(t *testing.T) { run(t, true) })
	t.Run("reply lands before the transport closes", func(t *testing.T) { run(t, false) })
}

func TestRunSettlesItsOpenChildrenBeforeItsTerminal(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventToolStart, 2, `{"tool_id":"t1","name":"read","context":"read"}`)
	f.event(native.EventClarifyRequest, 3, `{"request_id":"aaaa1111","question":"which?","choices":["a","b"]}`)
	f.event(native.EventMessageComplete, 4, settleFrame("complete", ""))
	admitted := <-ch
	events := drain(t, admitted.stream)

	terminal := -1
	resolved := -1
	failed := -1
	for i, envelope := range events {
		switch envelope.Type {
		case protocol.TypeRunCompleted:
			terminal = i
		case protocol.TypeUserInputResolved:
			resolved = i
		case protocol.TypeActionCallFailed:
			failed = i
		}
	}
	if resolved < 0 {
		t.Fatal("the open gate was never resolved")
	}
	if failed < 0 {
		t.Fatal("the unfinished tool was never settled")
	}
	if terminal < 0 {
		t.Fatal("the run never terminated")
	}
	if resolved > terminal || failed > terminal {
		t.Fatalf("children settled after the terminal: resolved=%d failed=%d terminal=%d", resolved, failed, terminal)
	}
	validateWithCapabilities(t, admitted.response, events)
}

func TestFailedRunSettlesItsOpenChildrenToo(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventToolStart, 2, `{"tool_id":"t1","name":"read","context":"read"}`)
	f.event(native.EventClarifyRequest, 3, `{"request_id":"aaaa1111","question":"which?","choices":["a","b"]}`)
	f.event(native.EventMessageComplete, 4, settleFrame("", ""))
	admitted := <-ch
	events := drain(t, admitted.stream)

	terminal := -1
	resolved := -1
	failed := -1
	for i, envelope := range events {
		switch envelope.Type {
		case protocol.TypeRunFailed:
			terminal = i
		case protocol.TypeUserInputResolved:
			resolved = i
		case protocol.TypeActionCallFailed:
			failed = i
		}
	}
	if terminal < 0 {
		t.Fatal("the run never failed")
	}
	if resolved < 0 || resolved > terminal {
		t.Fatalf("the gate was not resolved before the terminal: resolved=%d terminal=%d", resolved, terminal)
	}
	if failed < 0 || failed > terminal {
		t.Fatalf("the tool was not settled before the terminal: failed=%d terminal=%d", failed, terminal)
	}
	validateWithCapabilities(t, admitted.response, events)
}

func TestAGateNobodyCanAnswerIsRefused(t *testing.T) {
	for _, frame := range []struct {
		name  string
		event string
	}{
		{"no prompt", `{"request_id":"aaaa1111","question":"","choices":["a","b"]}`},
		{"an empty choice", `{"request_id":"aaaa1111","question":"which?","choices":[""]}`},
		{"an empty choice in a batch", `{"request_id":"aaaa1111","questions":[{"qid":"q1","question":"pick","choices":[""]}]}`},
	} {
		t.Run(frame.name, func(t *testing.T) { assertGateRefused(t, frame.event) })
	}
}

func assertGateRefused(t *testing.T, frame string) {
	t.Helper()
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, frame)
	got := <-ch
	events := drain(t, got.stream)
	for _, envelope := range events {
		if envelope.Type == protocol.TypeUserInputRequested {
			t.Fatal("a question with no prompt was offered to a responder")
		}
	}
	for _, envelope := range events {
		if envelope.Type != protocol.TypeRunFailed {
			continue
		}
		var failed protocol.RunFailedPayload
		if err := envelope.DecodePayload(&failed); err != nil {
			t.Fatal(err)
		}
		if failed.Error.Code != "hermes_invalid_event" || failed.Error.Message != "gate with a question nobody can answer" {
			t.Fatalf("refusal = %q/%q", failed.Error.Code, failed.Error.Message)
		}
		return
	}
	t.Fatal("the gate was not refused")
}

func TestAClarifyWithNoChoicesIsFreeTextRatherThanUnanswerable(t *testing.T) {
	s, f := openTest(t)
	ch := admit(t, s, f, true)
	f.event(native.EventClarifyRequest, 2, `{"request_id":"aaaa1111","question":"which?","choices":[]}`)
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	admitted := <-ch
	events := drain(t, admitted.stream)
	for _, envelope := range events {
		if envelope.Type != protocol.TypeUserInputRequested {
			continue
		}
		var requested protocol.UserInputRequestedPayload
		if err := envelope.DecodePayload(&requested); err != nil {
			t.Fatal(err)
		}
		if len(requested.Questions) != 1 || requested.Questions[0].Kind != protocol.InputText {
			t.Fatalf("questions = %+v, want one text question", requested.Questions)
		}
		return
	}
	t.Fatal("the gate was never opened")
}

func TestEveryQuestionAnswerableStatesWhatTheSchemaRequires(t *testing.T) {
	options := []protocol.InputOption{{ID: "a", Label: "a"}}
	for _, test := range []struct {
		name     string
		question protocol.InputQuestion
		want     bool
	}{
		{"a choice question with options", protocol.InputQuestion{ID: "q", Prompt: "which?", Kind: protocol.InputSingleChoice, Options: options}, true},
		{"a choice question with none", protocol.InputQuestion{ID: "q", Prompt: "which?", Kind: protocol.InputSingleChoice}, false},
		{"a multi choice with none", protocol.InputQuestion{ID: "q", Prompt: "which?", Kind: protocol.InputMultiChoice}, false},
		{"a text question needs none", protocol.InputQuestion{ID: "q", Prompt: "say?", Kind: protocol.InputText}, true},
		{"no id", protocol.InputQuestion{Prompt: "which?", Kind: protocol.InputText}, false},
		{"no prompt", protocol.InputQuestion{ID: "q", Kind: protocol.InputText}, false},
		{"an option with no id", protocol.InputQuestion{ID: "q", Prompt: "which?", Kind: protocol.InputSingleChoice, Options: []protocol.InputOption{{Label: "a"}}}, false},
		{"an option with no label", protocol.InputQuestion{ID: "q", Prompt: "which?", Kind: protocol.InputSingleChoice, Options: []protocol.InputOption{{ID: "a"}}}, false},
		{"one option of several with no id", protocol.InputQuestion{ID: "q", Prompt: "which?", Kind: protocol.InputSingleChoice, Options: []protocol.InputOption{{ID: "a", Label: "a"}, {Label: "b"}}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := everyQuestionAnswerable([]protocol.InputQuestion{test.question}); got != test.want {
				t.Fatalf("everyQuestionAnswerable = %v, want %v", got, test.want)
			}
		})
	}
}

func admitAt(t *testing.T, s base.Session, f *fakeClient, seq int64) outcome {
	t.Helper()
	f.queue(native.MethodPromptSubmit, reply{result: native.PromptSubmitResult{Status: native.SubmitStreaming}})
	ch := submitAsync(s)
	f.awaitCall(t, native.MethodPromptSubmit)
	f.event(native.EventMessageStart, seq, "")
	got := <-ch
	if got.err != nil {
		t.Fatal(got.err)
	}
	return got
}

func readUntilClosed(t *testing.T, stream base.EventStream) ([]protocol.Envelope, error) {
	t.Helper()
	var envelopes []protocol.Envelope
	var streamErr error
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case result, ok := <-stream:
			if !ok {
				return envelopes, streamErr
			}
			if result.Error != nil {
				streamErr = result.Error
				continue
			}
			envelopes = append(envelopes, result.Envelope)
		case <-timer.C:
			t.Fatal("event stream did not close")
			return nil, nil
		}
	}
}

func TestResumeAfterOverflowDeliversTheRestOfTheRun(t *testing.T) {
	s, f := openWithJournal(t, 256)
	run := admitAt(t, s, f, 1)
	var sent strings.Builder
	for index := range 2 * streamCapacity {
		f.event(native.EventMessageDelta, int64(index+2), `{"text":"`+strconv.Itoa(index)+`"}`)
		sent.WriteString(strconv.Itoa(index) + ",")
	}
	prefix, streamErr := readUntilClosed(t, run.stream)
	if !errors.Is(streamErr, base.ErrEventStreamOverflow) || len(prefix) != streamCapacity {
		t.Fatalf("stream closed with %v after %d envelopes", streamErr, len(prefix))
	}
	cursor := *prefix[len(prefix)-1].Sequence
	recovery, resumed, err := s.Resume(context.Background(), base.ResumeRequest{RunID: run.response.RunID, AfterSequence: cursor})
	if err != nil || recovery.ReplayGap != nil || recovery.RequestedAfter != cursor || recovery.ReplayedFrom != cursor+1 {
		t.Fatalf("resume = %+v, %v", recovery, err)
	}
	f.event(native.EventMessageComplete, int64(2*streamCapacity+2), settleFrame("complete", ""))
	events := append(prefix, drain(t, resumed)...)
	var received strings.Builder
	for index, event := range events {
		if *event.Sequence != uint64(index+1) {
			t.Fatalf("event %d carries sequence %d", index, *event.Sequence)
		}
		if event.Type != protocol.TypeContentDelta {
			continue
		}
		var delta protocol.ContentDeltaPayload
		if err := event.DecodePayload(&delta); err != nil {
			t.Fatal(err)
		}
		received.WriteString(delta.Part.Text + ",")
	}
	if received.String() != sent.String() || events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("resumed run delivered %q and ended with %s", received.String(), events[len(events)-1].Type)
	}
	validateWithCapabilities(t, run.response, events)
}

func TestResumeReportsAGapOnceTheJournalEvictsTheCursor(t *testing.T) {
	s, f := openWithJournal(t, 4)
	first := admitAt(t, s, f, 1)
	for index := range 6 {
		f.event(native.EventMessageDelta, int64(index+2), `{"text":"x"}`)
	}
	f.event(native.EventMessageComplete, 8, settleFrame("complete", ""))
	events := drain(t, first.stream)
	latest := *events[len(events)-1].Sequence
	oldest := latest - 3
	recovery, stream, err := s.Resume(context.Background(), base.ResumeRequest{RunID: first.response.RunID, AfterSequence: oldest - 2})
	var gap *base.ReplayGap
	if !errors.As(err, &gap) || *gap != (base.ReplayGap{RequestedAfter: oldest - 2, OldestAvailable: oldest, LatestAvailable: latest}) || recovery.ReplayGap != gap || recovery.ReplayedFrom != 0 || recovery.ReplayedThrough != 0 || recovery.State.Status != protocol.SessionIdle {
		t.Fatalf("resume = %+v, %v", recovery, err)
	}
	if replayed, _ := readUntilClosed(t, stream); len(replayed) != 0 {
		t.Fatalf("a gap replayed %d envelopes", len(replayed))
	}
	_, stream, err = s.Resume(context.Background(), base.ResumeRequest{RunID: first.response.RunID, AfterSequence: oldest - 1})
	if err != nil {
		t.Fatal(err)
	}
	if replayed := drain(t, stream); len(replayed) != 4 || *replayed[0].Sequence != oldest {
		t.Fatalf("boundary replay delivered %d envelopes", len(replayed))
	}
	second := admitAt(t, s, f, 9)
	for index := range 3 {
		f.event(native.EventMessageDelta, int64(index+10), `{"text":"y"}`)
	}
	f.event(native.EventMessageComplete, 13, settleFrame("complete", ""))
	drain(t, second.stream)
	recovery, _, err = s.Resume(context.Background(), base.ResumeRequest{RunID: first.response.RunID})
	if !errors.As(err, &gap) || *gap != (base.ReplayGap{RequestedAfter: 0, OldestAvailable: 0, LatestAvailable: latest}) {
		t.Fatalf("resume of an evicted run = %+v, %v", recovery, err)
	}
}

func TestResumeOfALiveRunRefusesAFutureCursorAndFollowsItToTheTerminal(t *testing.T) {
	s, f := openWithJournal(t, 64)
	run := admitAt(t, s, f, 1)
	f.event(native.EventMessageDelta, 2, `{"text":"Hi"}`)
	adaptertest.Next(t, run.stream, 2*time.Second)
	latest := *adaptertest.Next(t, run.stream, 2*time.Second).Sequence
	if _, stream, err := s.Resume(context.Background(), base.ResumeRequest{RunID: run.response.RunID, AfterSequence: latest + 1}); !errors.Is(err, base.ErrReplayCursorFuture) || stream != nil {
		t.Fatalf("future cursor on a live run = %v", err)
	}
	recovery, resumed, err := s.Resume(context.Background(), base.ResumeRequest{RunID: run.response.RunID, AfterSequence: latest})
	if err != nil || recovery.ReplayedFrom != latest || recovery.ReplayedThrough != latest || recovery.State.ActiveRunID != run.response.RunID {
		t.Fatalf("resume at the head = %+v, %v", recovery, err)
	}
	f.event(native.EventMessageComplete, 3, settleFrame("complete", ""))
	followed := drain(t, resumed)
	if len(followed) != 1 || *followed[0].Sequence != latest+1 || followed[0].Type != protocol.TypeRunCompleted {
		t.Fatalf("resumed live run delivered %v", followed)
	}
}

func TestResumeRefusesAFutureCursorAnUnknownRunAndAClosedSession(t *testing.T) {
	s, f := openWithJournal(t, 64)
	run := admitAt(t, s, f, 1)
	f.event(native.EventMessageComplete, 2, settleFrame("complete", ""))
	events := drain(t, run.stream)
	latest := *events[len(events)-1].Sequence
	if _, stream, err := s.Resume(context.Background(), base.ResumeRequest{RunID: run.response.RunID, AfterSequence: latest + 1}); !errors.Is(err, base.ErrReplayCursorFuture) || stream != nil {
		t.Fatalf("future cursor = %v", err)
	}
	if _, stream, err := s.Resume(context.Background(), base.ResumeRequest{RunID: "run-unknown"}); !errors.Is(err, base.ErrRunNotFound) || stream != nil {
		t.Fatalf("unknown run = %v", err)
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, stream, err := s.Resume(context.Background(), base.ResumeRequest{RunID: run.response.RunID}); !errors.Is(err, base.ErrSessionClosed) || stream != nil {
		t.Fatalf("closed session = %v", err)
	}
}

func TestResumeReplaysAnEndedRunFromADetachedJournal(t *testing.T) {
	s, f := openWithJournal(t, 64)
	run := admitAt(t, s, f, 1)
	for index := range 3 {
		f.event(native.EventMessageDelta, int64(index+2), `{"text":"x"}`)
	}
	f.event(native.EventMessageComplete, 5, settleFrame("complete", ""))
	events := drain(t, run.stream)
	last := *events[len(events)-1].Sequence
	want, err := json.Marshal(events[1:])
	if err != nil {
		t.Fatal(err)
	}
	vandalize := func(envelopes []protocol.Envelope) {
		for _, envelope := range envelopes {
			envelope.Payload[0] = 'X'
			*envelope.Sequence += 1000
			*envelope.TimestampMS += 1000
		}
	}
	vandalize(events)
	for range 2 {
		recovery, replay, err := s.Resume(context.Background(), base.ResumeRequest{RunID: run.response.RunID, AfterSequence: 1})
		if err != nil || recovery.ReplayedFrom != 2 || recovery.ReplayedThrough != last {
			t.Fatalf("resume = %+v, %v", recovery, err)
		}
		replayed := drain(t, replay)
		got, err := json.Marshal(replayed)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("replay = %s, want %s", got, want)
		}
		vandalize(replayed)
	}
}

func TestResumeReplaysARunAfterTheGatewayExits(t *testing.T) {
	s, f := openWithJournal(t, 64)
	run := admitAt(t, s, f, 1)
	f.event(native.EventMessageDelta, 2, `{"text":"Hi"}`)
	f.transportClose()
	events := drain(t, run.stream)
	if _, _, err := s.Submit(context.Background(), request()); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("session usable after the gateway exited: %v", err)
	}
	_, replay, err := s.Resume(context.Background(), base.ResumeRequest{RunID: run.response.RunID, AfterSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	replayed := drain(t, replay)
	want, _ := json.Marshal(events[1:])
	got, _ := json.Marshal(replayed)
	var failed protocol.RunFailedPayload
	if !bytes.Equal(got, want) || len(replayed) != 2 || replayed[1].DecodePayload(&failed) != nil || failed.Error.Code != "hermes_process_exit" {
		t.Fatalf("replay after the gateway exited = %s", got)
	}
}
