package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func frameOf(t *testing.T, envelope protocol.Envelope) string {
	t.Helper()
	body, err := envelope.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(body) + "\n\n"
}

func TestClientHoldsASettlementThatArrivesBeforeItsAdmission(t *testing.T) {
	submitStarted := make(chan struct{})
	settlement := protocol.Envelope{
		Type: protocol.TypeRunSteerApplied, ID: "event-steer",
		SessionID: "wire", RunID: "run-1",
		Payload: json.RawMessage(`{"session_id":"wire","run_id":"run-1","submission_id":"sub-steer","request_id":"req-steer","message_ids":["m-1"],"boundary":"turn"}`),
	}
	sequence := uint64(2)
	settlement.Sequence = &sequence
	admission := protocol.Envelope{
		Type: protocol.TypeSessionMessageSubmitResponse, ID: "oap-response-1", InReplyTo: "req-steer",
		SessionID: "wire", RunID: "run-1",
		Payload: json.RawMessage(`{"target_sequence":1,"session_id":"wire","accepted":true,"submission_id":"sub-steer","requested_delivery":"steer","effective_delivery":"steer","admission":"steered","run_id":"run-1","status":"running","message_ids":["m-1"]}`),
	}
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			<-submitStarted
			_, _ = io.WriteString(w, frameOf(t, settlement))
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(200 * time.Millisecond)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var submitted protocol.Envelope
		if err := json.Unmarshal(body, &submitted); err != nil {
			t.Error(err)
			return
		}
		if submitted.ID != "req-steer" {
			t.Errorf("submit id %q, want req-steer", submitted.ID)
		}
		close(submitStarted)
		time.Sleep(200 * time.Millisecond)
		answer, err := admission.MarshalJSON()
		if err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(answer)
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	stream := session.Events(ctx)

	admitted, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "wire", Delivery: protocol.DeliverySteer,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("wait")}},
	}, WithEnvelopeID("req-steer"))
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Admission != protocol.AdmissionSteered {
		t.Fatalf("admission = %+v", admitted)
	}

	next, err := stream.Next()
	if err != nil {
		t.Fatalf("the held settlement was not released: %v", err)
	}
	if next.Type != protocol.TypeRunSteerApplied {
		t.Fatalf("stream delivered %s, want the held settlement", next.Type)
	}
	var applied protocol.RunSteerAppliedPayload
	if err := next.DecodePayload(&applied); err != nil {
		t.Fatal(err)
	}
	if applied.RequestID != "req-steer" || applied.SubmissionID != "sub-steer" {
		t.Fatalf("settlement = %+v", applied)
	}
}

func TestClientDeliversASettlementForAnUnknownRequestInOrder(t *testing.T) {
	settlement := protocol.Envelope{
		Type: protocol.TypeRunSteerDropped, ID: "event-steer",
		SessionID: "wire", RunID: "run-1",
		Payload: json.RawMessage(`{"session_id":"wire","run_id":"run-1","submission_id":"sub-other","request_id":"req-other","reason":{"code":"run_terminated","message":"the run ended"}}`),
	}
	sequence := uint64(1)
	settlement.Sequence = &sequence
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, frameOf(t, settlement))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(100 * time.Millisecond)
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	stream := session.Events(ctx)
	session.beginSubmit("req-steer")

	next, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if next.Type != protocol.TypeRunSteerDropped {
		t.Fatalf("stream delivered %s, want the settlement", next.Type)
	}
	session.endSubmit("req-steer")
}
