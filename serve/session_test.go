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
// that attached for a newer run keeps receiving it. The B subscriber
// attaches only after the spanning subscriber observed the terminal —
// proof that A's publish and signal both completed — so it cannot have
// observed A and is deterministically out of the overflow's scope.
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
	// Run B takes the hub while A's stream still drains.
	entry.startRun("run-b", streamB)
	streamA <- base.Result{Error: base.ErrEventStreamOverflow}
	close(streamA)

	// The spanning subscriber observed A, so A's overflow is its terminal.
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

	// A subscriber attaching now never observes A: it receives B's
	// envelope and ends with the hub's clean terminal.
	bOnly, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe with a run active was refused")
	}
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	close(streamB)
	bSubscription := &Subscription{session: entry, ctx: context.Background(), sub: bOnly}
	envelope, err := bSubscription.Next()
	if err != nil || envelope.RunID != "run-b" || envelope.Sequence == nil || *envelope.Sequence != 1 {
		t.Fatalf("run-b envelope: run %s sequence %v error %v", envelope.RunID, envelope.Sequence, err)
	}
	if _, err := bSubscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("run-b terminal %v, want io.EOF", err)
	}
}

// TestOverflowFollowsDeliveredRuns pins exposure by delivery: a subscriber
// attached during run A that keeps receiving across the settle window into
// run B is terminated by B's adapter overflow — attachment alone does not
// scope it to A.
func TestOverflowFollowsDeliveredRuns(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	// Attach during run A and take one envelope of A.
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}
	spanning := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if envelope, err := spanning.Next(); err != nil || envelope.RunID != "run-a" {
		t.Fatalf("run-a envelope: run %s error %v", envelope.RunID, err)
	}
	// Run B is admitted; the subscriber receives B's envelope, then B's
	// stream overflows.
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	streamB <- base.Result{Error: base.ErrEventStreamOverflow}
	close(streamB)
	close(streamA)

	envelope, err := spanning.Next()
	if err != nil || envelope.RunID != "run-b" {
		t.Fatalf("run-b envelope: run %s error %v", envelope.RunID, err)
	}
	_, err = spanning.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) || overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("terminal %v (%T), want run-b overflow at 1", err, err)
	}
}

// gatedSession parks its Submit until the test releases it, signalling
// each adapter call through the entered channel — pinning Submit's reader
// reservation against the zero-reader detach racing an in-flight
// admission.
type gatedSession struct {
	stubSession
	entered chan struct{}
	release chan struct{}
	fail    error
	stream  chan base.Result
}

func (g *gatedSession) Submit(_ context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	g.entered <- struct{}{}
	<-g.release
	if g.fail != nil {
		return protocol.MessageSubmitResponse{}, nil, g.fail
	}
	g.stream = make(chan base.Result, 4)
	return protocol.MessageSubmitResponse{SessionID: request.SessionID, RunID: "run-b", Accepted: true}, g.stream, nil
}

// TestSubmitReservationBridgesAdmission pins the settle-window invariant:
// run A's reader may exit while the adapter is still admitting run B, and
// the subscriber attached before the resubmit must bridge into B rather
// than take A's end.
func TestSubmitReservationBridgesAdmission(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}

	admitted := make(chan error, 1)
	go func() {
		_, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
			SessionID: "gated", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("resubmit")}},
		})
		admitted <- err
	}()
	// The admission is in flight (its reader slot reserved) when run A's
	// stream ends; A's reader exits to a non-zero reader count, so the
	// subscriber is not finished with A's end.
	<-gated.entered
	close(streamA)
	close(gated.release)
	if err := <-admitted; err != nil {
		t.Fatal(err)
	}

	gated.stream <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	close(gated.stream)

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	for _, want := range [2]string{"run-a", "run-b"} {
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != protocol.RunID(want) {
			t.Fatalf("envelope: run %s error %v, want %s", envelope.RunID, err, want)
		}
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want io.EOF", err)
	}
}

// TestSubmitReservationReleasesOnFailure pins the failure unwind: an
// adapter rejection after A's reader exited still ends subscribers with
// A's stashed outcome instead of wedging or hanging them parked.
func TestSubmitReservationReleasesOnFailure(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrRunActive}
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}

	rejected := make(chan error, 1)
	go func() {
		_, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
			SessionID: "gated", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("resubmit")}},
		})
		rejected <- err
	}()
	<-gated.entered
	close(streamA)
	close(gated.release)
	if err := <-rejected; !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("submit error %v, want run-active", err)
	}

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-a" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want io.EOF", err)
	}
}

// TestDeferredFinishSurvivesLaterReservations pins the multi-submit case:
// the run's reader exits while two admissions are in flight, and the first
// rejection must not discard the deferred finish the second rejection (or
// a later admission) still owes the subscribers.
func TestDeferredFinishSurvivesLaterReservations(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrRunActive}
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}

	rejected := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
				SessionID: "gated", Delivery: protocol.DeliveryAuto,
				Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("resubmit")}},
			})
			rejected <- err
		}()
	}
	// Both admissions are in flight when run A's reader exits, deferring
	// its finish behind the reservations; both are then rejected.
	<-gated.entered
	<-gated.entered
	close(streamA)
	close(gated.release)
	for range 2 {
		if err := <-rejected; !errors.Is(err, base.ErrRunActive) {
			t.Fatalf("submit error %v, want run-active", err)
		}
	}

	// The last release applies the run's deferred clean finish.
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-a" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want the deferred clean end", err)
	}
}

// TestRejectedSubmitKeepsSubscriptions pins the subscribe-before-submit
// flow on an idle session: an adapter-level rejection unwinds its
// reservation without finishing anybody, so the corrected retry's run
// reaches the subscription that was parked all along.
func TestRejectedSubmitKeepsSubscriptions(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrInvalidSubmission}
	close(gated.release) // the adapter rejects at once; no interleaving needed
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "gated", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("rejected")}},
	}); !errors.Is(err, base.ErrInvalidSubmission) {
		t.Fatalf("submit error %v, want invalid submission", err)
	}
	select {
	case <-sub.finish:
		t.Fatal("a rejected submit finished the parked subscriber")
	default:
	}

	// The corrected retry drives a real run to the same subscription.
	gated.fail = nil
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "gated", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("corrected")}},
	}); err != nil {
		t.Fatal(err)
	}
	gated.stream <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	close(gated.stream)

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-b" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want io.EOF", err)
	}
}

// TestLateOverflowDoesNotCutNewerRun pins the position scope: a subscriber
// that moved past run A into run B has seen A complete, so a late overflow
// from A's drained stream must not terminate it — it keeps receiving B.
func TestLateOverflowDoesNotCutNewerRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	// Observe run A's envelope first, with A's channel then empty: no
	// further run-a publish can interleave once run B flows, so the
	// subscriber's position advance into B (observed next) is stable when
	// A's late overflow is sent.
	for {
		envelope, err := subscription.Next()
		if err != nil {
			t.Fatalf("early terminal %v", err)
		}
		if envelope.RunID == "run-a" {
			break
		}
	}
	entry.startRun("run-b", streamB)
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	for {
		envelope, err := subscription.Next()
		if err != nil {
			t.Fatalf("early terminal %v", err)
		}
		if envelope.RunID == "run-b" {
			break
		}
	}
	// A's stream reports its overflow only now, with the subscriber
	// positioned in B.
	streamA <- base.Result{Error: base.ErrEventStreamOverflow}
	close(streamA)
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 2)}
	close(streamB)

	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-b" || envelope.Sequence == nil || *envelope.Sequence != 2 {
		t.Fatalf("run-b tail: run %s sequence %v error %v", envelope.RunID, envelope.Sequence, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want the clean run-b end", err)
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
