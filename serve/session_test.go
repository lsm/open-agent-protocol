package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
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

// TestQueueOverflowCursorTracksPosition pins the queue-full cursor: it
// names the run of the dropped envelope — the run whose events were lost —
// from the consumer's last delivered position in it, or from its start
// when the run was never delivered, so the replayed suffix always covers
// the loss. A cursor rewritten onto an older observed run could not
// recover an entirely unseen newer stream.
func TestQueueOverflowCursorTracksPosition(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.startRun("run-a", make(chan base.Result, 1))
	entry.startRun("run-b", make(chan base.Result, 1))
	entry.publish(runEnvelope(t, "run-a", 1))
	entry.publish(runEnvelope(t, "run-a", 2)) // the two-slot mailbox is full
	entry.publish(runEnvelope(t, "run-b", 1)) // a newer run's envelope finds it full

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
		t.Fatalf("overflow cursor %+v, want run-b at sequence 0 — the unseen dropped run", overflow)
	}

	// With the newer run already observed, the cursor resumes it from the
	// consumer's position in it.
	latecomer := newSession("hub", "memory", nil)
	positioned, ok := latecomer.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	latecomer.startRun("run-a", make(chan base.Result, 1))
	latecomer.startRun("run-b", make(chan base.Result, 1))
	latecomer.publish(runEnvelope(t, "run-a", 1))
	latecomer.publish(runEnvelope(t, "run-b", 1))
	latecomer.publish(runEnvelope(t, "run-b", 2)) // full mailbox, position in run-b

	positionedSubscription := &Subscription{session: latecomer, ctx: context.Background(), sub: positioned}
	if _, err := positionedSubscription.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := positionedSubscription.Next(); err != nil {
		t.Fatal(err)
	}
	_, err = positionedSubscription.Next()
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 1 — the observed position", overflow)
	}
}

// TestDeferredEndDoesNotClobberOverflowTerminal pins first-writer-wins on a
// subscriber's terminal state: a slow consumer sitting in a deferred cohort
// that a queue-full publish already signalled must keep its recovery cursor
// when the deferred run end later stops the cohort again.
func TestDeferredEndDoesNotClobberOverflowTerminal(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.publish(runEnvelope(t, "run-a", 1))
	entry.publish(runEnvelope(t, "run-a", 2)) // the two-slot mailbox is full

	// The current run's drainer exits behind the older one, deferring an
	// error end onto the cohort that holds the slow subscriber.
	streamFailure := errors.New("run B stream died")
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	streamB <- base.Result{Error: streamFailure}
	close(streamB)
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		deferred := entry.readers == 1 && entry.deferred != nil
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run B's reader never deferred its end behind run A's")
		default:
		}
	}

	// The slow consumer's mailbox overflows first — that terminal wins.
	entry.publish(runEnvelope(t, "run-a", 3))
	close(streamA) // the older drainer applies the deferred error to the cohort

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
		t.Fatalf("terminal %v (%T), want the OverflowError that signalled first", err, err)
	}
	if overflow.RunID != "run-a" || overflow.LastSequence != 2 {
		t.Fatalf("overflow cursor %+v, want run-a at sequence 2", overflow)
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
	// Run B is admitted; the subscriber acknowledges B's envelope — only
	// then does B's stream overflow, so the acknowledged position that
	// scopes the overflow is pinned in B.
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	envelope, err := spanning.Next()
	if err != nil || envelope.RunID != "run-b" {
		t.Fatalf("run-b envelope: run %s error %v", envelope.RunID, err)
	}
	streamB <- base.Result{Error: base.ErrEventStreamOverflow}
	close(streamB)
	close(streamA)

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
	entered        chan struct{}
	release        chan struct{}
	fail           error
	failWithStream error
	stream         chan base.Result
}

func (g *gatedSession) Submit(_ context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	g.entered <- struct{}{}
	<-g.release
	if g.failWithStream != nil {
		return protocol.MessageSubmitResponse{}, g.stream, g.failWithStream
	}
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
	// subscriber is not finished with A's end. Waiting for the exit also
	// pins A1's publish before run B's reader can publish.
	<-gated.entered
	close(streamA)
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		exited := entry.readers == 0 && entry.reservations == 1
		entry.mu.Unlock()
		if exited {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run A's reader never exited behind the reservation")
		default:
		}
	}
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

// TestDeferredFinishSparesLaterSubscribers pins the deferred cohort: a
// subscriber registering between the run reader's deferred finish and the
// reservation's rejection is owed nothing — it stays parked and receives
// the next admitted run, while the cohort that did observe the run takes
// its terminal.
func TestDeferredFinishSparesLaterSubscribers(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrRunActive}
	entry := newSession("gated", "stub", gated)
	cohort, ok := entry.subscribe(8)
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
	close(streamA) // the reader exits into a deferred finish owed to cohort
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		deferred := entry.readers == 0 && entry.finishDue
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the reader never exited into the deferred finish")
		default:
		}
	}
	newcomer, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe during the reservation window was refused")
	}
	close(gated.release)
	if err := <-rejected; !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("submit error %v, want run-active", err)
	}

	// The cohort takes run A's outcome; the newcomer stays attached.
	cohortSubscription := &Subscription{session: entry, ctx: context.Background(), sub: cohort}
	envelope, err := cohortSubscription.Next()
	if err != nil || envelope.RunID != "run-a" {
		t.Fatalf("cohort envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := cohortSubscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("cohort terminal %v, want io.EOF", err)
	}
	select {
	case <-newcomer.finish:
		t.Fatal("the deferred finish swept a subscriber from inside the reservation window")
	default:
	}

	// The next admitted run reaches the newcomer.
	gated.fail = nil
	if _, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "gated", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("corrected")}},
	}); err != nil {
		t.Fatal(err)
	}
	gated.stream <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	close(gated.stream)
	newcomerSubscription := &Subscription{session: entry, ctx: context.Background(), sub: newcomer}
	envelope, err = newcomerSubscription.Next()
	if err != nil || envelope.RunID != "run-b" {
		t.Fatalf("newcomer envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := newcomerSubscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("newcomer terminal %v, want io.EOF", err)
	}
}

// TestDeferredRunEndSparesLaterSubscribers pins the cohort of a run whose
// drainer exits behind an older one: the deferred outcome reaches the
// subscribers that existed at the exit, while a subscriber registering in
// the window is not swept by the older drainer's eventual finish and stays
// parked for the next run.
func TestDeferredRunEndSparesLaterSubscribers(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	cohort, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}
	entry.startRun("run-b", streamB)
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}
	// The current run's drainer exits behind the older one, deferring its
	// clean end to the cohort.
	close(streamB)
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		deferred := entry.readers == 1 && entry.deferred != nil
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run B's reader never deferred its end behind run A's")
		default:
		}
	}
	newcomer, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe during the deferral window was refused")
	}
	close(streamA)

	// The cohort takes the current run's clean end after both envelopes;
	// the newcomer is untouched.
	cohortSubscription := &Subscription{session: entry, ctx: context.Background(), sub: cohort}
	seen := map[protocol.RunID]bool{}
	for {
		envelope, err := cohortSubscription.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("cohort terminal %v, want the clean end", err)
		}
		seen[envelope.RunID] = true
	}
	if !seen["run-a"] || !seen["run-b"] {
		t.Fatalf("cohort observed %v, want both runs", seen)
	}
	select {
	case <-newcomer.finish:
		t.Fatal("the deferred run end swept a subscriber from inside the window")
	default:
	}

	// The next run reaches the newcomer. Run A's still-buffered envelope
	// may publish after the newcomer subscribed — live fan-out delivers
	// whatever publishes from attachment on — so run-a may legitimately
	// arrive first; the pin is that run-c is observed and the end is clean.
	streamC := make(chan base.Result, 4)
	entry.startRun("run-c", streamC)
	streamC <- base.Result{Envelope: runEnvelope(t, "run-c", 1)}
	close(streamC)
	newcomerSubscription := &Subscription{session: entry, ctx: context.Background(), sub: newcomer}
	newcomerSeen := map[protocol.RunID]bool{}
	for {
		envelope, err := newcomerSubscription.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("newcomer terminal %v, want the clean end", err)
		}
		newcomerSeen[envelope.RunID] = true
	}
	if !newcomerSeen["run-c"] {
		t.Fatalf("newcomer observed %v, want run-c among them", newcomerSeen)
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

// TestCloseSessionsAttemptsEverySession pins the shutdown fairness: under
// a budget too tight for the old per-session floor, every session still
// gets its Close attempted — an early stuck child must not eat the slices
// the later sessions need to settle their own.
func TestCloseSessionsAttemptsEverySession(t *testing.T) {
	// A budget with headroom over the stuck sessions' settle loops: the
	// pin is that every entry is attempted, not the tight total bound.
	daemon := New(NewRegistry(), Options{ShutdownTimeout: 2 * time.Second})
	sessions := make([]*countingSession, 4)
	for index := range sessions {
		s := &countingSession{stubSession: stubSession{settleAfter: 1000}}
		sessions[index] = s
		if err := daemon.sessions.add(newSession(protocol.SessionID(fmt.Sprintf("stuck-%d", index)), "stub", s)); err != nil {
			t.Fatal(err)
		}
	}
	daemon.CloseSessions(context.Background())
	for index, session := range sessions {
		if session.closes.Load() == 0 {
			t.Fatalf("session %d never got a Close attempt", index)
		}
	}
}

// countingSession records every Close attempt on a never-settling stub.
type countingSession struct {
	stubSession
	closes atomic.Int32
}

func (c *countingSession) Close(ctx context.Context) error {
	c.closes.Add(1)
	return c.stubSession.Close(ctx)
}

// TestCloseEndsSubscribersRegisteredAfterDeferredRun pins the close
// transition behind stale readers: a subscriber registering after the
// current run deferred its end is still ended by the close — no future run
// can reach it — instead of parking until its context dies.
func TestCloseEndsSubscribersRegisteredAfterDeferredRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	early, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.startRun("run-b", streamB)
	close(streamB) // the current run's drainer defers its end behind A's
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		deferred := entry.readers == 1 && entry.deferred != nil
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run B's reader never deferred its end behind run A's")
		default:
		}
	}
	late, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe during the deferral window was refused")
	}
	entry.markClosed()
	close(streamA)
	for name, sub := range map[string]*subscriber{"early": early, "late": late} {
		select {
		case <-sub.finish:
		case <-time.After(testTimeout):
			t.Fatalf("the %s subscriber outlived the close", name)
		}
	}
}

// TestAdapterOverflowScopesByAcknowledgedRun pins that adapter-overflow
// scoping uses the position the consumer acknowledged, not the mailbox
// tail: run B enqueued behind unacknowledged run A envelopes does not move
// the subscriber out of A's overflow.
func TestAdapterOverflowScopesByAcknowledgedRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe with a run active was refused")
	}
	entry.publish(runEnvelope(t, "run-a", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil { // acknowledges run A
		t.Fatal(err)
	}
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-a", 2)) // enqueued, unacknowledged
	entry.publish(runEnvelope(t, "run-b", 1)) // enqueued behind it: tail B
	close(streamB)
	entry.signalOverflow("run-a")

	// The consumer is still positioned in run A: it drains the mailbox and
	// takes A's overflow instead of B's clean end.
	for _, want := range []protocol.RunID{"run-a", "run-b"} {
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != want {
			t.Fatalf("envelope: run %s error %v, want %s", envelope.RunID, err, want)
		}
	}
	_, err := subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want run-a overflow", err, err)
	}
	if overflow.RunID != "run-a" {
		t.Fatalf("overflow run %q, want run-a — the acknowledged position", overflow.RunID)
	}
	close(streamA)
}

// TestStaleRunErrorReachesExposedSubscribers pins the stale-error path: a
// run whose drainer exits with an error while a newer run holds the hub
// still terminates the subscribers exposed to it, instead of letting them
// read the newer run's clean end and never learn of the loss.
func TestStaleRunErrorReachesExposedSubscribers(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.publish(runEnvelope(t, "run-a", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil { // acknowledges run A
		t.Fatal(err)
	}
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1)) // queued; the consumer stays positioned in A
	streamFailure := errors.New("run A stream died")
	streamA <- base.Result{Error: streamFailure}
	close(streamA)
	close(streamB)
	// Both readers exit before the consumer drains, so the stale failure
	// has terminated the subscription ahead of any acknowledgement of B.
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		exited := entry.readers == 0
		entry.mu.Unlock()
		if exited {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the readers never exited")
		default:
		}
	}

	// The mailbox drains run B's queued envelope, then the stale failure
	// ends the subscription — never a clean EOF hiding the loss.
	if envelope, err := subscription.Next(); err != nil || envelope.RunID != "run-b" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("terminal %v, want run A's stale stream failure", err)
	}
}

// TestAcknowledgedPositionOverridesStalePending pins the exposure priority:
// once the consumer acknowledged a newer run, a late envelope from an older
// run still queued behind it does not re-expose it to the older run's
// overflow — its delivery of the newer run continues uninterrupted.
func TestAcknowledgedPositionOverridesStalePending(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.publish(runEnvelope(t, "run-a", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	if _, err := subscription.Next(); err != nil { // acknowledged run B
		t.Fatal(err)
	}
	entry.publish(runEnvelope(t, "run-a", 2)) // a stale drainer's late envelope
	entry.signalOverflow("run-a")             // must not terminate: position B

	entry.publish(runEnvelope(t, "run-b", 2))
	close(streamA)
	close(streamB)
	for _, want := range []protocol.RunID{"run-a", "run-b"} {
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != want {
			t.Fatalf("envelope: run %s error %v, want %s", envelope.RunID, err, want)
		}
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want the clean end", err)
	}
}

// TestNewerPendingRunExposesOverflow pins the newer-pending exposure: a
// consumer that acknowledged run A with run B's envelopes queued but
// unacknowledged is told of B's adapter overflow — B is its incomplete
// future, and a clean end would hide the loss.
func TestNewerPendingRunExposesOverflow(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.publish(runEnvelope(t, "run-a", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil { // acknowledges run A
		t.Fatal(err)
	}
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1)) // queued, unacknowledged
	entry.signalOverflow("run-b")             // newer pending run: exposed

	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-b" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	_, err = subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want run-b overflow", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 1", overflow)
	}
	close(streamA)
	close(streamB)
}

// TestQueueOverflowCursorRecoversDroppedRun pins the dropped-run cursor:
// when the consumer has a position in the run whose envelope found the
// queue full, the cursor resumes that run from the consumer's last
// delivered position — not the run that happens to own the mailbox tail.
func TestQueueOverflowCursorRecoversDroppedRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(1)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	for _, want := range []protocol.RunID{"run-a", "run-b"} {
		entry.publish(runEnvelope(t, want, 1))
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != want {
			t.Fatalf("envelope: run %s error %v, want %s", envelope.RunID, err, want)
		}
	}
	// A stale drainer's late terminal takes the one-slot tail; the next
	// run-B envelope is dropped by the full queue.
	entry.publish(runEnvelope(t, "run-a", 12))
	entry.publish(runEnvelope(t, "run-b", 2))

	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-a" || envelope.Sequence == nil || *envelope.Sequence != 12 {
		t.Fatalf("tail envelope: run %s sequence %v error %v", envelope.RunID, envelope.Sequence, err)
	}
	_, err = subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 1 — the dropped run's position", overflow)
	}
}

// TestSubmitErrorStreamStillDrains pins the adapter contract some adapters
// exercise (Pi): Submit may fail while returning a live stream whose run
// stays alive adapter-side. The hub drains and publishes that stream —
// subscriptions receive its events — while the caller still sees the error.
func TestSubmitErrorStreamStillDrains(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the caller's context dies mid-admission
	done := make(chan error, 1)
	go func() {
		_, err := entry.Submit(ctx, protocol.MessageSubmitRequest{
			SessionID: "gated", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("orphan")}},
		})
		done <- err
	}()
	<-gated.entered
	gated.stream = make(chan base.Result, 4)
	gated.failWithStream = context.Canceled
	close(gated.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("submit error %v, want context.Canceled", err)
	}

	// The orphaned stream still feeds the subscription, bound to the run
	// its envelopes name: a stream overflow attributes to that run with a
	// usable cursor, not an empty one.
	gated.stream <- base.Result{Envelope: runEnvelope(t, "run-x", 1)}
	gated.stream <- base.Result{Error: base.ErrEventStreamOverflow}
	close(gated.stream)
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-x" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	_, err = subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-x" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-x at sequence 1 — the bound run", overflow)
	}
}

// TestOrphanBecomesCurrentOverCompletedRun pins the orphan promotion: a
// stream returned with a submit error names a run whose envelopes make it
// current over the run that completed before the admission — so a bare
// Last-Event-ID reconnect resolves to it — while a genuinely newer
// admission keeps precedence.
func TestOrphanBecomesCurrentOverCompletedRun(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	// A run completed earlier leaves the hub's current run pointing at it.
	streamOld := make(chan base.Result, 4)
	entry.startRun("run-old", streamOld)
	close(streamOld)
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		settled := entry.readers == 0
		entry.mu.Unlock()
		if settled {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the old run's reader never exited")
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := entry.Submit(ctx, protocol.MessageSubmitRequest{
			SessionID: "gated", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("orphan")}},
		})
		done <- err
	}()
	<-gated.entered
	gated.stream = make(chan base.Result, 4)
	gated.failWithStream = context.Canceled
	close(gated.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("submit error %v, want context.Canceled", err)
	}
	gated.stream <- base.Result{Envelope: runEnvelope(t, "run-x", 1)}
	close(gated.stream)
	boundDeadline := time.After(testTimeout)
	for {
		if current, ok := entry.currentRun(); ok && current == "run-x" {
			break
		}
		select {
		case <-boundDeadline:
			current, _ := entry.currentRun()
			t.Fatalf("current run %q, want run-x — the orphan supersedes the completed run", current)
		default:
		}
	}
}

// TestEmptyErrorStreamKeepsSubscriptionsParked pins the ACP-shaped
// rejection: a submit error carrying an already-closed, empty stream is a
// rejected admission, not a completed run — parked subscribers stay parked
// for a corrected retry instead of reading a phantom run's end.
func TestEmptyErrorStreamKeepsSubscriptionsParked(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := entry.Submit(ctx, protocol.MessageSubmitRequest{
			SessionID: "gated", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("written-then-failed")}},
		})
		done <- err
	}()
	<-gated.entered
	closed := make(chan base.Result)
	close(closed)
	gated.stream = closed
	gated.failWithStream = context.Canceled
	close(gated.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("submit error %v, want context.Canceled", err)
	}
	// The empty stream ends without an envelope: the drainer releases the
	// reservation as a rejection and the parked subscriber survives.
	releaseDeadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		released := entry.readers == 0 && entry.reservations == 0
		entry.mu.Unlock()
		if released {
			break
		}
		select {
		case <-releaseDeadline:
			t.Fatal("the empty error stream never released its reservation")
		default:
		}
	}
	select {
	case <-sub.finish:
		t.Fatal("an empty error stream finished the parked subscriber")
	default:
	}

	// The corrected retry reaches the same subscription.
	gated.failWithStream = nil
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

// TestAcknowledgedOrderStaysMonotonic pins the acknowledged admission
// order: a late envelope from an older run consumed after a newer run must
// not drag the position backward and un-expose the subscriber to the newer
// run's overflow.
func TestAcknowledgedOrderStaysMonotonic(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA) // admitted first
	entry.startRun("run-b", streamB) // admitted second
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil { // acknowledges run B
		t.Fatal(err)
	}
	entry.publish(runEnvelope(t, "run-a", 1))      // the older run's late envelope
	if _, err := subscription.Next(); err != nil { // consumed after B
		t.Fatal(err)
	}
	// B's adapter overflows with nothing of B pending: the acknowledged
	// order still names B (the newer admission), so the subscriber is told.
	entry.signalOverflow("run-b")
	var overflow *OverflowError
	if _, err := subscription.Next(); !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	} else if overflow.RunID != "run-b" {
		t.Fatalf("overflow run %q, want run-b — the monotonic acknowledged order", overflow.RunID)
	}
}

// TestExposureByAdmissionOrder pins that overflow exposure compares run
// admission order, not mailbox-delivery order: a lagging older drainer
// publishing after a newer run's envelope must not count as newer.
func TestExposureByAdmissionOrder(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA) // admitted first
	entry.startRun("run-b", streamB) // admitted second
	// Delivery order inverts admission order: B publishes first.
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil { // acknowledges run B
		t.Fatal(err)
	}
	entry.publish(runEnvelope(t, "run-a", 1)) // the lagging older run's envelope
	entry.signalOverflow("run-a")             // admitted BEFORE the ack: not newer

	entry.publish(runEnvelope(t, "run-b", 2))
	close(streamA)
	close(streamB)
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-a" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	envelope, err = subscription.Next()
	if err != nil || envelope.RunID != "run-b" {
		t.Fatalf("envelope: run %s error %v, want run-b continuing past run-a's overflow", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want io.EOF", err)
	}
}

// TestCloseDuringEmptyErrorStreamEndsSubscribers pins the close-overlap: a
// session closed while an empty error stream's drainer still holds the
// reader slot ends its subscribers when that drainer releases — they must
// not outlive the close.
func TestCloseDuringEmptyErrorStreamEndsSubscribers(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan error, 1)
	go func() {
		_, err := entry.Submit(ctx, protocol.MessageSubmitRequest{
			SessionID: "gated", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("orphan")}},
		})
		done <- err
	}()
	<-gated.entered
	gated.stream = make(chan base.Result, 4) // open: the drainer lingers
	gated.failWithStream = context.Canceled
	close(gated.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("submit error %v, want context.Canceled", err)
	}
	// The close lands while the empty stream's drainer has not yet seen the
	// channel close; ending the stream afterwards must apply the close.
	entry.mu.Lock()
	draining := entry.readers > 0
	entry.mu.Unlock()
	if !draining {
		t.Fatal("the orphan drainer was not holding the reader slot")
	}
	entry.markClosed()
	close(gated.stream)
	select {
	case <-sub.finish:
	case <-time.After(testTimeout):
		t.Fatal("the closed session's subscriber outlived the empty error stream")
	}
}

// TestAttachmentRunOrdersPendingExposure pins the pre-acknowledgement
// baseline: a subscriber attaching while a newer run is current treats a
// pending envelope from an older, still-draining run as delivery lag, not
// loss — the older run's overflow must not detach it.
func TestAttachmentRunOrdersPendingExposure(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.startRun("run-b", streamB)
	sub, ok := entry.subscribe(8) // attaches while run B is current
	if !ok {
		t.Fatal("subscribe with a run active was refused")
	}
	entry.publish(runEnvelope(t, "run-a", 1)) // the older run's late envelope, pending
	entry.signalOverflow("run-a")             // older than the attachment: not exposed

	entry.publish(runEnvelope(t, "run-b", 1))
	close(streamA)
	close(streamB)
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-a" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	envelope, err = subscription.Next()
	if err != nil || envelope.RunID != "run-b" {
		t.Fatalf("envelope: run %s error %v, want run-b continuing past run-a's overflow", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want io.EOF", err)
	}
}

// TestDeferredErrorSurvivesNewAdmission pins the supersede rule: a new
// admission may bury a deferred clean end, but a deferred stream error is
// first delivered to the cohort that observed the failed run — such errors
// are terminal for the subscriptions that saw them.
func TestDeferredErrorSurvivesNewAdmission(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil { // cohort observes run B
		t.Fatal(err)
	}
	// A second admission is in flight when run B's stream fails, deferring
	// the error behind the reservation.
	admit := make(chan error, 1)
	go func() {
		_, err := entry.Submit(context.Background(), protocol.MessageSubmitRequest{
			SessionID: "gated", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("next run")}},
		})
		admit <- err
	}()
	<-gated.entered
	streamFailure := errors.New("run B stream died")
	streamB <- base.Result{Error: streamFailure}
	close(streamB)
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		deferred := entry.readers == 0 && entry.pendingEnd != nil
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run B's error end was never deferred")
		default:
		}
	}
	// The in-flight admission resolves into a new run: it supersedes the
	// deferred state but must first deliver the error to the cohort.
	close(gated.release)
	if err := <-admit; err != nil {
		t.Fatal(err)
	}
	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("terminal %v, want run B's deferred stream failure", err)
	}
}

// TestQueueOverflowPreservesNewerRun pins the queue-full loss cursor when a
// stale envelope is dropped: a subscriber that acknowledged run B and has
// B's future delivery discarded too must recover from B's position — never
// from the older run whose late envelope found the queue full.
func TestQueueOverflowPreservesNewerRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(1)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	for _, want := range []protocol.RunID{"run-a", "run-b"} {
		entry.publish(runEnvelope(t, want, 1))
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != want {
			t.Fatalf("envelope: run %s error %v, want %s", envelope.RunID, err, want)
		}
	}
	// A stale run-A terminal takes the one-slot tail; the next run-B
	// envelope is dropped — but B's remaining delivery is the loss.
	entry.publish(runEnvelope(t, "run-a", 12))
	entry.publish(runEnvelope(t, "run-b", 2))

	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-a" || envelope.Sequence == nil || *envelope.Sequence != 12 {
		t.Fatalf("tail envelope: run %s sequence %v error %v", envelope.RunID, envelope.Sequence, err)
	}
	_, err = subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 1 — the newer run's position", overflow)
	}
}

// TestAcknowledgedRunStaysPairedWithSerial pins the pairing: acknowledging
// a late older-run envelope after a newer run keeps the acknowledged run
// identifier at the newer run, so a later queue-full loss cursor resolved
// by acknowledged position names the newer run — not the stale pointer.
func TestAcknowledgedRunStaysPairedWithSerial(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.startRun("run-a", make(chan base.Result, 1))
	entry.startRun("run-b", make(chan base.Result, 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	for _, want := range []protocol.RunID{"run-a", "run-b"} {
		entry.publish(runEnvelope(t, want, 1))
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != want {
			t.Fatalf("envelope: run %s error %v, want %s", envelope.RunID, err, want)
		}
	}
	// The older run's late envelopes are acknowledged after B's — the
	// acknowledged serial stays at B's while a regressed pointer would
	// name A.
	entry.publish(runEnvelope(t, "run-a", 12))
	entry.publish(runEnvelope(t, "run-a", 13))
	for sequence := uint64(12); sequence <= 13; sequence++ {
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != "run-a" || envelope.Sequence == nil || *envelope.Sequence != sequence {
			t.Fatalf("late envelope %d: run %s sequence %v error %v", sequence, envelope.RunID, envelope.Sequence, err)
		}
	}
	// Stale run-A envelopes fill the two-slot tail and a third is dropped;
	// the acknowledged position is B — the cursor must name B.
	entry.publish(runEnvelope(t, "run-a", 14))
	entry.publish(runEnvelope(t, "run-a", 15))
	entry.publish(runEnvelope(t, "run-a", 16)) // dropped; loss resolves by acknowledged position
	for sequence := uint64(14); sequence <= 15; sequence++ {
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != "run-a" || envelope.Sequence == nil || *envelope.Sequence != sequence {
			t.Fatalf("tail envelope %d: run %s sequence %v error %v", sequence, envelope.RunID, envelope.Sequence, err)
		}
	}
	_, err := subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 1 — the paired acknowledged run", overflow)
	}
}

// TestCloseGivesNewcomersCleanEndOverDeferredError pins the two-cohort
// close: subscribers that observed a failed run receive its deferred error;
// subscribers that registered after that run's exit receive the close's
// clean end — never an error from a run they never saw.
func TestCloseGivesNewcomersCleanEndOverDeferredError(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	cohort, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA) // the older reader lags throughout
	entry.startRun("run-b", streamB) // B is current
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: cohort}
	if _, err := subscription.Next(); err != nil { // cohort observes run B
		t.Fatal(err)
	}
	// B's stream fails while A's older reader still drains: the current
	// run's error defers behind company.
	streamFailure := errors.New("run B stream died")
	streamB <- base.Result{Error: streamFailure}
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		deferred := entry.readers == 1 && entry.pendingEnd != nil && entry.pendingEnd.err == streamFailure
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run B's error end was never deferred behind run A's reader")
		default:
		}
	}
	newcomer, ok := entry.subscribe(8) // after B's exit: never observed B
	if !ok {
		t.Fatal("subscribe during the deferral window was refused")
	}
	entry.markClosed()
	close(streamA)

	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("cohort terminal %v, want run B's deferred stream failure", err)
	}
	newcomerSubscription := &Subscription{session: entry, ctx: context.Background(), sub: newcomer}
	for {
		_, err := newcomerSubscription.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("newcomer terminal %v, want the clean close — never B's error", err)
		}
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
