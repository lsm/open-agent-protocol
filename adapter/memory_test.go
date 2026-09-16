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
	if requested.RequestedBy != "agent" || requested.RespondedBy != "user" {
		t.Fatalf("ownership omitted: %+v", requested)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, SessionID: "session-1", RunID: runID, RequestedBy: "agent", RespondedBy: "user", ChoiceID: "approve", Granted: true}}); err != nil {
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
	if input.RequestedBy != "agent" || input.RespondedBy != "user" {
		t.Fatalf("ownership omitted: %+v", input)
	}
	resolution := adapter.InteractionResolution{
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
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}}); err != nil {
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
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	middle := drainAvailable(stream)
	var input protocol.UserInputRequestedPayload
	_ = middle[3].DecodePayload(&input)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}})
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
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
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
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: runID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}}); err != nil {
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
	for name, policy := range map[string]string{
		"none":                 `{"mode":"none"}`,
		"disallowed":           `{"mode":"auto","disallowed":["scripted_tool"]}`,
		"allowed without tool": `{"mode":"auto","allowed":[]}`,
	} {
		session := newTestSession(t, 64)
		request := protocol.MessageSubmitRequest{
			SessionID: "session-1", Delivery: protocol.DeliveryAuto,
			ToolChoice: json.RawMessage(policy),
			Messages:   []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}
		if name == "allowed without tool" {
			// An allowed list that omits the only tool filters it out; an
			// empty list under "auto" is not unsatisfiable, only empty.
			request.ToolChoice = json.RawMessage(`{"mode":"auto","allowed":["scripted_tool"]}`)
		}
		admission, stream, err := session.Submit(context.Background(), request)
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
		want := 0
		if name == "allowed without tool" {
			want = 1
		}
		if calls != want {
			t.Fatalf("%s: %d tool calls, want %d: %+v", name, calls, want, events)
		}
		_ = admission
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
	answer := protocol.UserInputResolveRequest{InteractionID: prompt.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: admission.RunID, Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}}}
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
	valid := protocol.PermissionResolveRequest{InteractionID: permission.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, ChoiceID: "approve", Granted: true}
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
		request := protocol.UserInputResolveRequest{InteractionID: input.InteractionID, RequestedBy: "agent", RespondedBy: "user", SessionID: "session-1", RunID: runID, Answers: answers}
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
	if descriptor.MaxActiveRunsPerSession != 1 || !descriptor.InteractiveGates || descriptor.CancellationTarget != "run" || descriptor.CancellationImplementation != "session_emulated" {
		t.Fatalf("descriptor: %+v", descriptor)
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
