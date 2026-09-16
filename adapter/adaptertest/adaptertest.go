// Package adaptertest provides black-box assertions for OAP adapters.
package adaptertest

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"
)

const DefaultTimeout = time.Second

// Next receives one successful event or fails the test after the timeout.
func Next(t testing.TB, stream adapter.EventStream, timeout time.Duration) protocol.Envelope {
	t.Helper()
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	select {
	case result, ok := <-stream:
		if !ok {
			t.Fatal("adapter event stream closed")
		}
		if result.Error != nil {
			t.Fatalf("adapter event stream: %v", result.Error)
		}
		return result.Envelope
	case <-time.After(timeout):
		t.Fatal("timed out waiting for adapter event")
		return protocol.Envelope{}
	}
}

// Drain receives a stream through normal closure or fails on timeout or error.
func Drain(t testing.TB, stream adapter.EventStream, timeout time.Duration) []protocol.Envelope {
	t.Helper()
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	var envelopes []protocol.Envelope
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case result, ok := <-stream:
			if !ok {
				return envelopes
			}
			if result.Error != nil {
				t.Fatalf("adapter event stream: %v", result.Error)
			}
			envelopes = append(envelopes, result.Envelope)
		case <-timer.C:
			t.Fatal("timed out waiting for adapter stream closure")
			return nil
		}
	}
}

// AssertRunTrace checks scope, sequence, capability revision, terminality, and
// the executable OAP schema/state machine for an admitted run. The trace
// carries no capability descriptor, so it certifies only runs whose envelopes
// use no optional features.
func AssertRunTrace(t testing.TB, admission protocol.MessageSubmitResponse, revision string, envelopes []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, adapter.Descriptor{}, revision, envelopes, false)
}

// AssertProtocolValid runs the executable OAP schema and state machine over the
// canonical trace of an admitted run without a capability descriptor, so the
// envelopes must use no optional features. Traces with tools, interactions, or
// non-auto delivery need AssertProtocolValidWithDescriptor.
func AssertProtocolValid(t testing.TB, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, adapter.Descriptor{}, "", events, false)
}

// AssertProtocolValidWithDescriptor prefixes the canonical trace with the
// adapter's live capability descriptor exchange so optional-feature envelopes
// (tools, permissions, user input, non-auto delivery) are certified against
// what the adapter actually advertises. The trace carries no cancellation
// exchange: a run.cancelled terminal without caller evidence is reported as
// the unsolicited cancellation it is.
func AssertProtocolValidWithDescriptor(t testing.TB, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, descriptor, descriptor.CapabilityRevision, events, false)
}

// AssertProtocolValidWithSubmit certifies a run against the submission that
// actually admitted it, rather than the neutral one the other assertions
// synthesize. A per-submit run control is judged on the correlated response,
// so a trace whose submit request does not carry the controls the caller sent
// exercises none of those rules: this is what an adapter test uses when the
// point is that a control was applied, refused, or gated.
func AssertProtocolValidWithSubmit(t testing.TB, request protocol.MessageSubmitRequest, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) {
	t.Helper()
	assertRunInvariants(t, admission, descriptor.CapabilityRevision, events)
	trace, err := protocolTraceWith(&request, admission, descriptor, events, false)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(trace, "adaptertest"); !result.Valid() {
		t.Fatalf("adapter trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, trace)
	}
}

// AssertProtocolValidWithCancellation additionally splices the harness-side
// cancel exchange for a cancellation the caller actually issued and the
// adapter accepted, so a legitimately cancelled run validates while an
// unsolicited run.cancelled still fails the state machine.
func AssertProtocolValidWithCancellation(t testing.TB, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, descriptor, descriptor.CapabilityRevision, events, true)
}

func assertProtocolValid(t testing.TB, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, revision string, events []protocol.Envelope, cancelled bool) {
	t.Helper()
	assertRunInvariants(t, admission, revision, events)
	trace, err := protocolTrace(admission, descriptor, events, cancelled)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(trace, "adaptertest"); !result.Valid() {
		t.Fatalf("adapter trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, trace)
	}
}

// QueuedSubmission is one submission in a session that admits more than one
// nonterminal run: the request the caller made and the admission it received.
// Cancelled marks a run the caller cancelled and the adapter accepted, so the
// harness-side cancel exchange is spliced for it.
type QueuedSubmission struct {
	Request   protocol.MessageSubmitRequest
	Admission protocol.MessageSubmitResponse
	Cancelled bool
}

// AssertProtocolValidQueued certifies a session whose trace carries more than
// one run. The single-run assertions cannot: they synthesize one submission
// and check one contiguous sequence domain, while a queued session has a
// reservation admitted beside a started run and two domains interleaved only
// by the one exception the ordering rule makes.
//
// Every submission's request and response is spliced ahead of the events in
// admission order, since the reservation is admitted while the started run is
// still nonterminal, and each run's own sequence is checked inside its domain.
func AssertProtocolValidQueued(t testing.TB, submissions []QueuedSubmission, descriptor adapter.Descriptor, events []protocol.Envelope) {
	t.Helper()
	assertQueuedInvariants(t, submissions, descriptor.CapabilityRevision, events)
	trace, err := queuedTrace(submissions, descriptor, events)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(trace, "adaptertest"); !result.Valid() {
		t.Fatalf("adapter trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, trace)
	}
}

// assertQueuedInvariants checks per-run scope, contiguity, revision, and
// terminality across a multi-run trace.
func assertQueuedInvariants(t testing.TB, submissions []QueuedSubmission, revision string, events []protocol.Envelope) {
	t.Helper()
	next := map[protocol.RunID]uint64{}
	owner := map[protocol.RunID]protocol.SessionID{}
	terminals := map[protocol.RunID]int{}
	for _, submission := range submissions {
		if !submission.Admission.Accepted || submission.Admission.RunID == "" {
			t.Fatalf("invalid admission: %+v", submission.Admission)
		}
		next[submission.Admission.RunID] = 1
		owner[submission.Admission.RunID] = submission.Admission.SessionID
	}
	for index, envelope := range events {
		if isControlExchange(envelope.Type) {
			continue
		}
		session, known := owner[envelope.RunID]
		if !known {
			t.Fatalf("event %d names run %s, which no submission admitted", index, envelope.RunID)
		}
		if envelope.SessionID != session {
			t.Fatalf("event %d scope: %+v", index, envelope)
		}
		if envelope.Sequence == nil || *envelope.Sequence != next[envelope.RunID] {
			t.Fatalf("event %d sequence: got %v want %d for run %s", index, envelope.Sequence, next[envelope.RunID], envelope.RunID)
		}
		next[envelope.RunID]++
		if revision != "" && envelope.CapabilityRevision != revision {
			t.Fatalf("event %d capability revision: got %q want %q", index, envelope.CapabilityRevision, revision)
		}
		if isTerminal(envelope.Type) {
			terminals[envelope.RunID]++
		} else if terminals[envelope.RunID] > 0 {
			t.Fatalf("event %d follows run %s's terminal", index, envelope.RunID)
		}
	}
	for run := range next {
		if terminals[run] != 1 {
			t.Fatalf("run %s has %d terminal events", run, terminals[run])
		}
	}
}

func queuedTrace(submissions []QueuedSubmission, descriptor adapter.Descriptor, events []protocol.Envelope) ([]byte, error) {
	var trace []protocol.Envelope
	if descriptor.CapabilityRevision != "" {
		request, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
		if err != nil {
			return nil, err
		}
		response, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
		if err != nil {
			return nil, err
		}
		response.InReplyTo = request.ID
		response.CapabilityRevision = descriptor.CapabilityRevision
		trace = append(trace, request, response)
	}
	for index, submission := range submissions {
		submit, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, protocol.EnvelopeID(fmt.Sprintf("submit-request-%d", index+1)), submission.Request)
		if err != nil {
			return nil, err
		}
		submit.SessionID = submission.Admission.SessionID
		response, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, protocol.EnvelopeID(fmt.Sprintf("submit-response-%d", index+1)), submission.Admission)
		if err != nil {
			return nil, err
		}
		response.SessionID = submission.Admission.SessionID
		response.InReplyTo = submit.ID
		if descriptor.CapabilityRevision != "" {
			submit.CapabilityRevision = descriptor.CapabilityRevision
			response.CapabilityRevision = descriptor.CapabilityRevision
		}
		trace = append(trace, submit, response)
	}
	// A cancel exchange is run-scoped and exempt from the ordering rule, so
	// it sits directly ahead of the terminal it settles.
	for index, submission := range submissions {
		if !submission.Cancelled {
			continue
		}
		run := submission.Admission.RunID
		request, err := protocol.NewEnvelope(protocol.TypeRunCancelRequest, protocol.EnvelopeID(fmt.Sprintf("cancel-request-%d", index+1)), protocol.RunCancelRequest{SessionID: submission.Admission.SessionID, RunID: run})
		if err != nil {
			return nil, err
		}
		request.SessionID, request.RunID = submission.Admission.SessionID, run
		ack, err := protocol.NewEnvelope(protocol.TypeRunCancelResponse, protocol.EnvelopeID(fmt.Sprintf("cancel-response-%d", index+1)), protocol.RunCancelResponse{SessionID: submission.Admission.SessionID, RunID: run, Accepted: true, Status: protocol.RunCancelling})
		if err != nil {
			return nil, err
		}
		ack.SessionID, ack.RunID, ack.InReplyTo = submission.Admission.SessionID, run, request.ID
		cut := runCancelCut(events, run)
		rest := append([]protocol.Envelope(nil), events[cut:]...)
		events = append(append(append([]protocol.Envelope(nil), events[:cut]...), request, ack), rest...)
	}
	trace = append(trace, events...)
	return json.Marshal(trace)
}

// runCancelCut reports where a cancel exchange for one run belongs: directly
// ahead of that run's first cancelling status update, otherwise ahead of its
// terminal, otherwise at the end.
func runCancelCut(events []protocol.Envelope, run protocol.RunID) int {
	for index, event := range events {
		if event.RunID != run || event.Type != protocol.TypeRunStatusUpdated {
			continue
		}
		var payload protocol.RunStatusUpdatedPayload
		if err := event.DecodePayload(&payload); err == nil && payload.Status == protocol.RunCancelling {
			return index
		}
	}
	for index, event := range events {
		if event.RunID == run && isTerminal(event.Type) {
			return index
		}
	}
	return len(events)
}

// AssertRunEvents checks adapter-owned invariants without constructing control
// requests. Use it when a caller supplies its own complete canonical trace.
func AssertRunEvents(t testing.TB, admission protocol.MessageSubmitResponse, revision string, envelopes []protocol.Envelope) {
	t.Helper()
	assertRunInvariants(t, admission, revision, envelopes)
}

func assertRunInvariants(t testing.TB, admission protocol.MessageSubmitResponse, revision string, envelopes []protocol.Envelope) {
	t.Helper()
	if !admission.Accepted || admission.RunID == "" {
		t.Fatalf("invalid admission: %+v", admission)
	}
	// Harness-fabricated control exchanges (interaction resolves a caller
	// spliced into the stream) carry no adapter sequence and no adapter-owned
	// identity; only the validator judges those.
	last := -1
	for index, envelope := range envelopes {
		if !isControlExchange(envelope.Type) {
			last = index
		}
	}
	terminals := 0
	emitted := 0
	for index, envelope := range envelopes {
		if isControlExchange(envelope.Type) {
			continue
		}
		emitted++
		if envelope.SessionID != admission.SessionID || envelope.RunID != admission.RunID {
			t.Fatalf("event %d scope: %+v", index, envelope)
		}
		wantSequence := uint64(emitted)
		if envelope.Sequence == nil || *envelope.Sequence != wantSequence {
			t.Fatalf("event %d sequence: got %v want %d", index, envelope.Sequence, wantSequence)
		}
		if revision != "" && envelope.CapabilityRevision != revision {
			t.Fatalf("event %d capability revision: got %q want %q", index, envelope.CapabilityRevision, revision)
		}
		if isTerminal(envelope.Type) {
			terminals++
			if index != last {
				t.Fatalf("terminal event at index %d of %d", index, len(envelopes))
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("got %d terminal events", terminals)
	}
}

// isControlExchange reports whether an envelope type belongs to a harness-side
// request/response exchange rather than the adapter's event stream. Adapters
// publish events only, so a control envelope in a validated stream was
// fabricated by the test harness.
func isControlExchange(typ protocol.EnvelopeType) bool {
	switch typ {
	case protocol.TypeCapabilitiesRequest, protocol.TypeCapabilitiesResponse,
		protocol.TypeSessionMessageSubmitRequest, protocol.TypeSessionMessageSubmitResponse,
		protocol.TypeRunCancelRequest, protocol.TypeRunCancelResponse,
		protocol.TypeActionPermissionResolveRequest, protocol.TypeActionPermissionResolveResponse,
		protocol.TypeUserInputResolveRequest, protocol.TypeUserInputResolveResponse,
		protocol.TypeUserInputCancelRequest, protocol.TypeUserInputCancelResponse:
		return true
	}
	return false
}

// AssertInitialState verifies that opening a session preserves the requested ID
// and creates no active run.
func AssertInitialState(t testing.TB, implementation adapter.Adapter, request adapter.OpenRequest) adapter.Session {
	t.Helper()
	session, err := implementation.Open(context.Background(), request)
	if err != nil {
		t.Fatalf("open adapter session: %v", err)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatalf("read initial session state: %v", err)
	}
	if state.SessionID != request.SessionID || state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
		t.Fatalf("initial session state: %+v", state)
	}
	return session
}

// ProtocolTrace serializes the canonical wire trace for an adapter-observed
// run: the capability exchange when the descriptor names a revision, the
// submit/admission exchange that admitted the run, and the adapter's
// envelopes. The bytes are valid input for
// validation.Validator.ValidateBytes, so test assertions and non-test
// binaries (oap check's demo) assemble exactly one trace shape. The trace
// carries no cancellation exchange: a run.cancelled terminal without caller
// evidence is left for the state machine to report.
func ProtocolTrace(admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) ([]byte, error) {
	return protocolTrace(admission, descriptor, events, false)
}

// ProtocolTraceWithCancellation additionally splices the harness-side cancel
// exchange for a cancellation the caller issued and the adapter accepted.
func ProtocolTraceWithCancellation(admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) ([]byte, error) {
	return protocolTrace(admission, descriptor, events, true)
}

func protocolTrace(admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope, cancelled bool) ([]byte, error) {
	return protocolTraceWith(nil, admission, descriptor, events, cancelled)
}

// protocolTraceWith assembles the canonical trace. When submitted is nil the
// submission is synthesized as a neutral one carrying no controls; when it is
// given, the caller's own request is what the admission answers, so the
// validator judges the controls it actually carried.
func protocolTraceWith(submitted *protocol.MessageSubmitRequest, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope, cancelled bool) ([]byte, error) {
	var trace []protocol.Envelope
	if descriptor.CapabilityRevision != "" {
		request, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
		if err != nil {
			return nil, err
		}
		response, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
		if err != nil {
			return nil, err
		}
		response.InReplyTo = request.ID
		response.CapabilityRevision = descriptor.CapabilityRevision
		trace = append(trace, request, response)
	}
	request := protocol.MessageSubmitRequest{
		SessionID: admission.SessionID,
		Delivery:  admission.RequestedDelivery,
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("adaptertest")}},
	}
	if submitted != nil {
		request = *submitted
	}
	submit, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, "submit-request", request)
	if err != nil {
		return nil, err
	}
	submit.SessionID = admission.SessionID
	response, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, "submit-response", admission)
	if err != nil {
		return nil, err
	}
	response.SessionID = admission.SessionID
	response.InReplyTo = submit.ID
	if descriptor.CapabilityRevision != "" {
		// A non-auto delivery or a per-submit run control makes the submit
		// request itself an optional-feature envelope: it must cite the
		// active descriptor revision, and the response must repeat it.
		submit.CapabilityRevision = descriptor.CapabilityRevision
		response.CapabilityRevision = descriptor.CapabilityRevision
	}
	trace = append(trace, submit, response)
	if cancelled {
		request, err := protocol.NewEnvelope(protocol.TypeRunCancelRequest, "cancel-request", protocol.RunCancelRequest{SessionID: admission.SessionID, RunID: admission.RunID})
		if err != nil {
			return nil, err
		}
		request.SessionID, request.RunID = admission.SessionID, admission.RunID
		ack, err := protocol.NewEnvelope(protocol.TypeRunCancelResponse, "cancel-response", protocol.RunCancelResponse{SessionID: admission.SessionID, RunID: admission.RunID, Accepted: true, Status: protocol.RunCancelling})
		if err != nil {
			return nil, err
		}
		ack.SessionID, ack.RunID, ack.InReplyTo = admission.SessionID, admission.RunID, request.ID
		cut := cancelExchangeCut(events)
		trace = append(trace, events[:cut]...)
		trace = append(trace, request, ack)
		trace = append(trace, events[cut:]...)
	} else {
		trace = append(trace, events...)
	}
	return json.Marshal(trace)
}

// cancelExchangeCut reports where a caller-issued cancel exchange belongs:
// ahead of the first cancelling status update when the adapter reported one,
// otherwise directly before the terminal event, and otherwise at the end of
// the stream. The state machine requires an accepted cancellation before a
// run.cancelled terminal; run.cancel.response also sets the cancelling
// status, and cancelling-to-cancelling is a legal identity transition, so
// either side of the adapter's own update is valid.
func cancelExchangeCut(events []protocol.Envelope) int {
	for index, event := range events {
		if event.Type != protocol.TypeRunStatusUpdated {
			continue
		}
		var payload protocol.RunStatusUpdatedPayload
		if err := event.DecodePayload(&payload); err == nil && payload.Status == protocol.RunCancelling {
			return index
		}
	}
	for index := len(events) - 1; index >= 0; index-- {
		switch events[index].Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			return index
		}
	}
	return len(events)
}

func AssertTypes(t testing.TB, envelopes []protocol.Envelope, want ...protocol.EnvelopeType) {
	t.Helper()
	if len(envelopes) != len(want) {
		t.Fatalf("got %d events, want %d: %+v", len(envelopes), len(want), envelopes)
	}
	for index := range want {
		if envelopes[index].Type != want[index] {
			t.Fatalf("event %d: got %s want %s", index, envelopes[index].Type, want[index])
		}
	}
}

func AssertDescriptor(t testing.TB, descriptor adapter.Descriptor) {
	t.Helper()
	if descriptor.CapabilityRevision == "" {
		t.Fatal("descriptor has no capability revision")
	}
	if descriptor.MaxActiveRunsPerSession < 1 {
		t.Fatalf("descriptor max active runs: %d", descriptor.MaxActiveRunsPerSession)
	}
	if descriptor.Journal.Replay != protocol.SupportUnavailable && descriptor.Journal.Capacity <= 0 {
		t.Fatalf("replayable journal has invalid capacity: %+v", descriptor.Journal)
	}
	for name, support := range descriptor.Capabilities.Features {
		switch support.Level {
		case protocol.SupportNative, protocol.SupportEmulated, protocol.SupportDegraded, protocol.SupportUnavailable:
		default:
			t.Fatalf("feature %q has invalid support level %q", name, support.Level)
		}
		if (support.Level == protocol.SupportDegraded || support.Level == protocol.SupportUnavailable) && support.Reason == "" {
			t.Fatalf("feature %q does not explain %s support", name, support.Level)
		}
	}
}

func isTerminal(typ protocol.EnvelopeType) bool {
	return typ == protocol.TypeRunCompleted || typ == protocol.TypeRunFailed || typ == protocol.TypeRunCancelled
}

func FormatDiagnostics(result validation.Result) string {
	if result.Valid() {
		return ""
	}
	return fmt.Sprint(result.Diagnostics)
}
