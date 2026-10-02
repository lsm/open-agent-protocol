package serveendpoint

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

type settlingAdapter struct{}

func (a *settlingAdapter) Probe(context.Context) (base.Descriptor, error) {
	return base.Descriptor{
		Capabilities: protocol.CapabilityDescriptor{
			Endpoint:         protocol.EndpointDescriptor{ID: "settling.endpoint", Name: "Settling endpoint", Version: protocol.Version},
			ProtocolVersions: []string{protocol.Version},
			Profiles:         []string{protocol.Profile},
			Features: map[string]protocol.FeatureSupport{
				protocol.FeatureDeliverySteer: {Level: protocol.SupportEmulated, Reason: "a steer settles inside the submit"},
			},
		},
		CapabilityRevision: "settling-endpoint-v1",
	}, nil
}

func (a *settlingAdapter) Open(context.Context, base.OpenRequest) (base.Session, error) {
	return &settlingSession{stream: make(chan base.Result, 8)}, nil
}

type settlingSession struct {
	stream chan base.Result
	run    protocol.RunID
}

func (s *settlingSession) Submit(_ context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
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
		go func() {
			time.Sleep(100 * time.Millisecond)
			sequence := uint64(3)
			after, err := protocol.NewEnvelope(protocol.TypeContentDelta, "event-after", protocol.ContentDeltaPayload{
				SessionID: "settle", RunID: s.run, MessageID: "m-3",
				Part: protocol.ContentPart{Type: protocol.ContentText, Text: "after"},
			})
			if err != nil {
				return
			}
			after.SessionID, after.RunID, after.Sequence = "settle", s.run, &sequence
			s.stream <- base.Result{Envelope: after}
		}()
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

func (s *settlingSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "settle", Status: protocol.SessionRunning, ActiveRunID: s.run}, nil
}
func (s *settlingSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *settlingSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: "settle", Accepted: true, Status: protocol.RunCancelling}, nil
}
func (s *settlingSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}
func (s *settlingSession) Close(context.Context) error { return nil }

var _ base.Adapter = (*settlingAdapter)(nil)
var _ base.Session = (*settlingSession)(nil)

type lockedBuffer struct {
	mu   sync.Mutex
	text strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.text.String()
}

func TestASteeredAdmissionPumpsTheTargetRunOnce(t *testing.T) {
	registry := serve.NewRegistry()
	if err := registry.Register("settle", &settlingAdapter{}); err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{})
	server, err := New(hub, Options{Adapter: "settle", Shutdown: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}

	input := strings.Join([]string{
		requestLine(t, protocol.TypeSessionOpenRequest, "open-1", protocol.SessionOpenRequest{SessionID: "settle"}, "settle"),
		requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-start", protocol.MessageSubmitRequest{
			SessionID: "settle", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}},
		}, "settle"),
		requestLine(t, protocol.TypeSessionMessageSubmitRequest, "submit-steer", protocol.MessageSubmitRequest{
			SessionID: "settle", Delivery: protocol.DeliverySteer, TargetRunID: "run-1",
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("adjust")}},
		}, "settle"),
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
	if count := strings.Count(written, `"run.steer.applied"`); count != 1 {
		t.Fatalf("the settlement was written %d times, want 1:\n%s", count, written)
	}
	if count := strings.Count(written, `"run.started"`); count != 1 {
		t.Fatalf("run.started was written %d times, want 1:\n%s", count, written)
	}
	if count := strings.Count(written, `"event-after"`); count != 1 {
		t.Fatalf("an envelope published after the steer was written %d times, want 1:\n%s", count, written)
	}
}
