package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
)

type fixedClock struct {
	mu sync.Mutex
	n  int64
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return time.UnixMilli(c.n)
}

type fixedIDs struct {
	mu sync.Mutex
	n  int
}

func (g *fixedIDs) NewID(kind string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	return fmt.Sprintf("%s-%02d", kind, g.n)
}

func newTestSession(t *testing.T, capacity int) Session {
	t.Helper()
	memory := NewMemory(Config{Clock: &fixedClock{}, IDs: &fixedIDs{}, JournalCapacity: capacity})
	session, err := memory.Open(context.Background(), OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func submit(t *testing.T, session Session) (protocol.RunID, EventStream) {
	t.Helper()
	admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !admission.Accepted || admission.Admission != protocol.AdmissionStarted {
		t.Fatalf("unexpected admission: %+v", admission)
	}
	return admission.RunID, stream
}

func drainAvailable(stream EventStream) []protocol.Envelope {
	var result []protocol.Envelope
	for {
		select {
		case item, ok := <-stream:
			if !ok {
				return result
			}
			if item.Error != nil {
				panic(item.Error)
			}
			result = append(result, item.Envelope)
		default:
			return result
		}
	}
}

func TestGoldenScript(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	events := drainAvailable(stream)
	wantInitial := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeActionCallRequested, protocol.TypeActionPermissionRequested}
	assertTypesAndSequence(t, events, wantInitial, 1)

	var requested protocol.PermissionRequestedPayload
	if err := events[3].DecodePayload(&requested); err != nil {
		t.Fatal(err)
	}
	if requested.RequestedBy != "agent" || requested.RespondedBy != "user" {
		t.Fatalf("ownership omitted: %+v", requested)
	}
	if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, SessionID: "session-1", RunID: runID, RequestedBy: "agent", RespondedBy: "user", ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	events = drainAvailable(stream)
	wantApproval := []protocol.EnvelopeType{protocol.TypeActionPermissionResolved, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeUserInputRequested, protocol.TypeRunStatusUpdated}
	assertTypesAndSequence(t, events, wantApproval, 5)

	var status protocol.RunStatusUpdatedPayload
	if err := events[4].DecodePayload(&status); err != nil {
		t.Fatal(err)
	}
	if status.Status != protocol.RunWaitingForInput || status.PendingUserInputID == "" {
		t.Fatalf("unexpected waiting status: %+v", status)
	}
	var input protocol.UserInputRequestedPayload
	if err := events[3].DecodePayload(&input); err != nil {
		t.Fatal(err)
	}
	if input.RequestedBy != "agent" || input.RespondedBy != "user" {
		t.Fatalf("ownership omitted: %+v", input)
	}
	resolution := InteractionResolution{
		RunID:       runID,
		RespondedBy: "user",
		Input: &protocol.UserInputResolveRequest{
			InteractionID: input.InteractionID,
			RequestedBy:   "agent",
			RespondedBy:   "user",
			SessionID:     "session-1",
			RunID:         runID,
			Answers:       []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		},
	}
	if err := session.Resolve(context.Background(), resolution); err != nil {
		t.Fatal(err)
	}
	events = drainAvailable(stream)
	wantFinal := []protocol.EnvelopeType{protocol.TypeUserInputResolved, protocol.TypeContentDelta, protocol.TypeRunCompleted}
	assertTypesAndSequence(t, events, wantFinal, 10)
	terminals := 0
	for _, event := range append(append([]protocol.Envelope{}, wantEnvelopes(wantInitial)...), append(wantEnvelopes(wantApproval), wantEnvelopes(wantFinal)...)...) {
		if event.Type == protocol.TypeRunCompleted || event.Type == protocol.TypeRunFailed || event.Type == protocol.TypeRunCancelled {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("got %d terminals", terminals)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
		t.Fatalf("unexpected final state: %+v", state)
	}
}

// The reference adapter advertises a non-empty capability revision, so every
// envelope it publishes must repeat it. A consumer such as
// adaptertest.AssertRunEvents binds events to the descriptor snapshot and
// rejects a run whose envelopes omit the revision.
func TestEmittedEnvelopesCarryAdvertisedRevision(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	events := drainAvailable(stream)
	if len(events) == 0 {
		t.Fatal("no events emitted")
	}
	ack, err := session.Cancel(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted {
		t.Fatalf("cancel not accepted: %+v", ack)
	}
	events = append(events, drainAvailable(stream)...)
	for i, event := range events {
		if event.CapabilityRevision != CapabilityRevision {
			t.Fatalf("event %d (%s) capability revision: got %q want %q", i, event.Type, event.CapabilityRevision, CapabilityRevision)
		}
	}
}

// The participant is the recorded responder for every permission and
// user-input gate, so an empty identity must be refused rather than silently
// producing schema-invalid events with an empty responded_by.
func TestOpenRejectsEmptyParticipant(t *testing.T) {
	memory := NewMemory(Config{Clock: &fixedClock{}, IDs: &fixedIDs{}, JournalCapacity: 8})
	if _, err := memory.Open(context.Background(), OpenRequest{SessionID: "session-1"}); !errors.Is(err, ErrInvalidParticipant) {
		t.Fatalf("err=%v", err)
	}
}

func wantEnvelopes(types []protocol.EnvelopeType) []protocol.Envelope {
	out := make([]protocol.Envelope, len(types))
	for i, typ := range types {
		out[i].Type = typ
	}
	return out
}

func assertTypesAndSequence(t *testing.T, events []protocol.Envelope, want []protocol.EnvelopeType, first uint64) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(events), len(want), events)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Fatalf("event %d type %q, want %q", i, events[i].Type, want[i])
		}
		if events[i].Sequence == nil || *events[i].Sequence != first+uint64(i) {
			t.Fatalf("event %d sequence %v", i, events[i].Sequence)
		}
		if strings.HasPrefix(string(events[i].Type), "action.call.") || events[i].Type == protocol.TypeActionPermissionRequested {
			if events[i].ToolCallID == "" {
				t.Fatalf("event %d %s lacks tool_call_id correlation", i, events[i].Type)
			}
		}
	}
}

func TestCancelAndDuplicateCancel(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	_ = drainAvailable(stream)
	ack, err := session.Cancel(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.Status != protocol.RunCancelling {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	events := drainAvailable(stream)
	assertTypesAndSequence(t, events, []protocol.EnvelopeType{protocol.TypeRunStatusUpdated, protocol.TypeActionPermissionResolved, protocol.TypeActionCallCancelled, protocol.TypeRunCancelled}, 5)
	ack, err = session.Cancel(context.Background(), runID)
	if err != nil || !ack.Accepted || ack.Status != protocol.RunCancelled {
		t.Fatalf("duplicate cancel: %+v, %v", ack, err)
	}
}

func TestCompletedCancelReturnsTypedError(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	initial := drainAvailable(stream)
	var permission protocol.PermissionRequestedPayload
	_ = initial[3].DecodePayload(&permission)
	if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = drainAvailable(stream)
	_, err := session.Cancel(context.Background(), runID)
	if !errors.Is(err, ErrRunAlreadyTerminal) {
		t.Fatalf("got %v", err)
	}
	var terminal *RunTerminalError
	if !errors.As(err, &terminal) || terminal.Status != protocol.RunCompleted {
		t.Fatalf("unexpected typed error: %#v", err)
	}
}

func TestTerminalGuardUnderRace(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	initial := drainAvailable(stream)
	var permission protocol.PermissionRequestedPayload
	_ = initial[3].DecodePayload(&permission)
	if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}})
	}()
	go func() { defer wg.Done(); _, _ = session.Cancel(context.Background(), runID) }()
	wg.Wait()
	events := drainAvailable(stream)
	terminals := 0
	for _, event := range events {
		if event.Type == protocol.TypeRunCompleted || event.Type == protocol.TypeRunFailed || event.Type == protocol.TypeRunCancelled {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("got %d terminal events in %+v", terminals, events)
	}
}

func TestResumeRetainedSuffix(t *testing.T) {
	session := newTestSession(t, 64)
	runID, original := submit(t, session)
	_ = drainAvailable(original)
	recovery, replay, err := session.Resume(context.Background(), ResumeRequest{RunID: runID, AfterSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if recovery.RequestedAfter != 2 || recovery.ReplayedFrom != 3 || recovery.ReplayedThrough != 4 {
		t.Fatalf("bad replay bounds: %+v", recovery)
	}
	if recovery.ReplayGap != nil {
		t.Fatal(recovery.ReplayGap)
	}
	events := drainAvailable(replay)
	assertTypesAndSequence(t, events, []protocol.EnvelopeType{protocol.TypeActionCallRequested, protocol.TypeActionPermissionRequested}, 3)
}

func TestResumeGapReturnsAuthoritativeState(t *testing.T) {
	session := newTestSession(t, 2)
	runID, stream := submit(t, session)
	_ = drainAvailable(stream)
	recovery, replay, err := session.Resume(context.Background(), ResumeRequest{RunID: runID, AfterSequence: 0})
	var gap *ReplayGap
	if !errors.As(err, &gap) {
		t.Fatalf("got %v", err)
	}
	if recovery.RequestedAfter != 0 || recovery.ReplayedFrom != 0 || recovery.ReplayedThrough != 0 {
		t.Fatalf("gap claimed replay bounds: %+v", recovery)
	}
	if recovery.ReplayGap == nil || recovery.State.ActiveRunID != runID || recovery.State.Status != protocol.SessionRunning {
		t.Fatalf("bad recovery: %+v", recovery)
	}
	if events := drainAvailable(replay); len(events) != 0 {
		t.Fatalf("gap replayed events: %+v", events)
	}
}

func TestNoDeadlockWithSlowSubscriber(t *testing.T) {
	session := newTestSession(t, 64)
	runID, _ := submit(t, session) // deliberately never consume the original stream
	_, replay, err := session.Resume(context.Background(), ResumeRequest{RunID: runID, AfterSequence: 4})
	if err != nil {
		t.Fatal(err)
	}
	_ = replay
	done := make(chan struct{})
	go func() { _, _ = session.Cancel(context.Background(), runID); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel deadlocked on slow subscriber")
	}
}

func TestSubmitRejectsInvalidRequestBeforeAdmission(t *testing.T) {
	session := newTestSession(t, 64)
	for _, request := range []protocol.MessageSubmitRequest{
		{SessionID: "session-1", Delivery: protocol.DeliveryAuto},
		{Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}, Delivery: protocol.DeliveryAuto},
	} {
		if _, _, err := session.Submit(context.Background(), request); !errors.Is(err, ErrInvalidSubmission) {
			t.Fatalf("got %v, want ErrInvalidSubmission", err)
		}
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
		t.Fatalf("invalid submission changed state: %+v", state)
	}
}

// The deterministic memory script runs no model and Probe advertises no model
// selection, so a caller ModelID must be refused rather than echoed as the
// effective model on the state, run, and admission.
func TestSubmitRejectsUnappliedModelID(t *testing.T) {
	session := newTestSession(t, 64)
	request := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, ModelID: "another-model", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}}
	if _, _, err := session.Submit(context.Background(), request); !errors.Is(err, ErrUnsupportedInput) {
		t.Fatalf("got %v, want ErrUnsupportedInput", err)
	}
	state, err := session.State(context.Background())
	if err != nil || state.Status != protocol.SessionIdle || state.CurrentModelID != "" {
		t.Fatalf("model override reached state: %+v err=%v", state, err)
	}
}

// The fixed memory script reads no instructions, tool choice, or output schema,
// so accepting them would report results from behavior the caller never got.
func TestSubmitRejectsUnappliedControls(t *testing.T) {
	session := newTestSession(t, 64)
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}
	for name, request := range map[string]protocol.MessageSubmitRequest{
		"instructions":  {SessionID: "session-1", Delivery: protocol.DeliveryAuto, Instructions: "be terse", Messages: message},
		"tool choice":   {SessionID: "session-1", Delivery: protocol.DeliveryAuto, ToolChoice: json.RawMessage(`"none"`), Messages: message},
		"output schema": {SessionID: "session-1", Delivery: protocol.DeliveryAuto, OutputSchema: json.RawMessage(`{"type":"object"}`), Messages: message},
	} {
		if _, _, err := session.Submit(context.Background(), request); !errors.Is(err, ErrInvalidSubmission) {
			t.Fatalf("%s: got %v, want ErrInvalidSubmission", name, err)
		}
	}
}

// A permission resolution must preserve the stored ownership and select a choice
// the gate actually offered, with a grant value that agrees.
func TestResolveRejectsInconsistentPermission(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	initial := drainAvailable(stream)
	var permission protocol.PermissionRequestedPayload
	_ = initial[3].DecodePayload(&permission)
	valid := protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}
	for name, mutate := range map[string]func(*protocol.PermissionResolveRequest){
		"foreign nested requester": func(p *protocol.PermissionResolveRequest) { p.RequestedBy = "intruder" },
		"foreign nested responder": func(p *protocol.PermissionResolveRequest) { p.RespondedBy = "intruder" },
		"unoffered choice":         func(p *protocol.PermissionResolveRequest) { p.ChoiceID = "bogus" },
		"grant disagrees":          func(p *protocol.PermissionResolveRequest) { p.Granted = false },
	} {
		request := valid
		mutate(&request)
		if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &request}); !errors.Is(err, ErrInvalidResolution) {
			t.Fatalf("%s: got %v, want ErrInvalidResolution", name, err)
		}
	}
	if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &valid}); err != nil {
		t.Fatalf("offered resolution rejected: %v", err)
	}
	// The input stage enforces the same nested ownership and the offered answer.
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	validAnswer := []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}
	foreign := protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "intruder", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: validAnswer}
	if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Input: &foreign}); !errors.Is(err, ErrInvalidResolution) {
		t.Fatalf("foreign input ownership: got %v, want ErrInvalidResolution", err)
	}
	for name, answers := range map[string][]protocol.InputAnswer{
		"empty answers":    nil,
		"unoffered option": {{QuestionID: "choice", SelectedOptionIDs: []string{"no"}}},
		"foreign question": {{QuestionID: "other", SelectedOptionIDs: []string{"yes"}}},
	} {
		request := protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: answers}
		if err := session.Resolve(context.Background(), InteractionResolution{RunID: runID, RespondedBy: "user", Input: &request}); !errors.Is(err, ErrInvalidResolution) {
			t.Fatalf("%s: got %v, want ErrInvalidResolution", name, err)
		}
	}
}

func TestCloseRejectsActiveRun(t *testing.T) {
	session := newTestSession(t, 64)
	_, stream := submit(t, session)
	if err := session.Close(context.Background()); !errors.Is(err, ErrRunActive) {
		t.Fatalf("got %v, want ErrRunActive", err)
	}
	if events := drainAvailable(stream); len(events) == 0 {
		t.Fatal("active run stream was closed")
	}
}

func TestResumeRejectsFutureCursor(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	_ = drainAvailable(stream)
	if _, _, err := session.Resume(context.Background(), ResumeRequest{RunID: runID, AfterSequence: 99}); !errors.Is(err, ErrReplayCursorFuture) {
		t.Fatalf("got %v, want ErrReplayCursorFuture", err)
	}
}

func TestResumeReportsWhollyEvictedRun(t *testing.T) {
	session := newTestSession(t, 4)
	runA, streamA := submit(t, session)
	initial := drainAvailable(streamA)
	var permission protocol.PermissionRequestedPayload
	if err := initial[3].DecodePayload(&permission); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Cancel(context.Background(), runA); err != nil {
		t.Fatal(err)
	}
	_ = drainAvailable(streamA)

	runB, streamB := submit(t, session)
	_ = drainAvailable(streamB)
	_, _, err := session.Resume(context.Background(), ResumeRequest{RunID: runA, AfterSequence: 0})
	var gap *ReplayGap
	if !errors.As(err, &gap) {
		t.Fatalf("got %v, want replay gap after run %s evicted by %s", err, runA, runB)
	}
	if gap.OldestAvailable != 0 || gap.LatestAvailable == 0 {
		t.Fatalf("unexpected complete-eviction gap: %+v", gap)
	}
}

func TestDescriptorTruthful(t *testing.T) {
	descriptor, err := NewMemory(Config{JournalCapacity: 7}).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Journal.Persistence != "process_memory" || descriptor.Journal.Replay != protocol.SupportDegraded || descriptor.Journal.Capacity != 7 {
		t.Fatalf("journal: %+v", descriptor.Journal)
	}
	if descriptor.MaxActiveRunsPerSession != 1 || !descriptor.InteractiveGates || descriptor.CancellationTarget != "run" || descriptor.CancellationImplementation != "session_emulated" {
		t.Fatalf("descriptor: %+v", descriptor)
	}
}
