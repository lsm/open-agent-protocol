package serveendpoint

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type cancellingAdapter struct{}

func (a *cancellingAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "cancelling.endpoint", Name: "Cancelling endpoint", Version: protocol.Version},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
		},
		CapabilityRevision: "cancelling-endpoint-v1",
	}, nil
}

func (a *cancellingAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return &cancellingSession{stream: make(chan base.Result, 8)}, nil
}

type cancellingSession struct {
	stream chan base.Result
}

func (s *cancellingSession) event(typ protocol.EnvelopeType, id protocol.EnvelopeID, sequence uint64, payload any) base.Result {
	envelope, err := protocol.NewEnvelope(typ, id, payload)
	if err != nil {
		return base.Result{Error: err}
	}
	envelope.SessionID, envelope.RunID, envelope.Sequence = "cancel", "run-1", &sequence
	return base.Result{Envelope: envelope}
}

func (s *cancellingSession) Submit(context.Context, base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.stream <- s.event(protocol.TypeRunStarted, "event-start", 1, protocol.RunStartedPayload{SessionID: "cancel", RunID: "run-1", Status: protocol.RunRunning})
	return protocol.MessageSubmitResponse{
		SessionID: "cancel", Accepted: true, SubmissionID: "sub-1",
		RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart,
		Admission: protocol.AdmissionStarted, RunID: "run-1", Status: protocol.RunRunning,
		MessageIDs: []protocol.MessageID{"m-1"},
	}, s.stream, nil
}

func (s *cancellingSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	s.stream <- s.event(protocol.TypeRunCancelled, "event-cancelled", 2, protocol.RunCancelledPayload{SessionID: "cancel", RunID: "run-1"})
	close(s.stream)
	time.Sleep(100 * time.Millisecond)
	return protocol.RunCancelResponse{SessionID: "cancel", RunID: "run-1", Accepted: true, Status: protocol.RunCancelling}, nil
}

func (s *cancellingSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "cancel", Status: protocol.SessionRunning, ActiveRunID: "run-1"}, nil
}
func (s *cancellingSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *cancellingSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}
func (s *cancellingSession) Close(context.Context) error { return nil }

var _ base.Adapter = (*cancellingAdapter)(nil)
var _ base.Session = (*cancellingSession)(nil)

func TestAnAcceptedCancelResponseIsWrittenBeforeTheRunCancelledItConfirms(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("cancel", &cancellingAdapter{}); err != nil {
		t.Fatal(err)
	}
	server, err := New(serve.New(registry, serve.Options{}), Options{Adapter: "cancel", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		requestLine(t, protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "cancel"}, "cancel"),
		requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-1", protocol.MessageSubmitRequest{
			SessionID: "cancel", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "cancel"),
		requestLine(t, protocol.TypeRunCancelRequest, "cancel-1", protocol.RunCancelRequest{SessionID: "cancel", RunID: "run-1"}, "cancel"),
	}, "\n") + "\n"

	out := &lockedBuffer{}
	stdin, writer := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- server.Run(context.Background(), stdin, out) }()
	if _, err := io.WriteString(writer, input); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_ = writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the endpoint never returned after its input ended")
	}

	written := out.String()
	response := strings.Index(written, `"run.cancel.response"`)
	cancelled := strings.Index(written, `"run.cancelled"`)
	if response < 0 || cancelled < 0 {
		t.Fatalf("want both the cancel response and run.cancelled:\n%s", written)
	}
	if cancelled < response {
		t.Fatalf("run.cancelled was written before the response accepting the cancel:\n%s", written)
	}
}
