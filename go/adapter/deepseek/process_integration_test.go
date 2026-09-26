package deepseek

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	deepseekMockSecret = "fixture-deepseek-key"

	deepseekRoute         = "deepseek-official"
	deepseekModel         = "deepseek-v4-pro"
	deepseekSmokeProvider = "deepseek-official"
	deepseekSmokeModel    = "deepseek-v4-pro"
)

func deepseekProfileArgs() []string { return []string{"--profile", "sdk"} }

func TestDeepSeekProcessSmoke(t *testing.T) {
	if os.Getenv("OAP_DEEPSEEK_HARNESS_SMOKE") != "1" {
		t.Skip("set OAP_DEEPSEEK_HARNESS_SMOKE=1 and absolute OAP_DEEPSEEK_HARNESS_BIN pointing to the pinned dsh-jsonrpc-agent runtime to run; optionally set OAP_DEEPSEEK_HARNESS_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := verifiedDeepSeekBinary(t)
	root := t.TempDir()
	implementation := newPinnedDeepSeek(t, binary, root, deepseekEnvironment(t, root, ""), deepseekProfileArgs(), deepseekSmokeProvider, deepseekSmokeModel)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "deepseek-smoke-session",
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
	if state.SessionID != "deepseek-smoke-session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}

	if err := session.Close(ctx); err != nil {
		t.Fatalf("close DeepSeek smoke session: %v", err)
	}
	closed = true
}

func TestDeepSeekProcessAgainstMessagesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in DeepSeek Harness process integration in short mode")
	}
	if os.Getenv("OAP_DEEPSEEK_HARNESS_INTEGRATION") != "1" {
		t.Skip("set OAP_DEEPSEEK_HARNESS_INTEGRATION=1 and absolute OAP_DEEPSEEK_HARNESS_BIN pointing to the pinned dsh-jsonrpc-agent runtime to run; optionally set OAP_DEEPSEEK_HARNESS_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := verifiedDeepSeekBinary(t)
	mock := providertest.New(t, providertest.Config{AnthropicKey: deepseekMockSecret})
	mock.Enqueue(providertest.AnthropicMessages, providertest.Success)

	root := t.TempDir()
	implementation := newPinnedDeepSeek(t, binary, root, deepseekEnvironment(t, root, mock.AnthropicBaseURL()), deepseekProfileArgs(), deepseekRoute, deepseekModel)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := implementation.Open(ctx, base.OpenRequest{
		SessionID:   "deepseek-process-session",
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
		SessionID: "deepseek-process-session", Delivery: protocol.DeliveryAuto,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Reply with the fixture response.")}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if admission.Admission != protocol.AdmissionStarted {
		t.Fatalf("admission=%+v", admission)
	}
	events := adaptertest.Drain(t, stream, 30*time.Second)
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

	text, ok := completed.FinalResponse.Content.Text()
	if !ok || text != providertest.FixtureText {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}
	if streamed.String() != providertest.FixtureText {
		t.Fatalf("streamed response=%q", streamed.String())
	}
	requests := mock.RequestsFor(providertest.AnthropicMessages)
	if len(requests) != 1 || requests[0].Path != providertest.MessagesPath || requests[0].Model != deepseekModel {
		t.Fatalf("messages requests=%d: %+v", len(requests), requests)
	}
	if requests[0].Header.Get("x-api-key") != deepseekMockSecret {
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
		t.Fatalf("close DeepSeek integration session: %v", err)
	}
	closed = true
}

func verifiedDeepSeekBinary(t *testing.T) string {
	t.Helper()
	binary := adaptertest.VerifiedBinary(t, "OAP_DEEPSEEK_HARNESS_BIN", "OAP_DEEPSEEK_HARNESS_SHA256", "the pinned dsh-jsonrpc-agent runtime executable")
	return binary
}

func newPinnedDeepSeek(t *testing.T, binary, root string, environment, args []string, provider, model string) *Adapter {
	t.Helper()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	implementation, err := New(Config{
		Executable: binary, Args: args, Environment: environment,
		WorkingDirectory: workspace, Provider: provider, Model: model,
		ShutdownTimeout: 5 * time.Second,

		ProcessFactory: ProcessFactoryFunc(func(ctx context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
			process, err := rpc.Start(ctx, config)
			if err != nil {
				return nil, err
			}
			if process.Initialize.ServerInfo.Name != native.ServerName || process.Initialize.ServerInfo.Version != native.ServerVersion {
				_ = process.Close(context.Background())
				return nil, fmt.Errorf("serverInfo %q/%q is not the pinned %s/%s runtime", process.Initialize.ServerInfo.Name, process.Initialize.ServerInfo.Version, native.ServerName, native.ServerVersion)
			}
			return &rpcProcess{process}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func deepseekEnvironment(t *testing.T, root, loopbackBaseURL string) []string {
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
		"HTTP_PROXY=http://127.0.0.1:1",
		"HTTPS_PROXY=http://127.0.0.1:1",
		"ALL_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",

		"DSH_HOME=" + filepath.Join(root, "dsh-home"),
		"DSH_CWD=" + root,
		"DSH_SESSION_ROOT=" + filepath.Join(root, "dsh-sessions"),
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	if loopbackBaseURL != "" {

		environment = append(environment,
			"DEEPSEEK_API_KEY="+deepseekMockSecret,
			"DEEPSEEK_BASE_URL="+loopbackBaseURL,
		)
	}
	return environment
}
