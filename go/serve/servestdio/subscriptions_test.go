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

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

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

func openLine(t *testing.T, id int64, sessionID string) string {
	t.Helper()
	envelope := requestEnvelope(t, "req-open", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID: protocol.SessionID(sessionID),
	}, "", "")
	return fmt.Sprintf(`{"id":%d,"op":"open","adapter":"memory","request":%s}`, id, envelope)
}

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

	var lastSequence uint64
	var terminal protocol.EnvelopeType
	nextID := int64(4)
	sent, answered := 1, 0
	for terminal == "" || answered < sent {
		line := f.line()
		if strings.HasPrefix(line, `{"id":`) {
			response := f.decodeResponse(line)
			if !response.OK {
				t.Fatalf("op %d failed: %+v", response.ID, response.Error)
			}
			answered++
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
			sent++
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

func TestEventsAcknowledgementPrecedesTheStream(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	session := openSessionEntry(t, hub, "ordered")
	runID, _ := runToCompletion(t, hub, session)

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

	f.drainSubscription(7)
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

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

func TestEventsOpReportsAReplayGap(t *testing.T) {
	hub := newTestHub(t, 2, 64)
	session := openSessionEntry(t, hub, "expired")
	_, _ = runToCompletion(t, hub, session)

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

func TestSubscriptionsAreNotChargedAgainstTheInFlightBound(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "interest")
	f := startFrontend(t, hub, Options{MaxConcurrentOps: 1})

	f.send(`{"id":1,"op":"events","session_id":"interest"}`)
	if response := f.expectResponse(1); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}

	response := f.opUntilAdmitted(2, `{"id":2,"op":"state","session_id":"interest"}`)
	if !response.OK {
		t.Fatalf("an op behind a live subscription was refused %q: %s", response.Error.Code, response.Error.Message)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func (f *frontend) opUntilAdmitted(id int64, line string) responseLine {
	f.t.Helper()
	for attempt := 0; attempt < 20; attempt++ {
		f.send(line)
		response := f.expectResponse(id)
		if response.OK || response.Error == nil || response.Error.Code != "busy" {
			return response
		}
		time.Sleep(10 * time.Millisecond)
	}
	f.t.Fatalf("op %d was refused busy on every attempt; the bound is not freeing", id)
	return responseLine{}
}

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

func runToCompletion(t *testing.T, hub *serve.Hub, entry *serve.Session) (protocol.RunID, uint64) {
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
			if envelope.Sequence == nil {
				t.Fatalf("terminal %s carries no sequence", envelope.Type)
			}
			return admission.RunID, *envelope.Sequence
		}
	}
}

func resolve(t *testing.T, entry *serve.Session, resolution base.InteractionResolution) {
	t.Helper()
	if err := entry.Resolve(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
}

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

	if strings.Contains(failure.Message, "native transport died") {
		t.Fatalf("the adapter diagnostic reached the wire: %s", failure.Message)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

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

func TestAnEndingTooLargeToFrameStillArrives(t *testing.T) {
	sessionID := strings.Repeat("s", 150)
	hub := newTestHub(t, 2, 64)
	entry := openSessionEntry(t, hub, sessionID)
	_, _ = runToCompletion(t, hub, entry)

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

	f.send(`{"id":4,"op":"state","session_id":"bounded"}`)
	if response := f.expectResponse(4); !response.OK {
		t.Fatalf("an ordinary op was refused behind full subscriptions: %+v", response.Error)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestATypedRefusalShedsItsDetailsRatherThanItsCode(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	id := strings.Repeat("d", 200)
	request := requestEnvelope(t, "r", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{
		SessionID:   "shed",
		ToolSources: []protocol.ToolSourceAttachment{{ID: id, Kind: protocol.ToolSourceProcess, Command: "/bin/sh"}},
	}, "", "")
	line := fmt.Sprintf(`{"id":1,"op":"open","adapter":"memory","request":%s}`, request)

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

func TestAnOpenThatCannotEncodeIsRolledBack(t *testing.T) {
	registry := serve.NewRegistry()
	adapter := &unencodableStateAdapter{}
	if err := registry.Register("broken", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	f := startFrontend(t, hub, Options{})

	request := requestEnvelope(t, "req-unencodable", protocol.TypeSessionOpenRequest, protocol.SessionOpenRequest{}, "", "")
	f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"broken","request":%s}`, request))
	response := f.expectResponse(1)
	if response.OK {
		t.Fatal("an open whose state cannot be encoded reported success")
	}
	if response.Error.Code != "internal" {
		t.Fatalf("code %q, want internal: %s", response.Error.Code, response.Error.Message)
	}
	if !strings.Contains(response.Error.Message, "rolled back") {
		t.Fatalf("the refusal does not say what it left behind: %s", response.Error.Message)
	}

	if !adapter.opened(t).isClosed() {
		t.Fatal("the failed open left a live session behind; the host was told it failed and cannot name what to close")
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestAnOpenTheHostNamedIsKeptAndSaidSo(t *testing.T) {
	registry := serve.NewRegistry()
	adapter := &unencodableStateAdapter{}
	if err := registry.Register("broken", adapter); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	f := startFrontend(t, hub, Options{})

	request := requestEnvelope(t, "req-named", protocol.TypeSessionOpenRequest,
		protocol.SessionOpenRequest{SessionID: "named-by-host"}, "", "")
	f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"broken","request":%s}`, request))
	response := f.expectResponse(1)
	if response.OK {
		t.Fatal("an open whose state cannot be encoded reported success")
	}
	if !strings.Contains(response.Error.Message, "the session is open under the session_id the request supplied") {
		t.Fatalf("the refusal hides the kept session: %s", response.Error.Message)
	}
	if adapter.opened(t).isClosed() {
		t.Fatal("a session the host named was rolled back; it is the one thing the host could still close itself")
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestAdvanceCursorHoldsItsHighWaterMark(t *testing.T) {
	at := func(run string, sequence uint64) protocol.Envelope {
		value := sequence
		return protocol.Envelope{RunID: protocol.RunID(run), Sequence: &value}
	}
	unsequenced := func(run string) protocol.Envelope {
		return protocol.Envelope{RunID: protocol.RunID(run)}
	}
	for _, testCase := range []struct {
		name         string
		run          string
		sequence     uint64
		envelope     protocol.Envelope
		wantRun      string
		wantSequence uint64
	}{
		{"advances within a run", "run-1", 4, at("run-1", 5), "run-1", 5},
		{"a redelivery behind the cursor does not drag it back", "run-1", 9, at("run-1", 3), "run-1", 9},
		{"a redelivery at the cursor holds", "run-1", 9, at("run-1", 9), "run-1", 9},
		{"a new run takes its own lower sequence", "run-1", 12, at("run-2", 1), "run-2", 1},
		{"an unsequenced envelope does not advance", "run-1", 7, unsequenced("run-1"), "run-1", 7},
		{"the first envelope sets both", "", 0, at("run-1", 1), "run-1", 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			run, sequence := advanceCursor(protocol.RunID(testCase.run), testCase.sequence, testCase.envelope)
			if string(run) != testCase.wantRun || sequence != testCase.wantSequence {
				t.Fatalf("cursor (%s, %d), want (%s, %d)", run, sequence, testCase.wantRun, testCase.wantSequence)
			}
		})
	}
}

func TestEventsReportsWhereALateSubscriptionJoined(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	session := openSessionEntry(t, hub, "joined")
	runID, lastSequence := runToCompletion(t, hub, session)

	f := startFrontend(t, hub, Options{})
	f.send(`{"id":9,"op":"events","session_id":"joined"}`)
	if response := f.expectResponse(9); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	line := f.line()
	var signal subscribedLine
	if err := json.Unmarshal([]byte(line), &signal); err != nil {
		t.Fatalf("signal line %q: %v", line, err)
	}
	if signal.Event != signalSubscribed {
		t.Fatalf("the line after the acknowledgement is %q, want %q", signal.Event, signalSubscribed)
	}
	if signal.ID != 9 || signal.SessionID != "joined" {
		t.Fatalf("signal correlates to %d/%q, want 9/joined", signal.ID, signal.SessionID)
	}
	if protocol.RunID(signal.RunID) != runID {
		t.Fatalf("signal names run %q, want %q", signal.RunID, runID)
	}
	if signal.JoinedAfter != lastSequence {
		t.Fatalf("signal reports joining after %d, want the run's last sequence %d", signal.JoinedAfter, lastSequence)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestEventsReportsNoJoinPointWhenNothingPrecededTheSubscription(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "fresh")

	f := startFrontend(t, hub, Options{})
	f.send(`{"id":11,"op":"events","session_id":"fresh"}`)
	if response := f.expectResponse(11); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	f.send(`{"id":12,"op":"sessions"}`)
	line := f.line()
	if strings.Contains(line, signalSubscribed) {
		t.Fatalf("a subscription with no run behind it reported a join point: %s", line)
	}
	if response := f.decodeResponse(line); response.ID != 12 {
		t.Fatalf("the line after the acknowledgement is response %d, want 12", response.ID)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestTheSubscribedSignalPrecedesEveryEnvelope(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	session := openSessionEntry(t, hub, "ahead")
	first, lastSequence := runToCompletion(t, hub, session)

	f := startFrontend(t, hub, Options{})
	f.send(`{"id":3,"op":"events","session_id":"ahead"}`)
	if response := f.expectResponse(3); !response.OK {
		t.Fatalf("events failed: %+v", response.Error)
	}
	var signal subscribedLine
	if err := json.Unmarshal([]byte(f.line()), &signal); err != nil {
		t.Fatal(err)
	}
	if signal.Event != signalSubscribed {
		t.Fatalf("the line after the acknowledgement is %q, want %q", signal.Event, signalSubscribed)
	}
	if protocol.RunID(signal.RunID) != first || signal.JoinedAfter != lastSequence {
		t.Fatalf("signal reports %s after %d, want %s after %d", signal.RunID, signal.JoinedAfter, first, lastSequence)
	}

	second, _ := runToCompletion(t, hub, session)
	if second == first {
		t.Fatal("the second submission reused the first run id")
	}
	for {
		envelope := f.expectSignal(3, signalEnvelope)
		var decoded protocol.Envelope
		if err := json.Unmarshal(envelope.Envelope, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.RunID != second {
			t.Fatalf("an envelope for run %q followed the signal, want only %q", decoded.RunID, second)
		}
		switch decoded.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			if err := f.finish(); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}
