package serve

import (
	"context"
	"errors"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type steerStubSession struct {
	stream   chan base.Result
	emits    []protocol.Envelope
	failures []error
	refusal  *base.InvalidSteerTargetError
	steerRun protocol.RunID
	boundary uint64
	steered  chan struct{}
	hold     chan struct{}
}

func (s *steerStubSession) Submit(_ context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	switch submit.Request.Delivery {
	case protocol.DeliveryAuto:
		return protocol.MessageSubmitResponse{
			SessionID: "stub", Accepted: true, SubmissionID: "sub-start",
			RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
			Admission: protocol.AdmissionStarted, RunID: "run-1", Status: protocol.RunRunning,
			MessageIDs: []protocol.MessageID{"m-1"},
		}, s.stream, nil
	case protocol.DeliverySteer:
		if s.steered != nil {
			close(s.steered)
		}
		for _, envelope := range s.emits {
			s.stream <- base.Result{Envelope: envelope}
		}
		for _, failure := range s.failures {
			s.stream <- base.Result{Error: failure}
		}
		if s.hold != nil {
			<-s.hold
		}
		if s.refusal != nil {
			return protocol.MessageSubmitResponse{}, nil, s.refusal
		}
		boundary := uint64(1)
		if s.boundary != 0 {
			boundary = s.boundary
		}
		return protocol.MessageSubmitResponse{
			SessionID: "stub", Accepted: true, SubmissionID: "sub-steer",
			RequestedDelivery: protocol.DeliverySteer, EffectiveDelivery: protocol.EffectiveDeliverySteer,
			Admission: protocol.AdmissionSteered, RunID: s.steerRun, Status: protocol.RunRunning,
			TargetSequence: &boundary, MessageIDs: []protocol.MessageID{"m-2"},
		}, nil, nil
	}
	return protocol.MessageSubmitResponse{}, nil, nil
}

func (s *steerStubSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "stub", Status: protocol.SessionRunning, ActiveRunID: "run-1"}, nil
}
func (s *steerStubSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *steerStubSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: "stub", Accepted: true, Status: protocol.RunCancelling}, nil
}
func (s *steerStubSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}
func (s *steerStubSession) Close(context.Context) error { return nil }

var _ base.Session = (*steerStubSession)(nil)

func steerStubDelta(sequence uint64) protocol.Envelope {
	envelope, err := protocol.NewEnvelope(protocol.TypeContentDelta, protocol.EnvelopeID("event-delta"), protocol.ContentDeltaPayload{
		SessionID: "stub", RunID: "run-1", MessageID: "m-3",
		Part: protocol.ContentPart{Type: protocol.ContentText, Text: "applied"},
	})
	if err != nil {
		panic(err)
	}
	envelope.SessionID, envelope.RunID = "stub", "run-1"
	envelope.Sequence = &sequence
	return envelope
}

func steerStubEnvelope(typ protocol.EnvelopeType, sequence uint64, submission protocol.SubmissionID, request protocol.EnvelopeID) protocol.Envelope {
	payload := any(protocol.RunSteerAppliedPayload{
		SessionID: "stub", RunID: "run-1", SubmissionID: submission, RequestID: request,
		MessageIDs: []protocol.MessageID{"m-2"}, Boundary: protocol.SteerTurn,
	})
	if typ == protocol.TypeRunSteerDropped {
		payload = protocol.RunSteerDroppedPayload{
			SessionID: "stub", RunID: "run-1", SubmissionID: submission, RequestID: request,
			Reason: protocol.ProtocolError{Code: "run_terminated", Message: "the run ended"},
		}
	}
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID("event-steer"), payload)
	if err != nil {
		panic(err)
	}
	envelope.SessionID, envelope.RunID = "stub", "run-1"
	envelope.Sequence = &sequence
	return envelope
}

func startStubRun(t *testing.T, entry *Session) {
	t.Helper()
	admission, err := entry.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "stub", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}})
	if err != nil || admission.Admission != protocol.AdmissionStarted || admission.RunID != "run-1" {
		t.Fatalf("start admission = %+v, err = %v", admission, err)
	}
}

func subscribeToRun(t *testing.T, entry *Session) *subscriber {
	t.Helper()
	sub, _, _, ok := entry.subscribe(16)
	if !ok {
		t.Fatal("the session refused a subscription")
	}
	t.Cleanup(func() { entry.unsubscribe(sub) })
	return sub
}

func nextEnvelope(t *testing.T, sub *subscriber) protocol.Envelope {
	t.Helper()
	select {
	case envelope := <-sub.ch:
		return envelope
	case <-time.After(testTimeout):
		t.Fatal("no envelope was published")
		return protocol.Envelope{}
	}
}

func expectNoEnvelope(t *testing.T, sub *subscriber, what string) {
	t.Helper()
	select {
	case envelope := <-sub.ch:
		t.Fatalf("%s published %s before the response was observable", what, envelope.Type)
	case <-time.After(50 * time.Millisecond):
	}
}

func testGate(run protocol.RunID, request protocol.EnvelopeID) *publicationGate {
	gate := &publicationGate{request: request, drainedCh: make(chan struct{})}
	gate.cover(run)
	return gate
}

func steerSubmit(envelope protocol.EnvelopeID) base.SubmitRequest {
	return base.SubmitRequest{
		EnvelopeID: envelope,
		Request: protocol.MessageSubmitRequest{
			SessionID: "stub", Delivery: protocol.DeliverySteer, TargetRunID: "run-1",
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("wait")}},
		},
	}
}

func TestASteeredAdmissionAdoptsNoRunAndHoldsTheTargetStream(t *testing.T) {
	stub := &steerStubSession{
		stream:   make(chan base.Result, 4),
		steerRun: "run-1",
		emits:    []protocol.Envelope{steerStubEnvelope(protocol.TypeRunSteerApplied, 2, "sub-steer", "req-steer")},
	}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	sub := subscribeToRun(t, entry)
	entry.mu.Lock()
	readers, serial := entry.readers, entry.serials["run-1"]
	entry.mu.Unlock()

	admission, err := entry.Submit(context.Background(), steerSubmit("req-steer"))
	if err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionSteered || admission.RunID != "run-1" {
		t.Fatalf("steer admission = %+v", admission)
	}
	entry.mu.Lock()
	adopted := entry.readers != readers || entry.serials["run-1"] != serial
	entry.mu.Unlock()
	if adopted {
		t.Fatal("the steered admission adopted a run")
	}

	expectNoEnvelope(t, sub, "a settlement the adapter emitted inside Submit")

	entry.Published("some-other-request")
	expectNoEnvelope(t, sub, "an unrelated submit's Published")

	entry.Published("req-steer")
	envelope := nextEnvelope(t, sub)
	if envelope.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("published %s, want the settlement", envelope.Type)
	}
	var applied protocol.RunSteerAppliedPayload
	if err := envelope.DecodePayload(&applied); err != nil {
		t.Fatal(err)
	}
	if applied.RequestID != "req-steer" || applied.SubmissionID != "sub-steer" {
		t.Fatalf("settlement = %+v", applied)
	}
	expectNoEnvelope(t, sub, "a repeated Published")
}

func TestASteerRefusalPublishesOnlyUpToItsBoundary(t *testing.T) {
	boundary := uint64(1)
	stub := &steerStubSession{
		stream: make(chan base.Result, 4),
		emits: []protocol.Envelope{
			steerStubDelta(1),
			steerStubEnvelope(protocol.TypeRunSteerDropped, 2, "sub-steer", "req-steer"),
		},
		refusal: &base.InvalidSteerTargetError{RunID: "run-1", Reason: base.SteerReasonTerminal, TargetSequence: &boundary},
	}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	sub := subscribeToRun(t, entry)

	if _, err := entry.Submit(context.Background(), steerSubmit("req-steer")); err == nil {
		t.Fatal("a refused steer was admitted")
	}
	if envelope := nextEnvelope(t, sub); envelope.Type != protocol.TypeContentDelta {
		t.Fatalf("published %s, want the envelope inside the refusal's boundary", envelope.Type)
	}
	expectNoEnvelope(t, sub, "a refusal's withheld settlement")

	entry.Published("req-steer")
	if envelope := nextEnvelope(t, sub); envelope.Type != protocol.TypeRunSteerDropped {
		t.Fatalf("published %s, want the withheld settlement", envelope.Type)
	}
}

func TestASteerRefusalWithoutABoundaryWithholdsEverythingEmittedSinceArming(t *testing.T) {
	stub := &steerStubSession{
		stream:  make(chan base.Result, 4),
		emits:   []protocol.Envelope{steerStubDelta(1), steerStubEnvelope(protocol.TypeRunSteerDropped, 2, "sub-steer", "req-steer")},
		refusal: &base.InvalidSteerTargetError{RunID: "run-9", Reason: base.SteerReasonUnknownTarget},
	}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	sub := subscribeToRun(t, entry)

	if _, err := entry.Submit(context.Background(), steerSubmit("req-steer")); err == nil {
		t.Fatal("a refused steer was admitted")
	}
	expectNoEnvelope(t, sub, "a boundary-free refusal")

	entry.Published("req-steer")
	if envelope := nextEnvelope(t, sub); envelope.Type != protocol.TypeContentDelta {
		t.Fatalf("published %s, want the withheld prefix in order", envelope.Type)
	}
	if envelope := nextEnvelope(t, sub); envelope.Type != protocol.TypeRunSteerDropped {
		t.Fatalf("published %s, want the withheld settlement", envelope.Type)
	}
}

func TestASteerNamingAnotherSessionsRunIsRefused(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4), steerRun: "run-1"}
	index := &runIndex{}
	index.claim("elsewhere", "run-foreign")
	entry := newSession("stub", "stub", stub, nil)
	entry.runs = index
	startStubRun(t, entry)

	submit := steerSubmit("req-steer")
	submit.Request.TargetRunID = "run-foreign"
	_, err := entry.Submit(context.Background(), submit)
	var refusal *base.InvalidSteerTargetError
	if !errors.As(err, &refusal) || refusal.Reason != base.SteerReasonCrossSession || refusal.RunID != "run-foreign" {
		t.Fatalf("steer = %v, want a cross_session refusal", err)
	}
}

func TestASteerOfARepeatedRunIDIsNotForeign(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("memory-a", base.NewMemory(base.Config{JournalCapacity: 64})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("memory-b", base.NewMemory(base.Config{JournalCapacity: 64})); err != nil {
		t.Fatal(err)
	}
	hub := New(registry, Options{})
	ctx := context.Background()
	first, _, err := hub.Open(ctx, "memory-a", base.OpenRequest{SessionID: "first", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := hub.Open(ctx, "memory-b", base.OpenRequest{SessionID: "second", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	messages := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}
	if _, err := first.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "first", Delivery: protocol.DeliveryAuto, Messages: messages}}); err != nil {
		t.Fatal(err)
	}
	own, err := second.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "second", Delivery: protocol.DeliveryAuto, Messages: messages}})
	if err != nil {
		t.Fatal(err)
	}
	steer, err := second.Submit(ctx, base.SubmitRequest{
		EnvelopeID: "req-steer",
		Request: protocol.MessageSubmitRequest{
			SessionID: "second", Delivery: protocol.DeliverySteer, TargetRunID: own.RunID,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("wait")}},
		},
	})
	if err != nil {
		t.Fatalf("a session's own run was refused as foreign: %v", err)
	}
	if steer.Admission != protocol.AdmissionSteered {
		t.Fatalf("steer admission = %+v", steer)
	}
}

func TestASteerNamingAnotherSessionsRunIsRefusedThroughTheHub(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: 64})); err != nil {
		t.Fatal(err)
	}
	hub := New(registry, Options{})
	ctx := context.Background()
	first, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "first", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "second", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	admission, err := first.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "first", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = second.Submit(ctx, base.SubmitRequest{
		EnvelopeID: "req-steer",
		Request: protocol.MessageSubmitRequest{
			SessionID: "second", Delivery: protocol.DeliverySteer, TargetRunID: admission.RunID,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("wait")}},
		},
	})
	var refusal *base.InvalidSteerTargetError
	if !errors.As(err, &refusal) || refusal.Reason != base.SteerReasonCrossSession {
		t.Fatalf("steer = %v, want a cross_session refusal", err)
	}
}

func TestAPublishedCallWithoutAGateIsANoOp(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4), steerRun: "run-1"}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	entry.Published("req-steer")
	entry.Published("")
	if _, err := entry.Submit(context.Background(), steerSubmit("req-steer")); err != nil {
		t.Fatal(err)
	}
	entry.Published("some-other-request")
	entry.Published("req-steer")
}

func TestASteerGateIsLiftedWhenTheSessionCloses(t *testing.T) {
	stub := &steerStubSession{
		stream:   make(chan base.Result, 4),
		steerRun: "run-1",
		emits:    []protocol.Envelope{steerStubEnvelope(protocol.TypeRunSteerApplied, 2, "sub-steer", "req-steer")},
	}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	if _, err := entry.Submit(context.Background(), steerSubmit("req-steer")); err != nil {
		t.Fatal(err)
	}
	entry.markClosed()
	entry.mu.Lock()
	gate := entry.gate
	entry.mu.Unlock()
	if gate != nil {
		t.Fatal("the close left the gate armed")
	}
}

func TestASteerDrainHandsAnErrorResultBackToTheReader(t *testing.T) {
	stub := &steerStubSession{
		stream:   make(chan base.Result, 4),
		steerRun: "run-1",
		failures: []error{base.ErrEventStreamOverflow},
	}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	sub := subscribeToRun(t, entry)

	if _, err := entry.Submit(context.Background(), steerSubmit("req-steer")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.finish:
	case envelope := <-sub.ch:
		t.Fatalf("the drain published %q for session %q instead of handing the error back", envelope.Type, envelope.SessionID)
	case <-time.After(testTimeout):
		t.Fatal("the drain swallowed the stream error")
	}
	state := sub.terminal.Load()
	if state == nil || !state.overflow || state.run != "run-1" {
		t.Fatalf("terminal state = %+v, want the overflow for run-1", state)
	}
	entry.Published("req-steer")
}

func TestAReaderThatEndsWhileADrainIsPendingDoesNotStallTheSubmit(t *testing.T) {
	stub := &steerStubSession{
		stream:   make(chan base.Result, 4),
		steerRun: "run-1",
		steered:  make(chan struct{}),
		hold:     make(chan struct{}),
	}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	subscribeToRun(t, entry)

	submitted := make(chan error, 1)
	started := time.Now()
	go func() {
		_, err := entry.Submit(context.Background(), steerSubmit("req-steer"))
		submitted <- err
	}()
	select {
	case <-stub.steered:
	case <-time.After(testTimeout):
		t.Fatal("the adapter never saw the steer")
	}
	close(stub.stream)
	time.Sleep(100 * time.Millisecond)
	close(stub.hold)

	select {
	case err := <-submitted:
		if err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("the submit waited %s for a drain the reader could not serve", elapsed)
		}
	case <-time.After(testTimeout):
		t.Fatal("the steer submit never returned")
	}
	entry.Published("req-steer")
}

func TestWhatADrainWithholdsAfterItsGateLiftsStillReachesSubscribers(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4)}
	entry := newSession("stub", "stub", stub, nil)
	sub := subscribeToRun(t, entry)

	gate := testGate("run-1", "req-steer")
	entry.mu.Lock()
	entry.gate = gate
	entry.mu.Unlock()
	entry.liftGate(gate)

	entry.drainInto(steerStubDelta(1))
	if envelope := nextEnvelope(t, sub); envelope.Type != protocol.TypeContentDelta {
		t.Fatalf("delivered %s, want the envelope the drain withheld after the lift", envelope.Type)
	}
}

func TestASettlementForTheGateRequestIsHeldWhateverRunItNames(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4)}
	entry := newSession("stub", "stub", stub, nil)
	sub := subscribeToRun(t, entry)

	gate := testGate("run-1", "req-steer")
	entry.mu.Lock()
	entry.gate = gate
	entry.mu.Unlock()

	settlement := steerStubEnvelope(protocol.TypeRunSteerApplied, 2, "sub-steer", "req-steer")
	settlement.RunID = "run-2"
	entry.publish(settlement)
	expectNoEnvelope(t, sub, "a settlement the gate's run does not match")

	entry.Published("req-steer")
	if envelope := nextEnvelope(t, sub); envelope.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("published %s, want the withheld settlement", envelope.Type)
	}
}

func TestASteerAdmissionRetargetsTheGateToTheAdmittedRun(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4), steerRun: "run-2"}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	subscribeToRun(t, entry)

	submit := steerSubmit("req-steer")
	submit.Request.TargetRunID = ""
	admission, err := entry.Submit(context.Background(), submit)
	if err != nil {
		t.Fatal(err)
	}
	if admission.RunID != "run-2" {
		t.Fatalf("admission run = %q, want the adapter's answered run", admission.RunID)
	}
	entry.mu.Lock()
	armed := entry.gate
	entry.mu.Unlock()
	if armed == nil || !armed.covers("run-2") {
		t.Fatalf("the gate is still armed on %+v, want the admitted run", armed)
	}
	entry.Published("req-steer")
}

func TestASteerSubmitDoesNotWaitForAnUnrelatedSaturatedStream(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 1), steerRun: "run-9"}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	subscribeToRun(t, entry)

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case stub.stream <- base.Result{Envelope: steerStubDelta(1)}:
			}
		}
	}()

	submit := steerSubmit("req-steer")
	submit.Request.TargetRunID = "run-9"
	started := time.Now()
	admission, err := entry.Submit(context.Background(), submit)
	if err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionSteered {
		t.Fatalf("admission = %+v", admission)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("a saturated stream stalled the steer submit for %s", elapsed)
	}
	entry.Published("req-steer")
}

func TestACancelledSteerSubmitDoesNotWaitForTheOtherGate(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4), steerRun: "run-1"}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	if _, err := entry.armGate(context.Background(), "run-1", "req-first"); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, err := entry.armGate(ctx, "run-1", "req-second")
		cancelled <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-cancelled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled steer armed the gate: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled steer waited on the gate condition")
	}
	entry.Published("req-first")
}

func TestADrainMovesTheWholeBacklogIntoTheGate(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4)}
	entry := newSession("stub", "stub", stub, nil)
	sub := subscribeToRun(t, entry)

	gate := testGate("run-1", "req-steer")
	gate.boundary = 2
	entry.mu.Lock()
	entry.gate = gate
	entry.mu.Unlock()

	backlog := make(chan base.Result, 4)
	backlog <- base.Result{Envelope: steerStubDelta(1)}
	backlog <- base.Result{Envelope: steerStubDelta(2)}
	backlog <- base.Result{Envelope: steerStubDelta(3)}
	close(backlog)

	if _, ready, ok := entry.drainGate(gate, backlog); ok || ready {
		t.Fatal("a drain of a closed backlog reported a result the reader still owes")
	}
	entry.mu.Lock()
	withheld := len(entry.gate.withheld)
	entry.mu.Unlock()
	if withheld != 3 {
		t.Fatalf("the drain withheld %d of the 3 queued envelopes, so the one behind the first waited for the release", withheld)
	}

	entry.releaseToBoundary(gate, 2)
	for want := uint64(1); want <= 2; want++ {
		envelope := nextEnvelope(t, sub)
		if envelope.Type != protocol.TypeContentDelta || envelope.Sequence == nil || *envelope.Sequence != want {
			t.Fatalf("the boundary released %s at %v, want delta %d", envelope.Type, envelope.Sequence, want)
		}
	}
	expectNoEnvelope(t, sub, "a delta behind the boundary")

	entry.Published("req-steer")
	envelope := nextEnvelope(t, sub)
	if envelope.Sequence == nil || *envelope.Sequence != 3 {
		t.Fatalf("the release published %s at %v, want delta 3", envelope.Type, envelope.Sequence)
	}
}

func TestOnlyTheGatesRunReaderTakesItsDrain(t *testing.T) {
	stub := &steerStubSession{stream: make(chan base.Result, 4)}
	entry := newSession("stub", "stub", stub, nil)

	gate := testGate("run-1", "req-steer")
	entry.mu.Lock()
	entry.gate = gate
	entry.mu.Unlock()
	gate.markDrainRequested()

	if taken := entry.drainingGate("run-2"); taken != nil {
		t.Fatal("a reader of another run took the drain")
	}
	if taken := entry.drainingGate("run-1"); taken != gate {
		t.Fatal("the reader of the gate's run did not take the drain")
	}
	if signal := entry.drainSignal("run-2"); signal == closedSignal {
		t.Fatal("a reader of another run was handed a closed drain signal")
	}
	entry.Published("req-steer")
}

func TestAwaitingADrainEndsWhenTheGateIsDisowned(t *testing.T) {
	disown := map[string]func(entry *Session, gate *publicationGate){
		"a concurrent close": func(entry *Session, _ *publicationGate) { entry.markClosed() },
		"the deadline":       func(entry *Session, gate *publicationGate) { entry.liftGate(gate) },
	}
	for name, drop := range disown {
		t.Run(name, func(t *testing.T) {
			stub := &steerStubSession{stream: make(chan base.Result, 4)}
			entry := newSession("stub", "stub", stub, nil)
			startStubRun(t, entry)
			subscribeToRun(t, entry)

			gate := testGate("run-1", "req-steer")
			entry.mu.Lock()
			entry.gate = gate
			entry.mu.Unlock()
			gate.markDrainRequested()

			waited := make(chan struct{})
			go func() {
				entry.awaitDrain(gate, 1)
				close(waited)
			}()
			time.Sleep(50 * time.Millisecond)
			drop(entry, gate)

			select {
			case <-waited:
			case <-time.After(2 * time.Second):
				t.Fatal("the submit waited on a drain no reader could serve once the gate was disowned")
			}
		})
	}
}

func TestASteerRefusalKeepsHoldingTheArmedRun(t *testing.T) {
	stub := &steerStubSession{
		stream:  make(chan base.Result, 4),
		emits:   []protocol.Envelope{steerStubEnvelope(protocol.TypeRunSteerDropped, 2, "sub-steer", "req-steer")},
		refusal: &base.InvalidSteerTargetError{RunID: "run-9", Reason: base.SteerReasonUnknownTarget},
	}
	entry := newSession("stub", "stub", stub, nil)
	startStubRun(t, entry)
	sub := subscribeToRun(t, entry)

	if _, err := entry.Submit(context.Background(), steerSubmit("req-steer")); err == nil {
		t.Fatal("a refused steer was admitted")
	}
	expectNoEnvelope(t, sub, "the refused steer's withheld settlement")

	entry.publish(steerStubDelta(3))
	expectNoEnvelope(t, sub, "an envelope of the run the gate armed on before its retarget")

	entry.Published("req-steer")
	if envelope := nextEnvelope(t, sub); envelope.Type != protocol.TypeRunSteerDropped {
		t.Fatalf("published %s, want the withheld settlement", envelope.Type)
	}
	if envelope := nextEnvelope(t, sub); envelope.Sequence == nil || *envelope.Sequence != 3 {
		t.Fatalf("published %s at %v, want the armed run's delta 3", envelope.Type, envelope.Sequence)
	}
}
