package appserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestPinnedCodexProcessAgainstResponsesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pinned Codex process integration in short mode")
	}
	if os.Getenv("OAP_CODEX_INTEGRATION") != "1" {
		t.Skip("set OAP_CODEX_INTEGRATION=1, absolute OAP_CODEX_BIN, and OAP_CODEX_COMMIT to run; optionally set OAP_CODEX_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	if os.Getenv("OAP_CODEX_COMMIT") != CodexCommit {
		t.Fatalf("OAP_CODEX_COMMIT must equal pinned commit %s", CodexCommit)
	}
	binary := adaptertest.VerifiedBinary(t, "OAP_CODEX_BIN", "OAP_CODEX_SHA256", "the codex app-server built from the pinned commit")

	mock := providertest.New(t, providertest.Config{OpenAIKey: "fixture-codex-key"})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	implementation := pinnedCodexAgainst(t, binary, mock)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, integrationOpenRequest("codex-process-session"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	admission, stream, err := session.Submit(ctx, adapter.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: "codex-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	}})
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

func pinnedCodexAgainst(t *testing.T, binary string, mock *providertest.Server) *Adapter {
	t.Helper()
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
	return implementation
}

func TestPinnedCodexProcessTakesALiveReasoningLevel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping pinned Codex process integration in short mode")
	}
	if os.Getenv("OAP_CODEX_INTEGRATION") != "1" {
		t.Skip("set OAP_CODEX_INTEGRATION=1, absolute OAP_CODEX_BIN, and OAP_CODEX_COMMIT to run; optionally set OAP_CODEX_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	if os.Getenv("OAP_CODEX_COMMIT") != CodexCommit {
		t.Fatalf("OAP_CODEX_COMMIT must equal pinned commit %s", CodexCommit)
	}
	binary := adaptertest.VerifiedBinary(t, "OAP_CODEX_BIN", "OAP_CODEX_SHA256", "the codex app-server built from the pinned commit")
	mock := providertest.New(t, providertest.Config{OpenAIKey: "fixture-codex-key"})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	implementation := pinnedCodexAgainst(t, binary, mock)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, integrationOpenRequest("codex-live-session"))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	if _, _, err := session.(adapter.SettingsUpdater).UpdateSettings(ctx, protocol.SessionSettingsUpdateRequest{SessionID: "codex-live-session", ReasoningLevel: protocol.ReasoningHigh}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, stream, err := session.Submit(ctx, adapter.SubmitRequest{Request: protocol.MessageSubmitRequest{
			SessionID: "codex-live-session", Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if events := adaptertest.Drain(t, stream, 30*time.Second); events[len(events)-1].Type != protocol.TypeRunCompleted {
			t.Fatalf("terminal=%s", events[len(events)-1].Type)
		}
	}
	requests := mock.RequestsFor(providertest.OpenAIResponses)
	if len(requests) != 2 {
		t.Fatalf("Responses requests=%d, want two", len(requests))
	}
	for index, request := range requests {
		var body struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		if err := json.Unmarshal(request.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body.Reasoning.Effort != "high" {
			t.Fatalf("request %d asked for effort %q, want high on the updated turn and the one after", index, body.Reasoning.Effort)
		}
	}
}

func integrationOpenRequest(sessionID protocol.SessionID) adapter.OpenRequest {
	return adapter.OpenRequest{SessionID: sessionID, Participant: protocol.Participant{ID: "integration-user"}}
}
