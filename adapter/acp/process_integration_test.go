package acp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// The gate drives an independently built, open-source ACP *agent* through the
// OAP ACP client adapter. docker/cagent (module github.com/docker/docker-agent)
// is Apache-2.0 and speaks the same coder/acp-go-sdk v1 surface the adapter
// targets, so it is a real cross-implementation peer rather than a fixture
// written against our own reducer.
const (
	acpMockSecret = "fixture-acp-key"
	acpRouteModel = "fixture-model"
	// acpTokenEnv is the environment variable name the agent config references
	// through token_key; only its test-owned value is ever supplied to the child.
	acpTokenEnv = "OAP_ACP_TOKEN"
)

// acpArgs is the pinned launch contract: `--data-dir` isolates the SQLite
// session store inside the temporary root, and `serve acp` runs the stdio ACP
// server over the generated agent file.
func acpArgs(root string) []string {
	return []string{"--data-dir", filepath.Join(root, "data"), "serve", "acp", filepath.Join(root, "agent.yaml")}
}

// TestACPProcessSmoke is credential-free evidence that a supplied open-source
// ACP agent binary starts as a standards-conforming ACP v1 server: the adapter
// completes `initialize`, negotiates protocol version 1, opens a native session
// through `session/new`, and tears the process down. Set OAP_ACP_SHA256 to bind
// the evidence to an exact artifact; the reported agent version alone does not
// prove the pinned source commit.
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

// TestACPProcessAgainstChatCompletionsMock is the hermetic behavioral gate. The
// only configured provider endpoint is an in-process loopback server, and the
// child receives a fixed allowlisted environment containing no ambient secrets.
// This is runtime-version evidence unless OAP_ACP_SHA256 binds the exact
// artifact.
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
	// ACP v1 keeps session/prompt pending for the whole turn, so admission is
	// synthesized once the complete request frame has been written.
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
	binary := os.Getenv("OAP_ACP_BIN")
	if binary == "" || !filepath.IsAbs(binary) {
		t.Fatal("OAP_ACP_BIN must be an absolute path to the pinned ACP agent server executable")
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
		t.Fatalf("OAP_ACP_BIN is not an executable file: %v", err)
	}
	if expected := os.Getenv("OAP_ACP_SHA256"); expected != "" {
		if len(expected) != sha256.Size*2 {
			t.Fatal("OAP_ACP_SHA256 must be exactly 64 hexadecimal characters")
		}
		expectedDigest, err := hex.DecodeString(expected)
		if err != nil {
			t.Fatal("OAP_ACP_SHA256 must be exactly 64 hexadecimal characters")
		}
		file, err := os.Open(binary)
		if err != nil {
			t.Fatalf("open OAP_ACP_BIN for digest verification: %v", err)
		}
		hash := sha256.New()
		_, copyErr := io.Copy(hash, file)
		closeErr := file.Close()
		if copyErr != nil {
			t.Fatalf("hash OAP_ACP_BIN: %v", copyErr)
		}
		if closeErr != nil {
			t.Fatalf("close OAP_ACP_BIN after hashing: %v", closeErr)
		}
		if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), hex.EncodeToString(expectedDigest)) {
			t.Fatal("OAP_ACP_BIN SHA-256 does not match OAP_ACP_SHA256")
		}
	}
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

// writeACPAgentConfig generates the isolated agent file. The provider is either
// the loopback mock or a dead loopback address; no checked-in real-provider
// configuration or ambient credential is ever reused.
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

// acpEnvironment fully replaces the child environment: isolated HOME and config
// directories, telemetry disabled, dead-loopback proxies with loopback-only
// NO_PROXY so nothing but the loopback mock is reachable, and only the fixed
// non-secret placeholder token. No ambient credential is forwarded.
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
