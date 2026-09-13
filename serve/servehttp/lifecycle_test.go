package servehttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
	"github.com/lsm/open-agent-protocol/validation"
)

func readAll(t *testing.T, response *http.Response) []byte {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeConfig(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "oap.json")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func staticEnviron(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

// The full-lifecycle tests drive one complete session over HTTP per adapter
// path and run the executable OAP schema and semantic state machine over every
// envelope the daemon emitted (POST responses and SSE events alike): the
// daemon must relay envelopes, not reshape them. GET-generated responses cite
// a daemon-minted correlation id; the test pairs each with a synthesized
// request envelope, exactly as a client would.
func TestValidationGatedLifecycleMemory(t *testing.T) {
	registry := memoryRegistry(64)
	testValidationGatedLifecycle(t, registry, "memory")
}

func TestValidationGatedLifecycleFakeAdapter(t *testing.T) {
	testValidationGatedLifecycle(t, fakeRegistry(64, 0, 0), "fake")
}

func testValidationGatedLifecycle(t *testing.T, registry *serve.Registry, adapterName string) {
	t.Helper()
	_, server := newServer(t, registry, Options{})
	sessionID := "lifecycle-" + adapterName
	var trace []protocol.Envelope

	// Capability exchange: the daemon's response plus a paired request.
	capsResponse := getCapabilities(t, server, adapterName)
	capsRequest := requestEnvelope(t, protocol.TypeCapabilitiesRequest, string(capsResponse.InReplyTo), protocol.CapabilitiesRequest{}, "", "", "")
	trace = append(trace, capsRequest, capsResponse)
	revision := capsResponse.CapabilityRevision

	// Session open: verbatim request and response envelopes.
	openRequest := requestEnvelope(t, protocol.TypeSessionOpenRequest, "lifecycle-open", protocol.SessionOpenRequest{SessionID: protocol.SessionID(sessionID)}, sessionID, "", "")
	status, openResponse := postEnvelope(t, server, "/adapters/"+adapterName+"/sessions", openRequest)
	if status != 200 {
		t.Fatalf("open status %d", status)
	}
	trace = append(trace, openRequest, openResponse)

	// Submit cites the active capability revision; the admission must echo it.
	submitRequest := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "lifecycle-submit", protocol.MessageSubmitRequest{
		SessionID: protocol.SessionID(sessionID), Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("drive the lifecycle")}},
	}, sessionID, "", revision)
	stream := connectSSE(t, server, "/sessions/"+sessionID+"/events", "")
	status, admissionEnvelope := postEnvelope(t, server, "/sessions/"+sessionID+"/submit", submitRequest)
	if status != 200 {
		t.Fatalf("submit status %d", status)
	}
	if admissionEnvelope.CapabilityRevision != revision {
		t.Fatalf("admission revision %q, want %q", admissionEnvelope.CapabilityRevision, revision)
	}
	var admission protocol.MessageSubmitResponse
	if err := admissionEnvelope.DecodePayload(&admission); err != nil {
		t.Fatal(err)
	}
	trace = append(trace, submitRequest, admissionEnvelope)

	// Scripted permission gate.
	initial := stream.drainUntil(protocol.TypeActionPermissionRequested)
	trace = append(trace, initial...)
	permission := permissionRequestAt(t, initial)
	permissionRequest := requestEnvelope(t, protocol.TypeActionPermissionResolveRequest, "lifecycle-resolve-permission", protocol.PermissionResolveRequest{
		InteractionID: permission.InteractionID, RequestedBy: permission.RequestedBy, RespondedBy: permission.RespondedBy,
		SessionID: protocol.SessionID(sessionID), RunID: permission.RunID, ChoiceID: "approve", Granted: true,
	}, sessionID, string(permission.RunID), revision)
	status, permissionResponse := postEnvelope(t, server, "/sessions/"+sessionID+"/resolve", permissionRequest)
	if status != 200 {
		t.Fatalf("permission resolve status %d", status)
	}
	trace = append(trace, permissionRequest, permissionResponse)

	// Scripted user-input gate and terminal settlement.
	middle := stream.drainUntil(protocol.TypeRunStatusUpdated)
	trace = append(trace, middle...)
	input := inputRequestAt(t, middle)
	inputRequest := requestEnvelope(t, protocol.TypeUserInputResolveRequest, "lifecycle-resolve-input", protocol.UserInputResolveRequest{
		InteractionID: input.InteractionID, RequestedBy: input.RequestedBy, RespondedBy: input.RespondedBy,
		SessionID: protocol.SessionID(sessionID), RunID: input.RunID,
		Answers: []protocol.InputAnswer{{QuestionID: "choice", SelectedOptionIDs: []string{"yes"}}},
	}, sessionID, string(input.RunID), revision)
	status, inputResponse := postEnvelope(t, server, "/sessions/"+sessionID+"/resolve", inputRequest)
	if status != 200 {
		t.Fatalf("input resolve status %d", status)
	}
	trace = append(trace, inputRequest, inputResponse)
	trace = append(trace, stream.drainUntil(protocol.TypeRunCompleted)...)
	stream.expectEnd()

	// Authoritative state exchange after settlement.
	stateResponse := getState(t, server, sessionID)
	stateRequest := requestEnvelope(t, protocol.TypeSessionStateRequest, string(stateResponse.InReplyTo), protocol.SessionStateRequest{SessionID: protocol.SessionID(sessionID)}, sessionID, "", "")
	trace = append(trace, stateRequest, stateResponse)

	// A submission the adapter refuses answers with a correlated
	// error.response, which is itself a legal terminal for a request.
	rejected := requestEnvelope(t, protocol.TypeSessionMessageSubmitRequest, "lifecycle-rejected", protocol.MessageSubmitRequest{
		SessionID: protocol.SessionID(sessionID), Delivery: protocol.DeliveryAuto, Instructions: "unsupported",
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
	}, sessionID, "", "")
	status, errorEnvelope := postEnvelope(t, server, "/sessions/"+sessionID+"/submit", rejected)
	if status != 400 {
		t.Fatalf("rejected submit status %d", status)
	}
	requireEnvelopeType(t, errorEnvelope, protocol.TypeErrorResponse)
	trace = append(trace, rejected, errorEnvelope)

	result := validation.MustNew().ValidateBytes(mustMarshal(t, trace), "serve-lifecycle-"+adapterName)
	if !result.Valid() {
		t.Fatalf("daemon lifecycle trace failed OAP validation: %v\ntrace: %s", result.Diagnostics, mustMarshal(t, trace))
	}
}

func getCapabilities(t *testing.T, server *httptest.Server, adapterName string) protocol.Envelope {
	t.Helper()
	response, err := server.Client().Get(server.URL + "/adapters/" + adapterName + "/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data := readAll(t, response)
	envelope, err := protocol.ParseEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	requireEnvelopeType(t, envelope, protocol.TypeCapabilitiesResponse)
	return envelope
}

func getState(t *testing.T, server *httptest.Server, sessionID string) protocol.Envelope {
	t.Helper()
	response, err := server.Client().Get(server.URL + "/sessions/" + sessionID + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	envelope, err := protocol.ParseEnvelope(readAll(t, response))
	if err != nil {
		t.Fatal(err)
	}
	requireEnvelopeType(t, envelope, protocol.TypeSessionStateResponse)
	return envelope
}

// TestCloseSessionsSettlesActiveRuns covers the daemon's graceful-shutdown
// session sweep: active runs that refuse Close are cancelled first, and every
// session ends closed.
func TestCloseSessionsSettlesActiveRuns(t *testing.T) {
	daemon, server := newServer(t, memoryRegistry(64), Options{})
	openSession(t, server, "memory", "shutdown-idle")
	openSession(t, server, "memory", "shutdown-active")
	stream := connectSSE(t, server, "/sessions/shutdown-active/events", "")
	submitRun(t, server, "shutdown-active", "submit-shutdown")

	daemon.CloseSessions(context.Background())

	// The cancelled run settles before the stream ends.
	envelopes := stream.drainUntil(protocol.TypeRunCancelled)
	if len(envelopes) == 0 {
		t.Fatal("active run produced no events before cancellation")
	}
	stream.expectEnd()

	for _, sessionID := range []string{"shutdown-idle", "shutdown-active"} {
		state := getState(t, server, sessionID)
		var payload protocol.SessionState
		if err := state.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Status != protocol.SessionClosed {
			t.Fatalf("session %s status after CloseSessions: %s", sessionID, payload.Status)
		}
	}
}

// TestDaemonOutputNeverCarriesEnvironmentValues pins the security invariant
// that resolved environment values never reach daemon output: the adapter
// listing and load errors carry names only.
func TestDaemonOutputNeverCarriesEnvironmentValues(t *testing.T) {
	const secret = "super-secret-value"
	registry, err := serve.LoadRegistry(writeConfig(t,
		`{"adapters": {"claude": {"type": "claude", "executable": "/bin/claude", "environment": ["OAP_SECRET"], "working_directory": "/tmp"}}}`),
		staticEnviron(map[string]string{"OAP_SECRET": secret}))
	if err != nil {
		t.Fatal(err)
	}
	_, server := newServer(t, registry, Options{})

	var listing struct {
		Adapters []adapterInfo `json:"adapters"`
	}
	getJSON(t, server, "/adapters", &listing)
	for _, info := range listing.Adapters {
		blob := string(mustMarshal(t, info))
		if strings.Contains(blob, secret) {
			t.Fatalf("adapter listing leaked environment value: %s", blob)
		}
	}

	_, err = serve.LoadRegistry(writeConfig(t,
		`{"adapters": {"broken": {"type": "nope", "environment": ["OAP_SECRET"]}}}`),
		staticEnviron(map[string]string{"OAP_SECRET": secret}))
	if err == nil {
		t.Fatal("broken entry must fail to load")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("load error leaked environment value: %v", err)
	}
}

// Compile-time assertion that the fake adapter satisfies the registry's
// adapter boundary.
var _ base.Adapter = (*fakeAdapter)(nil)
