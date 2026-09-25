package adaptertest

import (
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/validation"
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
	strict, err := ProtocolTrace(protocol.MessageSubmitRequest{SessionID: admission.SessionID, Delivery: admission.RequestedDelivery, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("adaptertest")}}}, admission, descriptor, events)
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

func TestSynthesizedSubmitIsRefusedWhenTheDescriptorOffersAControl(t *testing.T) {
	admission := protocol.MessageSubmitResponse{SessionID: "session", RequestedDelivery: protocol.DeliveryAuto, RunID: "run"}
	for _, key := range []string{protocol.FeatureInstructions, protocol.FeatureModelSelection, protocol.FeatureStructuredOutput} {
		descriptor := adapter.Descriptor{CapabilityRevision: "test-revision", Capabilities: protocol.CapabilityDescriptor{Features: map[string]protocol.FeatureSupport{key: {Level: protocol.SupportEmulated}}}}
		if _, err := protocolTraceWith(nil, admission, descriptor, nil, nil, false); err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("an offered %s was synthesized away: %v", key, err)
		}
	}
	for _, level := range []protocol.SupportLevel{protocol.SupportNative, protocol.SupportEmulated, protocol.SupportDegraded} {
		descriptor := adapter.Descriptor{CapabilityRevision: "test-revision", Capabilities: protocol.CapabilityDescriptor{Features: map[string]protocol.FeatureSupport{protocol.FeatureToolSelection: {Level: level}}}}
		if _, err := protocolTraceWith(nil, admission, descriptor, nil, nil, false); err == nil || !strings.Contains(err.Error(), protocol.FeatureToolSelection) {
			t.Fatalf("a %s control was synthesized away: %v", level, err)
		}
		request := protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, ToolChoice: []byte(`{"disallowed":["x"]}`), Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}}
		trace, err := protocolTraceWith(&request, admission, descriptor, nil, nil, false)
		if err != nil || !strings.Contains(string(trace), `"tool_choice":{"disallowed":["x"]}`) {
			t.Fatalf("the caller's submit did not reach the trace: %v %s", err, trace)
		}
	}
	unavailable := adapter.Descriptor{CapabilityRevision: "test-revision", Capabilities: protocol.CapabilityDescriptor{Features: map[string]protocol.FeatureSupport{protocol.FeatureToolSelection: {Level: protocol.SupportUnavailable}}}}
	if _, err := protocolTraceWith(nil, admission, unavailable, nil, nil, false); err != nil {
		t.Fatalf("an unavailable control refused the synthesized submit: %v", err)
	}
}
