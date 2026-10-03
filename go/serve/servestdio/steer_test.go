package servestdio

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type settlingStdioAdapter struct{}

func (a *settlingStdioAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "settling.stdio", Name: "Settling stdio", Version: protocol.Version},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
			Features: map[string]protocol.FeatureSupport{
				protocol.FeatureOpenSubscribe: {Level: protocol.SupportNative, Reason: "the journal exists from the open"},
				protocol.FeatureDeliverySteer: {Level: protocol.SupportEmulated, Reason: "a steer settles behind its admission"},
			},
		},
		CapabilityRevision: "settling-stdio-v1",
	}, nil
}

func (a *settlingStdioAdapter) Open(_ context.Context, request base.OpenRequest) (base.Session, error) {
	return &settlingStdioSession{id: string(request.SessionID), stream: make(chan base.Result, 8)}, nil
}

type settlingStdioSession struct {
	id     string
	stream chan base.Result
}

func (s *settlingStdioSession) Submit(_ context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	if submit.Request.Delivery != protocol.DeliverySteer {
		return protocol.MessageSubmitResponse{}, nil, base.ErrInvalidSubmission
	}
	sequence := uint64(2)
	envelope, err := protocol.NewEnvelope(protocol.TypeRunSteerApplied, "event-steer", protocol.RunSteerAppliedPayload{
		SessionID: protocol.SessionID(s.id), RunID: "run-1", SubmissionID: "sub-steer", RequestID: submit.EnvelopeID,
		MessageIDs: []protocol.MessageID{"m-2"}, Boundary: protocol.SteerTurn,
	})
	if err != nil {
		return protocol.MessageSubmitResponse{}, nil, err
	}
	envelope.SessionID, envelope.RunID, envelope.Sequence = protocol.SessionID(s.id), "run-1", &sequence
	s.stream <- base.Result{Envelope: envelope}
	boundary := uint64(1)
	return protocol.MessageSubmitResponse{
		SessionID: protocol.SessionID(s.id), Accepted: true, SubmissionID: "sub-steer",
		RequestedDelivery: protocol.DeliverySteer, EffectiveDelivery: protocol.EffectiveDeliverySteer,
		Admission: protocol.AdmissionSteered, RunID: "run-1", Status: protocol.RunRunning,
		TargetSequence: &boundary, MessageIDs: []protocol.MessageID{"m-2"},
	}, s.stream, nil
}

func (s *settlingStdioSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: protocol.SessionID(s.id), Status: protocol.SessionRunning, ActiveRunID: "run-1"}, nil
}

func (s *settlingStdioSession) Resolve(context.Context, base.InteractionResolution) error {
	return nil
}

func (s *settlingStdioSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: protocol.SessionID(s.id), Accepted: true, Status: protocol.RunCancelling}, nil
}

func (s *settlingStdioSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}

func (s *settlingStdioSession) Close(context.Context) error { return nil }

var _ base.Adapter = (*settlingStdioAdapter)(nil)
var _ base.Session = (*settlingStdioSession)(nil)

func TestACompoundOpenSteerIsReleasedByItsOpenResponse(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("settling", &settlingStdioAdapter{}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{StreamQueue: 16})
	f := startFrontend(t, hub, Options{})

	open := protocol.SessionOpenRequest{
		SessionID: "stdio-steer",
		Subscribe: true,
		Message: &protocol.OpenMessage{
			Delivery:    protocol.DeliverySteer,
			TargetRunID: "run-1",
			Messages:    []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("adjust")}},
		},
	}
	f.send(fmt.Sprintf(`{"id":1,"op":"open","adapter":"settling","request":%s}`,
		requestEnvelope(t, "req-open", protocol.TypeSessionOpenRequest, open, "", "")))
	response := f.expectResponse(1)
	requireOK(t, response)

	var opened protocol.Envelope
	if err := json.Unmarshal(response.Result, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.SessionID != "stdio-steer" {
		t.Fatalf("the open response names session %q, want stdio-steer", opened.SessionID)
	}

	for attempt := 0; attempt < 4; attempt++ {
		line := f.line()
		var signal signalLine
		if err := json.Unmarshal([]byte(line), &signal); err != nil {
			t.Fatalf("event line %q: %v", line, err)
		}
		if signal.Event != signalEnvelope {
			continue
		}
		var settlement protocol.Envelope
		if err := json.Unmarshal(signal.Envelope, &settlement); err != nil {
			t.Fatal(err)
		}
		var applied protocol.RunSteerAppliedPayload
		if err := settlement.DecodePayload(&applied); err != nil {
			t.Fatal(err)
		}
		if settlement.Type != protocol.TypeRunSteerApplied || applied.RequestID != "req-open" {
			t.Fatalf("event %s carries %+v, want the compound open's settlement", settlement.Type, applied)
		}
		if err := f.finish(); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("the settlement a compound open withholds was never released by its open response")
}

func TestAFailedOpenStillPublishesTheNamedSession(t *testing.T) {
	request := requestLine{Op: opOpen, Adapter: "settling", Request: requestEnvelope(t, "req-open", protocol.TypeSessionOpenRequest,
		protocol.SessionOpenRequest{SessionID: "stdio-steer", Subscribe: true}, "", "")}
	if got := publishedSession("", request); got != "stdio-steer" {
		t.Fatalf("published session = %q, want the session the open named", got)
	}
	if got := publishedSession("adapter-answer", request); got != "adapter-answer" {
		t.Fatalf("published session = %q, want the opened session", got)
	}
	broken := requestLine{Op: opOpen, Adapter: "settling", Request: json.RawMessage(`{"not":"an envelope"}`)}
	if got := publishedSession("", broken); got != "" {
		t.Fatalf("published session = %q, want no session for an undecodable open", got)
	}
}
