package servestdio

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

func submitLine(t *testing.T, id int, session string) string {
	t.Helper()
	envelope := requestEnvelope(t, fmt.Sprintf("submit-%d", id), protocol.TypeSessionMessageSubmitRequest, protocol.MessageSubmitRequest{
		SessionID: protocol.SessionID(session), Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}, session, "")
	return fmt.Sprintf(`{"id":%d,"op":"submit","session_id":%q,"request":%s}`, id, session, envelope)
}

func shutdownAnswer(t *testing.T, response responseLine) protocol.EndpointShutdownResponse {
	t.Helper()
	requireOK(t, response)
	var answer protocol.EndpointShutdownResponse
	if err := json.Unmarshal(response.Result, &answer); err != nil {
		t.Fatalf("shutdown answer %s: %v", response.Result, err)
	}
	return answer
}

func (f *frontend) stopsServing() {
	f.t.Helper()
	select {
	case err := <-f.done:
		if err != nil {
			f.t.Fatalf("serving ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		f.t.Fatal("serving did not end after the shutdown")
	}
}

func TestShutdownWithNothingRunningStopsAtOnce(t *testing.T) {
	f := startFrontend(t, newTestHub(t, 64, 64), Options{})
	f.send(`{"id":1,"op":"shutdown"}`)
	if answer := shutdownAnswer(t, f.expectResponse(1)); answer.Outcome != protocol.ShutdownStopping || len(answer.ActiveRuns) != 0 {
		t.Fatalf("idle shutdown answered %+v, want stopping with no runs", answer)
	}
	f.stopsServing()
}

func TestShutdownWithoutAModeAsksTheHostToChooseAndChangesNothing(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "busy")
	f := startFrontend(t, hub, Options{})
	f.send(submitLine(t, 1, "busy"))
	requireOK(t, f.expectResponse(1))
	f.send(`{"id":2,"op":"shutdown"}`)
	answer := shutdownAnswer(t, f.expectResponse(2))
	if answer.Outcome != protocol.ShutdownChoose || len(answer.ActiveRuns) != 1 || answer.ActiveRuns[0].SessionID != "busy" {
		t.Fatalf("shutdown with a run answered %+v, want choose listing the run", answer)
	}
	f.send(submitLine(t, 3, "busy"))
	requireOK(t, f.expectResponse(3))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestGracefulShutdownLosesQueuedRunsRefusesNewWorkAndForceStopsIt(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "busy")
	f := startFrontend(t, hub, Options{})
	f.send(submitLine(t, 1, "busy"))
	requireOK(t, f.expectResponse(1))
	f.send(submitLine(t, 2, "busy"))
	requireOK(t, f.expectResponse(2))
	f.send(`{"id":3,"op":"shutdown","request":{"mode":"graceful"}}`)
	answer := shutdownAnswer(t, f.expectResponse(3))
	if answer.Outcome != protocol.ShutdownDraining || len(answer.ActiveRuns) != 1 || len(answer.Lost) != 1 {
		t.Fatalf("graceful shutdown answered %+v, want draining with one running and one lost", answer)
	}
	if answer.ActiveRuns[0].Status == protocol.RunQueued {
		t.Fatalf("graceful shutdown kept the queued run active: %+v", answer)
	}
	f.send(submitLine(t, 4, "busy"))
	requireCode(t, f.expectResponse(4), "shutting_down")
	f.send(`{"id":5,"op":"open","adapter":"memory"}`)
	requireCode(t, f.expectResponse(5), "shutting_down")
	f.send(`{"id":6,"op":"shutdown","request":{"mode":"force"}}`)
	if answer := shutdownAnswer(t, f.expectResponse(6)); answer.Outcome != protocol.ShutdownStopping {
		t.Fatalf("force answered %+v, want stopping", answer)
	}
	f.stopsServing()
}

func TestShutdownRefusesAModeOtherThanGracefulOrForce(t *testing.T) {
	f := startFrontend(t, newTestHub(t, 64, 64), Options{})
	f.send(`{"id":1,"op":"shutdown","request":{"mode":"later"}}`)
	requireCode(t, f.expectResponse(1), "invalid_request")
	f.send(`{"id":2,"op":"shutdown","request":{"mode":"force","when":1}}`)
	requireCode(t, f.expectResponse(2), "invalid_request")
	f.send(`{"id":3,"op":"adapters"}`)
	requireOK(t, f.expectResponse(3))
	if err := f.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestGracefulShutdownEndsServingOnceTheDrainedRunSettles(t *testing.T) {
	hub := newTestHub(t, 64, 64)
	openSession(t, hub, "busy")
	f := startFrontend(t, hub, Options{})
	f.send(submitLine(t, 1, "busy"))
	admitted := f.expectResponse(1)
	requireOK(t, admitted)
	var admission protocol.MessageSubmitResponse
	if err := json.Unmarshal(admitted.Result, &admission); err != nil {
		t.Fatal(err)
	}
	f.send(`{"id":2,"op":"events","session_id":"busy"}`)
	requireOK(t, f.expectResponse(2))
	f.send(`{"id":3,"op":"shutdown","request":{"mode":"graceful"}}`)
	terminal, answered := false, false
	cancel := requestEnvelope(t, "cancel-4", protocol.TypeRunCancelRequest, protocol.RunCancelRequest{SessionID: "busy", RunID: admission.RunID}, "busy", string(admission.RunID))
	for !terminal || !answered {
		line := f.line()
		var shape struct {
			ID       int64             `json:"id"`
			Event    string            `json:"event"`
			Envelope protocol.Envelope `json:"envelope"`
		}
		if err := json.Unmarshal([]byte(line), &shape); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		switch {
		case shape.Event == signalEnvelope && shape.Envelope.Type == protocol.TypeRunCancelled:
			terminal = true
		case shape.Event == "" && shape.ID == 3:
			if answer := shutdownAnswer(t, f.decodeResponse(line)); answer.Outcome != protocol.ShutdownDraining {
				t.Fatalf("graceful answered %+v, want draining", answer)
			}
			f.send(fmt.Sprintf(`{"id":4,"op":"cancel","session_id":"busy","request":%s}`, cancel))
		case shape.Event == "" && shape.ID == 4:
			requireOK(t, f.decodeResponse(line))
			answered = true
		}
	}
	f.stopsServing()
}

func TestADrainedShutdownWaitsForAttachedPumpsBeforeTeardown(t *testing.T) {
	run := newRunState(context.Background(), 0, 0, 0)
	if _, outcome, _ := run.attach(); outcome != admitted {
		t.Fatal("attach was refused")
	}
	returned := make(chan struct{})
	go func() { run.awaitPumps(5 * time.Second); close(returned) }()
	select {
	case <-returned:
		t.Fatal("awaitPumps returned while a pump was still attached")
	case <-time.After(10 * drainPoll):
	}
	run.detach()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("awaitPumps did not return once the last pump detached")
	}
}

func TestOnlyAGracefulShutdownThatDrainedRunsWaitsOnPumps(t *testing.T) {
	st := newShutdownState()
	st.mode = protocol.ShutdownGraceful
	if st.drained() {
		t.Fatal("an unfinished graceful shutdown counted as drained")
	}
	idle := newShutdownState()
	idle.mode = protocol.ShutdownGraceful
	idle.finishLocked()
	if idle.drained() {
		t.Fatal("a graceful shutdown with nothing to drain counted as drained, so it would wait on idle pumps")
	}
	st.flushed = true
	st.finishLocked()
	if !st.drained() {
		t.Fatal("a graceful shutdown that drained runs did not count as drained")
	}
	forced := newShutdownState()
	forced.mode = protocol.ShutdownForce
	forced.finishLocked()
	if forced.drained() {
		t.Fatal("a force shutdown counted as drained, so its pumps would be waited on")
	}
}
