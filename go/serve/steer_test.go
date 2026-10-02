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
	refusal  *base.InvalidSteerTargetError
	steerRun protocol.RunID
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
		for _, envelope := range s.emits {
			s.stream <- base.Result{Envelope: envelope}
		}
		if s.refusal != nil {
			return protocol.MessageSubmitResponse{}, nil, s.refusal
		}
		boundary := uint64(1)
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
