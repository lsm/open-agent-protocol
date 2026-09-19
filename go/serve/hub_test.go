package serve_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

const testTimeout = 10 * time.Second

type manualAdapter struct {
	mu       sync.Mutex
	session  *manualSession
	stateErr error
}

func (a *manualAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities:       protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "reference.manual"}},
		CapabilityRevision: "manual-v1",
	}, nil
}

func (a *manualAdapter) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	session := &manualSession{id: request.SessionID, stateErr: a.stateErr}
	a.mu.Lock()
	a.session = session
	a.mu.Unlock()
	return session, nil
}

func (a *manualAdapter) active(t *testing.T) *manualSession {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.session == nil {
		t.Fatal("manual adapter opened no session yet")
	}
	return a.session
}

type manualSession struct {
	id             protocol.SessionID
	mu             sync.Mutex
	runs           int
	stream         chan base.Result
	active         protocol.RunID
	closed         bool
	replayOverflow bool
	stateErr       error
}

var _ base.Session = (*manualSession)(nil)

func (s *manualSession) Submit(_ context.Context, _ protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
	}
	if s.stream != nil {
		return protocol.MessageSubmitResponse{}, nil, base.ErrRunActive
	}
	s.runs++
	s.active = protocol.RunID(fmt.Sprintf("manual-run-%d", s.runs))
	s.stream = make(chan base.Result, 64)
	return protocol.MessageSubmitResponse{SessionID: s.id, RunID: s.active, Accepted: true}, s.stream, nil
}

func (s *manualSession) State(context.Context) (protocol.SessionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stateErr != nil {
		return protocol.SessionState{}, s.stateErr
	}
	state := protocol.SessionState{SessionID: s.id, Status: protocol.SessionIdle}
	if s.stream != nil {
		state.Status = protocol.SessionRunning
		state.ActiveRunID = s.active
	}
	if s.closed {
		state.Status = protocol.SessionClosed
	}
	return state, nil
}

func (s *manualSession) Resolve(context.Context, base.InteractionResolution) error { return nil }

func (s *manualSession) Cancel(_ context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	s.endRun()
	return protocol.RunCancelResponse{SessionID: s.id, RunID: runID, Accepted: true, Status: protocol.RunCancelling}, nil
}

func (s *manualSession) Resume(_ context.Context, _ base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.replayOverflow {
		return base.Recovery{}, nil, base.ErrRunNotFound
	}
	out := make(chan base.Result, 1)
	out <- base.Result{Error: base.ErrEventStreamOverflow}
	close(out)
	return base.Recovery{}, out, nil
}

func (s *manualSession) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		return base.ErrRunActive
	}
	s.closed = true
	return nil
}

func (s *manualSession) emit(t *testing.T, sequence uint64) {
	t.Helper()
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStatusUpdated, protocol.EnvelopeID(fmt.Sprintf("manual-%s-%d", s.id, sequence)), protocol.RunStatusUpdatedPayload{})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		t.Fatal("emit without an active run")
	}
	envelope.SessionID = s.id
	envelope.RunID = s.active
	envelope.Sequence = &sequence
	s.stream <- base.Result{Envelope: envelope}
}

func (s *manualSession) fail(t *testing.T, err error) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream == nil {
		t.Fatal("fail without an active run")
	}
	s.stream <- base.Result{Error: err}
}

func (s *manualSession) endRun() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		close(s.stream)
		s.stream = nil
	}
	s.active = ""
}

func memoryHub(t *testing.T, capacity int) *serve.Hub {
	t.Helper()
	registry := serve.NewRegistry()
	if err := registry.Register("memory", base.NewMemory(base.Config{JournalCapacity: capacity})); err != nil {
		t.Fatal(err)
	}
	return serve.New(registry, serve.Options{})
}

func openMemorySession(t *testing.T, hub *serve.Hub, id string) *serve.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	session, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: protocol.SessionID(id)})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func nextUntil(t *testing.T, subscription *serve.Subscription, stop func(protocol.Envelope) bool) []protocol.Envelope {
	t.Helper()
	var seen []protocol.Envelope
	for {
		envelope, err := subscription.Next()
		if err != nil {
			t.Fatalf("subscription error after %d envelopes: %v", len(seen), err)
		}
		seen = append(seen, envelope)
		if stop(envelope) {
			return seen
		}
	}
}

func typeStop(typ protocol.EnvelopeType) func(protocol.Envelope) bool {
	return func(envelope protocol.Envelope) bool { return envelope.Type == typ }
}

func envelopeOfType(t *testing.T, envelopes []protocol.Envelope, typ protocol.EnvelopeType) protocol.Envelope {
	t.Helper()
	for _, envelope := range envelopes {
		if envelope.Type == typ {
			return envelope
		}
	}
	t.Fatalf("no %s in %d envelopes", typ, len(envelopes))
	return protocol.Envelope{}
}

func resolveGate(t *testing.T, session *serve.Session, requested protocol.Envelope) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	switch requested.Type {
	case protocol.TypeActionPermissionRequested:
		var permission protocol.PermissionRequestedPayload
		if err := requested.DecodePayload(&permission); err != nil {
			t.Fatal(err)
		}
		err := session.Resolve(ctx, base.InteractionResolution{
			RunID: requested.RunID, RespondedBy: permission.RespondedBy,
			Permission: &protocol.PermissionResolveRequest{
				InteractionID: permission.InteractionID, RequestedBy: permission.RequestedBy,
				RespondedBy: permission.RespondedBy, SessionID: requested.SessionID, RunID: requested.RunID,
				ChoiceID: "approve", Granted: true,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	case protocol.TypeUserInputRequested:
		var input protocol.UserInputRequestedPayload
		if err := requested.DecodePayload(&input); err != nil {
			t.Fatal(err)
		}
		err := session.Resolve(ctx, base.InteractionResolution{
			RunID: requested.RunID, RespondedBy: input.RespondedBy,
			Input: &protocol.UserInputResolveRequest{
				InteractionID: input.InteractionID, RequestedBy: input.RequestedBy,
				RespondedBy: input.RespondedBy, SessionID: requested.SessionID, RunID: requested.RunID,
				Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("cannot resolve %s", requested.Type)
	}
}

func submitGolden(t *testing.T, session *serve.Session, prompt string) protocol.RunID {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	admission, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: session.ID(),
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(prompt)}},
		Delivery:  protocol.DeliveryAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !admission.Accepted || admission.RunID == "" {
		t.Fatalf("admission not accepted: %+v", admission)
	}
	return admission.RunID
}

func driveMemoryRun(t *testing.T, session *serve.Session, subscription *serve.Subscription) protocol.RunID {
	t.Helper()
	runID := submitGolden(t, session, "run the golden script")
	initial := nextUntil(t, subscription, typeStop(protocol.TypeActionPermissionRequested))
	resolveGate(t, session, initial[len(initial)-1])
	middle := nextUntil(t, subscription, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
	nextUntil(t, subscription, typeStop(protocol.TypeRunCompleted))
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}
	return runID
}

func TestHubOpenRejections(t *testing.T) {
	hub := memoryHub(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	if _, _, err := hub.Open(ctx, "ghost", base.OpenRequest{}); !errors.Is(err, serve.ErrUnknownAdapter) {
		t.Fatalf("unknown adapter error %v", err)
	}
	if _, err := hub.Session("ghost"); !errors.Is(err, serve.ErrUnknownSession) {
		t.Fatalf("unknown session error %v", err)
	}
	if _, err := hub.Subscribe(ctx, "ghost"); !errors.Is(err, serve.ErrUnknownSession) {
		t.Fatalf("subscribe unknown session error %v", err)
	}

	session := openMemorySession(t, hub, "dup")
	if _, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "dup"}); !errors.Is(err, serve.ErrSessionExists) {
		t.Fatalf("duplicate open error %v", err)
	}

	if session.ID() != "dup" {
		t.Fatalf("tracked session id %q", session.ID())
	}

	_, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "other",
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery:  protocol.DeliveryAuto,
	})
	if !errors.Is(err, serve.ErrScopeMismatch) {
		t.Fatalf("scope mismatch error %v", err)
	}

	openMemorySession(t, hub, "no-run")
	if _, err := hub.Subscribe(ctx, "no-run", serve.After("", 0)); !errors.Is(err, serve.ErrNoRunToResume) {
		t.Fatalf("no-run resume error %v", err)
	}
}

func TestHubOpenDefaultsParticipant(t *testing.T) {
	hub := memoryHub(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: "default-participant"})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.Subscribe(ctx, session.ID())
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	submitGolden(t, session, "whose participant am I")
	initial := nextUntil(t, subscription, typeStop(protocol.TypeActionPermissionRequested))
	var requested protocol.PermissionRequestedPayload
	if err := initial[len(initial)-1].DecodePayload(&requested); err != nil {
		t.Fatal(err)
	}
	if requested.RespondedBy != serve.DefaultParticipant {
		t.Fatalf("gate responder %q, want %q", requested.RespondedBy, serve.DefaultParticipant)
	}
}

func TestHubOpenClosesSessionWhenStateFails(t *testing.T) {
	stateFailure := errors.New("state probe failed")
	manual := &manualAdapter{stateErr: stateFailure}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	_, _, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "state-fail"})
	if !errors.Is(err, stateFailure) {
		t.Fatalf("open error %v, want the adapter state failure", err)
	}
	active := manual.active(t)
	active.mu.Lock()
	closed := active.closed
	active.mu.Unlock()
	if !closed {
		t.Fatal("unconfirmed adapter session was left open")
	}
	if got := len(hub.Sessions(ctx)); got != 0 {
		t.Fatalf("listing has %d entries, want 0", got)
	}
}

func TestHubOpenMarksClosedOnClosedConfirmation(t *testing.T) {
	gated := &manualClosedAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", gated); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, state, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "already-closed"})
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionClosed {
		t.Fatalf("confirmation state %+v, want the final closed state", state)
	}
	if !session.IsClosed() {
		t.Fatal("the entry did not record the closed confirmation")
	}
	if _, err := hub.Subscribe(ctx, session.ID()); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("subscribe error %v, want the session-closed refusal", err)
	}
}

type manualClosedAdapter struct{}

func (manualClosedAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities:       protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "reference.manual"}},
		CapabilityRevision: "manual-v1",
	}, nil
}

func (manualClosedAdapter) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	return &closedStateSession{id: request.SessionID}, nil
}

type closedStateSession struct {
	id protocol.SessionID
}

func (s *closedStateSession) Submit(context.Context, protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	return protocol.MessageSubmitResponse{}, nil, base.ErrSessionClosed
}

func (s *closedStateSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: s.id, Status: protocol.SessionClosed}, base.ErrSessionClosed
}

func (s *closedStateSession) Resolve(context.Context, base.InteractionResolution) error {
	return base.ErrSessionClosed
}

func (s *closedStateSession) Cancel(_ context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{}, base.ErrSessionClosed
}

func (s *closedStateSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, base.ErrSessionClosed
}

func (s *closedStateSession) Close(context.Context) error { return base.ErrSessionClosed }

var _ base.Session = (*closedStateSession)(nil)

func TestHubFansOutToSubscribers(t *testing.T) {
	manual := &manualAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, _, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "fan-out"})
	if err != nil {
		t.Fatal(err)
	}
	const subscribers = 4
	subscriptions := make([]*serve.Subscription, subscribers)
	for index := range subscriptions {
		subscription, err := hub.Subscribe(ctx, "fan-out")
		if err != nil {
			t.Fatal(err)
		}
		subscriptions[index] = subscription
	}
	runID := submitGolden(t, session, "fan out")
	active := manual.active(t)
	for sequence := uint64(1); sequence <= 3; sequence++ {
		active.emit(t, sequence)
	}
	active.endRun()

	for index, subscription := range subscriptions {
		for sequence := uint64(1); sequence <= 3; sequence++ {
			envelope, err := subscription.Next()
			if err != nil {
				t.Fatalf("subscriber %d: error at sequence %d: %v", index, sequence, err)
			}
			if envelope.RunID != runID || envelope.Sequence == nil || *envelope.Sequence != sequence {
				t.Fatalf("subscriber %d envelope: run %s sequence %v, want %s at %d", index, envelope.RunID, envelope.Sequence, runID, sequence)
			}
		}
		if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("subscriber %d post-run error %v, want io.EOF", index, err)
		}
		subscription.Close()
	}
}

func TestHubSubscriptionQueueOverflow(t *testing.T) {
	manual := &manualAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 1})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, _, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "overflow"})
	if err != nil {
		t.Fatal(err)
	}

	slow, err := hub.Subscribe(ctx, "overflow")
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	fast, err := hub.Subscribe(ctx, "overflow")
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	runID := submitGolden(t, session, "fall behind")
	active := manual.active(t)
	for sequence := uint64(1); sequence <= 3; sequence++ {
		active.emit(t, sequence)
		envelope, err := fast.Next()
		if err != nil || envelope.Sequence == nil || *envelope.Sequence != sequence {
			t.Fatalf("fast envelope %d: sequence %v error %v", sequence, envelope.Sequence, err)
		}
	}
	active.endRun()
	if _, err := fast.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("fast post-run error %v, want io.EOF", err)
	}

	first, err := slow.Next()
	if err != nil || first.Sequence == nil || *first.Sequence != 1 {
		t.Fatalf("first envelope sequence %v error %v", first.Sequence, err)
	}
	_, err = slow.Next()
	var overflow *serve.OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.LastSequence != 1 || overflow.RunID != runID {
		t.Fatalf("overflow cursor %+v, want sequence 1 run %s", overflow, runID)
	}

	if _, err := slow.Next(); !errors.Is(err, overflow) && err.Error() != overflow.Error() {
		t.Fatalf("repeated Next error %v, want the sticky overflow", err)
	}
}

func TestHubAdapterStreamOverflow(t *testing.T) {
	manual := &manualAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, _, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "adapter-overflow"})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.Subscribe(ctx, "adapter-overflow")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	runID := submitGolden(t, session, "the adapter falls behind")
	active := manual.active(t)
	active.emit(t, 1)
	active.fail(t, base.ErrEventStreamOverflow)

	first, err := subscription.Next()
	if err != nil || first.Sequence == nil || *first.Sequence != 1 {
		t.Fatalf("first envelope %+v error %v", first, err)
	}
	_, err = subscription.Next()
	var overflow *serve.OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.LastSequence != 1 || overflow.RunID != runID {
		t.Fatalf("overflow cursor %+v, want sequence 1 run %s", overflow, runID)
	}
}

func TestHubSubscriptionContextCancel(t *testing.T) {
	hub := memoryHub(t, 0)
	openMemorySession(t, hub, "cancel-subscription")
	streamCtx, stopStream := context.WithCancel(context.Background())
	subscription, err := hub.Subscribe(streamCtx, "cancel-subscription")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := subscription.Next()
		done <- err
	}()
	stopStream()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("parked Next error %v, want context.Canceled", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("parked Next survived its context")
	}

	if _, err := subscription.Next(); !errors.Is(err, context.Canceled) {
		t.Fatalf("repeated Next error %v", err)
	}
}

func TestHubLiveStreamErrorSurfaces(t *testing.T) {
	manual := &manualAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, _, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "stream-error"})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := hub.Subscribe(ctx, "stream-error")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	submitGolden(t, session, "fail mid-run")
	active := manual.active(t)
	streamFailure := errors.New("adapter stream died")
	active.emit(t, 1)
	active.emit(t, 2)
	active.fail(t, streamFailure)
	active.endRun()

	for sequence := uint64(1); sequence <= 2; sequence++ {
		envelope, err := subscription.Next()
		if err != nil || envelope.Sequence == nil || *envelope.Sequence != sequence {
			t.Fatalf("envelope %d: sequence %v error %v", sequence, envelope.Sequence, err)
		}
	}
	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("terminal error %v (%T), want the adapter stream error", err, err)
	}

	if _, err := subscription.Next(); !errors.Is(err, streamFailure) {
		t.Fatalf("repeated Next error %v, want the sticky stream error", err)
	}
}

func TestHubResumeFromCursorMidStream(t *testing.T) {
	hub := memoryHub(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, hub, "resume")
	live, err := hub.Subscribe(ctx, "resume")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	runID := submitGolden(t, session, "resume me mid-run")
	initial := nextUntil(t, live, typeStop(protocol.TypeActionPermissionRequested))
	if len(initial) != 4 {
		t.Fatalf("initial burst %d envelopes, want 4", len(initial))
	}

	resumed, err := hub.Subscribe(ctx, "resume", serve.After(runID, 2))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()

	resolveGate(t, session, initial[len(initial)-1])
	middle := nextUntil(t, live, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))

	seen := nextUntil(t, resumed, typeStop(protocol.TypeRunCompleted))
	for offset, envelope := range seen {
		want := uint64(3 + offset)
		if envelope.RunID != runID || envelope.Sequence == nil || *envelope.Sequence != want {
			t.Fatalf("resumed envelope %d: run %s sequence %v, want %s at %d", offset, envelope.RunID, envelope.Sequence, runID, want)
		}
	}
	if want := 12 - 3 + 1; len(seen) != want {
		t.Fatalf("resumed stream delivered %d envelopes, want %d", len(seen), want)
	}
	if _, err := resumed.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}
}

func TestHubReplayGap(t *testing.T) {

	hub := memoryHub(t, 2)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, hub, "gap")
	subscription, err := hub.Subscribe(ctx, "gap")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	runID := driveMemoryRun(t, session, subscription)

	_, err = hub.Subscribe(ctx, "gap", serve.After(runID, 1))
	var gap *base.ReplayGap
	if !errors.As(err, &gap) {
		t.Fatalf("error %v (%T), want *adapter.ReplayGap", err, err)
	}
	if gap.RequestedAfter != 1 || gap.OldestAvailable != 11 || gap.LatestAvailable != 12 {
		t.Fatalf("gap %+v, want after 1 retained 11..12", gap)
	}

	recovered, err := hub.Subscribe(ctx, "gap", serve.After(runID, 10))
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	replayed := nextUntil(t, recovered, typeStop(protocol.TypeRunCompleted))
	if len(replayed) != 2 {
		t.Fatalf("retained replay %d envelopes, want 2", len(replayed))
	}
	for offset, envelope := range replayed {
		if envelope.Sequence == nil || *envelope.Sequence != uint64(11+offset) {
			t.Fatalf("retained envelope %d sequence %v", offset, envelope.Sequence)
		}
	}
}

func TestHubCloseReplaySubscriptionEndsPromptly(t *testing.T) {
	hub := memoryHub(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, hub, "close-replay")
	live, err := hub.Subscribe(ctx, "close-replay")
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	runID := submitGolden(t, session, "park at the gate")
	initial := nextUntil(t, live, typeStop(protocol.TypeActionPermissionRequested))

	resumed, err := hub.Subscribe(ctx, "close-replay", serve.After(runID, 2))
	if err != nil {
		t.Fatal(err)
	}
	replayed := nextUntil(t, resumed, typeStop(protocol.TypeActionPermissionRequested))
	if len(replayed) != 2 {
		t.Fatalf("replayed suffix %d envelopes, want 2", len(replayed))
	}
	resumed.Close()

	done := make(chan error, 1)
	go func() {
		_, err := resumed.Next()
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("post-Close Next error %v, want io.EOF", err)
		}
	case <-time.After(testTimeout):
		t.Fatal("post-Close Next did not end while the run was parked")
	}

	resumed.Close()
	resolveGate(t, session, initial[len(initial)-1])
	middle := nextUntil(t, live, typeStop(protocol.TypeRunStatusUpdated))
	resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
	nextUntil(t, live, typeStop(protocol.TypeRunCompleted))
}

func TestHubCursorResumeBindsRun(t *testing.T) {
	hub := memoryHub(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, hub, "bind")
	first, err := hub.Subscribe(ctx, "bind")
	if err != nil {
		t.Fatal(err)
	}
	runA := driveMemoryRun(t, session, first)
	first.Close()
	second, err := hub.Subscribe(ctx, "bind")
	if err != nil {
		t.Fatal(err)
	}
	runB := driveMemoryRun(t, session, second)
	if runA == runB {
		t.Fatal("the scripted runs share a run id")
	}

	bound, err := hub.Subscribe(ctx, "bind", serve.After(runA, 9))
	if err != nil {
		t.Fatal(err)
	}
	defer bound.Close()
	replayed := nextUntil(t, bound, typeStop(protocol.TypeRunCompleted))
	if len(replayed) != 3 {
		t.Fatalf("bound replay %d envelopes, want 3", len(replayed))
	}
	for offset, envelope := range replayed {
		if envelope.RunID != runA || envelope.Sequence == nil || *envelope.Sequence != uint64(10+offset) {
			t.Fatalf("bound envelope %d: run %s sequence %v, want %s at %d", offset, envelope.RunID, envelope.Sequence, runA, 10+offset)
		}
	}

	current, err := hub.Subscribe(ctx, "bind", serve.After("", 9))
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	resolved := nextUntil(t, current, typeStop(protocol.TypeRunCompleted))
	if len(resolved) != 3 {
		t.Fatalf("current-run replay %d envelopes, want 3", len(resolved))
	}
	for offset, envelope := range resolved {
		if envelope.RunID != runB || envelope.Sequence == nil || *envelope.Sequence != uint64(10+offset) {
			t.Fatalf("current-run envelope %d: run %s sequence %v, want %s at %d", offset, envelope.RunID, envelope.Sequence, runB, 10+offset)
		}
	}
}

func TestHubOverflowSignalNamesOverflowedRun(t *testing.T) {
	manual := &manualAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 1})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, _, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "late-overflow"})
	if err != nil {
		t.Fatal(err)
	}
	slow, err := hub.Subscribe(ctx, "late-overflow")
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	witness, err := hub.Subscribe(ctx, "late-overflow")
	if err != nil {
		t.Fatal(err)
	}
	defer witness.Close()
	active := manual.active(t)

	runA := submitGolden(t, session, "overflow on run A")
	active.emit(t, 1)
	if _, err := witness.Next(); err != nil {
		t.Fatalf("witness envelope 1: %v", err)
	}
	active.emit(t, 2)
	if _, err := witness.Next(); err != nil {
		t.Fatalf("witness envelope 2: %v", err)
	}
	active.endRun()

	if _, err := witness.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("witness end error %v, want io.EOF", err)
	}

	runB := submitGolden(t, session, "run B intervenes")
	active.emit(t, 1)
	active.endRun()
	if runA == runB {
		t.Fatal("the manual adapter minted one run id for both runs")
	}

	first, err := slow.Next()
	if err != nil || first.Sequence == nil || *first.Sequence != 1 || first.RunID != runA {
		t.Fatalf("first envelope: run %s sequence %v error %v", first.RunID, first.Sequence, err)
	}
	_, err = slow.Next()
	var overflow *serve.OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != runA {
		t.Fatalf("overflow run %q, want the overflowed run %q (run B is current)", overflow.RunID, runA)
	}
	if overflow.LastSequence != 1 {
		t.Fatalf("overflow sequence %d, want 1", overflow.LastSequence)
	}
}

func TestHubReplayOverflowSeedsCursor(t *testing.T) {
	manual := &manualAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("manual", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session, _, err := hub.Open(ctx, "manual", base.OpenRequest{SessionID: "replay-overflow"})
	if err != nil {
		t.Fatal(err)
	}
	runID := submitGolden(t, session, "make a run to resume")
	manual.active(t).endRun()
	manual.active(t).mu.Lock()
	manual.active(t).replayOverflow = true
	manual.active(t).mu.Unlock()

	replayed, err := hub.Subscribe(ctx, "replay-overflow", serve.After(runID, 5))
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	_, err = replayed.Next()
	var overflow *serve.OverflowError
	if !errors.As(err, &overflow) {
		t.Fatalf("error %v (%T), want OverflowError", err, err)
	}
	if overflow.RunID != runID || overflow.LastSequence != 5 {
		t.Fatalf("overflow cursor %+v, want run %s sequence 5", overflow, runID)
	}
}

func TestHubSessionsListingAcrossAdapters(t *testing.T) {
	manual := &manualAdapter{}
	registry := serve.NewRegistry()
	if err := registry.Register("alpha", base.NewMemory(base.Config{})); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("beta", manual); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	listed, _, err := hub.Open(ctx, "alpha", base.OpenRequest{SessionID: "list-a"})
	if err != nil {
		t.Fatal(err)
	}
	running, _, err := hub.Open(ctx, "beta", base.OpenRequest{SessionID: "list-b"})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(hub.Sessions(ctx)); got != 2 {
		t.Fatalf("listing has %d entries, want 2", got)
	}

	subscription, err := hub.Subscribe(ctx, "list-b")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	runID := submitGolden(t, running, "list me running")
	assertListing := func(id, adapter string, status protocol.SessionStatus, activeRun protocol.RunID) {
		t.Helper()
		for _, entry := range hub.Sessions(ctx) {
			if entry.SessionID != protocol.SessionID(id) {
				continue
			}
			if entry.Adapter != adapter || entry.Status != status || entry.ActiveRunID != activeRun {
				t.Fatalf("session %s listed %+v, want %s/%s/%s", id, entry, adapter, status, activeRun)
			}
			if entry.CreatedAt.IsZero() {
				t.Fatalf("session %s listed without a creation time", id)
			}
			return
		}
		t.Fatalf("session %q missing from listing", id)
	}
	assertListing("list-a", "alpha", protocol.SessionIdle, "")
	assertListing("list-b", "beta", protocol.SessionRunning, runID)

	manual.active(t).emit(t, 1)
	manual.active(t).endRun()
	for {
		if _, err := subscription.Next(); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("subscription error: %v", err)
		}
	}
	assertListing("list-b", "beta", protocol.SessionIdle, "")
	if err := listed.Close(ctx); err != nil {
		t.Fatal(err)
	}
	assertListing("list-a", "alpha", protocol.SessionClosed, "")
}

func TestHubSessionCloseSemantics(t *testing.T) {
	hub := memoryHub(t, 0)
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()

	session := openMemorySession(t, hub, "close")
	subscription, err := hub.Subscribe(ctx, "close")
	if err != nil {
		t.Fatal(err)
	}
	runID := submitGolden(t, session, "refuse the close, then settle")
	initial := nextUntil(t, subscription, typeStop(protocol.TypeActionPermissionRequested))

	if err := session.Close(ctx); !errors.Is(err, base.ErrRunActive) {
		t.Fatalf("active close error %v", err)
	}

	if _, err := session.Cancel(ctx, runID); err != nil {
		t.Fatal(err)
	}
	settled := nextUntil(t, subscription, typeStop(protocol.TypeRunCancelled))
	for offset, envelope := range append(append([]protocol.Envelope{}, initial...), settled...) {
		if envelope.Sequence == nil || *envelope.Sequence != uint64(offset+1) {
			t.Fatalf("envelope %d (%s) sequence %v", offset, envelope.Type, envelope.Sequence)
		}
	}
	if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("post-terminal error %v, want io.EOF", err)
	}

	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !session.IsClosed() {
		t.Fatal("session did not record the close")
	}

	state, err := session.State(ctx)
	if !errors.Is(err, base.ErrSessionClosed) || state.Status != protocol.SessionClosed {
		t.Fatalf("closed state %+v error %v", state, err)
	}
	var listed bool
	for _, entry := range hub.Sessions(ctx) {
		if entry.SessionID == "close" && entry.Status == protocol.SessionClosed {
			listed = true
		}
	}
	if !listed {
		t.Fatal("closed session missing from listing")
	}

	if _, err := hub.Subscribe(ctx, "close"); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("subscribe on closed session error %v", err)
	}
	_, err = session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "close",
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery:  protocol.DeliveryAuto,
	})
	if !errors.Is(err, base.ErrSessionClosed) {
		t.Fatalf("submit on closed session error %v", err)
	}
}

func TestHubConcurrentSessions(t *testing.T) {
	hub := memoryHub(t, 0)
	const sessions = 4

	var wg sync.WaitGroup
	for index := range sessions {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
			defer cancel()
			id := protocol.SessionID(fmt.Sprintf("concurrent-%d", index))

			session, _, err := hub.Open(ctx, "memory", base.OpenRequest{SessionID: id})
			if err != nil {
				t.Errorf("session %d open: %v", index, err)
				return
			}
			subscription, err := hub.Subscribe(ctx, id)
			if err != nil {
				t.Errorf("session %d subscribe: %v", index, err)
				return
			}
			runID := submitGolden(t, session, fmt.Sprintf("run %d to completion", index))
			initial := nextUntil(t, subscription, typeStop(protocol.TypeActionPermissionRequested))
			resolveGate(t, session, initial[len(initial)-1])
			middle := nextUntil(t, subscription, typeStop(protocol.TypeRunStatusUpdated))
			resolveGate(t, session, envelopeOfType(t, middle, protocol.TypeUserInputRequested))
			final := nextUntil(t, subscription, typeStop(protocol.TypeRunCompleted))
			for offset, envelope := range append(append([]protocol.Envelope{}, initial...), append(middle, final...)...) {
				if envelope.RunID != runID || envelope.Sequence == nil || *envelope.Sequence != uint64(offset+1) {
					t.Errorf("session %d envelope %d: run %s sequence %v", index, offset, envelope.RunID, envelope.Sequence)
					return
				}
			}
			if _, err := subscription.Next(); !errors.Is(err, io.EOF) {
				t.Errorf("session %d post-terminal error %v", index, err)
				return
			}
			subscription.Close()
			if err := session.Close(ctx); err != nil {
				t.Errorf("session %d close: %v", index, err)
			}
		}(index)
	}
	wg.Wait()

	if got := len(hub.Sessions(context.Background())); got != sessions {
		t.Fatalf("listing has %d entries, want %d", got, sessions)
	}
}
