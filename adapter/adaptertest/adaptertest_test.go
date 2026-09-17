package adaptertest

import (
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/validation"
)

func TestProtocolTraceRepeatsRevisionForNonAutoDelivery(t *testing.T) {
	descriptor := adapter.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint: protocol.EndpointDescriptor{ID: "adaptertest.fixture"},
			Features: map[string]protocol.FeatureSupport{
				"delivery.steer": {Level: protocol.SupportNative},
			},
		},
		CapabilityRevision:      "test-revision",
		MaxActiveRunsPerSession: 1,
	}
	admission := protocol.MessageSubmitResponse{
		SessionID:          "session",
		Accepted:           true,
		SubmissionID:       "submission",
		RequestedDelivery:  protocol.DeliverySteer,
		EffectiveDelivery:  protocol.DeliveryStart,
		DeliveryResolution: "session_idle",
		Admission:          protocol.AdmissionStarted,
		RunID:              "run",
		Status:             protocol.RunRunning,
	}
	started, err := protocol.NewEnvelope(protocol.TypeRunStarted, "started", protocol.RunStartedPayload{SessionID: admission.SessionID, RunID: admission.RunID, Status: protocol.RunRunning, StartedAtMS: 1})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := protocol.NewEnvelope(protocol.TypeRunCompleted, "completed", protocol.RunCompletedPayload{SessionID: admission.SessionID, RunID: admission.RunID, FinalResponse: protocol.Message{ID: "message", Role: protocol.RoleAssistant, Content: protocol.TextContent("done")}, StopReason: "end_turn"})
	if err != nil {
		t.Fatal(err)
	}
	events := []protocol.Envelope{started, completed}
	for index := range events {
		events[index].SessionID = admission.SessionID
		events[index].RunID = admission.RunID
		events[index].CapabilityRevision = descriptor.CapabilityRevision
		sequence := uint64(index + 1)
		events[index].Sequence = &sequence
	}
	AssertProtocolValidWithDescriptor(t, admission, descriptor, events)
}

func TestProtocolTraceRequiresCancelEvidence(t *testing.T) {
	descriptor := adapter.Descriptor{
		Capabilities:            protocol.CapabilityDescriptor{Endpoint: protocol.EndpointDescriptor{ID: "adaptertest.fixture"}},
		CapabilityRevision:      "test-revision",
		MaxActiveRunsPerSession: 1,
	}
	admission := protocol.MessageSubmitResponse{
		SessionID:          "session",
		Accepted:           true,
		SubmissionID:       "submission",
		RequestedDelivery:  protocol.DeliveryAuto,
		EffectiveDelivery:  protocol.DeliveryStart,
		DeliveryResolution: "session_idle",
		Admission:          protocol.AdmissionStarted,
		RunID:              "run",
		Status:             protocol.RunRunning,
	}
	started, err := protocol.NewEnvelope(protocol.TypeRunStarted, "started", protocol.RunStartedPayload{SessionID: admission.SessionID, RunID: admission.RunID, Status: protocol.RunRunning, StartedAtMS: 1})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := protocol.NewEnvelope(protocol.TypeRunCancelled, "cancelled", protocol.RunCancelledPayload{SessionID: admission.SessionID, RunID: admission.RunID, Reason: "adapter settled spontaneously"})
	if err != nil {
		t.Fatal(err)
	}
	events := []protocol.Envelope{started, cancelled}
	for index := range events {
		events[index].SessionID = admission.SessionID
		events[index].RunID = admission.RunID
		events[index].CapabilityRevision = descriptor.CapabilityRevision
		sequence := uint64(index + 1)
		events[index].Sequence = &sequence
	}
	strict, err := ProtocolTrace(admission, descriptor, events)
	if err != nil {
		t.Fatal(err)
	}
	result := validation.MustNew().ValidateBytes(strict, "adaptertest")
	if result.Valid() {
		t.Fatal("unsolicited run.cancelled validated without caller evidence")
	}
	unsolicited := false
	for _, diagnostic := range result.Diagnostics {
		if strings.Contains(diagnostic.Message, "requires accepted cancellation") {
			unsolicited = true
		}
	}
	if !unsolicited {
		t.Fatalf("missing unsolicited-cancellation diagnostic: %v", result.Diagnostics)
	}
	AssertProtocolValidWithCancellation(t, admission, descriptor, events)
}
