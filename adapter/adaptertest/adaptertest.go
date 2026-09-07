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
// the executable OAP schema/state machine for an admitted run.
func AssertRunTrace(t testing.TB, admission protocol.MessageSubmitResponse, revision string, envelopes []protocol.Envelope) {
	t.Helper()
	assertRunInvariants(t, admission, revision, envelopes)
	trace := canonicalTrace(t, admission, envelopes)
	result := validation.MustNew().ValidateBytes(trace, "adaptertest")
	if !result.Valid() {
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
	terminals := 0
	for index, envelope := range envelopes {
		if envelope.SessionID != admission.SessionID || envelope.RunID != admission.RunID {
			t.Fatalf("event %d scope: %+v", index, envelope)
		}
		wantSequence := uint64(index + 1)
		if envelope.Sequence == nil || *envelope.Sequence != wantSequence {
			t.Fatalf("event %d sequence: got %v want %d", index, envelope.Sequence, wantSequence)
		}
		if revision != "" && envelope.CapabilityRevision != revision {
			t.Fatalf("event %d capability revision: got %q want %q", index, envelope.CapabilityRevision, revision)
		}
		if isTerminal(envelope.Type) {
			terminals++
			if index != len(envelopes)-1 {
				t.Fatalf("terminal event at index %d of %d", index, len(envelopes))
			}
		}
	}
	if terminals != 1 {
		t.Fatalf("got %d terminal events", terminals)
	}
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

func canonicalTrace(t testing.TB, admission protocol.MessageSubmitResponse, events []protocol.Envelope) []byte {
	t.Helper()
	request, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitRequest, "adaptertest-submit", protocol.MessageSubmitRequest{
		SessionID: admission.SessionID,
		Delivery:  admission.RequestedDelivery,
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("adaptertest")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request.SessionID = admission.SessionID
	response, err := protocol.NewEnvelope(protocol.TypeSessionMessageSubmitResponse, "adaptertest-admission", admission)
	if err != nil {
		t.Fatal(err)
	}
	response.SessionID = admission.SessionID
	response.InReplyTo = request.ID
	trace := append([]protocol.Envelope{request, response}, events...)
	encoded, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
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
