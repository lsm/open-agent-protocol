package appserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/internal/providertest"
	"github.com/lsm/open-agent-protocol/protocol"
)

func TestPinnedCodexProcessAgainstResponsesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pinned Codex process integration in short mode")
	}
	if os.Getenv("OAP_CODEX_INTEGRATION") != "1" {
		t.Skip("set OAP_CODEX_INTEGRATION=1, OAP_CODEX_BIN, and OAP_CODEX_COMMIT to run")
	}
	binary := os.Getenv("OAP_CODEX_BIN")
	if binary == "" {
		t.Fatal("OAP_CODEX_BIN is required when OAP_CODEX_INTEGRATION=1")
	}
	if os.Getenv("OAP_CODEX_COMMIT") != CodexCommit {
		t.Fatalf("OAP_CODEX_COMMIT must equal pinned commit %s", CodexCommit)
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("OAP_CODEX_BIN is not an executable file: %v", err)
	}

	mock := providertest.New(t, providertest.Config{OpenAIKey: "fixture-codex-key"})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	codexHome := t.TempDir()
	workspace := t.TempDir()
	config := fmt.Sprintf(`model = "mock-model"
model_provider = "local-mock"
approval_policy = "never"
sandbox_mode = "read-only"

[model_providers.local-mock]
name = "Local Responses mock"
base_url = %q
env_key = "OAP_CODEX_MOCK_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
request_max_retries = 0
stream_max_retries = 0
`, mock.OpenAIBaseURL())
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	environment := []string{
		"CODEX_HOME=" + codexHome,
		"HOME=" + codexHome,
		"OAP_CODEX_MOCK_KEY=fixture-codex-key",
		"RUST_LOG=error",
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	implementation, err := New(Config{
		Executable: binary, Environment: environment, WorkingDirectory: workspace,
		Model: "mock-model", ApprovalPolicy: "never", Sandbox: "read-only",
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, integrationOpenRequest("codex-process-session"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "codex-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 30*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", events[len(events)-1].Type)
	}
	requests := mock.RequestsFor(providertest.OpenAIResponses)
	if len(requests) != 1 || requests[0].Path != providertest.ResponsesPath || requests[0].Model != "mock-model" {
		t.Fatalf("Responses requests=%d: %+v", len(requests), requests)
	}
	if requests[0].Header.Get("Authorization") != "Bearer fixture-codex-key" {
		t.Fatal("unexpected mock authorization")
	}
}

func integrationOpenRequest(sessionID protocol.SessionID) adapter.OpenRequest {
	return adapter.OpenRequest{SessionID: sessionID, Participant: protocol.Participant{ID: "integration-user"}}
}
