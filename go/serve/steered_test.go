package serve

import (
	"context"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type steeredStubSession struct {
	submits int
}

func (s *steeredStubSession) Submit(context.Context, base.SubmitRequest) (protocol.MessageSubmitResponse, base.EventStream, error) {
	s.submits++
	return protocol.MessageSubmitResponse{
		SessionID: "stub", Accepted: true, SubmissionID: "sub-steer",
		RequestedDelivery: protocol.DeliverySteer, EffectiveDelivery: protocol.EffectiveDeliverySteer,
		Admission: protocol.AdmissionSteered, RunID: "run-1", Status: protocol.RunRunning,
		MessageIDs: []protocol.MessageID{"m-1"},
	}, nil, nil
}

func (s *steeredStubSession) State(context.Context) (protocol.SessionState, error) {
	return protocol.SessionState{SessionID: "stub", Status: protocol.SessionRunning}, nil
}
func (s *steeredStubSession) Resolve(context.Context, base.InteractionResolution) error { return nil }
func (s *steeredStubSession) Cancel(context.Context, protocol.RunID) (protocol.RunCancelResponse, error) {
	return protocol.RunCancelResponse{SessionID: "stub", Accepted: true, Status: protocol.RunCancelling}, nil
}
func (s *steeredStubSession) Resume(context.Context, base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	return base.Recovery{}, nil, nil
}
func (s *steeredStubSession) Close(context.Context) error { return nil }

var _ base.Session = (*steeredStubSession)(nil)

func TestASteeredAdmissionAdoptsNoRun(t *testing.T) {
	stub := &steeredStubSession{}
	entry := newSession("stub", "stub", stub, nil)
	admission, err := entry.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "stub", Delivery: protocol.DeliverySteer,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("wait")}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionSteered {
		t.Fatalf("admission = %+v", admission)
	}
	entry.mu.Lock()
	readers, reservations, serials := entry.readers, entry.reservations, len(entry.serials)
	entry.mu.Unlock()
	if readers != 0 || reservations != 0 || serials != 0 {
		t.Fatalf("a steered admission adopted a run: readers %d, reservations %d, serials %d", readers, reservations, serials)
	}
}
