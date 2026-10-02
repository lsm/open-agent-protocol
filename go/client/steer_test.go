package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
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

func steerSettlement() protocol.Envelope {
	sequence := uint64(2)
	envelope := protocol.Envelope{
		Type: protocol.TypeRunSteerApplied, ID: "event-steer",
		SessionID: "wire", RunID: "run-1", Sequence: &sequence,
		Payload: json.RawMessage(`{"session_id":"wire","run_id":"run-1","submission_id":"sub-steer","request_id":"req-steer","message_ids":["m-1"],"boundary":"turn"}`),
	}
	return envelope
}

func steerAdmission() protocol.Envelope {
	return protocol.Envelope{
		Type: protocol.TypeSessionMessageSubmitResponse, ID: "oap-response-1", InReplyTo: "req-steer",
		SessionID: "wire", RunID: "run-1", CapabilityRevision: "wire-v1",
		Payload: json.RawMessage(`{"target_sequence":1,"session_id":"wire","accepted":true,"submission_id":"sub-steer","requested_delivery":"steer","effective_delivery":"steer","admission":"steered","run_id":"run-1","status":"running","message_ids":["m-1"]}`),
	}
}

func TestClientHoldsASettlementThatArrivesBeforeItsAdmission(t *testing.T) {
	submitStarted := make(chan struct{})
	var delivered atomic.Int64
	var first sync.Once
	settlement, admission := steerSettlement(), steerAdmission()
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			if flusher != nil {
				flusher.Flush()
			}
			first.Do(func() {
				select {
				case <-submitStarted:
				case <-time.After(testTimeout):
				}
				_, _ = io.WriteString(w, frameOf(t, settlement))
				if flusher != nil {
					flusher.Flush()
				}
			})
			time.Sleep(2 * time.Second)
			return
		}
		var submitted protocol.Envelope
		body, err := io.ReadAll(r.Body)
		if err == nil {
			err = json.Unmarshal(body, &submitted)
		}
		if err != nil || submitted.ID != "req-steer" {
			t.Errorf("submit %q: %v", string(body), err)
			return
		}
		close(submitStarted)
		time.Sleep(300 * time.Millisecond)
		if delivered.Load() != 0 {
			t.Error("the client delivered a settlement before its admission response arrived")
		}
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

	next := make(chan protocol.Envelope, 1)
	failed := make(chan error, 1)
	go func() {
		envelope, err := stream.Next()
		if err != nil {
			failed <- err
			return
		}
		delivered.Add(1)
		next <- envelope
	}()

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

	select {
	case envelope := <-next:
		if envelope.Type != protocol.TypeRunSteerApplied {
			t.Fatalf("stream delivered %s, want the held settlement", envelope.Type)
		}
		var applied protocol.RunSteerAppliedPayload
		if err := envelope.DecodePayload(&applied); err != nil {
			t.Fatal(err)
		}
		if applied.RequestID != "req-steer" || applied.SubmissionID != "sub-steer" {
			t.Fatalf("settlement = %+v", applied)
		}
	case err := <-failed:
		t.Fatalf("the held settlement was not released: %v", err)
	case <-time.After(testTimeout):
		t.Fatal("the held settlement was never released")
	}
}

func TestClientDeliversASettlementForAnUnknownRequestInOrder(t *testing.T) {
	sequence := uint64(1)
	settlement := protocol.Envelope{
		Type: protocol.TypeRunSteerDropped, ID: "event-steer",
		SessionID: "wire", RunID: "run-1", Sequence: &sequence,
		Payload: json.RawMessage(`{"session_id":"wire","run_id":"run-1","submission_id":"sub-other","request_id":"req-other","reason":{"code":"run_terminated","message":"the run ended"}}`),
	}
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

	envelope, err := stream.Next()
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Type != protocol.TypeRunSteerDropped {
		t.Fatalf("stream delivered %s, want the settlement", envelope.Type)
	}
	session.endSubmit("req-steer")
}

func TestAReaderTakesTheWakeChannelWithTheEmptyRelease(t *testing.T) {
	session := &Session{}
	session.beginSubmit("req-steer")
	settlement := steerSettlement()
	if !session.holdSteer(settlement) {
		t.Fatal("the settlement was not held for its outstanding submit")
	}
	_, wake, ok := session.takeSteer()
	if ok {
		t.Fatal("a settlement was released before its submit returned")
	}
	session.endSubmit("req-steer")
	select {
	case <-wake:
	case <-time.After(testTimeout):
		t.Fatal("the release never closed the channel the reader took with the empty check")
	}
	released, _, ok := session.takeSteer()
	if !ok || released.ID != settlement.ID {
		t.Fatalf("takeSteer = %q, %v, want the released settlement", released.ID, ok)
	}
}

func TestClientWakesAReaderWaitingOnAnIdleStreamForARelease(t *testing.T) {
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	ctx, cancel := context.WithTimeout(context.Background(), testTimeout)
	defer cancel()
	stream := session.Events(ctx)

	next := make(chan protocol.Envelope, 1)
	go func() {
		envelope, err := stream.Next()
		if err != nil {
			next <- protocol.Envelope{ID: protocol.EnvelopeID("failed: " + err.Error())}
			return
		}
		next <- envelope
	}()

	settlement := steerSettlement()
	session.beginSubmit("req-steer")
	time.Sleep(100 * time.Millisecond)
	if !session.holdSteer(settlement) {
		t.Fatal("the settlement was not held for its outstanding submit")
	}
	session.endSubmit("req-steer")

	select {
	case envelope := <-next:
		if envelope.ID != settlement.ID {
			t.Fatalf("the waiting reader returned %q, want the released settlement", envelope.ID)
		}
	case <-time.After(testTimeout):
		t.Fatal("the reader waiting on an idle stream was never woken for the release")
	}
}
