package servestdio

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
