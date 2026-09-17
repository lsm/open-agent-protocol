package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
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

func newTestSession(t *testing.T, capacity int) adapter.Session {
	t.Helper()
	memory := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}, JournalCapacity: capacity})
	session, err := memory.Open(context.Background(), adapter.OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func submit(t *testing.T, session adapter.Session) (protocol.RunID, adapter.EventStream) {
	t.Helper()
	admission, stream := submitAdmission(t, session)
	return admission.RunID, stream
}

func submitAdmission(t *testing.T, session adapter.Session) (protocol.MessageSubmitResponse, adapter.EventStream) {
	t.Helper()
	admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}})
	if err != nil {
		t.Fatal(err)
	}
	if !admission.Accepted || admission.Admission != protocol.AdmissionStarted {
		t.Fatalf("unexpected admission: %+v", admission)
	}
	return admission, stream
}

// testDescriptor probes the reference adapter's live capability descriptor so
// the shared protocol assertion can certify optional-feature envelopes.
func testDescriptor(t *testing.T) adapter.Descriptor {
	t.Helper()
	implementation := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}, JournalCapacity: 64})
	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

func drainAvailable(stream adapter.EventStream) []protocol.Envelope {
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
	admission, stream := submitAdmission(t, session)
	runID := admission.RunID
	events := drainAvailable(stream)
	wantInitial := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeContentDelta, protocol.TypeActionCallRequested, protocol.TypeActionPermissionRequested}
	assertTypesAndSequence(t, events, wantInitial, 1)
	trace := append([]protocol.Envelope(nil), events...)

	var requested protocol.PermissionRequestedPayload
	if err := events[3].DecodePayload(&requested); err != nil {
		t.Fatal(err)
	}
	if requested.RequestedBy != "reference.memory" || requested.RespondedBy != "user" {
		t.Fatalf("ownership omitted: %+v", requested)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, SessionID: "session-1", RunID: runID, RequestedBy: "reference.memory", RespondedBy: "user", ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	events = drainAvailable(stream)
	wantApproval := []protocol.EnvelopeType{protocol.TypeActionPermissionResolved, protocol.TypeActionCallStarted, protocol.TypeActionCallCompleted, protocol.TypeUserInputRequested, protocol.TypeRunStatusUpdated}
	assertTypesAndSequence(t, events, wantApproval, 5)
	trace = append(trace, events...)

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
	if input.RequestedBy != "reference.memory" || input.RespondedBy != "user" {
		t.Fatalf("ownership omitted: %+v", input)
	}
	resolution := adapter.InteractionResolution{
		RunID:       runID,
		RespondedBy: "user",
		Input: &protocol.UserInputResolveRequest{
			InteractionID: input.InteractionID,
			RequestedBy:   "reference.memory",
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
	trace = append(trace, events...)
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, testDescriptor(t), trace)
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
		if event.CapabilityRevision != adapter.CapabilityRevision {
			t.Fatalf("event %d (%s) capability revision: got %q want %q", i, event.Type, event.CapabilityRevision, adapter.CapabilityRevision)
		}
	}
}

// The participant is the recorded responder for every permission and
// user-input gate, so an empty identity must be refused rather than silently
// producing schema-invalid events with an empty responded_by.
func TestOpenRejectsEmptyParticipant(t *testing.T) {
	memory := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}, JournalCapacity: 8})
	if _, err := memory.Open(context.Background(), adapter.OpenRequest{SessionID: "session-1"}); !errors.Is(err, adapter.ErrInvalidParticipant) {
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
	admission, stream := submitAdmission(t, session)
	runID := admission.RunID
	initial := drainAvailable(stream)
	ack, err := session.Cancel(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Accepted || ack.Status != protocol.RunCancelling {
		t.Fatalf("unexpected ack: %+v", ack)
	}
	events := drainAvailable(stream)
	assertTypesAndSequence(t, events, []protocol.EnvelopeType{protocol.TypeRunStatusUpdated, protocol.TypeActionPermissionResolved, protocol.TypeActionCallCancelled, protocol.TypeRunCancelled}, 5)
	adaptertest.AssertProtocolValidWithCancellation(t, admission, testDescriptor(t), append(append([]protocol.Envelope(nil), initial...), events...))
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
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = drainAvailable(stream)
	_, err := session.Cancel(context.Background(), runID)
	if !errors.Is(err, adapter.ErrRunAlreadyTerminal) {
		t.Fatalf("got %v", err)
	}
	var terminal *adapter.RunTerminalError
	if !errors.As(err, &terminal) || terminal.Status != protocol.RunCompleted {
		t.Fatalf("unexpected typed error: %#v", err)
	}
}

func TestTerminalGuardUnderRace(t *testing.T) {
	session := newTestSession(t, 64)
	admission, stream := submitAdmission(t, session)
	runID := admission.RunID
	initial := drainAvailable(stream)
	var permission protocol.PermissionRequestedPayload
	_ = initial[3].DecodePayload(&permission)
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}})
	}()
	cancelAck := make(chan protocol.RunCancelResponse, 1)
	go func() {
		defer wg.Done()
		ack, _ := session.Cancel(context.Background(), runID)
		cancelAck <- ack
	}()
	wg.Wait()
	events := drainAvailable(stream)
	combined := append(append([]protocol.Envelope(nil), initial...), middle...)
	// The raced cancel may or may not win; its acknowledgement is the
	// caller-side evidence for the cancel exchange.
	if ack := <-cancelAck; ack.Accepted {
		adaptertest.AssertProtocolValidWithCancellation(t, admission, testDescriptor(t), append(combined, events...))
	} else {
		adaptertest.AssertProtocolValidWithDescriptor(t, admission, testDescriptor(t), append(combined, events...))
	}
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

// action.call.completed has additionalProperties:false and does not permit the
// request-only arguments_json, so the completion must not reuse the start
// payload's arguments.
func TestToolCompletionOmitsRequestOnlyArguments(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	events := drainAvailable(stream)
	var permission protocol.PermissionRequestedPayload
	for _, envelope := range events {
		if envelope.Type == protocol.TypeActionPermissionRequested {
			_ = envelope.DecodePayload(&permission)
		}
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	observed := false
	for _, envelope := range drainAvailable(stream) {
		if envelope.Type != protocol.TypeActionCallCompleted {
			continue
		}
		observed = true
		var completed protocol.ActionCallPayload
		if err := envelope.DecodePayload(&completed); err != nil {
			t.Fatal(err)
		}
		if completed.ArgumentsJSON != nil {
			t.Fatalf("completion carried arguments_json: %s", completed.ArgumentsJSON)
		}
	}
	if !observed {
		t.Fatal("no action.call.completed observed")
	}
}

// A user-input request carries the run's tool binding in its payload; the
// envelope must repeat it, or the validator rejects the trace as a
// scope_mismatch between the envelope and payload.
func TestUserInputRequestEnvelopeCarriesToolBinding(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	events := drainAvailable(stream)
	var permission protocol.PermissionRequestedPayload
	for _, envelope := range events {
		if envelope.Type == protocol.TypeActionPermissionRequested {
			_ = envelope.DecodePayload(&permission)
		}
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, envelope := range drainAvailable(stream) {
		if envelope.Type != protocol.TypeUserInputRequested {
			continue
		}
		found = true
		var input protocol.UserInputRequestedPayload
		if err := envelope.DecodePayload(&input); err != nil {
			t.Fatal(err)
		}
		if envelope.ToolCallID != input.ToolCallID || envelope.ToolCallID == "" {
			t.Fatalf("envelope tool_call_id = %q, payload = %q", envelope.ToolCallID, input.ToolCallID)
		}
	}
	if !found {
		t.Fatal("no user-input request observed")
	}
}

// A consumer that rewrites a received payload must not corrupt the retained
// replay journal.
func TestPublishedPayloadDoesNotAliasTheJournal(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	events := drainAvailable(stream)
	index := -1
	for i := range events {
		if events[i].Type == protocol.TypeActionPermissionRequested {
			index = i
			break
		}
	}
	if index == -1 {
		t.Fatal("no permission request event")
	}
	original := append([]byte(nil), events[index].Payload...)
	copy(events[index].Payload, []byte(`{"tampered":true}`))
	_, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runID, AfterSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	sequence := *events[index].Sequence
	for _, envelope := range drainAvailable(replay) {
		if envelope.Sequence != nil && *envelope.Sequence == sequence {
			if string(envelope.Payload) != string(original) {
				t.Fatalf("published payload aliased the journal: got %s want %s", envelope.Payload, original)
			}
			return
		}
	}
	t.Fatalf("sequence %d not replayed", sequence)
}

// A consumer that edits a replayed envelope must not corrupt retained history
// (payload slice, sequence, or timestamp storage).
func TestReplayedEnvelopeDoesNotAliasTheJournal(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	_ = drainAvailable(stream)
	_, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runID, AfterSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	replayed := drainAvailable(replay)
	if len(replayed) == 0 {
		t.Fatal("no replay")
	}
	originalPayload := append([]byte(nil), replayed[0].Payload...)
	originalSequence, originalTimestamp := *replayed[0].Sequence, *replayed[0].TimestampMS
	copy(replayed[0].Payload, []byte(`{"tampered":true}`))
	*replayed[0].Sequence = originalSequence + 100
	*replayed[0].TimestampMS = originalTimestamp + 100
	_, second, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runID, AfterSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	again := drainAvailable(second)
	if len(again) == 0 {
		t.Fatal("no second replay")
	}
	if string(again[0].Payload) != string(originalPayload) {
		t.Fatalf("payload aliased: got %s want %s", again[0].Payload, originalPayload)
	}
	if *again[0].Sequence != originalSequence || *again[0].TimestampMS != originalTimestamp {
		t.Fatalf("sequence/timestamp aliased: got %d/%d want %d/%d", *again[0].Sequence, *again[0].TimestampMS, originalSequence, originalTimestamp)
	}
}

func TestResumeRetainedSuffix(t *testing.T) {
	session := newTestSession(t, 64)
	runID, original := submit(t, session)
	_ = drainAvailable(original)
	recovery, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runID, AfterSequence: 2})
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
	recovery, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runID, AfterSequence: 0})
	var gap *adapter.ReplayGap
	if !errors.As(err, &gap) {
		t.Fatalf("got %v", err)
	}
	if recovery.RequestedAfter != 0 || recovery.ReplayedFrom != 0 || recovery.ReplayedThrough != 0 {
		t.Fatalf("gap claimed replay bounds: %+v", recovery)
	}
	// The script stops at the permission gate, so the authoritative state the
	// gap hands back is a session waiting on it — which is what the entry
	// beside it says too.
	if recovery.ReplayGap == nil || recovery.State.ActiveRunID != runID || recovery.State.Status != protocol.SessionWaitingForInput {
		t.Fatalf("bad recovery: %+v", recovery)
	}
	if events := drainAvailable(replay); len(events) != 0 {
		t.Fatalf("gap replayed events: %+v", events)
	}
}

func TestNoDeadlockWithSlowSubscriber(t *testing.T) {
	session := newTestSession(t, 64)
	runID, _ := submit(t, session) // deliberately never consume the original stream
	_, replay, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runID, AfterSequence: 4})
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
		if _, _, err := session.Submit(context.Background(), request); !errors.Is(err, adapter.ErrInvalidSubmission) {
			t.Fatalf("got %v, want adapter.ErrInvalidSubmission", err)
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

// A model id outside the advertised catalog is refused with the typed
// model_not_found, and an id inside it is authoritative for its run alone: the
// application is per_run, so current_model_id, the model the next control-free
// submission would use, must not move.
func TestSubmitAppliesModelPerRun(t *testing.T) {
	session := newTestSession(t, 64)
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}
	unknown := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue("another-model"), Messages: message}
	var notFound *adapter.ModelNotFoundError
	if _, _, err := session.Submit(context.Background(), unknown); !errors.As(err, &notFound) || !errors.Is(err, adapter.ErrModelNotFound) {
		t.Fatalf("unknown model: got %v, want adapter.ErrModelNotFound", err)
	}
	// An empty id is a control the endpoint must judge, not an absent one, and
	// no catalog can list it: it is a catalog miss like any other.
	empty := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue(""), Messages: message}
	if _, _, err := session.Submit(context.Background(), empty); !errors.As(err, &notFound) || notFound.ModelID != "" {
		t.Fatalf("empty model: got %v, want model_not_found naming the empty id", err)
	}
	state, err := session.State(context.Background())
	if err != nil || state.Status != protocol.SessionIdle || state.CurrentModelID != "" {
		t.Fatalf("refused model reached state: %+v err=%v", state, err)
	}

	admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue(adapter.ModelSecondary), Messages: message})
	if err != nil {
		t.Fatalf("admitted model refused: %v", err)
	}
	if admission.ModelID != adapter.ModelSecondary {
		t.Fatalf("admission model = %q, want %q", admission.ModelID, adapter.ModelSecondary)
	}
	events := drainAvailable(stream)
	var started protocol.RunStartedPayload
	_ = events[0].DecodePayload(&started)
	if started.ModelID != adapter.ModelSecondary {
		t.Fatalf("run.started model = %q, want %q", started.ModelID, adapter.ModelSecondary)
	}
	if state, err := session.State(context.Background()); err != nil || state.CurrentModelID != "" {
		t.Fatalf("per_run selection moved the session default: %+v err=%v", state, err)
	}
}

// Every control the reference adapter advertises is either applied or refused
// with a typed error before admission; none is accepted and ignored.
func TestSubmitJudgesEveryControl(t *testing.T) {
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}
	for name, testCase := range map[string]struct {
		request protocol.MessageSubmitRequest
		feature string
		tool    string
	}{
		"untyped tool choice": {
			request: protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`"none"`)},
			feature: protocol.FeatureToolSelection,
		},
		"unknown tool choice member": {
			request: protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`{"mode":"auto","limit":2}`)},
			feature: protocol.FeatureToolSelection,
		},
		"allowed and disallowed": {
			request: protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`{"mode":"auto","allowed":["scripted_tool"],"disallowed":["scripted_tool"]}`)},
			feature: protocol.FeatureToolSelection,
		},
		"tool outside the catalog": {
			request: protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`{"mode":"named","name":"absent_tool"}`)},
			feature: protocol.FeatureToolSelection,
			tool:    "absent_tool",
		},
		"required against an empty filtered set": {
			request: protocol.MessageSubmitRequest{ToolChoice: json.RawMessage(`{"mode":"required","disallowed":["scripted_tool"]}`)},
			feature: protocol.FeatureToolSelection,
		},
		"non-object output schema": {
			request: protocol.MessageSubmitRequest{OutputSchema: json.RawMessage(`{"type":"array"}`)},
			feature: protocol.FeatureStructuredOutput,
		},
		"external output schema reference": {
			request: protocol.MessageSubmitRequest{OutputSchema: json.RawMessage(`{"type":"object","properties":{"a":{"$ref":"https://example.test/s.json"}}}`)},
			feature: protocol.FeatureStructuredOutput,
		},
		"uncompilable output schema": {
			request: protocol.MessageSubmitRequest{OutputSchema: json.RawMessage(`{"type":"object","required":"x"}`)},
			feature: protocol.FeatureStructuredOutput,
		},
		"output schema the fixed result cannot satisfy": {
			request: protocol.MessageSubmitRequest{OutputSchema: json.RawMessage(`{"type":"object","required":["answer"]}`)},
			feature: protocol.FeatureStructuredOutput,
		},
	} {
		session := newTestSession(t, 64)
		request := testCase.request
		request.SessionID, request.Delivery, request.Messages = "session-1", protocol.DeliveryAuto, message
		_, _, err := session.Submit(context.Background(), request)
		var refusal *adapter.UnsupportedControlError
		if !errors.As(err, &refusal) || !errors.Is(err, adapter.ErrUnsupportedInput) {
			t.Fatalf("%s: got %v, want a typed unsupported-control refusal", name, err)
		}
		if refusal.Feature != testCase.feature || refusal.Reason != adapter.ControlUnsatisfiable {
			t.Fatalf("%s: refusal %+v, want feature %q reason %q", name, refusal, testCase.feature, adapter.ControlUnsatisfiable)
		}
		if testCase.tool != "" && refusal.Tool != testCase.tool {
			t.Fatalf("%s: refusal names tool %q, want %q", name, refusal.Tool, testCase.tool)
		}
		if state, err := session.State(context.Background()); err != nil || state.ActiveRunID != "" || state.Status != protocol.SessionIdle {
			t.Fatalf("%s: refused control allocated identity: %+v err=%v", name, state, err)
		}
	}
}

// An admitted tool_choice selects whether the scripted tool runs at all, and
// an admitted output_schema binds the completion to the disclosed fixed
// result.
func TestSubmitExecutesToolChoiceAndOutputSchema(t *testing.T) {
	for name, testCase := range map[string]struct {
		policy string
		calls  int
	}{
		"none":       {policy: `{"mode":"none"}`},
		"disallowed": {policy: `{"mode":"auto","disallowed":["scripted_tool"]}`},
		// An allowlist the caller sent empty permits no tool at all. Under
		// "auto" that is empty rather than unsatisfiable — the run simply
		// calls nothing — so the policy is admitted and then governs.
		"empty allowlist":     {policy: `{"mode":"auto","allowed":[]}`},
		"allowlist with tool": {policy: `{"mode":"auto","allowed":["scripted_tool"]}`, calls: 1},
	} {
		session := newTestSession(t, 64)
		_, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
			SessionID: "session-1", Delivery: protocol.DeliveryAuto,
			ToolChoice: json.RawMessage(testCase.policy),
			Messages:   []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		})
		if err != nil {
			t.Fatalf("%s: policy refused: %v", name, err)
		}
		events := drainAvailable(stream)
		calls := 0
		for _, event := range events {
			if event.Type == protocol.TypeActionCallRequested {
				calls++
			}
		}
		if calls != testCase.calls {
			t.Fatalf("%s: %d tool calls, want %d: %+v", name, calls, testCase.calls, events)
		}
	}

	// "required" over an allowlist the caller sent empty has an empty
	// filtered set, which no run can satisfy, so it is refused rather than
	// admitted and quietly run against the whole catalog.
	session0 := newTestSession(t, 64)
	_, _, err0 := session0.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		ToolChoice: json.RawMessage(`{"mode":"required","allowed":[]}`),
		Messages:   []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	})
	var emptyRequired *adapter.UnsupportedControlError
	if !errors.As(err0, &emptyRequired) || emptyRequired.Feature != protocol.FeatureToolSelection || emptyRequired.Reason != adapter.ControlUnsatisfiable {
		t.Fatalf("required over an empty allowlist: got %v, want an unsatisfiable tool_selection refusal", err0)
	}

	session := newTestSession(t, 64)
	admission, stream, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		ToolChoice:   json.RawMessage(`{"mode":"none"}`),
		OutputSchema: json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}}}`),
		Instructions: protocol.ControlValue("Be terse."),
		Messages:     []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	})
	if err != nil {
		t.Fatalf("structured submission refused: %v", err)
	}
	initial := drainAvailable(stream)
	var delta protocol.ContentDeltaPayload
	_ = initial[1].DecodePayload(&delta)
	if !strings.HasPrefix(delta.Part.Text, "Be terse.") {
		t.Fatalf("instructions were not applied to the scripted text: %q", delta.Part.Text)
	}
	var prompt protocol.UserInputRequestedPayload
	_ = initial[2].DecodePayload(&prompt)
	answer := protocol.UserInputResolveRequest{InteractionID: prompt.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &answer}); err != nil {
		t.Fatalf("resolve input: %v", err)
	}
	events := append(initial, drainAvailable(stream)...)
	completed := events[len(events)-1]
	if completed.Type != protocol.TypeRunCompleted {
		t.Fatalf("last event is %s", completed.Type)
	}
	var payload protocol.RunCompletedPayload
	_ = completed.DecodePayload(&payload)
	if string(payload.Result) != `{"ok":true}` {
		t.Fatalf("structured result = %s, want the disclosed fixed result", payload.Result)
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, testDescriptor(t), events)
}

// A permission resolution must preserve the stored ownership and select a choice
// the gate actually offered, with a grant value that agrees.
func TestResolveRejectsInconsistentPermission(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	initial := drainAvailable(stream)
	var permission protocol.PermissionRequestedPayload
	_ = initial[3].DecodePayload(&permission)
	valid := protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}
	for name, mutate := range map[string]func(*protocol.PermissionResolveRequest){
		"foreign nested requester": func(p *protocol.PermissionResolveRequest) { p.RequestedBy = "intruder" },
		"foreign nested responder": func(p *protocol.PermissionResolveRequest) { p.RespondedBy = "intruder" },
		"unoffered choice":         func(p *protocol.PermissionResolveRequest) { p.ChoiceID = "bogus" },
		"grant disagrees":          func(p *protocol.PermissionResolveRequest) { p.Granted = false },
	} {
		request := valid
		mutate(&request)
		if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &request}); !errors.Is(err, adapter.ErrInvalidResolution) {
			t.Fatalf("%s: got %v, want adapter.ErrInvalidResolution", name, err)
		}
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &valid}); err != nil {
		t.Fatalf("offered resolution rejected: %v", err)
	}
	// The input stage enforces the same nested ownership and the offered answer.
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	validAnswer := []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}
	foreign := protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "intruder", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: validAnswer}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Input: &foreign}); !errors.Is(err, adapter.ErrInvalidResolution) {
		t.Fatalf("foreign input ownership: got %v, want adapter.ErrInvalidResolution", err)
	}
	for name, answers := range map[string][]protocol.InputAnswer{
		"empty answers":    nil,
		"unoffered option": {{QuestionID: "choice", SelectedOptionIDs: []string{"no"}}},
		"mixed form":       {{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}, Text: "yes"}},
		"foreign question": {{QuestionID: "other", SelectedOptionIDs: []string{"yes"}}},
	} {
		request := protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: answers}
		if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Input: &request}); !errors.Is(err, adapter.ErrInvalidResolution) {
			t.Fatalf("%s: got %v, want adapter.ErrInvalidResolution", name, err)
		}
	}
}

func TestCloseRejectsActiveRun(t *testing.T) {
	session := newTestSession(t, 64)
	_, stream := submit(t, session)
	if err := session.Close(context.Background()); !errors.Is(err, adapter.ErrRunActive) {
		t.Fatalf("got %v, want adapter.ErrRunActive", err)
	}
	if events := drainAvailable(stream); len(events) == 0 {
		t.Fatal("active run stream was closed")
	}
}

func TestResumeRejectsFutureCursor(t *testing.T) {
	session := newTestSession(t, 64)
	runID, stream := submit(t, session)
	_ = drainAvailable(stream)
	if _, _, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runID, AfterSequence: 99}); !errors.Is(err, adapter.ErrReplayCursorFuture) {
		t.Fatalf("got %v, want adapter.ErrReplayCursorFuture", err)
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
	_, _, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: runA, AfterSequence: 0})
	var gap *adapter.ReplayGap
	if !errors.As(err, &gap) {
		t.Fatalf("got %v, want replay gap after run %s evicted by %s", err, runA, runB)
	}
	if gap.OldestAvailable != 0 || gap.LatestAvailable == 0 {
		t.Fatalf("unexpected complete-eviction gap: %+v", gap)
	}
}

func TestDescriptorTruthful(t *testing.T) {
	descriptor, err := adapter.NewMemory(adapter.Config{JournalCapacity: 7}).Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Journal.Persistence != "process_memory" || descriptor.Journal.Replay != protocol.SupportDegraded || descriptor.Journal.Capacity != 7 {
		t.Fatalf("journal: %+v", descriptor.Journal)
	}
	// One started run beside one reservation, and the wire projection of that
	// bound beside it: a descriptor advertising a queue with no reachable
	// bound promises nothing.
	if descriptor.MaxActiveRunsPerSession != 2 || !descriptor.InteractiveGates || descriptor.CancellationTarget != "run" || descriptor.CancellationImplementation != "session_emulated" {
		t.Fatalf("descriptor: %+v", descriptor)
	}
	limits := descriptor.Capabilities.Limits
	if limits == nil || limits.MaxActiveRunsPerSession == nil || *limits.MaxActiveRunsPerSession != 2 || limits.MaxQueuedRunsPerSession == nil || *limits.MaxQueuedRunsPerSession != 1 {
		t.Fatalf("limits: %+v", limits)
	}
}

// The golden script emits action.call.* events, so the descriptor must
// affirmatively advertise the feature key the state machine consults for them.
// Advertising only "action.tools.execute" left the reference adapter's own
// tool lifecycle rejected as an unadvertised optional feature.
func TestDescriptorAdvertisesEmittedOptionalFeatures(t *testing.T) {
	descriptor := testDescriptor(t)
	for _, feature := range []string{"action.tools", "action.permissions", "user_input"} {
		support, ok := descriptor.Capabilities.Features[feature]
		if !ok || support.Level == protocol.SupportUnavailable {
			t.Fatalf("feature %q is not affirmatively advertised: %+v", feature, support)
		}
	}
}

// A request can fail several controls at once and one error.response carries
// one code, so the plan ranks the failures rather than conjoining them: within
// the unsatisfiability rung the lower capability key wins. An endpoint that
// answered with whichever defect it happened to find first would name a
// different control than the validator names for the same request, and a
// caller acting on that answer would fix a control and be refused again for
// one it was never told about.
func TestRefusalPrecedenceRanksByCapabilityKey(t *testing.T) {
	unsatisfiableChoice := json.RawMessage(`{"mode":"named","name":"absent_tool"}`)
	uncompilableSchema := json.RawMessage(`{"type":"object","required":"x"}`)
	for name, testCase := range map[string]struct {
		request protocol.MessageSubmitRequest
		feature string
	}{
		// run.structured_output sorts below run.tool_selection.
		"output schema and tool choice": {
			request: protocol.MessageSubmitRequest{OutputSchema: uncompilableSchema, ToolChoice: unsatisfiableChoice},
			feature: protocol.FeatureStructuredOutput,
		},
		// run.model_selection sorts below both.
		"model, output schema, and tool choice": {
			request: protocol.MessageSubmitRequest{ModelID: protocol.ControlValue("no-such-model"), OutputSchema: uncompilableSchema, ToolChoice: unsatisfiableChoice},
			feature: protocol.FeatureModelSelection,
		},
		"model and tool choice": {
			request: protocol.MessageSubmitRequest{ModelID: protocol.ControlValue("no-such-model"), ToolChoice: unsatisfiableChoice},
			feature: protocol.FeatureModelSelection,
		},
	} {
		session := newTestSession(t, 64)
		request := testCase.request
		request.SessionID, request.Delivery = "session-1", protocol.DeliveryAuto
		request.Messages = []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}
		_, _, err := session.Submit(context.Background(), request)
		switch testCase.feature {
		case protocol.FeatureModelSelection:
			// A catalog miss is the one unsatisfiability whose conforming
			// refusal is model_not_found rather than unsupported_feature.
			var missing *adapter.ModelNotFoundError
			if !errors.As(err, &missing) {
				t.Fatalf("%s: got %v, want the model refusal the lowest key owes", name, err)
			}
		default:
			var refusal *adapter.UnsupportedControlError
			if !errors.As(err, &refusal) {
				t.Fatalf("%s: got %v, want a typed unsupported-control refusal", name, err)
			}
			if refusal.Feature != testCase.feature {
				t.Fatalf("%s: refused %q, want the lower key %q", name, refusal.Feature, testCase.feature)
			}
		}
		if state, err := session.State(context.Background()); err != nil || state.ActiveRunID != "" || state.Status != protocol.SessionIdle {
			t.Fatalf("%s: refused controls allocated identity: %+v err=%v", name, state, err)
		}
	}
}

// The refusal ladder ranks a control refusal above ordinary submission
// validation, so the gate runs first: a request that is malformed *and*
// carries a control the endpoint cannot honour is answered with the control.
// A caller told only "invalid submission" would fix its messages, resubmit,
// and be refused again for a control it was never told about. The reference
// adapter has to answer the way every harness adapter here does, or the
// conformant behaviour it demonstrates is not the one the plan describes.
func TestControlRefusalOutranksOrdinaryValidation(t *testing.T) {
	message := []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}
	unsatisfiable := json.RawMessage(`{"mode":"named","name":"absent_tool"}`)
	for name, request := range map[string]protocol.MessageSubmitRequest{
		"no messages":      {SessionID: "session-1", Delivery: protocol.DeliveryAuto, ToolChoice: unsatisfiable},
		"no session":       {Delivery: protocol.DeliveryAuto, ToolChoice: unsatisfiable, Messages: message},
		"unsupported mode": {SessionID: "session-1", Delivery: protocol.DeliveryQueue, ToolChoice: unsatisfiable, Messages: message},
	} {
		session := newTestSession(t, 64)
		_, _, err := session.Submit(context.Background(), request)
		var refusal *adapter.UnsupportedControlError
		if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureToolSelection || refusal.Reason != adapter.ControlUnsatisfiable {
			t.Fatalf("%s: got %v, want the control named ahead of the ordinary refusal", name, err)
		}
		if state, err := session.State(context.Background()); err != nil || state.ActiveRunID != "" || state.Status != protocol.SessionIdle {
			t.Fatalf("%s: a refused submission allocated identity: %+v err=%v", name, state, err)
		}
	}

	// With no control at fault the ordinary refusal still answers.
	session := newTestSession(t, 64)
	if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto, ModelID: protocol.ControlValue(adapter.ModelSecondary),
	}); !errors.Is(err, adapter.ErrInvalidSubmission) {
		t.Fatalf("got %v, want the ordinary refusal when no control is at fault", err)
	}
}

// The catalog a tool_choice is judged against is the descriptor's, and the
// validator treats every served descriptor as a known catalog — so an endpoint
// that enforces a policy against a catalog it never published is judged
// against an empty one. Its `auto` runs are diagnosed for calling a tool the
// descriptor does not carry, and its `required` and `named` policies are
// deemed unsatisfiable, both while the adapter itself accepts and executes
// them. The reference adapter is the one endpoint that must not be able to
// drift that way, so it publishes the scripted tool and reads that same
// catalog at the gate.
//
// Proved end to end rather than by inspecting the descriptor: each policy is
// submitted, the scripted call runs, and the whole trace — submit request
// included, so the policy is in it — goes through the real validator.
func TestPublishedCatalogGovernsToolSelection(t *testing.T) {
	for name, policy := range map[string]string{
		"named":    `{"mode":"named","name":"scripted_tool"}`,
		"required": `{"mode":"required"}`,
		"auto":     `{"mode":"auto"}`,
		"allowed":  `{"mode":"auto","allowed":["scripted_tool"]}`,
	} {
		session := newTestSession(t, 64)
		request := protocol.MessageSubmitRequest{
			SessionID: "session-1", Delivery: protocol.DeliveryAuto,
			ToolChoice: json.RawMessage(policy),
			Messages:   []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}
		admission, stream, err := session.Submit(context.Background(), request)
		if err != nil {
			t.Fatalf("%s: the adapter refused a policy its own catalog satisfies: %v", name, err)
		}
		trace := runScriptedTool(t, session, admission, stream)
		calls := 0
		for _, event := range trace {
			if event.Type == protocol.TypeActionCallRequested {
				calls++
			}
		}
		if calls != 1 {
			t.Fatalf("%s: %d scripted calls, want 1", name, calls)
		}
		adaptertest.AssertProtocolValidWithSubmit(t, request, admission, testDescriptor(t), trace)
	}

	// The descriptor and the gate read one catalog, so a name outside it is
	// refused rather than admitted against a private list.
	session := newTestSession(t, 64)
	if _, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto,
		ToolChoice: json.RawMessage(`{"mode":"named","name":"absent_tool"}`),
		Messages:   []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
	}); err == nil {
		t.Fatal("a tool outside the published catalog was admitted")
	}

	// And the descriptor publishes exactly what the gate enforces.
	descriptor := testDescriptor(t)
	if len(descriptor.Capabilities.Tools) != 1 || descriptor.Capabilities.Tools[0].Name != "scripted_tool" {
		t.Fatalf("published catalog = %+v, want the one scripted tool", descriptor.Capabilities.Tools)
	}
}

// runScriptedTool drives one admitted run through the scripted gates and
// returns its whole event trace.
func runScriptedTool(t *testing.T, session adapter.Session, admission protocol.MessageSubmitResponse, stream adapter.EventStream) []protocol.Envelope {
	t.Helper()
	trace := drainAvailable(stream)
	var requested protocol.PermissionRequestedPayload
	for _, event := range trace {
		if event.Type == protocol.TypeActionPermissionRequested {
			if err := event.DecodePayload(&requested); err != nil {
				t.Fatal(err)
			}
		}
	}
	if requested.InteractionID == "" {
		t.Fatalf("the scripted run raised no permission gate: %+v", trace)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: admission.RunID, RespondedBy: "user",
		Permission: &protocol.PermissionResolveRequest{
			InteractionID: requested.InteractionID, SessionID: "session-1", RunID: admission.RunID,
			RequestedBy: "reference.memory", RespondedBy: "user", ChoiceID: "approve", Granted: true,
		},
	}); err != nil {
		t.Fatal(err)
	}
	events := drainAvailable(stream)
	trace = append(trace, events...)
	var input protocol.UserInputRequestedPayload
	for _, event := range events {
		if event.Type == protocol.TypeUserInputRequested {
			if err := event.DecodePayload(&input); err != nil {
				t.Fatal(err)
			}
		}
	}
	if input.InteractionID == "" {
		t.Fatalf("the scripted run raised no input gate: %+v", events)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: admission.RunID, RespondedBy: "user",
		Input: &protocol.UserInputResolveRequest{
			InteractionID: input.InteractionID, RequestedBy: "reference.memory", RespondedBy: "user",
			SessionID: "session-1", RunID: admission.RunID,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return append(trace, drainAvailable(stream)...)
}

// A session waiting for input and the run it is waiting on must say the same
// thing. The entry is the surface a recovering caller reads the interaction id
// from, and one that reported the run as running beside a session status of
// waiting_for_input would describe a session that is not the one parked.
func TestActiveRunEntryFollowsTheRunStatus(t *testing.T) {
	session := newTestSession(t, 64)
	run, stream := submit(t, session)
	events := drainAvailable(stream)
	requested := envelopeOfType(t, events, protocol.TypeActionPermissionRequested)
	var permission protocol.PermissionRequestedPayload
	if err := requested.DecodePayload(&permission); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: run, RespondedBy: permission.RespondedBy,
		Permission: &protocol.PermissionResolveRequest{
			InteractionID: permission.InteractionID, SessionID: "session-1", RunID: run,
			RequestedBy: permission.RequestedBy, RespondedBy: permission.RespondedBy,
			ChoiceID: "approve", Granted: true,
		}}); err != nil {
		t.Fatal(err)
	}
	gate := envelopeOfType(t, drainAvailable(stream), protocol.TypeUserInputRequested)
	var input protocol.UserInputRequestedPayload
	if err := gate.DecodePayload(&input); err != nil {
		t.Fatal(err)
	}

	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionWaitingForInput {
		t.Fatalf("session status = %s", state.Status)
	}
	if len(state.ActiveRuns) != 1 {
		t.Fatalf("active_runs = %+v", state.ActiveRuns)
	}
	entry := state.ActiveRuns[0]
	if entry.RunID != run || entry.Status != protocol.RunWaitingForInput {
		t.Fatalf("entry = %+v, want %s waiting_for_input", entry, run)
	}
	if len(entry.PendingInteractions) != 1 || entry.PendingInteractions[0] != input.InteractionID {
		t.Fatalf("pending interactions = %+v, want %s", entry.PendingInteractions, input.InteractionID)
	}
}

// A state read taken while a gate is open has to survive the validator like
// any other snapshot. Nothing else in the kit reads State, so a projection
// contradicting the very run the same trace carries passed every assertion
// here: the permission gate blocks the run without moving its status, and the
// session reported itself running beside an entry naming what it was blocked
// on — the shape queue-state-running-beside-a-pending-interaction rejects.
func TestStateDuringAGateValidates(t *testing.T) {
	session := newTestSession(t, 64)
	admission, stream := submitAdmission(t, session)
	run := admission.RunID
	events := drainAvailable(stream)
	requested := envelopeOfType(t, events, protocol.TypeActionPermissionRequested)

	snapshot, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Status != protocol.SessionWaitingForInput {
		t.Fatalf("session status during the permission gate = %s, want waiting_for_input", snapshot.Status)
	}
	if len(snapshot.ActiveRuns) != 1 || len(snapshot.ActiveRuns[0].PendingInteractions) != 1 {
		t.Fatalf("active_runs during the permission gate = %+v", snapshot.ActiveRuns)
	}

	// Resolve the gate and let the run finish, so the trace the snapshot is
	// spliced into is a complete one.
	var permission protocol.PermissionRequestedPayload
	if err := requested.DecodePayload(&permission); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: run, RespondedBy: permission.RespondedBy,
		Permission: &protocol.PermissionResolveRequest{
			InteractionID: permission.InteractionID, SessionID: "session-1", RunID: run,
			RequestedBy: permission.RequestedBy, RespondedBy: permission.RespondedBy,
			ChoiceID: "approve", Granted: true,
		}}); err != nil {
		t.Fatal(err)
	}
	events = append(events, drainAvailable(stream)...)
	gate := envelopeOfType(t, events, protocol.TypeUserInputRequested)
	var input protocol.UserInputRequestedPayload
	if err := gate.DecodePayload(&input); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: run, RespondedBy: input.RespondedBy,
		Input: &protocol.UserInputResolveRequest{
			InteractionID: input.InteractionID, SessionID: "session-1", RunID: run,
			RequestedBy: input.RequestedBy, RespondedBy: input.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		}}); err != nil {
		t.Fatal(err)
	}
	events = append(events, drainAvailable(stream)...)

	exchange, err := adaptertest.StateExchange(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertProtocolValidWithDescriptor(t, admission, testDescriptor(t), adaptertest.SpliceAfter(t, events, requested.ID, exchange))
}

// A snapshot handed to a caller owns its own backing array. active_runs is a
// slice of entries carrying pointers and slices of their own, so a by-value
// copy shares all of it with the session: a caller editing what it was given
// would reach into the adapter's own projection. The recovery snapshot is
// handed out on the same terms as State's.
func TestHandedOutStateDoesNotAliasTheSession(t *testing.T) {
	session := newTestSession(t, 64)
	run, stream := submit(t, session)
	drainAvailable(stream)

	recovery, _, err := session.Resume(context.Background(), adapter.ResumeRequest{RunID: run, AfterSequence: 0})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for name, given := range map[string]protocol.SessionState{"State": snapshot, "Resume": recovery.State} {
		if len(given.ActiveRuns) != 1 {
			t.Fatalf("%s: active_runs = %+v", name, given.ActiveRuns)
		}
		given.ActiveRuns[0].RunID = "tampered"
		given.ActiveRuns[0].Status = protocol.RunCompleted
		if sequence := given.ActiveRuns[0].AsOfSequence; sequence != nil {
			*sequence = 99
		}
		for i := range given.ActiveRuns[0].PendingInteractions {
			given.ActiveRuns[0].PendingInteractions[i] = "tampered"
		}
	}

	after, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.ActiveRuns) != 1 || after.ActiveRuns[0].RunID != run {
		t.Fatalf("the session's own runs were edited through a snapshot: %+v", after.ActiveRuns)
	}
	entry := after.ActiveRuns[0]
	if entry.Status == protocol.RunCompleted {
		t.Fatalf("run status was edited through a snapshot: %+v", entry)
	}
	if entry.AsOfSequence == nil || *entry.AsOfSequence == 99 {
		t.Fatalf("capture position was edited through a snapshot: %+v", entry)
	}
	for _, id := range entry.PendingInteractions {
		if id == "tampered" {
			t.Fatalf("pending interactions were edited through a snapshot: %+v", entry)
		}
	}
}

// gatedIDs is fixedIDs with one identifier held open, so a test can stop the
// adapter at a named point inside a call and look at what the session shows
// from outside while it is there.
type gatedIDs struct {
	mu      sync.Mutex
	n       int
	counts  map[string]int
	trip    func(kind string, nth int) bool
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedIDs(trip func(kind string, nth int) bool) *gatedIDs {
	return &gatedIDs{counts: map[string]int{}, trip: trip, reached: make(chan struct{}), release: make(chan struct{})}
}

func (g *gatedIDs) NewID(kind string) string {
	g.mu.Lock()
	g.n++
	g.counts[kind]++
	id, hold := fmt.Sprintf("%s-%02d", kind, g.n), g.trip(kind, g.counts[kind])
	g.mu.Unlock()
	if hold {
		close(g.reached)
		<-g.release
	}
	return id
}

func (g *gatedIDs) open() { g.once.Do(func() { close(g.release) }) }

// TestStateOmitsARunUntilItsAdmissionIsHandedBack pins the boundary a
// projection may cross. A run exists inside the adapter from the moment Submit
// creates it, and Submit emits its opening events before it returns — but the
// caller creates the submit response envelope afterwards, and until that
// envelope exists the trace carries no admission this run could have been
// anchored to. Nothing Submit publishes reaches the trace ahead of it either,
// because the caller does not hold the stream yet. So a state read taken while
// the call is still running must describe a session without the run, and one
// taken after it must describe the run: the projection flips as the response
// is handed back and not before.
func TestStateOmitsARunUntilItsAdmissionIsHandedBack(t *testing.T) {
	// The second message identifier is the opening delta's, which the adapter
	// allocates after it has emitted run.started for the run — the deepest
	// point inside Submit at which a state read could see anything at all.
	ids := newGatedIDs(func(kind string, nth int) bool { return kind == "message" && nth == 2 })
	defer ids.open()
	memory := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: ids, JournalCapacity: 64})
	session, err := memory.Open(context.Background(), adapter.OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	type answer struct {
		admission protocol.MessageSubmitResponse
		err       error
	}
	done := make(chan answer, 1)
	go func() {
		admission, _, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}})
		done <- answer{admission, err}
	}()
	select {
	case <-ids.reached:
	case <-time.After(10 * time.Second):
		t.Fatal("submit never reached the opening delta")
	}
	held, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(held.ActiveRuns) != 0 || held.ActiveRunID != "" || held.Status != protocol.SessionIdle {
		t.Fatalf("state named a run whose admission has not been handed back: %+v", held)
	}
	ids.open()
	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	after, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if after.ActiveRunID != got.admission.RunID || len(after.ActiveRuns) != 1 {
		t.Fatalf("state does not name the admitted run: %+v", after)
	}
	if after.ActiveRuns[0].RunID != got.admission.RunID {
		t.Fatalf("state lists another run: %+v", after.ActiveRuns[0])
	}
}

// A state read is answered synchronously while lifecycle reaches a consumer
// through a buffered stream, so a snapshot taken at a settlement can be handed
// out — and be on the wire — before the terminal it reflects. Dropping the run
// and saying nothing made that snapshot read as erasing a live run. The
// capture anchor is what the endpoint actually knows: this run is gone, and
// here is the sequence of the event that ended it. The reservation is the
// clean case to pin, because its terminal is the only envelope its domain
// ever carries, so a stream holding it undelivered is exactly the window.
func TestStateAnchorsARunItSettledBeforeTheTerminalIsDelivered(t *testing.T) {
	session := newTestSession(t, 64)
	first, firstStream := submitAdmission(t, session)
	events := drainAvailable(firstStream)
	gate := envelopeOfType(t, events, protocol.TypeActionPermissionRequested)

	queuedRequest := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryQueue, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("after you")}}}
	reservation, reservedStream, err := session.Submit(context.Background(), queuedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Admission != protocol.AdmissionQueued || reservation.Status != protocol.RunQueued {
		t.Fatalf("reservation = %+v", reservation)
	}
	if _, err := session.Cancel(context.Background(), reservation.RunID); err != nil {
		t.Fatal(err)
	}

	// Nothing has read the reservation's stream, so its terminal is admitted
	// history the trace has not been told about. This is the read the race
	// produces.
	snapshot, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range snapshot.ActiveRuns {
		if entry.RunID == reservation.RunID {
			t.Fatalf("a settled reservation is still listed: %+v", entry)
		}
	}
	if snapshot.AsOf == nil || len(snapshot.AsOf.Settled) != 1 {
		t.Fatalf("snapshot dropped the reservation without saying so: %+v", snapshot.AsOf)
	}
	if snapshot.AsOf.Settled[0].RunID != reservation.RunID || snapshot.AsOf.Settled[0].Sequence != 1 {
		t.Fatalf("settlement anchor = %+v, want %s at its terminal sequence 1", snapshot.AsOf.Settled[0], reservation.RunID)
	}

	settled := drainAvailable(reservedStream)
	if len(settled) != 1 || settled[0].Type != protocol.TypeRunCancelled || settled[0].Sequence == nil || *settled[0].Sequence != 1 {
		t.Fatalf("reservation domain = %+v, want one run.cancelled at sequence 1", settled)
	}

	// Finish the started run, so the trace the snapshot is spliced into is a
	// complete one.
	rest := resolveScriptedGates(t, session, first.RunID, firstStream, events)

	exchange, err := adaptertest.StateExchange(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	// The order the race produces: the snapshot reaches the wire, then the
	// terminal it already reflects, then the started run's remainder.
	trace := adaptertest.SpliceAfter(t, events, gate.ID, exchange)
	trace = append(trace, settled[0])
	trace = append(trace, rest...)
	adaptertest.AssertProtocolValidQueued(t, []adaptertest.QueuedSubmission{
		{Request: protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}}, Admission: first},
		{Request: queuedRequest, Admission: reservation, Cancelled: true},
	}, testDescriptor(t), trace)
}

// The same anchor for the started run: once a run settles, every snapshot the
// session hands out names it and the sequence its terminal carries.
func TestStateAnchorsASettledStartedRun(t *testing.T) {
	session := newTestSession(t, 64)
	admission, stream := submitAdmission(t, session)
	events := drainAvailable(stream)
	events = append(events, resolveScriptedGates(t, session, admission.RunID, stream, events)...)
	terminal := envelopeOfType(t, events, protocol.TypeRunCompleted)

	snapshot, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.ActiveRuns) != 0 || snapshot.ActiveRunID != "" || snapshot.Status != protocol.SessionIdle {
		t.Fatalf("state after the terminal = %+v", snapshot)
	}
	if snapshot.AsOf == nil || len(snapshot.AsOf.Settled) != 1 {
		t.Fatalf("snapshot dropped the run without saying so: %+v", snapshot.AsOf)
	}
	if snapshot.AsOf.Settled[0].RunID != admission.RunID || terminal.Sequence == nil || snapshot.AsOf.Settled[0].Sequence != *terminal.Sequence {
		t.Fatalf("settlement anchor = %+v, want %s at sequence %v", snapshot.AsOf.Settled[0], admission.RunID, terminal.Sequence)
	}
}

// resolveScriptedGates answers the permission and input gates of one run and
// returns everything it published after the events already drained.
func resolveScriptedGates(t *testing.T, session adapter.Session, run protocol.RunID, stream adapter.EventStream, drained []protocol.Envelope) []protocol.Envelope {
	t.Helper()
	requested := envelopeOfType(t, drained, protocol.TypeActionPermissionRequested)
	var permission protocol.PermissionRequestedPayload
	if err := requested.DecodePayload(&permission); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: run, RespondedBy: permission.RespondedBy,
		Permission: &protocol.PermissionResolveRequest{
			InteractionID: permission.InteractionID, SessionID: "session-1", RunID: run,
			RequestedBy: permission.RequestedBy, RespondedBy: permission.RespondedBy,
			ChoiceID: "approve", Granted: true,
		}}); err != nil {
		t.Fatal(err)
	}
	rest := drainAvailable(stream)
	gate := envelopeOfType(t, rest, protocol.TypeUserInputRequested)
	var input protocol.UserInputRequestedPayload
	if err := gate.DecodePayload(&input); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: run, RespondedBy: input.RespondedBy,
		Input: &protocol.UserInputResolveRequest{
			InteractionID: input.InteractionID, SessionID: "session-1", RunID: run,
			RequestedBy: input.RequestedBy, RespondedBy: input.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		}}); err != nil {
		t.Fatal(err)
	}
	return append(rest, drainAvailable(stream)...)
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
