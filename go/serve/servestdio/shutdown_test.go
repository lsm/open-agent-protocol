package servestdio

import (
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
