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
