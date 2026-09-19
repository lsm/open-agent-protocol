package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	hermesMockSecret = "fixture-hermes-key"

	hermesLoopbackModel = "gpt-5.6-sol"
)

func verifiedHermesPython(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("OAP_HERMES_BIN")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("OAP_HERMES_BIN must be an absolute path to the python interpreter that runs the pinned hermes-agent checkout")
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("OAP_HERMES_BIN is not an executable file: %v", err)
	}
	if expected := os.Getenv("OAP_HERMES_SHA256"); expected != "" {
		if len(expected) != sha256.Size*2 {
			t.Fatal("OAP_HERMES_SHA256 must be exactly 64 hexadecimal characters")
		}
		expectedDigest, err := hex.DecodeString(expected)
		if err != nil {
			t.Fatal("OAP_HERMES_SHA256 must be exactly 64 hexadecimal characters")
		}
		file, err := os.Open(binary)
		if err != nil {
			t.Fatalf("open OAP_HERMES_BIN for digest verification: %v", err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("hash OAP_HERMES_BIN: %v", copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close OAP_HERMES_BIN after hashing: %v", closeErr)
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedDigest)) {
			t.Fatal("OAP_HERMES_BIN SHA-256 does not match OAP_HERMES_SHA256")
		}
	}
	return binary
}

func verifiedHermesRoot(t *testing.T) string {
	t.Helper()
	root := os.Getenv("OAP_HERMES_ROOT")
	if root == "" || !filepath.IsAbs(root) {
		t.Fatal("OAP_HERMES_ROOT must be an absolute path to the pinned hermes-agent checkout (release v2026.8.31)")
	}
	entry := filepath.Join(root, "tui_gateway", "entry.py")
	if _, err := os.Stat(entry); err != nil {
		t.Fatalf("OAP_HERMES_ROOT does not contain tui_gateway/entry.py: %v", err)
	}
	return root
}

func newPinnedHermes(t *testing.T, root string, environment []string, model string) *Adapter {
	t.Helper()
	implementation, err := New(Config{
		Executable: verifiedHermesPython(t), Args: []string{"-m", "tui_gateway.entry"},
		Environment: environment, WorkingDirectory: root, Model: model,
		ExitTimeout: 15 * time.Second,
		ProcessFactory: ProcessFactoryFunc(func(ctx context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
			process, err := rpc.Start(ctx, config)
			if err != nil {
				return nil, err
			}
			if len(process.Ready.ReplayEpoch) != 32 || !process.Ready.ChangeEvents {
				_ = process.Close(context.Background())
				return nil, fmt.Errorf("gateway.ready epoch=%q change_events=%v is not the pinned handshake", process.Ready.ReplayEpoch, process.Ready.ChangeEvents)
			}
			return &rpcProcess{process}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func TestHermesProcessSmoke(t *testing.T) {
	if os.Getenv("OAP_HERMES_SMOKE") != "1" {
		t.Skip("set OAP_HERMES_SMOKE=1 with absolute OAP_HERMES_BIN (python interpreter) and OAP_HERMES_ROOT (pinned hermes-agent checkout) to run; optionally set OAP_HERMES_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	implementation := newPinnedHermes(t, verifiedHermesRoot(t), hermesEnvironment(t, t.TempDir(), ""), "")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "hermes-smoke-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatalf("open pinned gateway (ready handshake + session.create): %v", err)
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
	if state.SessionID != "hermes-smoke-session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}

	if err := session.Close(ctx); err != nil {
		t.Fatalf("close hermes smoke session: %v", err)
	}
	closed = true
}

func TestHermesProcessAgainstChatMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in Hermes process integration in short mode")
	}
	if os.Getenv("OAP_HERMES_INTEGRATION") != "1" {
		t.Skip("set OAP_HERMES_INTEGRATION=1 with absolute OAP_HERMES_BIN (python interpreter) and OAP_HERMES_ROOT (pinned hermes-agent checkout) to run; optionally set OAP_HERMES_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	mock := providertest.New(t, providertest.Config{OpenAIKey: hermesMockSecret})
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)
	root := verifiedHermesRoot(t)
	isolated := t.TempDir()
	environment := hermesEnvironment(t, isolated, mock.OpenAIBaseURL())
	writeHermesLoopbackConfig(t, isolated, mock.OpenAIBaseURL())
	implementation := newPinnedHermes(t, root, environment, hermesLoopbackModel)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "hermes-process-session",
		Participant: protocol.Participant{ID: "integration-user"},
	})
	if err != nil {
		t.Fatalf("open pinned gateway: %v", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = session.Close(context.Background())
		}
	}()
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "hermes-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if admission.Admission != protocol.AdmissionStarted {
		t.Fatalf("admission=%+v", admission)
	}
	events := adaptertest.Drain(t, stream, 45*time.Second)
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
	if text, ok := completed.FinalResponse.Content.Text(); !ok || !strings.Contains(text, "fixture response") {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}
	requests := mock.RequestsFor(providertest.OpenAIChatCompletion)
	if len(requests) == 0 {
		t.Fatal("loopback provider received no chat completions request")
	}
	if requests[0].Path != providertest.ChatCompletionPath || requests[0].Model != hermesLoopbackModel {
		t.Fatalf("chat completions request: %+v", requests[0])
	}
	if requests[0].Header.Get("Authorization") != "Bearer "+hermesMockSecret {
		t.Fatal("unexpected mock authorization")
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != protocol.SessionIdle {
		t.Fatalf("post-run state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close hermes integration session: %v", err)
	}
	closed = true
}

func writeHermesLoopbackConfig(t *testing.T, root, baseURL string) {
	t.Helper()
	directory := filepath.Join(root, "home", ".hermes")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	config := "model:\n" +
		"  provider: custom:loopback\n" +
		"  model: " + hermesLoopbackModel + "\n" +
		"custom_providers:\n" +
		"  - name: loopback\n" +
		"    base_url: " + strconv.Quote(baseURL) + "\n" +
		"    key_env: OPENAI_API_KEY\n" +
		"    api_mode: chat\n" +
		"    models:\n" +
		"      - " + hermesLoopbackModel + "\n"
	if err := os.WriteFile(filepath.Join(directory, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hermesEnvironment(t *testing.T, root, loopbackBaseURL string) []string {
	t.Helper()
	home := filepath.Join(root, "home")
	configDir := filepath.Join(root, "config")
	cacheDir := filepath.Join(root, "cache")
	dataDir := filepath.Join(root, "data")
	stateDir := filepath.Join(root, "state")
	tmpDir := filepath.Join(root, "tmp")
	for _, directory := range []string{home, configDir, cacheDir, dataDir, stateDir, tmpDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	environment := []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + configDir,
		"XDG_CACHE_HOME=" + cacheDir,
		"XDG_DATA_HOME=" + dataDir,
		"XDG_STATE_HOME=" + stateDir,
		"TMPDIR=" + tmpDir,
		"NO_COLOR=1",
		"PYTHONUNBUFFERED=1",
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
			"OPENAI_BASE_URL="+loopbackBaseURL,
			"OPENAI_API_KEY="+hermesMockSecret,
		)
	}
	return environment
}
