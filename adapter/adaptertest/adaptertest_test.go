package adaptertest

import (
	"testing"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// A non-auto delivery (steer, queue, btw) makes the fabricated submit request
// an optional-feature envelope: the validator requires it to cite the active
// descriptor revision and the response to repeat it, exactly like a real
// harness-side exchange.
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
