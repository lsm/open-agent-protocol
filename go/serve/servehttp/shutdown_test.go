package servehttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func newShutdownServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	daemon, err := New(serve.New(memoryRegistry(64), serve.Options{}), Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(daemon.Handler())
	t.Cleanup(server.Close)
	return daemon, server
}

func postShutdown(t *testing.T, server *httptest.Server, body string) (int, protocol.EndpointShutdownResponse) {
	t.Helper()
	response, data := post(t, server, "/shutdown", "application/json", []byte(body))
	var answer protocol.EndpointShutdownResponse
	if response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(data, &answer); err != nil {
			t.Fatalf("shutdown answer %s: %v", data, err)
		}
	}
	return response.StatusCode, answer
}

func requireDone(t *testing.T, daemon *Server) {
	t.Helper()
	select {
	case <-daemon.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon never reported its shutdown complete")
	}
}

func TestAShutdownPostedWithNothingRunningStopsAtOnce(t *testing.T) {
	daemon, server := newShutdownServer(t)
	status, answer := postShutdown(t, server, "")
	if status != http.StatusOK || answer.Outcome != protocol.ShutdownStopping {
		t.Fatalf("idle shutdown answered %d %+v, want 200 stopping", status, answer)
	}
	requireDone(t, daemon)
}

func TestAShutdownPostedAsksWhileARunIsActiveThenGracefulRefusesNewWork503AndForceStops(t *testing.T) {
	daemon, server := newShutdownServer(t)
	openSession(t, server, "memory", "busy")
	submitRun(t, server, "busy", "submit-1")

	if _, asked := postShutdown(t, server, ""); asked.Outcome != protocol.ShutdownChoose || len(asked.ActiveRuns) != 1 {
		t.Fatalf("a modeless shutdown with a run answered %+v, want choose listing it", asked)
	}
	if _, graceful := postShutdown(t, server, `{"mode":"graceful"}`); graceful.Outcome != protocol.ShutdownDraining {
		t.Fatalf("graceful answered %+v, want draining", graceful)
	}
	status, refused := postEnvelope(t, server, "/sessions/busy/submit", requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "submit-2",
		protocol.MessageSubmitRequest{SessionID: "busy", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("late")}}}, "busy", "", ""))
	var refusal protocol.ErrorResponse
	if err := refused.DecodePayload(&refusal); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusServiceUnavailable || refusal.Error.Code != "shutting_down" {
		t.Fatalf("a submit after the shutdown answered %d %s, want 503 shutting_down", status, refusal.Error.Code)
	}
	if response, _ := post(t, server, "/sessions/busy/submit", "text/plain", []byte("x")); response.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("a wrong media type after the shutdown answered %d, want 415 ahead of 503", response.StatusCode)
	}
	if _, forced := postShutdown(t, server, `{"mode":"force"}`); forced.Outcome != protocol.ShutdownStopping {
		t.Fatalf("force answered %+v, want stopping", forced)
	}
	requireDone(t, daemon)
	if !daemon.Forced() {
		t.Fatal("a forced shutdown did not report itself forced")
	}
}

func TestAShutdownPostedWithAnUnknownModeIsRefusedAndChangesNothing(t *testing.T) {
	daemon, server := newShutdownServer(t)
	if status, _ := postShutdown(t, server, `{"mode":"later"}`); status != http.StatusBadRequest {
		t.Fatalf("an unknown mode answered %d, want 400", status)
	}
	if status, _ := postShutdown(t, server, `null`); status != http.StatusBadRequest {
		t.Fatalf("a null body answered %d, want 400 as the Zig daemon does", status)
	}
	select {
	case <-daemon.Done():
		t.Fatal("a refused shutdown completed one")
	default:
	}
}
