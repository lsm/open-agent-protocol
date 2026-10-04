package adapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/validation"
)

func compactor(t *testing.T, session adapter.Session) adapter.Compactor {
	t.Helper()
	compacting, ok := session.(adapter.Compactor)
	if !ok {
		t.Fatal("the memory session does not compact")
	}
	return compacting
}

func compact(t *testing.T, session adapter.Session, request protocol.SessionCompactRequest, envelope protocol.EnvelopeID) (protocol.SessionCompactResponse, adapter.EventStream) {
	t.Helper()
	response, stream, err := compactor(t, session).Compact(context.Background(), adapter.CompactRequest{Request: request, EnvelopeID: envelope})
	if err != nil {
		t.Fatal(err)
	}
	return response, stream
}

type exchange struct {
	request, response protocol.Envelope
}

func compactExchange(t *testing.T, descriptor adapter.Descriptor, id string, request protocol.SessionCompactRequest, response protocol.SessionCompactResponse) exchange {
	t.Helper()
	asked, err := protocol.NewEnvelope(protocol.TypeSessionCompactRequest, protocol.EnvelopeID(id), request)
	if err != nil {
		t.Fatal(err)
	}
	asked.SessionID, asked.CapabilityRevision = request.SessionID, descriptor.CapabilityRevision
	answered, err := protocol.NewEnvelope(protocol.TypeSessionCompactResponse, protocol.EnvelopeID(id+"-response"), response)
	if err != nil {
		t.Fatal(err)
	}
	answered.SessionID, answered.InReplyTo, answered.CapabilityRevision = response.SessionID, asked.ID, descriptor.CapabilityRevision
	return exchange{asked, answered}
}

func submitExchange(t *testing.T, descriptor adapter.Descriptor, id string, request protocol.MessageSubmitRequest, response protocol.MessageSubmitResponse) exchange {
	t.Helper()
	asked, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, protocol.EnvelopeID(id), request)
	if err != nil {
		t.Fatal(err)
	}
	asked.SessionID, asked.CapabilityRevision = request.SessionID, descriptor.CapabilityRevision
	answered, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, protocol.EnvelopeID(id+"-response"), response)
	if err != nil {
		t.Fatal(err)
	}
	answered.SessionID, answered.InReplyTo, answered.CapabilityRevision = response.SessionID, asked.ID, descriptor.CapabilityRevision
	return exchange{asked, answered}
}

func requireValidTrace(t *testing.T, descriptor adapter.Descriptor, parts ...[]protocol.Envelope) {
	t.Helper()
	asked, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	answered, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	answered.InReplyTo, answered.CapabilityRevision = asked.ID, descriptor.CapabilityRevision
	trace := []protocol.Envelope{asked, answered}
	for _, part := range parts {
		trace = append(trace, part...)
	}
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(encoded, "memory-compaction"); !result.Valid() {
		t.Fatalf("memory compaction trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, encoded)
	}
}

func types(envelopes []protocol.Envelope) []protocol.EnvelopeType {
	out := make([]protocol.EnvelopeType, len(envelopes))
	for i, envelope := range envelopes {
		out[i] = envelope.Type
	}
	return out
}

func TestMemoryAdvertisesCompaction(t *testing.T) {
	descriptor := testDescriptor(t)
	for _, key := range []string{protocol.FeatureSessionCompact, protocol.FeatureRunCompaction} {
		if support := descriptor.Capabilities.Features[key]; support.Level != protocol.SupportEmulated || support.Reason == "" {
			t.Fatalf("%s = %+v, want emulated with a reason", key, support)
		}
	}
}

func TestMemoryCompactsAnIdleSessionInARunOfItsOwn(t *testing.T) {
	session := newTestSession(t, 64)
	descriptor := testDescriptor(t)
	focus := "the parser"
	request := protocol.SessionCompactRequest{SessionID: "session-1", Focus: &focus}
	response, stream := compact(t, session, request, "compact-request")
	if !response.Accepted || response.Admission != protocol.AdmissionStarted || response.EffectiveDelivery != protocol.DeliveryStart || response.RequestedDelivery != protocol.DeliveryAuto || response.Status != protocol.RunRunning {
		t.Fatalf("admission = %+v", response)
	}
	events := drainAvailable(stream)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeRunCompactionStarted, protocol.TypeRunCompactionEnded, protocol.TypeRunCompleted}
	if got := types(events); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || got[3] != want[3] {
		t.Fatalf("events = %v, want %v", got, want)
	}
	var started protocol.RunCompactionStartedPayload
	if err := events[1].DecodePayload(&started); err != nil {
		t.Fatal(err)
	}
	var ended protocol.RunCompactionEndedPayload
	if err := events[2].DecodePayload(&ended); err != nil {
		t.Fatal(err)
	}
	if started.Reason != protocol.CompactionRequested || ended.CompactionID != started.CompactionID || ended.Outcome != protocol.CompactionCompleted {
		t.Fatalf("compaction = %+v then %+v", started, ended)
	}
	summary := ""
	if ended.Summary != nil {
		summary, _ = ended.Summary.Content.Text()
	}
	if !strings.Contains(summary, focus) {
		t.Fatalf("summary = %+v, want it to name the focus", ended.Summary)
	}
	var completed protocol.RunCompletedPayload
	if err := events[3].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if completed.StopReason != "compacted" {
		t.Fatalf("stop reason = %q", completed.StopReason)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle {
		t.Fatalf("session after a compaction = %s, want idle", state.Status)
	}
	requested := compactExchange(t, descriptor, "compact-request", request, response)
	requireValidTrace(t, descriptor, []protocol.Envelope{requested.request, requested.response}, events)
}

func TestMemoryCompactionThatContinuesTakesTheScriptedTurn(t *testing.T) {
	session := newTestSession(t, 64)
	descriptor := testDescriptor(t)
	request := protocol.SessionCompactRequest{SessionID: "session-1", Continue: true}
	response, stream := compact(t, session, request, "compact-request")
	events := drainAvailable(stream)
	ended := -1
	for i, envelope := range events {
		if envelope.Type == protocol.TypeRunCompactionEnded {
			ended = i
		}
		if isTerminalType(envelope.Type) {
			t.Fatalf("a continuing compaction settled before its model turn: %v", types(events))
		}
	}
	if ended < 0 || ended+1 >= len(events) || events[ended+1].Type != protocol.TypeContentDelta {
		t.Fatalf("events = %v, want a model turn after the compaction", types(events))
	}
	gate := envelopeOfType(t, events, protocol.TypeUserInputRequested)
	var input protocol.UserInputRequestedPayload
	if err := gate.DecodePayload(&input); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{
		RunID: response.RunID, RespondedBy: input.RespondedBy,
		Input: &protocol.UserInputResolveRequest{
			InteractionID: input.InteractionID, SessionID: "session-1", RunID: response.RunID,
			RequestedBy: input.RequestedBy, RespondedBy: input.RespondedBy,
			Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
		}}); err != nil {
		t.Fatal(err)
	}
	rest := drainAvailable(stream)
	completed := envelopeOfType(t, rest, protocol.TypeRunCompleted)
	var payload protocol.RunCompletedPayload
	if err := completed.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.StopReason == "compacted" {
		t.Fatal("a compaction that continued settled as compacted")
	}
	requested := compactExchange(t, descriptor, "compact-request", request, response)
	requireValidTrace(t, descriptor, []protocol.Envelope{requested.request, requested.response}, events, rest)
}

func TestMemoryQueuesACompactionBehindABusyRun(t *testing.T) {
	session := newTestSession(t, 64)
	descriptor := testDescriptor(t)
	submitted := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}}
	admission, first, err := session.Submit(context.Background(), adapter.SubmitRequest{Request: submitted, EnvelopeID: "submit-request"})
	if err != nil {
		t.Fatal(err)
	}
	initial := drainAvailable(first)

	request := protocol.SessionCompactRequest{SessionID: "session-1"}
	response, second := compact(t, session, request, "compact-request")
	if response.Admission != protocol.AdmissionQueued || response.EffectiveDelivery != protocol.EffectiveDeliveryQueue || response.Status != protocol.RunQueued || response.DeliveryResolution != "session_busy" {
		t.Fatalf("busy admission = %+v", response)
	}
	if early := drainAvailable(second); len(early) != 0 {
		t.Fatalf("a queued compaction ran before the busy run settled: %v", types(early))
	}

	rest := resolveScriptedGates(t, session, admission.RunID, first, initial)
	promoted := drainAvailable(second)
	if got := types(promoted); len(got) != 4 || got[1] != protocol.TypeRunCompactionStarted || got[3] != protocol.TypeRunCompleted {
		t.Fatalf("promoted compaction = %v", got)
	}
	asked := submitExchange(t, descriptor, "submit-request", submitted, admission)
	requested := compactExchange(t, descriptor, "compact-request", request, response)
	requireValidTrace(t, descriptor,
		[]protocol.Envelope{asked.request, asked.response}, initial,
		[]protocol.Envelope{requested.request, requested.response}, rest, promoted)
}

func TestMemoryRefusesACompactionItCannotQueue(t *testing.T) {
	session := newTestSession(t, 64)
	submitted := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}}
	for _, id := range []protocol.EnvelopeID{"first", "second"} {
		if _, _, err := session.Submit(context.Background(), adapter.SubmitRequest{Request: submitted, EnvelopeID: id}); err != nil {
			t.Fatal(err)
		}
	}
	_, stream, err := compactor(t, session).Compact(context.Background(), adapter.CompactRequest{Request: protocol.SessionCompactRequest{SessionID: "session-1"}})
	if !errors.Is(err, adapter.ErrRunActive) || stream != nil {
		t.Fatalf("full queue: err = %v, stream = %v", err, stream)
	}
}

func TestMemoryRefusesASteerOrBTWCompactionNamingTheDelivery(t *testing.T) {
	session := newTestSession(t, 64)
	for _, delivery := range []protocol.RequestedDeliveryMode{protocol.DeliverySteer, protocol.DeliveryBTW} {
		_, stream, err := compactor(t, session).Compact(context.Background(), adapter.CompactRequest{Request: protocol.SessionCompactRequest{SessionID: "session-1", Delivery: delivery}})
		var refused *adapter.UnsupportedControlError
		if !errors.As(err, &refused) || refused.Feature != protocol.DeliveryKey(delivery) || stream != nil {
			t.Fatalf("%s: err = %v, stream = %v", delivery, err, stream)
		}
	}
}

func isTerminalType(typ protocol.EnvelopeType) bool {
	return typ == protocol.TypeRunCompleted || typ == protocol.TypeRunFailed || typ == protocol.TypeRunCancelled
}

func openWithPolicy(t *testing.T, policy *protocol.CompactionPolicy) (adapter.Session, error) {
	t.Helper()
	memory := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}, JournalCapacity: 64})
	return memory.Open(context.Background(), adapter.OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}, CompactionPolicy: policy})
}

func submitText(t *testing.T, session adapter.Session, text string) []protocol.Envelope {
	t.Helper()
	request := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}}}
	admission, stream, err := session.Submit(context.Background(), adapter.SubmitRequest{Request: request, EnvelopeID: "submit-request"})
	if err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionStarted {
		t.Fatalf("admission = %+v", admission)
	}
	return drainAvailable(stream)
}

func compactions(t *testing.T, events []protocol.Envelope) []protocol.RunCompactionStartedPayload {
	t.Helper()
	var started []protocol.RunCompactionStartedPayload
	for _, envelope := range events {
		if envelope.Type != protocol.TypeRunCompactionStarted {
			continue
		}
		var payload protocol.RunCompactionStartedPayload
		if err := envelope.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		started = append(started, payload)
	}
	return started
}

func TestMemoryCompactsARunWhoseHistoryReachesTheThresholdAndCarriesOnWithTheTurn(t *testing.T) {
	session, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	events := submitText(t, session, "go")
	got := types(events)
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeRunCompactionStarted, protocol.TypeRunCompactionEnded, protocol.TypeContentDelta}
	if len(got) < len(want) {
		t.Fatalf("events = %v, want them to open with %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want them to open with %v", got, want)
		}
	}
	for _, envelope := range events {
		if isTerminalType(envelope.Type) {
			t.Fatalf("the run settled at its compaction instead of taking its turn: %v", got)
		}
	}
	started := compactions(t, events)[0]
	if started.Reason != protocol.CompactionThreshold || started.HistoryTokens == nil || *started.HistoryTokens != 1 {
		t.Fatalf("compaction started = %+v, want a threshold compaction of one token", started)
	}
	var ended protocol.RunCompactionEndedPayload
	if err := events[2].DecodePayload(&ended); err != nil {
		t.Fatal(err)
	}
	if ended.Outcome != protocol.CompactionCompleted || ended.Summary == nil || ended.HistoryTokens == nil || *ended.HistoryTokens != 8 {
		t.Fatalf("compaction ended = %+v, want completed with the eight-token summary", ended)
	}
}

func TestMemoryLeavesARunBelowItsThresholdUncompacted(t *testing.T) {
	session, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 2})
	if err != nil {
		t.Fatal(err)
	}
	if started := compactions(t, submitText(t, session, "go")); len(started) != 0 {
		t.Fatalf("a one-token history compacted against a two-token threshold: %+v", started)
	}
}

func TestMemoryTakesAThresholdAsAShareOfTheReferenceWindow(t *testing.T) {
	long := strings.Repeat("abcd", 100)
	narrow, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 1})
	if err != nil {
		t.Fatal(err)
	}
	if started := compactions(t, submitText(t, narrow, long)); len(started) != 1 || started[0].Reason != protocol.CompactionThreshold {
		t.Fatalf("a hundred tokens against 1%% of the window = %+v, want one threshold compaction", started)
	}
	wide, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 50})
	if err != nil {
		t.Fatal(err)
	}
	if started := compactions(t, submitText(t, wide, long)); len(started) != 0 {
		t.Fatalf("a hundred tokens against half the window compacted: %+v", started)
	}
	auto, err := openWithPolicy(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if started := compactions(t, submitText(t, auto, long)); len(started) != 0 {
		t.Fatalf("a hundred tokens against the default threshold compacted: %+v", started)
	}
}

func TestMemoryOpensAnOffPolicyAndNeverCompactsOnItsOwn(t *testing.T) {
	session, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionOff})
	if err != nil {
		t.Fatalf("open with an off policy answered %v", err)
	}
	if started := compactions(t, submitText(t, session, strings.Repeat("abcd", 100))); len(started) != 0 {
		t.Fatalf("an off policy compacted: %+v", started)
	}
	descriptor := testDescriptor(t)
	if support := descriptor.Capabilities.Features[protocol.FeatureCompactionPolicy]; support.Level != protocol.SupportEmulated || !support.DisclosesMode(protocol.ModeSessionOpen) {
		t.Fatalf("%s = %+v, want emulated at session open", protocol.FeatureCompactionPolicy, support)
	}
}

func driveToCompletion(t *testing.T, session adapter.Session, id string, text string) []protocol.Envelope {
	t.Helper()
	request := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}}}
	admission, stream, err := session.Submit(context.Background(), adapter.SubmitRequest{Request: request, EnvelopeID: protocol.EnvelopeID(id)})
	if err != nil {
		t.Fatal(err)
	}
	asked := submitExchange(t, testDescriptor(t), id, request, admission)
	trace := []protocol.Envelope{asked.request, asked.response}
	trace = append(trace, drainAvailable(stream)...)
	permission := trace[len(trace)-1]
	var requested protocol.PermissionRequestedPayload
	if err := permission.DecodePayload(&requested); err != nil || permission.Type != protocol.TypeActionPermissionRequested {
		t.Fatalf("run %s stopped at %s, want a permission request", id, permission.Type)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, SessionID: "session-1", RunID: admission.RunID, RequestedBy: requested.RequestedBy, RespondedBy: "user", ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	trace = append(trace, drainAvailable(stream)...)
	for i := len(trace) - 1; i >= 0; i-- {
		if trace[i].Type == protocol.TypeUserInputRequested {
			answerInput(t, session, trace[i])
			break
		}
	}
	return append(trace, drainAvailable(stream)...)
}

func TestMemoryDefersAThresholdCrossedAtSettlementToTheNextRunAndBothRunsValidate(t *testing.T) {
	session, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 9})
	if err != nil {
		t.Fatal(err)
	}
	first := driveToCompletion(t, session, "submit-first", "go")
	if started := compactions(t, first); len(started) != 0 {
		t.Fatalf("the first run compacted below its threshold: %+v", started)
	}
	if last := first[len(first)-1]; last.Type != protocol.TypeRunCompleted {
		t.Fatalf("the first run ended with %s", last.Type)
	}
	second := driveToCompletion(t, session, "submit-second", "go")
	started := compactions(t, second)
	if len(started) != 1 || started[0].Reason != protocol.CompactionThreshold || *started[0].HistoryTokens != 9 {
		t.Fatalf("the second run's compactions = %+v, want one threshold compaction of nine tokens", started)
	}
	if second[3].Type != protocol.TypeRunCompactionStarted {
		t.Fatalf("the second run did not compact before its turn: %v", types(second))
	}
	if last := second[len(second)-1]; last.Type != protocol.TypeRunCompleted {
		t.Fatalf("the second run ended with %s", last.Type)
	}
	requireValidTrace(t, testDescriptor(t), first, second)
}

func TestMemoryRefusesAShareOrTokenCountOutOfRange(t *testing.T) {
	for _, policy := range []protocol.CompactionPolicy{
		{Kind: protocol.CompactionShare, SharePercent: 0},
		{Kind: protocol.CompactionShare, SharePercent: 101},
		{Kind: protocol.CompactionTokens, Tokens: 0},
		{Kind: protocol.CompactionTokens, Tokens: -5},
		{Kind: "sometimes"},
	} {
		_, err := openWithPolicy(t, &policy)
		var refusal *adapter.UnsupportedControlError
		if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureCompactionPolicy || refusal.Field != "compaction_policy" || refusal.Reason != adapter.ControlUnsatisfiable {
			t.Fatalf("open with %+v answered %v, want an unsatisfiable compaction policy", policy, err)
		}
	}
}

func TestMemoryCompactsAQueuedRunPromotedByTheSettlementThatCrossedTheThreshold(t *testing.T) {
	session, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 9})
	if err != nil {
		t.Fatal(err)
	}
	first := protocol.MessageSubmitRequest{SessionID: "session-1", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}}
	running, stream, err := session.Submit(context.Background(), adapter.SubmitRequest{Request: first, EnvelopeID: "submit-first"})
	if err != nil {
		t.Fatal(err)
	}
	queued := first
	queued.Delivery = protocol.DeliveryQueue
	waiting, queuedStream, err := session.Submit(context.Background(), adapter.SubmitRequest{Request: queued, EnvelopeID: "submit-queued"})
	if err != nil || waiting.Admission != protocol.AdmissionQueued {
		t.Fatalf("queued admission = %+v, %v", waiting, err)
	}
	events := drainAvailable(stream)
	var requested protocol.PermissionRequestedPayload
	if err := events[len(events)-1].DecodePayload(&requested); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(context.Background(), adapter.InteractionResolution{RunID: running.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{InteractionID: requested.InteractionID, SessionID: "session-1", RunID: running.RunID, RequestedBy: requested.RequestedBy, RespondedBy: "user", ChoiceID: "approve", Granted: true}}); err != nil {
		t.Fatal(err)
	}
	events = drainAvailable(stream)
	answerInput(t, session, events[len(events)-2])
	drainAvailable(stream)
	promoted := drainAvailable(queuedStream)
	started := compactions(t, promoted)
	if len(started) != 1 || started[0].RunID != waiting.RunID || *started[0].HistoryTokens != 9 {
		t.Fatalf("the promoted run's compactions = %+v, want one of nine tokens on %s", started, waiting.RunID)
	}
}

func TestMemoryStateReportsThePolicyTheSessionRunsUnder(t *testing.T) {
	for _, tc := range []struct {
		asked *protocol.CompactionPolicy
		want  protocol.CompactionPolicy
	}{
		{nil, protocol.CompactionPolicy{Kind: protocol.CompactionAuto}},
		{&protocol.CompactionPolicy{Kind: protocol.CompactionOff}, protocol.CompactionPolicy{Kind: protocol.CompactionOff}},
		{&protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 50}, protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 50}},
		{&protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 9}, protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 9}},
	} {
		session, err := openWithPolicy(t, tc.asked)
		if err != nil {
			t.Fatal(err)
		}
		state, err := session.State(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if state.CompactionPolicy == nil || *state.CompactionPolicy != tc.want {
			t.Fatalf("state for %+v reports %+v, want %+v", tc.asked, state.CompactionPolicy, tc.want)
		}
	}
}

func TestMemoryReopenKeepsTheCompactionPolicyUnlessItNamesANewOne(t *testing.T) {
	memory := adapter.NewMemory(adapter.Config{Clock: &fixedClock{}, IDs: &fixedIDs{}, JournalCapacity: 64})
	open := func(reopen bool, policy *protocol.CompactionPolicy) adapter.Session {
		t.Helper()
		session, err := memory.Open(context.Background(), adapter.OpenRequest{SessionID: "kept", Participant: protocol.Participant{ID: "user"}, Reopen: reopen, CompactionPolicy: policy})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	reported := func(session adapter.Session) protocol.CompactionPolicy {
		t.Helper()
		state, err := session.State(context.Background())
		if err != nil || state.CompactionPolicy == nil {
			t.Fatalf("state = %+v, %v", state, err)
		}
		return *state.CompactionPolicy
	}
	tokens := protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 9}
	if err := open(false, &tokens).Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened := open(true, nil)
	if got := reported(reopened); got != tokens {
		t.Fatalf("reopened policy = %+v, want %+v", got, tokens)
	}
	if err := reopened.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	share := protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 50}
	if got := reported(open(true, &share)); got != share {
		t.Fatalf("reopened with a new policy = %+v, want %+v", got, share)
	}
}

func updater(t *testing.T, session adapter.Session) adapter.SettingsUpdater {
	t.Helper()
	updating, ok := session.(adapter.SettingsUpdater)
	if !ok {
		t.Fatal("the memory session does not take a settings update")
	}
	return updating
}

func TestMemoryUpdateMovesTheThresholdTheNextRunCompactsAgainst(t *testing.T) {
	session, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionOff})
	if err != nil {
		t.Fatal(err)
	}
	tokens := protocol.CompactionPolicy{Kind: protocol.CompactionTokens, Tokens: 1}
	response, state, err := updater(t, session).UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session-1", CompactionPolicy: &tokens})
	if err != nil {
		t.Fatal(err)
	}
	if response.CompactionPolicy == nil || *response.CompactionPolicy != tokens || response.PreviousCompactionPolicy == nil || response.PreviousCompactionPolicy.Kind != protocol.CompactionOff {
		t.Fatalf("response = %+v, want tokens 1 replacing off", response)
	}
	if state.CompactionPolicy == nil || *state.CompactionPolicy != tokens {
		t.Fatalf("state = %+v, want tokens 1", state.CompactionPolicy)
	}
	if started := compactions(t, submitText(t, session, "go")); len(started) != 1 || started[0].Reason != protocol.CompactionThreshold {
		t.Fatalf("the run after the update compacted %+v, want one threshold compaction", started)
	}
}

func TestMemoryRefusesALiveUpdateWholeAndKeepsItsPolicy(t *testing.T) {
	kept := protocol.CompactionPolicy{Kind: protocol.CompactionShare, SharePercent: 50}
	session, err := openWithPolicy(t, &kept)
	if err != nil {
		t.Fatal(err)
	}
	off := &protocol.CompactionPolicy{Kind: protocol.CompactionOff}
	_, _, err = updater(t, session).UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session-1", ReasoningLevel: protocol.ReasoningHigh, CompactionPolicy: off})
	var refusal *adapter.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureSessionReasoning || refusal.Field != "reasoning_level" {
		t.Fatalf("an update with a reasoning level answered %v, want unsupported_feature naming it", err)
	}
	_, _, err = updater(t, session).UpdateSettings(context.Background(), protocol.SessionSettingsUpdateRequest{SessionID: "session-1", CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionTokens}})
	if !errors.As(err, &refusal) || refusal.Reason != adapter.ControlUnsatisfiable {
		t.Fatalf("a zero token count answered %v, want an unsatisfiable refusal", err)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.CompactionPolicy == nil || *state.CompactionPolicy != kept {
		t.Fatalf("policy after two refusals = %+v, want %+v", state.CompactionPolicy, kept)
	}
}

func TestMemoryCompactsForOverflowOnceTheHistoryOutgrowsTheWindowWhateverThePolicy(t *testing.T) {
	overflowing := strings.Repeat("abcd", 8200)
	for _, policy := range []*protocol.CompactionPolicy{
		{Kind: protocol.CompactionOff},
		{Kind: protocol.CompactionTokens, Tokens: 100000},
		{Kind: protocol.CompactionShare, SharePercent: 1},
	} {
		session, err := openWithPolicy(t, policy)
		if err != nil {
			t.Fatal(err)
		}
		started := compactions(t, submitText(t, session, overflowing))
		if len(started) != 1 || started[0].Reason != protocol.CompactionOverflow {
			t.Fatalf("policy %+v compacted %+v, want one overflow compaction", policy, started)
		}
	}
	session, err := openWithPolicy(t, &protocol.CompactionPolicy{Kind: protocol.CompactionOff})
	if err != nil {
		t.Fatal(err)
	}
	if started := compactions(t, submitText(t, session, strings.Repeat("abcd", 8000))); len(started) != 0 {
		t.Fatalf("a history inside the window compacted under off: %+v", started)
	}
}
