package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

const testTimeout = 5 * time.Second

func hubEnvelope(t *testing.T, typ protocol.EnvelopeType, sequence uint64) protocol.Envelope {
	t.Helper()
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID("hub-event"), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Sequence = &sequence
	return envelope
}

// runEnvelope is hubEnvelope carrying a run id, for driving the hub's
// per-run cursor accounting directly.
func runEnvelope(t *testing.T, runID protocol.RunID, sequence uint64) protocol.Envelope {
	t.Helper()
	envelope := hubEnvelope(t, protocol.TypeRunStatusUpdated, sequence)
	envelope.RunID = runID
	return envelope
}

// TestOverflowCursorZeroWhenRunUnseen pins the sequence half of the overflow
// cursor: a mailbox full of run A's envelopes that run B's publish overflows
// yields (B, 0) — the consumer observed nothing of B, so recovery replays B
// from its start — never B carrying A's last position.
func TestOverflowCursorZeroWhenRunUnseen(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.publish(runEnvelope(t, "run-a", 1))
	entry.publish(runEnvelope(t, "run-a", 2)) // the two-slot mailbox is full
	entry.publish(runEnvelope(t, "run-b", 1)) // overflows, naming run B

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		envelope, err := subscription.Next()
		if err != nil || envelope.Sequence == nil || *envelope.Sequence != sequence {
			t.Fatalf("envelope %d: sequence %v error %v", sequence, envelope.Sequence, err)
		}
	}
	_, err := subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 0 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 0", overflow)
	}
}

// TestMarkClosedDefersFinishToReader pins the drain-before-finish contract:
// an adapter reports its run terminal once the terminal envelope is queued,
// so a close that lands while the reader still holds final events must not
// finish subscribers before those events are delivered.
func TestMarkClosedDefersFinishToReader(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	stream := make(chan base.Result, 4)
	stream <- base.Result{Envelope: hubEnvelope(t, protocol.TypeRunStatusUpdated, 1)}
	entry.startRun("run-1", stream)

	entry.markClosed()
	if !entry.IsClosed() {
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
	entry := newSession("hub", "memory", nil)
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

// TestOverlappingReadersDeliverCurrentRunEnd pins the finish path when a
// resubmit's reader exits before the previous run's drainer: however the
// two readers interleave, the subscriber ends with the current run's
// terminal outcome instead of parking forever.
func TestOverlappingReadersDeliverCurrentRunEnd(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}
	// The resubmit lands inside run A's settle window; run B takes the hub.
	entry.startRun("run-b", streamB)
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	streamFailure := errors.New("run B stream died")
	streamB <- base.Result{Error: streamFailure}
	close(streamB)
	close(streamA)

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelopes := 0
	for {
		_, err := subscription.Next()
		if err == nil {
			envelopes++
			continue
		}
		if !errors.Is(err, streamFailure) {
			t.Fatalf("terminal error %v, want run B's stream failure", err)
		}
		break
	}
	if envelopes != 2 {
		t.Fatalf("delivered %d envelopes before the terminal, want 2", envelopes)
	}
}

// TestAdapterOverflowScopedToExposedSubscribers pins the adapter-overflow
// scoping: a late overflow from an older run's still-draining stream
// terminates the subscribers that observed that run, while a subscriber
// that attached for a newer run keeps receiving it.
func TestAdapterOverflowScopedToExposedSubscribers(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	spanning, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}
	// Run B takes the hub while A's stream still drains; a fresh subscriber
	// joins for B and never observes A.
	entry.startRun("run-b", streamB)
	bOnly, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe with a run active was refused")
	}
	streamA <- base.Result{Error: base.ErrEventStreamOverflow}
	close(streamA)

	// The spanning subscriber observed A, so A's overflow is its terminal.
	// (The two readers race, so whether B's envelope interleaves before the
	// drain is unspecified; the terminal is not.)
	spanningSubscription := &Subscription{session: entry, ctx: context.Background(), sub: spanning}
	for {
		_, err := spanningSubscription.Next()
		if err != nil {
			var overflow *OverflowError
			if !errors.As(err, &overflow) || overflow.RunID != "run-a" {
				t.Fatalf("spanning terminal %v (%T), want run-a overflow", err, err)
			}
			break
		}
	}

	// The B subscriber is untouched: it observes whatever publishes from its
	// attachment on (live semantics — possibly A's tail, in either order)
	// and ends with the hub's clean terminal, never A's overflow.
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	close(streamB)
	bSubscription := &Subscription{session: entry, ctx: context.Background(), sub: bOnly}
	sawRunB := false
	for {
		envelope, err := bSubscription.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("run-b subscriber terminal %v, want io.EOF", err)
		}
		if envelope.RunID == "run-b" {
			sawRunB = true
		}
	}
	if !sawRunB {
		t.Fatal("run-b subscriber never observed run B")
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
	entry := newSession("stub", "stub", stub)
	start := time.Now()
	if err := entry.closeForShutdown(context.Background()); err != nil {
		t.Fatalf("close did not settle: %v", err)
	}
	if stub.cancels != 2 {
		t.Fatalf("close settled after %d cancels, want 2", stub.cancels)
	}
	if !entry.IsClosed() {
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
	entry := newSession("stub", "stub", stub)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := entry.closeForShutdown(ctx); !errors.Is(err, base.ErrRunActive) {
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
	daemon := New(NewRegistry(), Options{ShutdownTimeout: 600 * time.Millisecond})
	blocker := &blockingSession{stubSession: stubSession{}, unblocked: make(chan struct{})}
	blockerEntry := newSession("blocker", "stub", blocker)
	quickEntry := newSession("quick", "stub", &stubSession{})
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
	if !quickEntry.IsClosed() {
		t.Fatal("second session was never closed")
	}
}

// TestCloseSessionsBoundsTotalSweep proves the configured timeout bounds the
// WHOLE sweep: sessions that never settle each burn only their share of the
// remaining budget, so four stuck sessions under a 600ms window cannot run
// the per-session 500ms floor into a two-second sweep.
func TestCloseSessionsBoundsTotalSweep(t *testing.T) {
	daemon := New(NewRegistry(), Options{ShutdownTimeout: 600 * time.Millisecond})
	for index := range 4 {
		if err := daemon.sessions.add(newSession(protocol.SessionID(fmt.Sprintf("stuck-%d", index)), "stub", &stubSession{settleAfter: 1000})); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan struct{})
	start := time.Now()
	go func() { daemon.CloseSessions(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(testTimeout):
		t.Fatal("CloseSessions wedged")
	}
	// The old floor-per-session behavior ran ~4x500ms; the bounded sweep
	// stays inside roughly twice the configured window even with scheduler
	// noise between sessions.
	if elapsed := time.Since(start); elapsed > 1200*time.Millisecond {
		t.Fatalf("CloseSessions ran %v for four stuck sessions, outside the 600ms budget", elapsed)
	}
}

func TestHubSubscriberQueueOverflow(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	slow, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	fast, _ := entry.subscribe(64)
	for sequence := uint64(1); sequence <= 5; sequence++ {
		envelope, err := protocol.NewEnvelope(protocol.TypeContentDelta, protocol.EnvelopeID(fmt.Sprintf("hub-event-%d", sequence)), protocol.ContentDeltaPayload{})
		if err != nil {
			t.Fatal(err)
		}
		value := sequence
		envelope.Sequence = &value
		entry.publish(envelope)
	}
	if terminal := slow.terminal.Load(); terminal == nil || !terminal.overflow {
		t.Fatal("slow subscriber was not marked overflowed")
	}
	select {
	case <-slow.finish:
	default:
		t.Fatal("slow subscriber was not finished")
	}
	if len(fast.ch) != 5 {
		t.Fatalf("fast subscriber queued %d envelopes, want 5", len(fast.ch))
	}
	entry.finishSubs(nil)
	select {
	case <-fast.finish:
	default:
		t.Fatal("fast subscriber was not finished at run end")
	}
}
