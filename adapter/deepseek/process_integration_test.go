package deepseek

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/adapter/deepseek/internal/native"
	"github.com/lsm/open-agent-protocol/adapter/deepseek/internal/rpc"
	"github.com/lsm/open-agent-protocol/internal/providertest"
	"github.com/lsm/open-agent-protocol/protocol"
)

const (
	deepseekMockSecret = "fixture-deepseek-key"
	// The pinned SDK boundary is the shipped `sdk` profile, which stacks the
	// dsh-base bundle with the dsh-sdk-app patch layer that mounts
	// dsh-sdk-jsonrpc-server. The stock deepseek provider reads
	// DEEPSEEK_BASE_URL/DEEPSEEK_API_KEY, which is how the loopback gate
	// redirects it.
	deepseekRoute         = "deepseek-official"
	deepseekModel         = "deepseek-v4-pro"
	deepseekSmokeProvider = "deepseek-official"
	deepseekSmokeModel    = "deepseek-v4-pro"
)

// deepseekProfileArgs is the pinned launch contract: the runtime boots a
// profile under $DSH_HOME/profiles rather than a positional composition file.
func deepseekProfileArgs() []string { return []string{"--profile", "sdk"} }

// TestDeepSeekProcessSmoke is credential-free runtime evidence that a supplied
// executable starts as the pinned dsh-jsonrpc-agent runtime and completes the
// adapter readiness handshake over its own process layer. The correlated
// initialize and shutdown round-trips prove request/response correlation, and
// a nil Session.Close error proves the process exited. Set
// OAP_DEEPSEEK_HARNESS_SHA256 to bind the evidence to an exact artifact; the
// serverInfo version alone does not prove the pinned source commit.
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
	// Close sends the correlated shutdown request and only returns nil after
	// the child process exits; any hang or nonzero exit fails the gate.
	if err := session.Close(ctx); err != nil {
		t.Fatalf("close DeepSeek smoke session: %v", err)
	}
	closed = true
}

// TestDeepSeekProcessAgainstResponsesMock is the hermetic behavioral gate. The
// only configured provider endpoint is an in-process loopback server, and the
// child receives a fixed allowlisted environment containing no ambient
// secrets. This is runtime-version evidence unless OAP_DEEPSEEK_HARNESS_SHA256
// binds the exact artifact.
func TestDeepSeekProcessAgainstResponsesMock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping opt-in DeepSeek Harness process integration in short mode")
	}
	if os.Getenv("OAP_DEEPSEEK_HARNESS_INTEGRATION") != "1" {
		t.Skip("set OAP_DEEPSEEK_HARNESS_INTEGRATION=1 and absolute OAP_DEEPSEEK_HARNESS_BIN pointing to the pinned dsh-jsonrpc-agent runtime to run; optionally set OAP_DEEPSEEK_HARNESS_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := verifiedDeepSeekBinary(t)
	mock := providertest.New(t, providertest.Config{OpenAIKey: deepseekMockSecret})
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)

	root := t.TempDir()
	implementation := newPinnedDeepSeek(t, binary, root, deepseekEnvironment(t, root, mock.OpenAIBaseURL()), deepseekProfileArgs(), deepseekRoute, deepseekModel)

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
	// Admission is only "started" after the receipt matched a direct-user
	// user/message inside an owned turn and step.
	if admission.Admission != protocol.AdmissionStarted {
		t.Fatalf("admission=%+v", admission)
	}
	events := adaptertest.Drain(t, stream, 30*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	// The adapter withholds the single terminal until the owned turn/end
	// "completed" and a later session.status idle were both observed, so a
	// run.completed terminal is the owned settlement proof.
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
	// A lone text part is normalized to bare-string content (the shape the
	// deepseek corpus fixtures pin), so assert through the text reader rather
	// than the parts reader.
	text, ok := completed.FinalResponse.Content.Text()
	if !ok || text != providertest.FixtureText {
		t.Fatalf("final response=%s", completed.FinalResponse.Content)
	}
	if streamed.String() != providertest.FixtureText {
		t.Fatalf("streamed response=%q", streamed.String())
	}
	requests := mock.RequestsFor(providertest.OpenAIChatCompletion)
	if len(requests) != 1 || requests[0].Path != providertest.ChatCompletionPath || requests[0].Model != deepseekModel {
		t.Fatalf("chat-completions requests=%d: %+v", len(requests), requests)
	}
	if requests[0].Header.Get("Authorization") != "Bearer "+deepseekMockSecret {
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
	binary := os.Getenv("OAP_DEEPSEEK_HARNESS_BIN")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("OAP_DEEPSEEK_HARNESS_BIN must be an absolute path to the pinned dsh-jsonrpc-agent runtime executable")
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("OAP_DEEPSEEK_HARNESS_BIN is not an executable file: %v", err)
	}
	if expected := os.Getenv("OAP_DEEPSEEK_HARNESS_SHA256"); expected != "" {
		if len(expected) != sha256.Size*2 {
			t.Fatal("OAP_DEEPSEEK_HARNESS_SHA256 must be exactly 64 hexadecimal characters")
		}
		expectedDigest, err := hex.DecodeString(expected)
		if err != nil {
			t.Fatal("OAP_DEEPSEEK_HARNESS_SHA256 must be exactly 64 hexadecimal characters")
		}
		file, err := os.Open(binary)
		if err != nil {
			t.Fatalf("open OAP_DEEPSEEK_HARNESS_BIN for digest verification: %v", err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("hash OAP_DEEPSEEK_HARNESS_BIN: %v", copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close OAP_DEEPSEEK_HARNESS_BIN after hashing: %v", closeErr)
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedDigest)) {
			t.Fatal("OAP_DEEPSEEK_HARNESS_BIN SHA-256 does not match OAP_DEEPSEEK_HARNESS_SHA256")
		}
	}
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
		// rpc.Start already rejects any other serverInfo during the adapter's
		// own startup; restating the exact pinned values here keeps the
		// observed identity in the gate evidence itself.
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

// deepseekEnvironment fully replaces the child environment: isolated HOME,
// config, cache, data, state, and TMP directories; dead-loopback proxies with
// loopback-only NO_PROXY so nothing but the loopback mock is reachable; and no
// ambient credentials. Only the fixed non-secret placeholder key is injected
// for the loopback gate.
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
		// The runtime boots a profile under $DSH_HOME/profiles and writes
		// sessions under $DSH_SESSION_ROOT; both stay inside the isolated
		// temporary root.
		"DSH_HOME=" + filepath.Join(root, "dsh-home"),
		"DSH_CWD=" + root,
		"DSH_SESSION_ROOT=" + filepath.Join(root, "dsh-sessions"),
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	if loopbackBaseURL != "" {
		// The stock deepseek provider reads these; the pinned runtime appends
		// /chat/completions to the base URL.
		environment = append(environment,
			"DEEPSEEK_API_KEY="+deepseekMockSecret,
			"DEEPSEEK_BASE_URL="+loopbackBaseURL,
		)
	}
	return environment
}
