// Package adaptertest provides black-box assertions for OAP adapters.
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
	trace, err := protocolTraceWith(&request, admission, descriptor, nil, events, false)
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

// AssertProtocolValidWithCatalog certifies a run against the catalog the
// session had already served when the run happened, by splicing that exchange
// into the trace ahead of the submission.
//
// A call's `source` is a cross-reference, and what it may reference is the
// catalog in force: the session's own where one has been served under the
// active revision, and otherwise the descriptor's. A trace that drops the
// serve therefore judges the run against the wrong catalog — it would report a
// correctly attributed call as naming a source nothing declares, and would
// excuse an omitted attribution the served catalog obliged. Use this wherever
// the endpoint served a session catalog before the run; the plain assertion
// covers the other ordering, where only the descriptor has published anything.
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
	return protocolTraceWith(nil, admission, descriptor, nil, events, cancelled)
}

// protocolTraceWith assembles the canonical trace. When submitted is nil the
// submission is synthesized as a neutral one carrying no controls; when it is
// given, the caller's own request is what the admission answers, so the
// validator judges the controls it actually carried.
// servedCatalog is one catalog exchange that actually happened: the request as
// the caller sent it, and the catalog the endpoint answered with. The request
// is kept rather than synthesized because it carries the caller's own degraded
// opt-in, and a catalog requested without consent is its own diagnostic — a
// trace that dropped it would fail for a defect the endpoint never had.
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
		// The serve sits between the descriptor and the submission, which is
		// where it happened: the catalog it published is the one in force for
		// every call the run below emits.
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

// AssertToolCatalog runs one served catalog through the real validator: the
// descriptor that advertises the capability, the caller's own request, and the
// correlated response. It is what proves a catalog resolves — one source per
// id, one tool per name, every tool's source declared, and every attachment
// the open made still listed — rather than asserting those rules a second time
// in each adapter's tests.
//
// attached are the sources one session.open attached, spliced in as the open
// exchange the catalog is judged against; pass none for an endpoint-level
// catalog or a session that attached nothing.
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

// ToolCatalogTrace assembles the canonical catalog trace: the capabilities
// exchange, the open that attached the sources when there was one, and the
// list exchange.
//
// It takes the open request the caller actually made, not a list of
// attachments, because an open carries more than its attachments and every
// missing part convicted a conforming adapter. The synthesized open could not
// carry `allow_degraded_features`, so an endpoint advertising attachment as
// `degraded` failed here for `degraded_without_optin` however correctly its
// caller had consented — the helper reporting a defect the endpoint did not
// have, which is the worst thing a certifier can do.
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
		// The descriptor's declared sources are read across its layers, because
		// a valid descriptor may declare them under one alone and the validator
		// reads them that way. Reading the top level alone made this helper
		// expect a union it had itself truncated, and report the trace it
		// generated as invalid for an adapter using a shape the protocol
		// explicitly supports.
		declared := descriptor.Capabilities.EffectiveSources()
		sources := append([]protocol.ToolSourceDescriptor(nil), declared...)
		// An open response publishes what the session publishes, and an endpoint
		// may fill a member the attachment left blank. The served catalog is that
		// session's own description of the source, and every later snapshot and
		// catalog is held to what the open response published — exactly — so the
		// reconstructed response adopts the catalog's descriptor where it has
		// one. Publishing the bare attachment instead would convict an endpoint
		// that filled a display name at open, which is behaviour the unit invites.
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
	// The revision on the envelope is the one the catalog came back with, not
	// the descriptor's, so a listing served under a revision the adapter no
	// longer advertises is visible here rather than laundered into agreement.
	listResponse.SessionID, listResponse.InReplyTo, listResponse.CapabilityRevision = catalog.Tools.SessionID, listRequest.ID, catalog.Revision
	return json.Marshal(append(trace, listRequest, listResponse))
}

// indexSources indexes published descriptors by id, reporting the first
// duplicate. One id resolving to two descriptors is a defect the validator
// diagnoses on the catalog itself, so this keeps the first and leaves the
// diagnosis where it belongs.
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
