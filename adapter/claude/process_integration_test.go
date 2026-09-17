package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/internal/providertest"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	claudeMockSecret = "fixture-claude-key"

	claudeLoopbackModel = "claude-sonnet-4-5"
)

func verifiedClaudeCLI(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("OAP_CLAUDE_BIN")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("OAP_CLAUDE_BIN must be an absolute path to the pinned claude binary (2.1.263)")
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("OAP_CLAUDE_BIN is not an executable file: %v", err)
	}
	if expected := os.Getenv("OAP_CLAUDE_SHA256"); expected != "" {
		if len(expected) != sha256.Size*2 {
			t.Fatal("OAP_CLAUDE_SHA256 must be exactly 64 hexadecimal characters")
		}
		expectedDigest, err := hex.DecodeString(expected)
		if err != nil {
			t.Fatal("OAP_CLAUDE_SHA256 must be exactly 64 hexadecimal characters")
		}
		file, err := os.Open(binary)
		if err != nil {
			t.Fatalf("open OAP_CLAUDE_BIN for digest verification: %v", err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("hash OAP_CLAUDE_BIN: %v", copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close OAP_CLAUDE_BIN after hashing: %v", closeErr)
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedDigest)) {
			t.Fatal("OAP_CLAUDE_BIN SHA-256 does not match OAP_CLAUDE_SHA256")
		}
	}
	return binary
}

func newPinnedClaude(t *testing.T, environment []string, workDir string) *Adapter {
	t.Helper()
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	implementation, err := New(Config{
		Executable:       verifiedClaudeCLI(t),
		Environment:      environment,
		WorkingDirectory: workDir,
		Model:            claudeLoopbackModel,
		ExitTimeout:      15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func TestClaudeProcessSmoke(t *testing.T) {
	if os.Getenv("OAP_CLAUDE_SMOKE") != "1" {
		t.Skip("set OAP_CLAUDE_SMOKE=1 with absolute OAP_CLAUDE_BIN (pinned claude 2.1.263 binary) to run; optionally set OAP_CLAUDE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	root := t.TempDir()
	implementation := newPinnedClaude(t, claudeEnvironment(t, root, ""), filepath.Join(root, "work"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "claude-smoke-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatalf("open pinned CLI (spawn + initialize exchange): %v", err)
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
	if state.SessionID != "claude-smoke-session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}

	if err := session.Close(ctx); err != nil {
		t.Fatalf("close claude smoke session: %v", err)
	}
	closed = true
}

func TestClaudeProcessAgainstMessagesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Claude Code process integration in short mode")
	}
	if os.Getenv("OAP_CLAUDE_INTEGRATION") != "1" {
		t.Skip("set OAP_CLAUDE_INTEGRATION=1 with absolute OAP_CLAUDE_BIN (pinned claude 2.1.263 binary) to run; optionally set OAP_CLAUDE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	mock := providertest.New(t, providertest.Config{AnthropicKey: claudeMockSecret})
	mock.Enqueue(providertest.AnthropicMessages, providertest.Success)
	root := t.TempDir()
	implementation := newPinnedClaude(t, claudeEnvironment(t, root, mock.AnthropicBaseURL()), filepath.Join(root, "work"))

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "claude-process-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatalf("open pinned CLI (spawn + initialize exchange): %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "claude-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if admission.Admission != protocol.AdmissionStarted || admission.RunID == "" {
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
	if streamed.Len() == 0 {
		t.Fatal("run produced no streamed content deltas")
	}
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", events[len(events)-1].Type)
	}
	var completed protocol.RunCompletedPayload
	if err := events[len(events)-1].DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	if text, ok := completed.FinalResponse.Content.Text(); !ok || !strings.Contains(text, providertest.FixtureText) {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}

	requests := mock.RequestsFor(providertest.AnthropicMessages)
	if len(requests) == 0 {
		t.Fatal("loopback provider received no messages request")
	}
	first := requests[0]
	if first.Path != providertest.MessagesPath || first.Model != claudeLoopbackModel {
		t.Fatalf("messages request: %+v", first)
	}
	if first.Header.Get("x-api-key") != claudeMockSecret {
		t.Fatal("unexpected mock authentication")
	}
	if first.Header.Get("anthropic-version") == "" {
		t.Fatal("messages request carried no anthropic-version header")
	}
	if strings.HasPrefix(first.Header.Get("Authorization"), "Bearer ") {
		t.Fatal("messages request carried a bearer token")
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle {
		t.Fatalf("post-run state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close claude integration session: %v", err)
	}
	closed = true
}

func claudeEnvironment(t *testing.T, root, loopbackBaseURL string) []string {
	t.Helper()
	home := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	tmpDir := filepath.Join(root, "tmp")
	for _, directory := range []string{home, configDir, tmpDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + home,
		"CLAUDE_CONFIG_DIR=" + configDir,
		"TMPDIR=" + tmpDir,
		"LANG=C.UTF-8",
		"NO_COLOR=1",
		"DISABLE_TELEMETRY=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	if loopbackBaseURL != "" {
		environment = append(environment,
			"ANTHROPIC_BASE_URL="+loopbackBaseURL,
			"ANTHROPIC_API_KEY="+claudeMockSecret,
		)
	}
	return environment
}

func TestClaudeProcessEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ambient-must-not-leak")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "ambient-must-not-leak")
	environment := claudeEnvironment(t, t.TempDir(), "")
	for _, entry := range environment {
		for _, secret := range []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "ANTHROPIC_BASE_URL="} {
			if strings.HasPrefix(entry, secret) {
				t.Fatalf("credential-free environment carries %s", secret)
			}
		}
	}
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "ambient-must-not-leak") {
		t.Fatal("ambient credential leaked into the child environment")
	}
	loopback := claudeEnvironment(t, t.TempDir(), "http://127.0.0.1:1")
	hasKey := false
	for _, entry := range loopback {
		if entry == "ANTHROPIC_API_KEY="+claudeMockSecret {
			hasKey = true
		}
	}
	if !hasKey {
		t.Fatal("loopback environment lost the test-owned key")
	}
}
