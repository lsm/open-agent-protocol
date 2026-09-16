package servestdio

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
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// The open and events ops complete the stdio op mirror. Open is an ordinary
// request/response op and is tested as one. Events is not: it answers once
// and then writes for as long as the subscription lives, so what these tests
// pin is the part that has no HTTP counterpart — that the acknowledgement
// precedes the stream, that interest is not charged against the bound meant
// for work, and that a live subscription does not turn every shutdown into a
// full window.

// signalLine is the decoded form of any non-response line: every line kind a
// subscription emits carries the event name and the events request's id, and
// the rest are read per kind where a test needs them.
type signalLine struct {
	Event     string          `json:"event"`
	ID        int64           `json:"id"`
	SessionID string          `json:"session_id"`
	Sequence  *uint64         `json:"sequence"`
	Envelope  json.RawMessage `json:"envelope"`
}

func (f *frontend) expectSignal(id int64, want string) signalLine {
	f.t.Helper()
	line := f.line()
	var signal signalLine
	if err := json.Unmarshal([]byte(line), &signal); err != nil {
		f.t.Fatalf("signal line %q: %v", line, err)
	}
	if signal.Event != want {
		f.t.Fatalf("line %q is %q, want %q", line, signal.Event, want)
	}
	if signal.ID != id {
		f.t.Fatalf("signal id %d, want %d", signal.ID, id)
	}
	return signal
}

// openLine builds an open request line for the memory adapter.
func openLine(t *testing.T, id int64, sessionID string) string {
	t.Helper()
	envelope := requestEnvelope(t, "req-open", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID: protocol.SessionID(sessionID),
	}, "", "")
	return fmt.Sprintf(`{"id":%d,"op":"open","adapter":"memory","request":%s}`, id, envelope)
}

// TestOpenOpOpensASession drives the op end to end: the response is the
// session.open.response envelope the HTTP route returns, and the session it
// reports is live on the hub afterwards.
func TestOpenOpOpensASession(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})

	f.send(openLine(t, 1, "stdio-open"))
	response := f.expectResponse(1)
	if !response.OK {
		t.Fatalf("open failed: %+v", response.Error)
	}
	var envelope protocol.Envelope
	if err := json.Unmarshal(response.Result, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != protocol.TypeSessionOpenResponse {
		t.Fatalf("response type %s, want %s", envelope.Type, protocol.TypeSessionOpenResponse)
	}
	if envelope.SessionID != "stdio-open" {
		t.Fatalf("response session %q, want stdio-open", envelope.SessionID)
	}
	if envelope.InReplyTo != "req-open" {
		t.Fatalf("response in_reply_to %q, want req-open", envelope.InReplyTo)
	}
	if _, err := hub.Session("stdio-open"); err != nil {
		t.Fatalf("session is not live on the hub after a successful open: %v", err)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenOpRefusals pins the refusals that are the op's own, each under the
// code the HTTP route answers with.
func TestOpenOpRefusals(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "taken")
	f := startFrontend(t, hub, Options{})

	for _, testCase := range []struct {
		name string
		line string
		code string
	}{
		{
			name: "unknown adapter",
			line: strings.Replace(openLine(t, 1, "fresh"), `"adapter":"memory"`, `"adapter":"nope"`, 1),
			code: "unknown_adapter",
		},
		{
			name: "session exists",
			line: openLine(t, 2, "taken"),
			code: "session_exists",
		},
		{
			name: "wrong envelope type",
			line: fmt.Sprintf(`{"id":3,"op":"open","adapter":"memory","request":%s}`,
				requestEnvelope(t, "req-wrong", protocol.TypeSessionStateRequest, protocol.SessionStateRequest{SessionID: "taken"}, "taken", "")),
			code: "type_mismatch",
		},
		{
			name: "no adapter named",
			line: `{"id":4,"op":"open","request":null}`,
			code: "invalid_request",
		},
		{
			name: "session id is not an open parameter",
			line: `{"id":5,"op":"open","adapter":"memory","session_id":"taken","request":null}`,
			code: "invalid_request",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f.send(testCase.line)
			response := f.decodeResponse(f.line())
			if response.OK {
				t.Fatalf("open succeeded, want %s", testCase.code)
			}
			if response.Error.Code != testCase.code {
				t.Fatalf("code %q, want %q (%s)", response.Error.Code, testCase.code, response.Error.Message)
			}
		})
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestEventsOpDeliversTheRunStream is the op's whole purpose: a subscription
// started over stdio carries the run's envelopes as their own lines, in
// sequence, ending at the terminal envelope with no further signal — the
// terminal is the marker, exactly as the SSE response simply ends after it.
func TestEventsOpDeliversTheRunStream(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})

	f.send(openLine(t, 1, "stream"))
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("open failed: %+v", response.Error)
	}
	f.send(`{"id":2,"op":"events","session_id":"stream"}`)
	ack := f.expectResponse(2)
	if !ack.OK {
		t.Fatalf("events failed: %+v", ack.Error)
	}
	if string(ack.Result) != "null" {
		t.Fatalf("events result %s, want null", ack.Result)
	}

	submit := requestEnvelope(t, "req-submit", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "stream", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	}, "stream", "")
	f.send(fmt.Sprintf(`{"id":3,"op":"submit","session_id":"stream","request":%s}`, submit))

	// The submit acknowledgement and the subscription's envelopes share one
	// writer, so the only ordering claimed here is per-subscription: the
	// envelope lines arrive in sequence order and end at the terminal.
	// The reference adapter's run waits on two scripted gates, which are
	// answered here from the subscription's own lines: the stream is the only
	// thing telling this host a gate is open, which is what an events op is
	// for.
	var lastSequence uint64
	var terminal protocol.EnvelopeType
	nextID := int64(4)
	for terminal == "" {
		line := f.line()
		if strings.HasPrefix(line, `{"id":`) {
			response := f.decodeResponse(line)
			if !response.OK {
				t.Fatalf("op %d failed: %+v", response.ID, response.Error)
			}
			continue
		}
		var signal signalLine
		if err := json.Unmarshal([]byte(line), &signal); err != nil {
			t.Fatal(err)
		}
		if signal.Event != signalEnvelope {
			t.Fatalf("unexpected signal %q on a healthy stream: %s", signal.Event, line)
		}
		if signal.ID != 2 {
			t.Fatalf("envelope line correlated to %d, want the events request 2", signal.ID)
		}
		if signal.SessionID != "stream" {
			t.Fatalf("envelope line names session %q, want stream", signal.SessionID)
		}
		if signal.Sequence == nil || *signal.Sequence <= lastSequence {
			t.Fatalf("sequence %v does not advance past %d", signal.Sequence, lastSequence)
		}
		lastSequence = *signal.Sequence
		var envelope protocol.Envelope
		if err := json.Unmarshal(signal.Envelope, &envelope); err != nil {
			t.Fatal(err)
		}
		switch envelope.Type {
		case protocol.TypeActionPermissionRequested, protocol.TypeUserInputRequested:
			f.send(fmt.Sprintf(`{"id":%d,"op":"resolve","session_id":"stream","request":%s}`,
				nextID, resolveEnvelope(t, fmt.Sprintf("req-resolve-%d", nextID), envelope, "stream")))
			nextID++
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			terminal = envelope.Type
		}
	}
	if terminal != protocol.TypeRunCompleted {
		t.Fatalf("run settled %s, want %s", terminal, protocol.TypeRunCompleted)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestEventsAcknowledgementPrecedesTheStream pins the ordering the op
// promises: a host never sees an envelope line for a subscription it has not
// been told exists. The subscription is started on a session whose run is
// already complete and replayed from the very beginning, so the hub has a
// full journal to deliver the instant the pump starts — the case where an
// acknowledgement sent after the pump would lose the race.
func TestEventsAcknowledgementPrecedesTheStream(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	session := openSessionEntry(t, hub, "ordered")
	runID := runToCompletion(t, hub, session)

	f := startFrontend(t, hub, Options{})
	f.send(`{"id":7,"op":"events","session_id":"ordered","after":0}`)
	line := f.line()
	if !strings.HasPrefix(line, `{"id":`) {
		t.Fatalf("first line is %q, want the events acknowledgement", line)
	}
	if response := f.decodeResponse(line); !response.OK || response.ID != 7 {
		t.Fatalf("first line is not the acknowledgement: %+v", response)
	}
	signal := f.expectSignal(7, signalEnvelope)
	var envelope protocol.Envelope
	if err := json.Unmarshal(signal.Envelope, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.RunID != runID {
		t.Fatalf("replay starts on run %q, want %q", envelope.RunID, runID)
	}
	// The rest of the replay is read before finishing. The writer parks
	// inside a write to a pipe no one is reading, so a test that walks away
	// mid-stream stalls the drain rather than the frontend.
	f.drainSubscription(7)
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// drainSubscription reads one subscription's lines up to and including the
// run's terminal envelope.
func (f *frontend) drainSubscription(id int64) {
	f.t.Helper()
	for {
		signal := f.expectSignal(id, signalEnvelope)
		var envelope protocol.Envelope
		if err := json.Unmarshal(signal.Envelope, &envelope); err != nil {
			f.t.Fatal(err)
		}
		switch envelope.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			return
		}
	}
}

// TestEventsOpRefusals pins the refusals decided on the worker, which are the
// op's own answer rather than a signal arriving after a success.
func TestEventsOpRefusals(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "live")
	closed := openSessionEntry(t, hub, "gone")
	if err := closed.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})

	for _, testCase := range []struct {
		name string
		line string
		code string
	}{
		{"unknown session", `{"id":1,"op":"events","session_id":"nope"}`, "unknown_session"},
		{"closed session", `{"id":2,"op":"events","session_id":"gone"}`, "session_closed"},
		{"non-numeric cursor", `{"id":3,"op":"events","session_id":"live","after":"twelve"}`, "invalid_cursor"},
		{"no run to resume", `{"id":4,"op":"events","session_id":"live","after":1}`, "no_run_to_resume"},
		{"adapter is not an events parameter", `{"id":5,"op":"events","session_id":"live","adapter":"memory"}`, "invalid_request"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f.send(testCase.line)
			response := f.decodeResponse(f.line())
			if response.OK {
				t.Fatalf("events succeeded, want %s", testCase.code)
			}
			if response.Error.Code != testCase.code {
				t.Fatalf("code %q, want %q (%s)", response.Error.Code, testCase.code, response.Error.Message)
			}
		})
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestEventsOpReportsAReplayGap holds the one refusal that is not a failed
// op: an expired cursor answers ok and then says what cannot be delivered,
// which is what keeps a gap from becoming fake continuity. The journal is
// sized so the run overruns it.
func TestEventsOpReportsAReplayGap(t *testing.T) {
	hub := newTestHub(t, 2, 64)
	session := openSessionEntry(t, hub, "expired")
	runToCompletion(t, hub, session)

	f := startFrontend(t, hub, Options{})
	f.send(`{"id":9,"op":"events","session_id":"expired","after":1}`)
	if response := f.expectResponse(9); !response.OK {
		t.Fatalf("an expired cursor is a successful op that reports a gap, got %+v", response.Error)
	}
	f.expectSignal(9, signalReplayGap)
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestSubscriptionsAreNotChargedAgainstTheInFlightBound pins what attach
// exists for. The bound caps concurrent adapter work; a pump is interest in a
// stream and performs none, so a frontend admitting one op at a time still
// serves ops while a subscription is live. Charged against the bound, the
// state op below would be refused busy.
func TestSubscriptionsAreNotChargedAgainstTheInFlightBound(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "interest")
	f := startFrontend(t, hub, Options{MaxConcurrentOps: 1})

	f.send(`{"id":1,"op":"events","session_id":"interest"}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	f.send(`{"id":2,"op":"state","session_id":"interest"}`)
	response := f.expectResponse(2)
	if !response.OK {
		t.Fatalf("an op behind a live subscription was refused %q: %s", response.Error.Code, response.Error.Message)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestALiveSubscriptionDoesNotStallShutdown is the liveness claim. A pump
// parks in Subscription.Next, which no worker wait can end: waiting for one
// the way teardown waits for a worker would spend the whole shutdown window
// on every session that ends with a subscription open, which is every
// ordinary session. Closing admission cancels them instead.
//
// The window is set far above the time a correct teardown needs, so the test
// fails on the behaviour and not on a timing margin: a stalled shutdown takes
// the whole 30 seconds and this allows two.
func TestALiveSubscriptionDoesNotStallShutdown(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "parked")
	f := startFrontend(t, hub, Options{ShutdownTimeout: 30 * time.Second})

	f.send(`{"id":1,"op":"events","session_id":"parked"}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	started := time.Now()
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("shutdown behind a live subscription took %s; the pump was waited for rather than told to stop", elapsed)
	}
}

// openSessionEntry opens a session and hands back the hub entry, for the
// tests that need to drive a run before the frontend starts.
func openSessionEntry(t *testing.T, hub *serve.Hub, id string) *serve.Session {
	t.Helper()
	entry, _, err := hub.Open(context.Background(), "memory", base.OpenRequest{
		SessionID: protocol.SessionID(id), Participant: protocol.Participant{ID: serve.DefaultParticipant},
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

// runToCompletion submits one message and drains the run to its terminal
// envelope on a hub-side subscription, answering the reference adapter's two
// scripted gates on the way, so the journal the events op later replays is
// complete before the frontend starts.
func runToCompletion(t *testing.T, hub *serve.Hub, entry *serve.Session) protocol.RunID {
	t.Helper()
	subscription, err := hub.Subscribe(context.Background(), entry.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	admission, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: entry.ID(), Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for {
		envelope, err := subscription.Next()
		if err != nil {
			t.Fatalf("run did not reach a terminal envelope: %v", err)
		}
		switch envelope.Type {
		case protocol.TypeActionPermissionRequested:
			var gate protocol.PermissionRequestedPayload
			if err := envelope.DecodePayload(&gate); err != nil {
				t.Fatal(err)
			}
			resolve(t, entry, base.InteractionResolution{
				RunID: gate.RunID, RespondedBy: gate.RespondedBy,
				Permission: &protocol.PermissionResolveRequest{
					InteractionID: gate.InteractionID, SessionID: entry.ID(), RunID: gate.RunID,
					RequestedBy: gate.RequestedBy, RespondedBy: gate.RespondedBy,
					ChoiceID: "approve", Granted: true,
				},
			})
		case protocol.TypeUserInputRequested:
			var gate protocol.UserInputRequestedPayload
			if err := envelope.DecodePayload(&gate); err != nil {
				t.Fatal(err)
			}
			resolve(t, entry, base.InteractionResolution{
				RunID: gate.RunID, RespondedBy: gate.RespondedBy,
				Input: &protocol.UserInputResolveRequest{
					InteractionID: gate.InteractionID, SessionID: entry.ID(), RunID: gate.RunID,
					RequestedBy: gate.RequestedBy, RespondedBy: gate.RespondedBy,
					Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
				},
			})
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			return admission.RunID
		}
	}
}

func resolve(t *testing.T, entry *serve.Session, resolution base.InteractionResolution) {
	t.Helper()
	if err := entry.Resolve(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
}

// streamAdapter hands the test direct control of one run's event stream, so
// the ending a real adapter reaches by failing can be produced on demand.
type streamAdapter struct {
	mu      sync.Mutex
	session *streamSession
}

func (a *streamAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities:       protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "reference.stream"}},
		CapabilityRevision: "stream-v1",
	}, nil
}

func (a *streamAdapter) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	session := &streamSession{id: request.SessionID}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	return session, nil
}

func (a *streamAdapter) active(t *testing.T) *streamSession {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		t.Fatal("no session opened yet")
	}
	return a.session
}

type streamSession struct {
	id          protocol.SessionID
	mu          sync.Mutex
	stream      chan base.Result
	run         protocol.RunID
	closed      bool
	resumeFails bool
}

var _ base.Session = (*streamSession)(nil)

func (s *streamSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	if s.stream != nil {
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunActive
	}
	s.run = "stream-run-1"
	s.stream = make(chan base.Result, 8)
	return protocol.MessageSubmitResponse{SessionID: s.id, RunID: s.run, Accepted: true}, s.stream, nil
}

func (s *streamSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: s.id, Status: protocol.SessionIdle}, nil
}

func (s *streamSession) Resolve(context.Context, base.InteractionResolution) error { return nil }

func (s *streamSession) Cancel(_ context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: s.id, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
}

// Resume hands back a stream that fails before delivering anything, which is
// the case where the pump has no delivered position of its own.
func (s *streamSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	if !s.resumeFails {
		return base.Recovery{}, nil, base.ErrRunNotFound
	}
	out := make(chan base.Result, 1)
	out <- base.Result{Error: errors.New("the resumed stream died")}
	close(out)
	return base.Recovery{}, out, nil
}

func (s *streamSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *streamSession) emit(t *testing.T, sequence uint64) {
	t.Helper()
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStatusUpdated,
		protocol.EnvelopeID(fmt.Sprintf("stream-%d", sequence)), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	envelope.SessionID, envelope.RunID, envelope.Sequence = s.id, s.run, &sequence
	s.stream <- base.Result{Envelope: envelope}
}

// emitBroken publishes an envelope whose payload cannot be re-encoded.
func (s *streamSession) emitBroken(t *testing.T, sequence uint64) {
	t.Helper()
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStatusUpdated, "broken", protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	envelope.SessionID, envelope.RunID, envelope.Sequence = s.id, s.run, &sequence
	envelope.Payload = json.RawMessage("{not json")
	s.stream <- base.Result{Envelope: envelope}
}

func (s *streamSession) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stream <- base.Result{Error: err}
	close(s.stream)
	s.stream = nil
}

// TestAFailedRunStreamEndsTheSubscriptionOutLoud is the ending this framing
// has to name and SSE does not. There, the response body stops and the client
// sees a closed connection; here the pipe stays open and carries every other
// subscription, so a host given no line cannot tell a dead subscription from
// an idle one and waits on events that are never coming.
//
// The signal names the last position the host actually received, so a fresh
// events op resumes from what it got rather than from what the hub sent.
func TestAFailedRunStreamEndsTheSubscriptionOutLoud(t *testing.T) {
	registry := serve.NewRegistry()
	adapter := &streamAdapter{}
	if err := registry.Register("stream", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 64})
	entry, _, err := hub.Open(context.Background(), "stream", base.OpenRequest{
		SessionID: "failing", Participant: protocol.Participant{ID: serve.DefaultParticipant},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})

	f.send(`{"id":1,"op":"events","session_id":"failing"}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "failing", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}); err != nil {
		t.Fatal(err)
	}
	session := adapter.active(t)
	session.emit(t, 1)
	delivered := f.expectSignal(1, signalEnvelope)
	if delivered.Sequence == nil || *delivered.Sequence != 1 {
		t.Fatalf("first envelope sequence %v, want 1", delivered.Sequence)
	}
	session.fail(errors.New("native transport died"))

	line := f.line()
	var failure struct {
		Event     string `json:"event"`
		ID        int64  `json:"id"`
		SessionID string `json:"session_id"`
		RunID     string `json:"run_id"`
		Sequence  uint64 `json:"sequence"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Event != signalStreamFailed {
		t.Fatalf("line %q is %q, want %q", line, failure.Event, signalStreamFailed)
	}
	if failure.ID != 1 || failure.SessionID != "failing" {
		t.Fatalf("signal is not correlated to the events request: %s", line)
	}
	if failure.RunID != "stream-run-1" {
		t.Fatalf("signal names run %q, want stream-run-1", failure.RunID)
	}
	if failure.Sequence != 1 {
		t.Fatalf("signal resumes after %d, want the last delivered sequence 1", failure.Sequence)
	}
	// The adapter's own diagnostic stays out of the line: it is unbounded and
	// the frame limit's floor has to hold.
	if strings.Contains(failure.Message, "native transport died") {
		t.Fatalf("the adapter diagnostic reached the wire: %s", failure.Message)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestEveryEndingFitsTheFrameLimitFloor is the invariant behind the minimal
// forms: a subscription's last line is the one line that must always be
// deliverable, because the op was already acknowledged and nothing else will
// tell the host it has stopped. The full forms carry identifiers a host
// supplied or an adapter minted and can exceed any limit; the fallbacks carry
// the cursor and nothing else, and must fit the smallest limit New accepts.
//
// The values are the largest each field can hold, so the check is the floor
// and not a sample.
func TestEveryEndingFitsTheFrameLimitFloor(t *testing.T) {
	const wide = int64(1) << 62
	const far = uint64(1) << 63
	for _, ending := range []struct {
		name string
		line any
	}{
		{signalOverflow, overflowLine{Event: signalOverflow, ID: wide, LastSequence: far}},
		{signalReplayGap, gapLine{Event: signalReplayGap, ID: wide, RequestedAfter: far, OldestAvailable: far, LatestAvailable: far}},
		{signalSessionClosed, sessionClosedLine{Event: signalSessionClosed, ID: wide}},
		{signalFrameLimit, frameLimitLine{Event: signalFrameLimit, ID: wide, Sequence: far}},
		{signalStreamFailed, streamFailedLine{Event: signalStreamFailed, ID: wide, Sequence: far}},
	} {
		t.Run(ending.name, func(t *testing.T) {
			encoded, err := json.Marshal(ending.line)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > minFrameLimit {
				t.Fatalf("the minimal %s is %d bytes, over the %d-byte floor: %s", ending.name, len(encoded), minFrameLimit, encoded)
			}
		})
	}
}

// TestAnEndingTooLargeToFrameStillArrives drives the fallback through the
// real frontend rather than trusting the encoding check above. The session id
// is long enough that the full replay-gap line cannot be framed, and short
// enough that the request naming it can — the exact window where an ending
// was previously logged and dropped, leaving the host waiting on a
// subscription that never started.
func TestAnEndingTooLargeToFrameStillArrives(t *testing.T) {
	sessionID := strings.Repeat("s", 150)
	hub := newTestHub(t, 2, 64)
	entry := openSessionEntry(t, hub, sessionID)
	runToCompletion(t, hub, entry)

	f := startFrontend(t, hub, Options{FrameLimit: minFrameLimit})
	f.send(fmt.Sprintf(`{"id":1,"op":"events","session_id":%q,"after":1}`, sessionID))
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("an expired cursor is a successful op that reports a gap, got %+v", response.Error)
	}
	gap := f.expectSignal(1, signalReplayGap)
	if gap.SessionID != "" {
		t.Fatalf("the fallback kept the session id that made the full form unframable: %+v", gap)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestAFailedResumeReportsTheRequestedCursor pins where a subscription that
// never delivered anything tells the host to resume from. Zero would send a
// host that asked from sequence 6 back to the beginning, redelivering
// everything it had already consumed — or into a replay gap.
func TestAFailedResumeReportsTheRequestedCursor(t *testing.T) {
	registry := serve.NewRegistry()
	adapter := &streamAdapter{}
	if err := registry.Register("stream", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 64})
	entry, _, err := hub.Open(context.Background(), "stream", base.OpenRequest{
		SessionID: "resuming", Participant: protocol.Participant{ID: serve.DefaultParticipant},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "resuming", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}); err != nil {
		t.Fatal(err)
	}
	session := adapter.active(t)
	session.mu.Lock()
	session.resumeFails = true
	session.mu.Unlock()

	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"events","session_id":"resuming","after":6}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	line := f.line()
	var failure struct {
		Event    string `json:"event"`
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal([]byte(line), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Event != signalStreamFailed {
		t.Fatalf("line %q is %q, want %q", line, failure.Event, signalStreamFailed)
	}
	if failure.Sequence != 6 {
		t.Fatalf("the ending resumes after %d, want the requested cursor 6", failure.Sequence)
	}
	session.mu.Lock()
	session.stream = nil
	session.mu.Unlock()
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestAnUnencodableEnvelopeEndsTheSubscriptionOutLoud covers the other way a
// pump can die with nothing to show for it. An envelope this frontend cannot
// encode stops the subscription exactly as a failed stream does, and leaves
// the host exactly as unable to tell.
func TestAnUnencodableEnvelopeEndsTheSubscriptionOutLoud(t *testing.T) {
	registry := serve.NewRegistry()
	adapter := &streamAdapter{}
	if err := registry.Register("stream", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 64})
	entry, _, err := hub.Open(context.Background(), "stream", base.OpenRequest{
		SessionID: "unencodable", Participant: protocol.Participant{ID: serve.DefaultParticipant},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"events","session_id":"unencodable"}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "unencodable", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}); err != nil {
		t.Fatal(err)
	}
	adapter.active(t).emitBroken(t, 1)

	line := f.line()
	var failure struct {
		Event string `json:"event"`
		ID    int64  `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Event != signalStreamFailed || failure.ID != 1 {
		t.Fatalf("line %q is not a correlated stream-failed ending", line)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenRefusalsAreBounded holds the open op to the bound its mirrored
// route applies. A tool source id is caller-supplied and has no schema length
// of its own, so a refusal that echoes one verbatim is as long as the caller
// wants — where the HTTP route sends 300 runes, and where under a small frame
// limit the typed refusal would degrade to response_too_large and lose the
// same-code parity the op claims.
func TestOpenRefusalsAreBounded(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	f := startFrontend(t, hub, Options{})
	huge := strings.Repeat("x", 4000)
	request := requestEnvelope(t, "req-huge", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID:   "bounded",
		ToolSources: []protocol.ToolSourceAttachment{{ID: huge, Kind: protocol.ToolSourceProcess, Command: "/bin/sh"}},
	}, "", "")
	f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"memory","request":%s}`, request))
	response := f.expectResponse(1)
	if response.OK {
		t.Fatal("an open naming a wire-supplied command succeeded")
	}
	if runes := len([]rune(response.Error.Message)); runes > 301 {
		t.Fatalf("refusal message is %d runes; the mirrored route bounds it at 300", runes)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestSubscriptionsAreBounded pins the ceiling on interest. A pump charges no
// ops slot by design, and the events op releases the slot it held the moment
// it acknowledges, so without a bound of its own a host looping on events
// against an idle session accumulates pumps without limit — each one a
// goroutine, a hub subscriber queue, and a share of every envelope the hub
// fans out.
//
// The refusal is the one an op over the in-flight bound already gets, so a
// host needs no new vocabulary to handle it, and ordinary ops keep working
// behind a frontend whose subscriptions are full.
func TestSubscriptionsAreBounded(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "bounded")
	f := startFrontend(t, hub, Options{MaxSubscriptions: 2})

	for id := int64(1); id <= 2; id++ {
		f.send(fmt.Sprintf(`{"id":%d,"op":"events","session_id":"bounded"}`, id))
		if response := f.expectResponse(id); !response.OK {
			t.Fatalf("subscription %d was refused below the bound: %+v", id, response.Error)
		}
	}
	f.send(`{"id":3,"op":"events","session_id":"bounded"}`)
	refused := f.expectResponse(3)
	if refused.OK {
		t.Fatal("a third subscription was admitted past the bound of two")
	}
	if refused.Error.Code != "busy" {
		t.Fatalf("code %q, want busy: %s", refused.Error.Code, refused.Error.Message)
	}
	if !strings.Contains(refused.Error.Message, "send this request again") {
		t.Fatalf("the refusal does not say it may be retried: %s", refused.Error.Message)
	}
	// Interest being full is not work being full: ordinary ops still run.
	f.send(`{"id":4,"op":"state","session_id":"bounded"}`)
	if response := f.expectResponse(4); !response.OK {
		t.Fatalf("an ordinary op was refused behind full subscriptions: %+v", response.Error)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestATypedRefusalShedsItsDetailsRatherThanItsCode is the response-side form
// of the rule every subscription ending already follows: the answer must be
// deliverable. A refusal naming a tool source repeats a caller-supplied id in
// its details, and an id long enough to overflow the line turned a typed
// refusal into response_too_large — discarding the actionable code for a
// request the frame limit had accepted.
//
// The details are the largest thing on the line and the least load-bearing:
// the caller already knows the id it sent. So they are shed first, and the
// code survives.
func TestATypedRefusalShedsItsDetailsRatherThanItsCode(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	id := strings.Repeat("d", 200)
	request := requestEnvelope(t, "r", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID:   "shed",
		ToolSources: []protocol.ToolSourceAttachment{{ID: id, Kind: protocol.ToolSourceProcess, Command: "/bin/sh"}},
	}, "", "")
	line := fmt.Sprintf(`{"id":1,"op":"open","adapter":"memory","request":%s}`, request)
	// The limit is sized from the request, which is the window the finding
	// names: the frame limit accepted the request, so it must carry an answer
	// to it. The full refusal cannot fit, because it repeats the id twice —
	// once in the message and once in the details — where the request carries
	// it once.
	limit := len(line) + 16
	f := startFrontend(t, hub, Options{FrameLimit: limit})
	f.send(line)
	response := f.expectResponse(1)
	if response.OK {
		t.Fatal("an open naming a wire-supplied command succeeded")
	}
	if response.Error.Code != "unsupported_feature" {
		t.Fatalf("code %q, want unsupported_feature — the typed refusal was shed instead of its details: %s",
			response.Error.Code, response.Error.Message)
	}
	if len(response.Error.Details) != 0 {
		t.Fatalf("the reduced refusal kept details that did not fit: %v", response.Error.Details)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// TestAnAdapterContextFailureIsStillAnnounced separates two questions the
// pump used to confuse: was a context cancelled, and was it mine.
//
// An adapter's stream can fail with an error wrapping context.Canceled — its
// own request context, or a child process's — while this subscription is
// perfectly alive. Matching the sentinel answered the first question and
// silently ended the pump, which is exactly the ending a host cannot see.
func TestAnAdapterContextFailureIsStillAnnounced(t *testing.T) {
	registry := serve.NewRegistry()
	adapter := &streamAdapter{}
	if err := registry.Register("stream", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 64})
	entry, _, err := hub.Open(context.Background(), "stream", base.OpenRequest{
		SessionID: "borrowed", Participant: protocol.Participant{ID: serve.DefaultParticipant},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := startFrontend(t, hub, Options{})
	f.send(`{"id":1,"op":"events","session_id":"borrowed"}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "borrowed", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}); err != nil {
		t.Fatal(err)
	}
	// The adapter's context ended, not this subscription's.
	adapter.active(t).fail(fmt.Errorf("adapter transport: %w", context.Canceled))

	line := f.line()
	var failure struct {
		Event string `json:"event"`
		ID    int64  `json:"id"`
	}
	if err := json.Unmarshal([]byte(line), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Event != signalStreamFailed || failure.ID != 1 {
		t.Fatalf("line %q is not a correlated stream-failed ending", line)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

// unencodableStateAdapter opens successfully and then reports a session state
// this frontend cannot encode, which is the window where the hub has already
// registered the session and the open has no answer to give.
type unencodableStateAdapter struct {
	mu      sync.Mutex
	session *unencodableStateSession
}

func (a *unencodableStateAdapter) opened(t *testing.T) *unencodableStateSession {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		t.Fatal("the adapter opened no session")
	}
	return a.session
}

func (*unencodableStateAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities:       protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "reference.unencodable"}},
		CapabilityRevision: "unencodable-v1",
	}, nil
}

func (a *unencodableStateAdapter) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	session := &unencodableStateSession{id: request.SessionID}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	return session, nil
}

type unencodableStateSession struct {
	id     protocol.SessionID
	mu     sync.Mutex
	closed bool
}

var _ base.Session = (*unencodableStateSession)(nil)

func (s *unencodableStateSession) State(context.Context) (protocol.SessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := protocol.SessionIdle
	if s.closed {
		status = protocol.SessionClosed
	}
	return protocol.SessionState{
		SessionID: s.id, Status: status,
		Metadata: map[string]json.RawMessage{"broken": json.RawMessage("{not json")},
	}, nil
}

// isClosed reports whether the frontend rolled this session back. It is the
// ground truth the listing cannot give: a closed session stays listed.
func (s *unencodableStateSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *unencodableStateSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
}
func (s *unencodableStateSession) Resolve(context.Context, base.InteractionResolution) error {
	return nil
}
func (s *unencodableStateSession) Cancel(_ context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: s.id, RunID: runID}, nil
}
func (s *unencodableStateSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, base.ErrRunNotFound
}
func (s *unencodableStateSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// TestAnOpenThatCannotEncodeIsRolledBack reaches the two-facts problem one
// step earlier than an unframable response does: the hub has registered the
// session, and the answer says the open failed. A host given a minted id it
// never saw cannot close what it does not know exists.
func TestAnOpenThatCannotEncodeIsRolledBack(t *testing.T) {
	registry := serve.NewRegistry()
	adapter := &unencodableStateAdapter{}
	if err := registry.Register("broken", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	f := startFrontend(t, hub, Options{})

	// No session_id: the daemon mints one, so the host could not name it.
	request := requestEnvelope(t, "req-unencodable", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{}, "", "")
	f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"broken","request":%s}`, request))
	response := f.expectResponse(1)
	if response.OK {
		t.Fatal("an open whose state cannot be encoded reported success")
	}
	if response.Error.Code != "internal" {
		t.Fatalf("code %q, want internal: %s", response.Error.Code, response.Error.Message)
	}
	// The adapter's own session is the ground truth. A closed session stays
	// in the hub's listing, so the listing cannot answer this.
	if !adapter.opened(t).isClosed() {
		t.Fatal("the failed open left a live session behind; the host was told it failed and cannot name what to close")
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}
