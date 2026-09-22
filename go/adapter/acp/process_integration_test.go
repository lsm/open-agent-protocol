package acp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	acpMockSecret = "fixture-acp-key"
	acpRouteModel = "fixture-model"

	acpTokenEnv = "OAP_ACP_TOKEN"
)

func acpArgs(root string) []string {
	return []string{"--data-dir", filepath.Join(root, "data"), "serve", "acp", filepath.Join(root, "agent.yaml")}
}

func TestACPProcessSmoke(t *testing.T) {
	if os.Getenv("OAP_ACP_SMOKE") != "1" {
		t.Skip("set OAP_ACP_SMOKE=1 and absolute OAP_ACP_BIN pointing to a pinned open-source ACP agent server (docker/cagent `docker-agent serve acp`) to run; optionally set OAP_ACP_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := verifiedACPServerBinary(t)
	root := t.TempDir()
	writeACPAgentConfig(t, root, "http://127.0.0.1:1/v1")
	implementation := newPinnedACP(t, binary, root, acpEnvironment(t, root))

	descriptor, err := implementation.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.Capabilities.Endpoint.Adapter != "acp-v1-stdio" || descriptor.CapabilityRevision != CapabilityRevision {
		t.Fatalf("descriptor=%+v", descriptor)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "acp-smoke-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "acp-smoke-session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close ACP smoke session: %v", err)
	}
	closed = true
}

func TestACPProcessAgainstChatCompletionsMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in ACP process integration in short mode")
	}
	if os.Getenv("OAP_ACP_INTEGRATION") != "1" {
		t.Skip("set OAP_ACP_INTEGRATION=1 and absolute OAP_ACP_BIN pointing to a pinned open-source ACP agent server (docker/cagent `docker-agent serve acp`) to run; optionally set OAP_ACP_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := verifiedACPServerBinary(t)
	mock := providertest.New(t, providertest.Config{OpenAIKey: acpMockSecret})
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)

	root := t.TempDir()
	writeACPAgentConfig(t, root, mock.OpenAIBaseURL())
	implementation := newPinnedACP(t, binary, root, acpEnvironment(t, root))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "acp-process-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "acp-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if admission.Admission != protocol.AdmissionStarted {
		t.Fatalf("admission=%+v", admission)
	}
	events := adaptertest.Drain(t, stream, 60*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)

	var streamed strings.Builder
	for _, event := range events {
		if event.Type != protocol.TypeContentDelta {
			continue
		}
		var payload protocol.ContentDeltaPayload
		if err := event.DecodePayload(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Part.Type == protocol.ContentText {
			streamed.WriteString(payload.Part.Text)
		}
	}
	if streamed.String() != providertest.FixtureText {
		t.Fatalf("streamed response=%q", streamed.String())
	}
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", events[len(events)-1].Type)
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	text, ok := completed.FinalResponse.Content.Text()
	if !ok || text != providertest.FixtureText {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}
	requests := mock.RequestsFor(providertest.OpenAIChatCompletion)
	if len(requests) != 1 || requests[0].Path != providertest.ChatCompletionPath || requests[0].Model != acpRouteModel {
		t.Fatalf("chat-completions requests=%d: %+v", len(requests), requests)
	}
	if requests[0].Header.Get("Authorization") != "Bearer "+acpMockSecret {
		t.Fatal("unexpected mock authorization")
	}
	var body struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(requests[0].Body, &body); err != nil || !body.Stream {
		t.Fatalf("loopback provider request stream=%v (decode err=%v)", body.Stream, err)
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle {
		t.Fatalf("post-run state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close ACP integration session: %v", err)
	}
	closed = true
}

func verifiedACPServerBinary(t *testing.T) string {
	t.Helper()
	binary := adaptertest.VerifiedBinary(t, "OAP_ACP_BIN", "OAP_ACP_SHA256", "the pinned ACP agent server executable")
	return binary
}

func newPinnedACP(t *testing.T, binary, root string, environment []string) *Adapter {
	t.Helper()
	workspace := filepath.Join(root, "workspace")
	for _, directory := range []string{workspace, filepath.Join(root, "data")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	implementation, err := New(Config{
		Executable: binary, Args: acpArgs(root), Environment: environment,
		WorkingDirectory: workspace, ShutdownTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func writeACPAgentConfig(t *testing.T, root, baseURL string) {
	t.Helper()
	config := `providers:
  loopback:
    api_type: openai_chatcompletions
    base_url: ` + baseURL + `
    token_key: ` + acpTokenEnv + `

models:
  loopback-model:
    provider: loopback
    model: ` + acpRouteModel + `

agents:
  root:
    model: loopback-model
    description: Loopback fixture assistant
    instruction: You are a fixture assistant.
`
	if err := os.WriteFile(filepath.Join(root, "agent.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func acpEnvironment(t *testing.T, root string) []string {
	t.Helper()
	home := filepath.Join(root, "home")
	tmpDir := filepath.Join(root, "tmp")
	for _, directory := range []string{home, tmpDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + home,
		"TMPDIR=" + tmpDir,
		"NO_COLOR=1",
		"TELEMETRY_ENABLED=false",
		acpTokenEnv + "=" + acpMockSecret,
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	return environment
}
