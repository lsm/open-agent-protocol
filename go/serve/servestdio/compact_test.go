package servestdio

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestCompactionIsAdmittedOnTheSubmitOpAndItsRunReachesSubscribers(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "compact-stdio")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	subscription, err := hub.Subscribe(ctx, "compact-stdio")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	f := startFrontend(t, hub, Options{})

	focus := "the release plan"
	f.send(`{"id":1,"op":"submit","session_id":"compact-stdio","request":` + string(requestEnvelope(t, "compact-1", protocol.TypeSessionCompactRequest, protocol.SessionCompactRequest{
		SessionID: "compact-stdio", Focus: &focus,
	}, "compact-stdio", "")) + `}`)
	answer := f.expectResponse(1)
	requireOK(t, answer)
	var envelope protocol.Envelope
	if err := json.Unmarshal(answer.Result, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Type != protocol.TypeSessionCompactResponse || envelope.InReplyTo != "compact-1" {
		t.Fatalf("compact answer = %s in reply to %q", envelope.Type, envelope.InReplyTo)
	}
	var admission protocol.SessionCompactResponse
	if err := envelope.DecodePayload(&admission); err != nil {
		t.Fatal(err)
	}
	if admission.Admission != protocol.AdmissionStarted || admission.RunID == "" || envelope.RunID != admission.RunID {
		t.Fatalf("compact admission = %+v on run %q", admission, envelope.RunID)
	}

	var seen []protocol.EnvelopeType
	for {
		event, err := subscription.Next()
		if err != nil {
			t.Fatalf("draining the compaction run after %v: %v", seen, err)
		}
		seen = append(seen, event.Type)
		if event.Type == protocol.TypeRunCompleted || event.Type == protocol.TypeRunFailed {
			break
		}
	}
	want := []protocol.EnvelopeType{protocol.TypeRunStarted, protocol.TypeRunCompactionStarted, protocol.TypeRunCompactionEnded, protocol.TypeRunCompleted}
	if len(seen) != len(want) {
		t.Fatalf("compaction run events = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("compaction run events = %v, want %v", seen, want)
		}
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}

func TestACompactionTheAdapterRefusesAnswersWithTheSubmitOpsRefusal(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "compact-refused")
	f := startFrontend(t, hub, Options{})

	f.send(`{"id":1,"op":"submit","session_id":"compact-refused","request":` + string(requestEnvelope(t, "submit-busy", protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: "compact-refused", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("busy")}},
	}, "compact-refused", "")) + `}`)
	requireOK(t, f.expectResponse(1))

	f.send(`{"id":2,"op":"submit","session_id":"compact-refused","request":` + string(requestEnvelope(t, "compact-steer", protocol.TypeSessionCompactRequest, protocol.SessionCompactRequest{
		SessionID: "compact-refused", Delivery: protocol.DeliverySteer,
	}, "compact-refused", "")) + `}`)
	if refused := f.expectResponse(2); refused.OK || refused.Error == nil || refused.Error.Code != "unsupported_feature" {
		t.Fatalf("steered compaction answer = %+v", refused)
	}

	f.send(`{"id":3,"op":"submit","session_id":"compact-refused","request":` + string(requestEnvelope(t, "compact-scope", protocol.TypeSessionCompactRequest, protocol.SessionCompactRequest{
		SessionID: "elsewhere",
	}, "compact-refused", "")) + `}`)
	if refused := f.expectResponse(3); refused.OK || refused.Error == nil || refused.Error.Code != "scope_mismatch" {
		t.Fatalf("misscoped compaction answer = %+v", refused)
	}
	if err := f.finish(); err != nil {
		t.Fatal(err)
	}
}
