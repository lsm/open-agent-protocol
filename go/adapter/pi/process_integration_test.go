package pi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const piMockSecret = "fixture-pi-key"

func TestPiProcessSmoke(t *testing.T) {
	if os.Getenv("OAP_PI_SMOKE") != "1" {
		t.Skip("set OAP_PI_SMOKE=1 and absolute OAP_PI_BIN pointing to Pi v0.85.1 to run; optionally set OAP_PI_SHA256 for exact-artifact evidence")
	}
	binary := verifiedPiBinary(t)
	root := t.TempDir()
	implementation := newPinnedPi(t, binary, root, piEnvironment(t, root, ""), nil)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "pi-smoke-session",
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
	if state.SessionID != "pi-smoke-session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close Pi smoke session: %v", err)
	}
	closed = true
}

func TestPiProcessAgainstResponsesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Pi process integration in short mode")
	}
	if os.Getenv("OAP_PI_INTEGRATION") != "1" {
		t.Skip("set OAP_PI_INTEGRATION=1 and absolute OAP_PI_BIN pointing to Pi v0.85.1 to run; optionally set OAP_PI_SHA256 for exact-artifact evidence")
	}
	binary := verifiedPiBinary(t)
	mock := providertest.New(t, providertest.Config{OpenAIKey: piMockSecret})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)

	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	models := map[string]any{"providers": map[string]any{"oap-loopback": map[string]any{
		"baseUrl": mock.OpenAIBaseURL(), "api": "openai-responses", "apiKey": "$OAP_PI_MOCK_KEY",
		"models": []map[string]any{{"id": "fixture-model", "name": "OAP loopback fixture"}},
	}}}
	data, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "models.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	args := []string{"--provider", "oap-loopback", "--model", "fixture-model"}
	implementation := newPinnedPi(t, binary, root, piEnvironment(t, root, piMockSecret), args)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "pi-process-session",
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
		SessionID: "pi-process-session", Delivery: protocol.DeliveryAuto,
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
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	parts, ok := completed.FinalResponse.Content.Parts()
	if !ok || len(parts) != 1 || parts[0].Text != providertest.FixtureText {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}
	requests := mock.RequestsFor(providertest.OpenAIResponses)
	if len(requests) != 1 || requests[0].Path != providertest.ResponsesPath || requests[0].Model != "fixture-model" {
		t.Fatalf("Responses requests=%d: %+v", len(requests), requests)
	}
	if requests[0].Header.Get("Authorization") != "Bearer "+piMockSecret {
		t.Fatal("unexpected mock authorization")
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close Pi integration session: %v", err)
	}
	closed = true
}

func verifiedPiBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("OAP_PI_BIN")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("OAP_PI_BIN must be an absolute path to a Pi v0.85.1 executable")
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("OAP_PI_BIN is not an executable file: %v", err)
	}
	if expected := os.Getenv("OAP_PI_SHA256"); expected != "" {
		if len(expected) != sha256.Size*2 {
			t.Fatal("OAP_PI_SHA256 must be exactly 64 hexadecimal characters")
		}
		expectedDigest, err := hex.DecodeString(expected)
		if err != nil {
			t.Fatal("OAP_PI_SHA256 must be exactly 64 hexadecimal characters")
		}
		file, err := os.Open(binary)
		if err != nil {
			t.Fatalf("open OAP_PI_BIN for digest verification: %v", err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("hash OAP_PI_BIN: %v", copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close OAP_PI_BIN after hashing: %v", closeErr)
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedDigest)) {
			t.Fatal("OAP_PI_BIN SHA-256 does not match OAP_PI_SHA256")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "--version")
	command.Env = piEnvironment(t, t.TempDir(), "")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("verify Pi version: %v", err)
	}
	if got := strings.TrimSpace(string(output)); got != strings.TrimPrefix(PinnedVersion, "v") {
		t.Fatalf("Pi --version=%q, want %q", got, strings.TrimPrefix(PinnedVersion, "v"))
	}
	return binary
}

func newPinnedPi(t *testing.T, binary, root string, environment, args []string) *Adapter {
	t.Helper()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	implementation, err := New(Config{
		Executable: binary, Args: args, Environment: environment,
		WorkingDirectory: workspace, ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func piEnvironment(t *testing.T, root, secret string) []string {
	t.Helper()
	agentDir := filepath.Join(root, "agent")
	sessionDir := filepath.Join(root, "sessions")
	tmpDir := filepath.Join(root, "tmp")
	for _, directory := range []string{agentDir, sessionDir, tmpDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + root,
		"PI_CODING_AGENT_DIR=" + agentDir,
		"PI_CODING_AGENT_SESSION_DIR=" + sessionDir,
		"PI_TELEMETRY=0",
		"TMPDIR=" + tmpDir,
		"NO_COLOR=1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	if secret != "" {
		environment = append(environment, fmt.Sprintf("OAP_PI_MOCK_KEY=%s", secret))
	}
	return environment
}
