package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

func hubEnvelope(t *testing.T, typ protocol.EnvelopeType, sequence uint64) protocol.Envelope {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID("hub-event"), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Sequence = &sequence
	return envelope
}

// TestMarkClosedDefersFinishToReader pins the drain-before-finish contract:
// an adapter reports its run terminal once the terminal envelope is queued,
// so a close that lands while the reader still holds final events must not
// finish subscribers before those events are delivered.
func TestMarkClosedDefersFinishToReader(t *testing.T) {
	entry := newServerSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	stream := make(chan base.Result, 4)
	stream <- base.Result{Envelope: hubEnvelope(t, protocol.TypeRunStatusUpdated, 1)}
	entry.startRun("run-1", stream)

	entry.markClosed()
	if !entry.isClosed() {
		t.Fatal("session should record closed immediately")
	}
	// The queued event must still be delivered, and the subscriber must not
	// be finished while the reader holds it.
	select {
	case <-sub.finish:
		t.Fatal("subscriber finished before the reader drained")
	case envelope := <-sub.ch:
		if envelope.Sequence == nil || *envelope.Sequence != 1 {
			t.Fatalf("first delivered sequence %v", envelope.Sequence)
		}
	case <-time.After(testTimeout):
		t.Fatal("queued envelope was not delivered")
	}

	stream <- base.Result{Envelope: hubEnvelope(t, protocol.TypeRunCompleted, 2)}
	close(stream)
	deadline := time.After(testTimeout)
	for {
		select {
		case envelope, open := <-sub.ch:
			if !open {
				t.Fatal("hub channel must never be closed by the producer")
			}
			if envelope.Sequence != nil && *envelope.Sequence == 2 {
				// Terminal envelope delivered; the finish must follow.
				select {
				case <-sub.finish:
					return
				case <-time.After(testTimeout):
					t.Fatal("deferred finish never fired after the reader drained")
				}
			}
		case <-deadline:
			t.Fatal("reader did not drain the terminal envelope")
		}
	}
}

// TestMarkClosedFinishesImmediatelyWithoutReader covers the idle path: a
// session closed with no run draining ends parked subscribers at once.
func TestMarkClosedFinishesImmediatelyWithoutReader(t *testing.T) {
	entry := newServerSession("hub", "memory", nil)
	sub, ok := entry.subscribe(4)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.markClosed()
	select {
	case <-sub.finish:
	default:
		t.Fatal("parked subscriber survived an idle close")
	}
	if _, accepted := entry.subscribe(4); accepted {
		t.Fatal("subscribe after close must be refused")
	}
}

// stubSession settles its run asynchronously after Cancel: Close keeps
// refusing until settleAfter cancels have been issued, mimicking adapters
// that acknowledge a cancel before the run settles.
type stubSession struct {
	cancels     int
	settleAfter int
	closeErr    error
}

func (s *stubSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, nil
}
func (s *stubSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{ActiveRunID: "run-stub"}, nil
}
func (s *stubSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *stubSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	s.cancels++
	return protocol.RunCancelResponse{Accepted: true}, nil
}
func (s *stubSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}
func (s *stubSession) Close(context.Context) error {
	if s.closeErr != nil {
		return s.closeErr
	}
	if s.cancels < s.settleAfter {
		return base.ErrRunActive
	}
	return nil
}

// TestCloseRetriesThroughAsyncCancel proves the shutdown sweep settles a
// session whose cancel is acknowledged before its run settles, instead of
// leaving the session (and its child process) unclosed.
func TestCloseRetriesThroughAsyncCancel(t *testing.T) {
	stub := &stubSession{settleAfter: 2}
	entry := newServerSession("stub", "stub", stub)
	start := time.Now()
	if err := entry.close(context.Background()); err != nil {
		t.Fatalf("close did not settle: %v", err)
	}
	if stub.cancels != 2 {
		t.Fatalf("close settled after %d cancels, want 2", stub.cancels)
	}
	if !entry.isClosed() {
		t.Fatal("session did not record the close")
	}
	if elapsed := time.Since(start); elapsed > testTimeout {
		t.Fatalf("close took %v", elapsed)
	}
}

// TestCloseStopsAtContextDeadline proves a session that never settles cannot
// wedge shutdown: the retry loop yields at the context deadline.
func TestCloseStopsAtContextDeadline(t *testing.T) {
	stub := &stubSession{settleAfter: 1000}
	entry := newServerSession("stub", "stub", stub)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := entry.close(ctx); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("unsettled session must report run-active, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > testTimeout {
		t.Fatalf("close ran %v past its context", elapsed)
	}
}

var _ base.Session = (*stubSession)(nil)

// blockingSession refuses to close until its context is done, recording
// whether it ever observed a live (not-yet-expired) context.
type blockingSession struct {
	stubSession
	unblocked chan struct{}
	sawLive   bool
}

func (s *blockingSession) Close(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.unblocked:
		s.sawLive = true
		return nil
	}
}

// TestCloseSessionsSplitsBudgetPerSession proves one lingering session
// cannot starve the rest of the sweep: the first session blocks through its
// own share of the window and the second still closes on a live context.
func TestCloseSessionsSplitsBudgetPerSession(t *testing.T) {
	daemon, err := New(NewRegistry(), Options{ShutdownTimeout: 600 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	blocker := &blockingSession{stubSession: stubSession{}, unblocked: make(chan struct{})}
	blockerEntry := newServerSession("blocker", "stub", blocker)
	quickEntry := newServerSession("quick", "stub", &stubSession{})
	if err := daemon.sessions.add(blockerEntry); err != nil {
		t.Fatal(err)
	}
	if err := daemon.sessions.add(quickEntry); err != nil {
		t.Fatal(err)
	}
	close(blocker.unblocked) // both settle immediately; the split is what's pinned

	done := make(chan struct{})
	go func() { daemon.CloseSessions(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("CloseSessions wedged")
	}
	if !blocker.sawLive {
		t.Fatal("first session closed on an already-dead context")
	}
	if !quickEntry.isClosed() {
		t.Fatal("second session was never closed")
	}
}
