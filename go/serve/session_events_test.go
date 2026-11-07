package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type eventSourceSession struct {
	base.Session
	events   chan base.Result
	onClose  func()
	closeErr error
}

func (s *eventSourceSession) SessionEvents() base.EventStream { return s.events }

func eventEnvelope(t *testing.T, kind protocol.EnvelopeType, id string) protocol.Envelope {
	t.Helper()
	e, err := protocol.NewEnvelope(kind, protocol.EnvelopeID(id), map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	e.SessionID = "s"
	return e
}

func TestSessionEventsAreLiveAndDoNotMoveRunCursor(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result, 2)}
	session := newSession("s", "test", source, nil)
	session.startSessionEvents()
	defer session.markClosed()
	session.mu.Lock()
	session.runID, session.nextSerial = "r", 1
	session.serials["r"] = 1
	session.mu.Unlock()
	sub, _, _, ok := session.subscribe(8)
	if !ok {
		t.Fatal("subscribe")
	}
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub, positions: make(map[protocol.RunID]uint64), firstRun: 1}
	defer stream.Close()
	run := eventEnvelope(t, protocol.TypeRunStarted, "r1")
	run.RunID = "r"
	one := uint64(1)
	run.Sequence = &one
	session.publish(run)
	first, err := stream.NextWithin(time.Second)
	if err != nil || first.RunID != "r" {
		t.Fatalf("run: %+v %v", first, err)
	}
	source.events <- base.Result{Envelope: eventEnvelope(t, protocol.TypeSessionTaskStarted, "e1")}
	event, err := stream.NextWithin(time.Second)
	if err != nil || event.RunID != "" || event.Sequence == nil || *event.Sequence != 1 {
		t.Fatalf("event: %+v %v", event, err)
	}
	if stream.lastRun != "r" || stream.last != 1 || stream.positions["r"] != 1 {
		t.Fatalf("cursor changed: %+v", stream)
	}
	source.events <- base.Result{Envelope: eventEnvelope(t, protocol.TypeSessionTaskEnded, "e2")}
	event, err = stream.NextWithin(time.Second)
	if err != nil || event.Sequence == nil || *event.Sequence != 2 {
		t.Fatalf("event 2: %+v %v", event, err)
	}
	session.mu.Lock()
	_, sessionCursor := session.sequences[""]
	session.mu.Unlock()
	if sessionCursor {
		t.Fatal("session event entered run sequence map")
	}
}

func TestSessionEventFailureReachesCurrentAndFutureSubscribers(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result, 1)}
	session := newSession("s", "test", source, nil)
	session.startSessionEvents()
	defer session.markClosed()
	sub, _, _, _ := session.subscribe(8)
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub}
	want := errors.New("session event source failed")
	source.events <- base.Result{Error: want}
	if _, err := stream.NextWithin(time.Second); !errors.Is(err, want) {
		t.Fatalf("current: %v", err)
	}
	later, _, _, _ := session.subscribe(8)
	next := &Subscription{session: session, ctx: context.Background(), sub: later}
	if _, err := next.NextWithin(time.Second); !errors.Is(err, want) {
		t.Fatalf("future: %v", err)
	}
}

func TestForeignSessionEventFailsTheStream(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result, 1)}
	session := newSession("s", "test", source, nil)
	session.startSessionEvents()
	defer session.markClosed()
	sub, _, _, _ := session.subscribe(8)
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub}
	event := eventEnvelope(t, protocol.TypeSessionTaskStarted, "e")
	event.SessionID = "other"
	source.events <- base.Result{Envelope: event}
	if _, err := stream.NextWithin(time.Second); err == nil || errors.Is(err, ErrQuiet) {
		t.Fatalf("foreign event: %v", err)
	}
}

type eventSourceAdapter struct{ session *eventSourceSession }

func (a eventSourceAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{Capabilities: protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "source"}}, CapabilityRevision: "test"}, nil
}
func (a eventSourceAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return a.session, nil
}
func (s *eventSourceSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "s", Status: protocol.SessionIdle}, nil
}
func (s *eventSourceSession) Close(context.Context) error {
	if s.onClose != nil {
		s.onClose()
	}
	return s.closeErr
}

func TestOpenStartsSessionEventSourceAndCloseStopsIt(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result, 1)}
	registry := NewRegistry()
	if err := registry.Register("events", eventSourceAdapter{session: source}); err != nil {
		t.Fatal(err)
	}
	hub := New(registry, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	session, _, err := hub.Open(ctx, "events", base.OpenRequest{SessionID: "s"})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := hub.Subscribe(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	source.events <- base.Result{Envelope: eventEnvelope(t, protocol.TypeSessionTaskStarted, "e")}
	event, err := sub.NextWithin(time.Second)
	if err != nil || event.Type != protocol.TypeSessionTaskStarted {
		t.Fatalf("event: %+v %v", event, err)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.sessionEventsStop:
	default:
		t.Fatal("event reader was not stopped")
	}
}

func TestUnexpectedSessionEventEOFIsReported(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result)}
	session := newSession("s", "test", source, nil)
	session.startSessionEvents()
	defer session.markClosed()
	sub, _, _, _ := session.subscribe(8)
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub}
	close(source.events)
	if _, err := stream.NextWithin(time.Second); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("EOF: %v", err)
	}
}

func TestFailedSessionEventStreamContinuesDrainingUntilClose(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result)}
	session := newSession("s", "test", source, nil)
	session.startSessionEvents()
	defer session.markClosed()
	sub, _, _, _ := session.subscribe(8)
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub}
	source.events <- base.Result{Error: errors.New("failed")}
	if _, err := stream.NextWithin(time.Second); err == nil {
		t.Fatal("missing stream failure")
	}
	sent := make(chan struct{})
	event := eventEnvelope(t, protocol.TypeSessionTaskEnded, "e")
	go func() {
		source.events <- base.Result{Envelope: event}
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("failed stream stopped draining")
	}
}

func TestSessionEventEOFWhileClosingIsClean(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprintf("shutdown=%t", shutdown), func(t *testing.T) {
			source := &eventSourceSession{events: make(chan base.Result)}
			session := newSession("s", "test", source, nil)
			session.startSessionEvents()
			sub, _, _, _ := session.subscribe(8)
			stream := &Subscription{session: session, ctx: context.Background(), sub: sub}
			source.onClose = func() {
				close(source.events)
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					session.mu.Lock()
					observed := session.sessionEventErr != nil
					session.mu.Unlock()
					if observed {
						return
					}
					time.Sleep(time.Millisecond)
				}
				t.Error("event reader did not see the closed stream")
			}
			var err error
			if shutdown {
				err = session.closeForShutdown(context.Background())
			} else {
				err = session.Close(context.Background())
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := stream.NextWithin(time.Second); !errors.Is(err, io.EOF) {
				t.Fatalf("close: %v", err)
			}
		})
	}
}

func TestFailedShutdownReportsSessionEventEOF(t *testing.T) {
	failure := errors.New("close failed")
	source := &eventSourceSession{events: make(chan base.Result), closeErr: failure}
	session := newSession("s", "test", source, nil)
	session.startSessionEvents()
	defer session.markClosed()
	sub, _, _, _ := session.subscribe(8)
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub}
	source.onClose = func() {
		close(source.events)
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			session.mu.Lock()
			observed := session.sessionEventErr != nil
			session.mu.Unlock()
			if observed {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Error("event reader did not see the closed stream")
	}
	if err := session.closeForShutdown(context.Background()); !errors.Is(err, failure) {
		t.Fatalf("close: %v", err)
	}
	if _, err := stream.NextWithin(time.Second); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("stream: %v", err)
	}
}

func TestSessionOnlySubscriptionSurvivesRunStreamFailure(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result, 1)}
	session := newSession("s", "test", source, nil)
	session.startSessionEvents()
	defer session.markClosed()
	session.mu.Lock()
	session.runID, session.nextSerial, session.readers = "r", 1, 1
	session.serials["r"] = 1
	session.mu.Unlock()
	sub, _, _, ok := session.subscribe(1, true)
	if !ok {
		t.Fatal("subscribe")
	}
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub, positions: make(map[protocol.RunID]uint64)}
	defer stream.Close()
	run := eventEnvelope(t, protocol.TypeRunStarted, "run")
	run.RunID = "r"
	for i := 0; i < 3; i++ {
		session.publish(run)
	}
	session.exitReader("r", &terminalState{err: errors.New("run failed")})
	source.events <- base.Result{Envelope: eventEnvelope(t, protocol.TypeSessionTaskEnded, "task")}
	event, err := stream.NextWithin(time.Second)
	if err != nil || event.ID != "task" {
		t.Fatalf("session event: %+v, %v", event, err)
	}
}

func TestClosedSessionOnlySubscriptionDoesNotInheritRunFailure(t *testing.T) {
	source := &eventSourceSession{events: make(chan base.Result, 1)}
	session := newSession("s", "test", source, nil)
	session.mu.Lock()
	session.runID, session.nextSerial, session.readers = "r", 1, 1
	session.serials["r"] = 1
	session.mu.Unlock()
	sub, _, _, ok := session.subscribe(1, true)
	if !ok {
		t.Fatal("subscribe")
	}
	stream := &Subscription{session: session, ctx: context.Background(), sub: sub, positions: make(map[protocol.RunID]uint64)}
	defer stream.Close()
	session.markClosed()
	session.exitReader("r", &terminalState{err: errors.New("run failed after close")})
	if _, err := stream.NextWithin(time.Second); !errors.Is(err, io.EOF) {
		t.Fatalf("closed session stream: %v, want EOF", err)
	}
}
