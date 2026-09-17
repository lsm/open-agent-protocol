package adaptertest

import (
	"bytes"
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

func AssertRunTrace(t testing.TB, admission protocol.MessageSubmitResponse, revision string, envelopes []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, adapter.Descriptor{}, revision, envelopes, false)
}

func AssertProtocolValid(t testing.TB, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, adapter.Descriptor{}, "", events, false)
}

func AssertProtocolValidWithDescriptor(t testing.TB, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, descriptor, descriptor.CapabilityRevision, events, false)
}

func AssertProtocolValidWithSubmit(t testing.TB, request protocol.MessageSubmitRequest, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) {
	t.Helper()
	assertRunInvariants(t, admission, descriptor.CapabilityRevision, events)
	trace, err := protocolTraceWith(&request, admission, descriptor, nil, events, false)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(trace, "adaptertest"); !result.Valid() {
		t.Fatalf("adapter trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, trace)
	}
}

func AssertProtocolValidWithCancellation(t testing.TB, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) {
	t.Helper()
	assertProtocolValid(t, admission, descriptor, descriptor.CapabilityRevision, events, true)
}

func AssertProtocolValidWithCatalog(t testing.TB, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, request protocol.ToolsListRequest, catalog adapter.ToolCatalog, events []protocol.Envelope) {
	t.Helper()
	assertRunInvariants(t, admission, descriptor.CapabilityRevision, events)
	trace, err := protocolTraceWith(nil, admission, descriptor, &servedCatalog{request: request, catalog: catalog}, events, false)
	if err != nil {
		t.Fatal(err)
	}
	if result := validation.MustNew().ValidateBytes(trace, "adaptertest"); !result.Valid() {
		t.Fatalf("adapter trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, trace)
	}
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

type QueuedSubmission struct {
	Request   protocol.MessageSubmitRequest
	Admission protocol.MessageSubmitResponse
	Cancelled bool
}

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

func AssertRunEvents(t testing.TB, admission protocol.MessageSubmitResponse, revision string, envelopes []protocol.Envelope) {
	t.Helper()
	assertRunInvariants(t, admission, revision, envelopes)
}

func assertRunInvariants(t testing.TB, admission protocol.MessageSubmitResponse, revision string, envelopes []protocol.Envelope) {
	t.Helper()
	if !admission.Accepted || admission.RunID == "" {
		t.Fatalf("invalid admission: %+v", admission)
	}

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

func isControlExchange(typ protocol.EnvelopeType) bool {
	switch typ {
	case protocol.TypeCapabilitiesRequest, protocol.TypeCapabilitiesResponse,
		protocol.TypeSessionMessageSubmitRequest, protocol.TypeSessionMessageSubmitResponse,
		protocol.TypeRunCancelRequest, protocol.TypeRunCancelResponse,
		protocol.TypeActionPermissionResolveRequest, protocol.TypeActionPermissionResolveResponse,
		protocol.TypeUserInputResolveRequest, protocol.TypeUserInputResolveResponse,
		protocol.TypeUserInputCancelRequest, protocol.TypeUserInputCancelResponse,
		protocol.TypeSessionStateRequest, protocol.TypeSessionStateResponse:
		return true
	}
	return false
}

func StateExchange(state protocol.SessionState) ([]protocol.Envelope, error) {
	request, err := protocol.NewEnvelope(protocol.TypeSessionStateRequest, "state-request", protocol.SessionStateRequest{SessionID: state.SessionID})
	if err != nil {
		return nil, err
	}
	request.SessionID = state.SessionID
	response, err := protocol.NewEnvelope(protocol.TypeSessionStateResponse, "state-response", state)
	if err != nil {
		return nil, err
	}
	response.SessionID = state.SessionID
	response.InReplyTo = request.ID
	return []protocol.Envelope{request, response}, nil
}

func SpliceAfter(t testing.TB, events []protocol.Envelope, after protocol.EnvelopeID, exchange []protocol.Envelope) []protocol.Envelope {
	t.Helper()
	for index, envelope := range events {
		if envelope.ID != after {
			continue
		}
		head := append([]protocol.Envelope(nil), events[:index+1]...)
		return append(append(head, exchange...), events[index+1:]...)
	}
	t.Fatalf("no envelope %s to splice a state read after", after)
	return nil
}

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

func ProtocolTrace(admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) ([]byte, error) {
	return protocolTrace(admission, descriptor, events, false)
}

func ProtocolTraceWithCancellation(admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope) ([]byte, error) {
	return protocolTrace(admission, descriptor, events, true)
}

func protocolTrace(admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, events []protocol.Envelope, cancelled bool) ([]byte, error) {
	return protocolTraceWith(nil, admission, descriptor, nil, events, cancelled)
}

type servedCatalog struct {
	request protocol.ToolsListRequest
	catalog adapter.ToolCatalog
}

func protocolTraceWith(submitted *protocol.MessageSubmitRequest, admission protocol.MessageSubmitResponse, descriptor adapter.Descriptor, served *servedCatalog, events []protocol.Envelope, cancelled bool) ([]byte, error) {
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
	if served != nil {

		listRequest, err := protocol.NewEnvelope(protocol.TypeActionToolsListRequest, "tools-request", served.request)
		if err != nil {
			return nil, err
		}
		listRequest.SessionID, listRequest.CapabilityRevision = admission.SessionID, served.catalog.Revision
		listResponse, err := protocol.NewEnvelope(protocol.TypeActionToolsListResponse, "tools-response", served.catalog.Tools)
		if err != nil {
			return nil, err
		}
		listResponse.SessionID, listResponse.InReplyTo, listResponse.CapabilityRevision = admission.SessionID, listRequest.ID, served.catalog.Revision
		trace = append(trace, listRequest, listResponse)
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

func AssertToolCatalog(t testing.TB, descriptor adapter.Descriptor, open protocol.SessionOpenRequest, request protocol.ToolsListRequest, catalog adapter.ToolCatalog) {
	t.Helper()
	trace, err := ToolCatalogTrace(descriptor, open, request, catalog)
	if err != nil {
		t.Fatalf("assemble catalog trace: %v", err)
	}
	result := validation.MustNew().Validate(bytes.NewReader(trace), "adaptertest-catalog")
	if !result.Valid() {
		t.Fatalf("served catalog is not protocol-valid:\n%s\ntrace: %s", FormatDiagnostics(result), trace)
	}
}

func ToolCatalogTrace(descriptor adapter.Descriptor, open protocol.SessionOpenRequest, request protocol.ToolsListRequest, catalog adapter.ToolCatalog) ([]byte, error) {
	revision := descriptor.CapabilityRevision
	var trace []protocol.Envelope
	capabilitiesRequest, err := protocol.NewEnvelope(protocol.TypeCapabilitiesRequest, "capabilities-request", protocol.CapabilitiesRequest{})
	if err != nil {
		return nil, err
	}
	capabilities, err := protocol.NewEnvelope(protocol.TypeCapabilitiesResponse, "capabilities-response", descriptor.Capabilities)
	if err != nil {
		return nil, err
	}
	capabilities.InReplyTo, capabilities.CapabilityRevision = capabilitiesRequest.ID, revision
	trace = append(trace, capabilitiesRequest, capabilities)
	session := request.SessionID
	if session == "" {
		session = catalog.Tools.SessionID
	}
	if len(open.ToolSources) > 0 {
		open.SessionID = session
		openRequest, err := protocol.NewEnvelope(protocol.TypeSessionOpenRequest, "open-request", open)
		if err != nil {
			return nil, err
		}
		openRequest.SessionID, openRequest.CapabilityRevision = session, revision

		declared := descriptor.Capabilities.EffectiveSources()
		sources := append([]protocol.ToolSourceDescriptor(nil), declared...)

		served, _ := indexSources(catalog.Tools.Sources)
		for _, attachment := range open.ToolSources {
			if published, ok := served[attachment.ID]; ok {
				sources = append(sources, published)
				continue
			}
			sources = append(sources, attachment.Descriptor())
		}
		opened, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, "open-response", protocol.SessionOpenResponse{SessionID: session, Status: protocol.SessionIdle, Sources: sources})
		if err != nil {
			return nil, err
		}
		opened.SessionID, opened.InReplyTo, opened.CapabilityRevision = session, openRequest.ID, revision
		trace = append(trace, openRequest, opened)
	}
	listRequest, err := protocol.NewEnvelope(protocol.TypeActionToolsListRequest, "tools-request", request)
	if err != nil {
		return nil, err
	}
	listRequest.SessionID, listRequest.CapabilityRevision = session, revision
	listResponse, err := protocol.NewEnvelope(protocol.TypeActionToolsListResponse, "tools-response", catalog.Tools)
	if err != nil {
		return nil, err
	}

	listResponse.SessionID, listResponse.InReplyTo, listResponse.CapabilityRevision = catalog.Tools.SessionID, listRequest.ID, catalog.Revision
	return json.Marshal(append(trace, listRequest, listResponse))
}

func indexSources(sources []protocol.ToolSourceDescriptor) (map[string]protocol.ToolSourceDescriptor, string) {
	indexed := make(map[string]protocol.ToolSourceDescriptor, len(sources))
	duplicate := ""
	for _, source := range sources {
		if _, seen := indexed[source.ID]; seen {
			if duplicate == "" {
				duplicate = source.ID
			}
			continue
		}
		indexed[source.ID] = source
	}
	return indexed, duplicate
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
