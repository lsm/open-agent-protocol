package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
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

func runEnvelope(t *testing.T, runID protocol.RunID, sequence uint64) protocol.Envelope {
	t.Helper()
	envelope := hubEnvelope(t, protocol.TypeRunStatusUpdated, sequence)
	envelope.RunID = runID
	return envelope
}

func TestQueueOverflowCursorTracksPosition(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.startRun("run-a", make(chan base.Result, 1))
	entry.startRun("run-b", make(chan base.Result, 1))
	entry.publish(runEnvelope(t, "run-a", 1))
	entry.publish(runEnvelope(t, "run-a", 2))
	entry.publish(runEnvelope(t, "run-b", 1))

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

	latecomer := newSession("hub", "memory", nil)
	positioned, _, _, ok := latecomer.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	latecomer.startRun("run-a", make(chan base.Result, 1))
	latecomer.startRun("run-b", make(chan base.Result, 1))
	latecomer.publish(runEnvelope(t, "run-a", 1))
	latecomer.publish(runEnvelope(t, "run-b", 1))
	latecomer.publish(runEnvelope(t, "run-b", 2))

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

func TestOverflowRecoversFromTheLiveRunNotASettledReservation(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}

	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.publish(runEnvelope(t, "run-a", 1))

	entry.mu.Lock()
	entry.reservations++
	entry.mu.Unlock()
	streamB := make(chan base.Result, 4)
	entry.adoptRun("run-b", streamB, true)
	cancelled := runEnvelope(t, "run-b", 1)
	cancelled.Type = protocol.TypeRunCancelled
	streamB <- base.Result{Envelope: cancelled}
	close(streamB)

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	for _, want := range []protocol.RunID{"run-a", "run-b"} {
		envelope, err := subscription.Next()
		if err != nil {
			t.Fatalf("reading %s: %v", want, err)
		}
		if envelope.RunID != want {
			t.Fatalf("envelope run = %s, want %s", envelope.RunID, want)
		}
	}
	waitForFinished(t, entry, "run-b")

	entry.publish(runEnvelope(t, "run-a", 2))
	entry.publish(runEnvelope(t, "run-a", 3))
	entry.publish(runEnvelope(t, "run-a", 4))

	drained := 0
	var overflow *OverflowError
	for {
		envelope, err := subscription.Next()
		if errors.As(err, &overflow) {
			break
		}
		if err != nil {
			t.Fatalf("terminal %v (%T), want an OverflowError", err, err)
		}
		if drained++; drained > 8 {
			t.Fatal("the subscriber never overflowed")
		}
		_ = envelope
	}
	if overflow.RunID != "run-a" {
		t.Fatalf("overflow cursor %+v, want the still-live run the drop lost", overflow)
	}
	close(streamA)
}

func waitForFinished(t *testing.T, entry *Session, run protocol.RunID) {
	t.Helper()
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		done := entry.finished[run]
		entry.mu.Unlock()
		if done {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("run %s never finished draining", run)
		default:
		}
	}
}

func TestDeferredEndDoesNotClobberOverflowTerminal(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.publish(runEnvelope(t, "run-a", 1))
	entry.publish(runEnvelope(t, "run-a", 2))

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

	entry.publish(runEnvelope(t, "run-a", 3))
	close(streamA)

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

	if overflow.RunID != "run-b" || overflow.LastSequence != 0 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 0", overflow)
	}
}

func TestMarkClosedDefersFinishToReader(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
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

func TestMarkClosedFinishesImmediatelyWithoutReader(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(4)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.markClosed()
	select {
	case <-sub.finish:
	default:
		t.Fatal("parked subscriber survived an idle close")
	}
	if _, _, _, accepted := entry.subscribe(4); accepted {
		t.Fatal("subscribe after close must be refused")
	}
}

func TestOverlappingReadersDeliverCurrentRunEnd(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}

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

func TestAdapterOverflowScopedToExposedSubscribers(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	spanning, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}

	entry.startRun("run-b", streamB)
	streamA <- base.Result{Error: base.ErrEventStreamOverflow}
	close(streamA)

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

	bOnly, _, _, ok := entry.subscribe(8)
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

func TestOverflowFollowsDeliveredRuns(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}

	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}
	spanning := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if envelope, err := spanning.Next(); err != nil || envelope.RunID != "run-a" {
		t.Fatalf("run-a envelope: run %s error %v", envelope.RunID, err)
	}

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

func TestSubmitReservationBridgesAdmission(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
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

func TestSubmitReservationReleasesOnFailure(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrRunActive}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
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

func TestDeferredFinishSurvivesLaterReservations(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrRunActive}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
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

	<-gated.entered
	<-gated.entered
	close(streamA)
	close(gated.release)
	for range 2 {
		if err := <-rejected; !errors.Is(err, base.ErrRunActive) {
			t.Fatalf("submit error %v, want run-active", err)
		}
	}

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-a" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want the deferred clean end", err)
	}
}

func TestDeferredFinishSparesLaterSubscribers(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrRunActive}
	entry := newSession("gated", "stub", gated)
	cohort, _, _, ok := entry.subscribe(8)
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
	newcomer, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe during the reservation window was refused")
	}
	close(gated.release)
	if err := <-rejected; !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("submit error %v, want run-active", err)
	}

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

func TestDeferredRunEndSparesLaterSubscribers(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	cohort, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}
	entry.startRun("run-b", streamB)
	streamB <- base.Result{Envelope: runEnvelope(t, "run-b", 1)}

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
	newcomer, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe during the deferral window was refused")
	}
	close(streamA)

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

func TestRejectedSubmitKeepsSubscriptions(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrInvalidSubmission}
	close(gated.release)
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
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

func TestLateOverflowDoesNotCutNewerRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	streamA <- base.Result{Envelope: runEnvelope(t, "run-a", 1)}

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}

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

func TestCloseSessionsAttemptsEverySession(t *testing.T) {

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

type countingSession struct {
	stubSession
	closes atomic.Int32
}

func (c *countingSession) Close(ctx context.Context) error {
	c.closes.Add(1)
	return c.stubSession.Close(ctx)
}

func TestCloseEndsSubscribersRegisteredAfterDeferredRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	early, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.startRun("run-b", streamB)
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
	late, _, _, ok := entry.subscribe(8)
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

func TestAdapterOverflowScopesByAcknowledgedRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe with a run active was refused")
	}
	entry.publish(runEnvelope(t, "run-a", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-a", 2))
	entry.publish(runEnvelope(t, "run-b", 1))
	close(streamB)
	entry.signalOverflow("run-a")

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

func TestStaleRunErrorReachesExposedSubscribers(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
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
	streamFailure := errors.New("run A stream died")
	streamA <- base.Result{Error: streamFailure}
	close(streamA)
	close(streamB)

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

	if envelope, err := subscription.Next(); err != nil || envelope.RunID != "run-b" {
		t.Fatalf("envelope: run %s error %v", envelope.RunID, err)
	}
	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("terminal %v, want run A's stale stream failure", err)
	}
}

func TestAcknowledgedPositionOverridesStalePending(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
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
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	entry.publish(runEnvelope(t, "run-a", 2))
	entry.signalOverflow("run-a")

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

func TestNewerPendingRunExposesOverflow(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.publish(runEnvelope(t, "run-a", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	entry.signalOverflow("run-b")

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

func TestQueueOverflowCursorRecoversDroppedRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(1)
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

func TestSubmitErrorStreamStillDrains(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
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
	gated.stream = make(chan base.Result, 4)
	gated.failWithStream = context.Canceled
	close(gated.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("submit error %v, want context.Canceled", err)
	}

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

func TestOrphanBecomesCurrentOverCompletedRun(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)

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

func TestEmptyErrorStreamKeepsSubscriptionsParked(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
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

func TestAcknowledgedOrderStaysMonotonic(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	entry.publish(runEnvelope(t, "run-a", 1))
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}

	entry.signalOverflow("run-b")
	var overflow *OverflowError
	if _, err := subscription.Next(); !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	} else if overflow.RunID != "run-b" {
		t.Fatalf("overflow run %q, want run-b — the monotonic acknowledged order", overflow.RunID)
	}
}

func TestExposureByAdmissionOrder(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.startRun("run-b", streamB)

	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	entry.publish(runEnvelope(t, "run-a", 1))
	entry.signalOverflow("run-a")

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

func TestCloseDuringEmptyErrorStreamEndsSubscribers(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
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
	gated.stream = make(chan base.Result, 4)
	gated.failWithStream = context.Canceled
	close(gated.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("submit error %v, want context.Canceled", err)
	}

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

func TestAttachmentRunOrdersPendingExposure(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.startRun("run-b", streamB)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe with a run active was refused")
	}
	entry.publish(runEnvelope(t, "run-a", 1))
	entry.signalOverflow("run-a")

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

func TestDeferredErrorSurvivesNewAdmission(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}

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

	close(gated.release)
	if err := <-admit; err != nil {
		t.Fatal(err)
	}
	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("terminal %v, want run B's deferred stream failure", err)
	}
}

func TestQueueOverflowPreservesNewerRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(1)
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

func TestAcknowledgedRunStaysPairedWithSerial(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	sub, _, _, ok := entry.subscribe(2)
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

	entry.publish(runEnvelope(t, "run-a", 12))
	entry.publish(runEnvelope(t, "run-a", 13))
	for sequence := uint64(12); sequence <= 13; sequence++ {
		envelope, err := subscription.Next()
		if err != nil || envelope.RunID != "run-a" || envelope.Sequence == nil || *envelope.Sequence != sequence {
			t.Fatalf("late envelope %d: run %s sequence %v error %v", sequence, envelope.RunID, envelope.Sequence, err)
		}
	}

	entry.publish(runEnvelope(t, "run-a", 14))
	entry.publish(runEnvelope(t, "run-a", 15))
	entry.publish(runEnvelope(t, "run-a", 16))
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

func TestCloseGivesNewcomersCleanEndOverDeferredError(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	cohort, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamA := make(chan base.Result, 4)
	streamB := make(chan base.Result, 4)
	entry.startRun("run-a", streamA)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: cohort}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}

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
	newcomer, _, _, ok := entry.subscribe(8)
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

func TestQueueOverflowPrefersAttachedRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	streamA := make(chan base.Result, 1)
	entry.startRun("run-a", streamA)
	streamB := make(chan base.Result, 1)
	entry.startRun("run-b", streamB)
	sub, _, _, ok := entry.subscribe(1)
	if !ok {
		t.Fatal("subscribe with a run active was refused")
	}
	entry.publish(runEnvelope(t, "run-b", 1))
	entry.publish(runEnvelope(t, "run-a", 9))

	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-b" || envelope.Sequence == nil || *envelope.Sequence != 1 {
		t.Fatalf("envelope: run %s sequence %v error %v", envelope.RunID, envelope.Sequence, err)
	}
	_, err = subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 1 — the attached run", overflow)
	}
}

func TestEmptyOrphanAppliesDeferredRunEnd(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
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

	streamFailure := errors.New("run B stream died")
	streamB <- base.Result{Error: streamFailure}
	close(streamB)
	deadline := time.After(testTimeout)
	for {
		entry.mu.Lock()
		deferred := entry.pendingEnd != nil && entry.pendingEnd.err == streamFailure
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run B's error end was never deferred behind the orphan")
		default:
		}
	}

	close(gated.stream)
	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("terminal %v, want run B's deferred stream failure", err)
	}
}

func TestQueueOverflowPrefersNewerQueuedRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	streamA := make(chan base.Result, 1)
	entry.startRun("run-a", streamA)
	streamB := make(chan base.Result, 1)
	entry.startRun("run-b", streamB)
	sub, _, _, ok := entry.subscribe(1)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.publish(runEnvelope(t, "run-a", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	entry.publish(runEnvelope(t, "run-b", 1))
	entry.publish(runEnvelope(t, "run-a", 9))

	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-b" || envelope.Sequence == nil || *envelope.Sequence != 1 {
		t.Fatalf("envelope: run %s sequence %v error %v", envelope.RunID, envelope.Sequence, err)
	}
	_, err = subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-b" || overflow.LastSequence != 1 {
		t.Fatalf("overflow cursor %+v, want run-b at sequence 1 — the newer queued run", overflow)
	}
}

func TestTerminalSubmitErrorClosesEntry(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{}), fail: base.ErrSessionClosed}
	close(gated.release)
	entry := newSession("gated", "stub", gated)
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	if _, err := entry.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "gated", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("after the transport died")}},
	}); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("submit error %v, want session-closed", err)
	}
	if !entry.IsClosed() {
		t.Fatal("the entry did not record the closed adapter session")
	}
	subscription := &Subscription{session: entry, ctx: ctx, sub: sub}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want the clean end of a closed session", err)
	}
	if _, _, _, accepted := entry.subscribe(8); accepted {
		t.Fatal("subscribe after the terminal rejection was accepted")
	}
}

func TestCloseOverReservationErrorSplitsCohorts(t *testing.T) {
	gated := &gatedSession{entered: make(chan struct{}, 4), release: make(chan struct{})}
	entry := newSession("gated", "stub", gated)
	cohort, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	streamB := make(chan base.Result, 4)
	entry.startRun("run-b", streamB)
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: cohort}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}

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
		deferred := entry.readers == 0 && entry.pendingEnd != nil && entry.pendingEnd.err == streamFailure
		entry.mu.Unlock()
		if deferred {
			break
		}
		select {
		case <-deadline:
			t.Fatal("run B's error end was never deferred behind the reservation")
		default:
		}
	}
	newcomer, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe during the deferral window was refused")
	}
	entry.markClosed()
	gated.fail = base.ErrRunActive
	close(gated.release)
	if err := <-admit; err == nil {
		t.Fatal("the lingering admission unexpectedly succeeded after the close")
	}

	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("cohort terminal %v, want run B's deferred stream failure", err)
	}
	newcomerSubscription := &Subscription{session: entry, ctx: context.Background(), sub: newcomer}
	if _, err := newcomerSubscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("newcomer terminal %v, want the clean close — never B's error", err)
	}
}

func TestQueueOverflowIncludesCurrentRun(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	streamA := make(chan base.Result, 1)
	entry.startRun("run-a", streamA)
	streamB := make(chan base.Result, 1)
	entry.startRun("run-b", streamB)
	sub, _, _, ok := entry.subscribe(1)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	entry.publish(runEnvelope(t, "run-b", 1))
	subscription := &Subscription{session: entry, ctx: context.Background(), sub: sub}
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}

	entry.publish(runEnvelope(t, "run-b", 2))
	streamC := make(chan base.Result, 1)
	entry.startRun("run-c", streamC)
	entry.publish(runEnvelope(t, "run-a", 9))

	envelope, err := subscription.Next()
	if err != nil || envelope.RunID != "run-b" || envelope.Sequence == nil || *envelope.Sequence != 2 {
		t.Fatalf("envelope: run %s sequence %v error %v", envelope.RunID, envelope.Sequence, err)
	}
	_, err = subscription.Next()
	var overflow *OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("terminal %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != "run-c" || overflow.LastSequence != 0 {
		t.Fatalf("overflow cursor %+v, want run-c at sequence 0 — the current run, replayed from its start", overflow)
	}
}

type idleClosedSession struct{ id protocol.SessionID }

func (s *idleClosedSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
}

func (s *idleClosedSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: s.id, Status: protocol.SessionClosed}, base.ErrSessionClosed
}

func (s *idleClosedSession) Resolve(context.Context, base.InteractionResolution) error {
	return base.ErrSessionClosed
}

func (s *idleClosedSession) Cancel(_ context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{}, base.ErrSessionClosed
}

func (s *idleClosedSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, base.ErrSessionClosed
}

func (s *idleClosedSession) Close(context.Context) error { return base.ErrSessionClosed }

var _ base.Session = (*idleClosedSession)(nil)

func TestStateReportingClosedClosesEntry(t *testing.T) {
	entry := newSession("idle-death", "stub", &idleClosedSession{id: "idle-death"})
	sub, _, _, ok := entry.subscribe(8)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	state, err := entry.State(ctx)
	if !errors.Is(err, base.ErrSessionClosed) || state.Status != protocol.SessionClosed {
		t.Fatalf("state %+v error %v, want the closed final state", state, err)
	}
	if !entry.IsClosed() {
		t.Fatal("the entry did not record the closed adapter session")
	}
	subscription := &Subscription{session: entry, ctx: ctx, sub: sub}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal %v, want the clean end of a closed session", err)
	}
}

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

type queuedStubSession struct {
	live      []protocol.ActiveRun
	activeRun protocol.RunID
	needed    int
	cancelled []protocol.RunID
}

func (s *queuedStubSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, nil
}
func (s *queuedStubSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{ActiveRunID: s.activeRun, ActiveRuns: s.live}, nil
}
func (s *queuedStubSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *queuedStubSession) Cancel(_ context.Context, run protocol.RunID) (protocol.RunCancelResponse, error) {
	s.cancelled = append(s.cancelled, run)
	return protocol.RunCancelResponse{Accepted: true}, nil
}
func (s *queuedStubSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}
func (s *queuedStubSession) Close(context.Context) error {
	if len(s.cancelled) < s.needed {
		return base.ErrRunActive
	}
	return nil
}

var _ base.Session = (*queuedStubSession)(nil)

func TestCloseCancelsReservationsTheSnapshotNames(t *testing.T) {
	stub := &queuedStubSession{needed: 1, live: []protocol.ActiveRun{{RunID: "run-queued", Status: protocol.RunQueued, Relationship: protocol.RelationshipPrimary}}}
	entry := newSession("stub", "stub", stub)
	if err := entry.closeForShutdown(context.Background()); err != nil {
		t.Fatalf("close did not settle a reservation-only session: %v", err)
	}
	if fmt.Sprint(stub.cancelled) != fmt.Sprint([]protocol.RunID{"run-queued"}) {
		t.Fatalf("cancelled %v, want the reservation", stub.cancelled)
	}
	if !entry.IsClosed() {
		t.Fatal("session did not record the close")
	}
}

func TestCloseCancelsEveryRunTheSnapshotLists(t *testing.T) {
	stub := &queuedStubSession{
		needed:    2,
		activeRun: "run-started",
		live: []protocol.ActiveRun{
			{RunID: "run-started", Status: protocol.RunRunning, Relationship: protocol.RelationshipPrimary},
			{RunID: "run-queued", Status: protocol.RunQueued, Relationship: protocol.RelationshipPrimary},
		},
	}
	entry := newSession("stub", "stub", stub)
	if err := entry.closeForShutdown(context.Background()); err != nil {
		t.Fatalf("close did not settle: %v", err)
	}
	if fmt.Sprint(stub.cancelled) != fmt.Sprint([]protocol.RunID{"run-started", "run-queued"}) {
		t.Fatalf("cancelled %v, want both runs in admission order", stub.cancelled)
	}
}

func TestCloseFallsBackToTheNamedActiveRun(t *testing.T) {
	stub := &queuedStubSession{needed: 1, activeRun: "run-started"}
	entry := newSession("stub", "stub", stub)
	if err := entry.closeForShutdown(context.Background()); err != nil {
		t.Fatalf("close did not settle: %v", err)
	}
	if fmt.Sprint(stub.cancelled) != fmt.Sprint([]protocol.RunID{"run-started"}) {
		t.Fatalf("cancelled %v, want the named active run", stub.cancelled)
	}
}

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
	close(blocker.unblocked)

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

	if elapsed := time.Since(start); elapsed > 1200*time.Millisecond {
		t.Fatalf("CloseSessions ran %v for four stuck sessions, outside the 600ms budget", elapsed)
	}
}

func TestHubSubscriberQueueOverflow(t *testing.T) {
	entry := newSession("hub", "memory", nil)
	slow, _, _, ok := entry.subscribe(2)
	if !ok {
		t.Fatal("subscribe on an open session was refused")
	}
	fast, _, _, _ := entry.subscribe(64)
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
