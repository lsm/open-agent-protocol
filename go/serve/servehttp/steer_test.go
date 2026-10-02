package servehttp

import (
	"context"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type settlingSteerAdapter struct{}

func (a *settlingSteerAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "settling.steer", Name: "Settling steer", Version: protocol.Version},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
			Features: map[string]protocol.FeatureSupport{
				protocol.FeatureDeliverySteer: {Level: protocol.SupportEmulated, Reason: "a steer settles inside the submit"},
			},
		},
		CapabilityRevision: "settling-steer-v1",
	}, nil
}

func (a *settlingSteerAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return &settlingSteerSession{stream: make(chan base.Result, 8)}, nil
}

type settlingSteerSession struct {
	stream chan base.Result
	run    protocol.RunID
}

func (s *settlingSteerSession) Submit(_ context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if submit.Request.Delivery == protocol.DeliverySteer {
		sequence := uint64(2)
		envelope, err := protocol.NewEnvelope(protocol.TypeRunSteerApplied, "event-steer", protocol.RunSteerAppliedPayload{
			SessionID: "settle", RunID: s.run, SubmissionID: "sub-steer", RequestID: submit.EnvelopeID,
			MessageIDs: []protocol.MessageID{"m-2"}, Boundary: protocol.SteerImmediate,
		})
		if err != nil {
			return protocol.MessageSubmitResponse{}, nil, err
		}
		envelope.SessionID, envelope.RunID, envelope.Sequence = "settle", s.run, &sequence
		s.stream <- base.Result{Envelope: envelope}
		boundary := uint64(1)
		return protocol.MessageSubmitResponse{
			SessionID: "settle", Accepted: true, SubmissionID: "sub-steer",
			RequestedDelivery: protocol.DeliverySteer, EffectiveDelivery: protocol.EffectiveDeliverySteer,
			Admission: protocol.AdmissionSteered, RunID: s.run, Status: protocol.RunRunning,
			TargetSequence: &boundary, MessageIDs: []protocol.MessageID{"m-2"},
		}, nil, nil
	}
	s.run = "run-1"
	sequence := uint64(1)
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStarted, "event-start", protocol.RunStartedPayload{
		SessionID: "settle", RunID: "run-1", Status: protocol.RunRunning,
	})
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	envelope.SessionID, envelope.RunID, envelope.Sequence = "settle", "run-1", &sequence
	s.stream <- base.Result{Envelope: envelope}
	return protocol.MessageSubmitResponse{
		SessionID: "settle", Accepted: true, SubmissionID: "sub-start",
		RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
		Admission: protocol.AdmissionStarted, RunID: "run-1", Status: protocol.RunRunning,
		MessageIDs: []protocol.MessageID{"m-1"},
	}, s.stream, nil
}

func (s *settlingSteerSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "settle", Status: protocol.SessionRunning, ActiveRunID: s.run}, nil
}
func (s *settlingSteerSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *settlingSteerSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: "settle", Accepted: true, Status: protocol.RunCancelling}, nil
}
func (s *settlingSteerSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}
func (s *settlingSteerSession) Close(context.Context) error { return nil }

var _ base.Adapter = (*settlingSteerAdapter)(nil)
var _ base.Session = (*settlingSteerSession)(nil)

func TestASettlingSteerReachesTheSubscriberOnce(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("settling", &settlingSteerAdapter{}); err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})
	openSession(t, server, "settling", "settle")

	stream := connectSSE(t, server, "/sessions/settle/events", "")
	settled := make(chan protocol.Envelope, 1)
	started := make(chan struct{})
	go func() {
		seen := 0
		for {
			envelope := stream.envelope()
			if envelope.Type == protocol.TypeRunStarted && seen == 0 {
				seen = 1
				close(started)
				continue
			}
			if envelope.Type == protocol.TypeRunSteerApplied {
				settled <- envelope
				return
			}
		}
	}()

	startRequest := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-start",
		protocol.MessageSubmitRequest{
			SessionID: "settle", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "settle", "", "")
	if status, response := postEnvelope(t, server, "/sessions/settle/submit", startRequest); status != 200 {
		t.Fatalf("start status %d: %+v", status, response)
	}
	select {
	case <-started:
	case <-time.After(testTimeout):
		t.Fatal("the target run's stream never reached the subscriber")
	}

	steerRequest := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-steer",
		protocol.MessageSubmitRequest{
			SessionID: "settle", Delivery: protocol.DeliverySteer, TargetRunID: "run-1",
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("adjust")}},
		}, "settle", "", "")
	status, response := postEnvelope(t, server, "/sessions/settle/submit", steerRequest)
	if status != 200 {
		t.Fatalf("steer status %d: %+v", status, response)
	}
	var admission protocol.MessageSubmitResponse
	if err := response.DecodePayload(&admission); err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionSteered || admission.SubmissionID != "sub-steer" {
		t.Fatalf("steer admission = %+v", admission)
	}

	select {
	case envelope := <-settled:
		var applied protocol.RunSteerAppliedPayload
		if err := envelope.DecodePayload(&applied); err != nil {
			t.Fatal(err)
		}
		if applied.SubmissionID != "sub-steer" || applied.RequestID != "submit-steer" {
			t.Fatalf("settlement = %+v", applied)
		}
	case <-time.After(testTimeout):
		t.Fatal("the settlement never reached the subscriber, so the binding never lifted the gate")
	}
}
