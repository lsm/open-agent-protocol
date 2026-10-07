package opencode

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestOpenCodeServerIntegration(t *testing.T) {
	if os.Getenv("OAP_OPENCODE_INTEGRATION") != "1" {
		t.Skip("set OAP_OPENCODE_INTEGRATION=1 and absolute OAP_OPENCODE_BIN to run the pinned server gate; optionally set OAP_OPENCODE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := adaptertest.VerifiedBinary(t, "OAP_OPENCODE_BIN", "OAP_OPENCODE_SHA256", "an opencode "+PinnedTag+" binary")
	if got := os.Getenv("OAP_OPENCODE_TAG"); got != "" && got != PinnedTag {
		t.Fatalf("OAP_OPENCODE_TAG=%q does not match the pinned %s", got, PinnedTag)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	endpoint := startPinnedServer(t, ctx, binary, nil)

	adapter, err := New(integrationConfig(endpoint, nil))
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := adapter.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if descriptor.CapabilityRevision != CapabilityRevision {
		t.Fatalf("revision=%s", descriptor.CapabilityRevision)
	}
	session, err := adapter.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "integration"}})
	if err != nil {
		t.Fatalf("open against real server: %v", err)
	}
	state, err := session.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "session" || state.Status != protocol.SessionIdle {
		t.Fatalf("state=%+v", state)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

const integrationPassword = "integration"

func integrationConfig(endpoint string, model *native.ModelRef) Config {
	return Config{Endpoint: endpoint, Username: "opencode", Password: integrationPassword, Model: model, Clock: systemClock{}, IDs: &sequenceIDs{}}
}

func startPinnedServer(t *testing.T, ctx context.Context, binary string, environment []string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	home := t.TempDir()
	command := exec.CommandContext(ctx, binary, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	command.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "NO_COLOR=1", "OPENCODE_PASSWORD=" + integrationPassword}, environment...)
	stderr, err := os.Create(filepath.Join(home, "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start opencode serve: %v", err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/info", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.SetBasicAuth("opencode", integrationPassword)
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("opencode serve did not become healthy")
		}
		time.Sleep(250 * time.Millisecond)
	}

	return endpoint
}

func TestOpenCodeServerTakesALiveVariant(t *testing.T) {
	if os.Getenv("OAP_OPENCODE_INTEGRATION") != "1" {
		t.Skip("set OAP_OPENCODE_INTEGRATION=1 and absolute OAP_OPENCODE_BIN to run the pinned server gate; optionally set OAP_OPENCODE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := adaptertest.VerifiedBinary(t, "OAP_OPENCODE_BIN", "OAP_OPENCODE_SHA256", "an opencode "+PinnedTag+" binary")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config := `{"provider":{"fixture":{"npm":"@ai-sdk/openai-compatible","name":"fixture","options":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"fixture"},"models":{"model":{"name":"model","reasoning":true,"variants":{"low":{"reasoningEffort":"low"},"high":{"reasoningEffort":"high"}}}}}}}`
	endpoint := startPinnedServer(t, ctx, binary, []string{"OPENCODE_CONFIG_CONTENT=" + config})
	adapter, err := New(integrationConfig(endpoint, &native.ModelRef{ID: "model", ProviderID: "fixture"}))
	if err != nil {
		t.Fatal(err)
	}
	live, err := adapter.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "integration"}, ReasoningLevel: protocol.ReasoningLow})
	if err != nil {
		t.Fatalf("open against real server: %v", err)
	}
	defer live.Close(ctx)
	response, state, err := live.(base.SettingsUpdater).UpdateSettings(ctx, protocol.SessionSettingsUpdateRequest{SessionID: "session", ReasoningLevel: protocol.ReasoningHigh})
	if err != nil {
		t.Fatal(err)
	}
	if response.PreviousReasoningLevel != protocol.ReasoningLow || response.ReasoningLevel != protocol.ReasoningHigh || state.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("response %+v and state %q, want low replaced by high", response, state.ReasoningLevel)
	}
	opened := live.(*session)
	recorded, err := opened.client.Session(ctx, opened.nativeID)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.Model == nil || recorded.Model.ID != "model" || recorded.Model.ProviderID != "fixture" || recorded.Model.Variant != "high" {
		t.Fatalf("the server records %+v, want fixture/model at high", recorded.Model)
	}
}

func fakeProvider(t *testing.T, reply string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta string, finish string) string {
			finishField := "null"
			if finish != "" {
				finishField = strconv.Quote(finish)
			}
			return `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"model","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finishField + `}]}` + "\n\n"
		}
		_, _ = io.WriteString(w, chunk(`{"role":"assistant","content":`+strconv.Quote(reply)+`}`, ""))
		_, _ = io.WriteString(w, chunk(`{}`, "stop"))
		_, _ = io.WriteString(w, `data: {"id":"c1","object":"chat.completion.chunk","created":1,"model":"model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server.URL + "/v1"
}

func TestOpenCodeServerRunsATurnToCompletionAgainstALocalProvider(t *testing.T) {
	if os.Getenv("OAP_OPENCODE_INTEGRATION") != "1" {
		t.Skip("set OAP_OPENCODE_INTEGRATION=1 and absolute OAP_OPENCODE_BIN to run the pinned server gate; optionally set OAP_OPENCODE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := adaptertest.VerifiedBinary(t, "OAP_OPENCODE_BIN", "OAP_OPENCODE_SHA256", "an opencode "+PinnedTag+" binary")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config := `{"provider":{"fixture":{"npm":"@ai-sdk/openai-compatible","name":"fixture","options":{"baseURL":` + strconv.Quote(fakeProvider(t, "pong")) + `,"apiKey":"fixture"},"models":{"model":{"name":"model"}}}},"model":"fixture/model","small_model":"fixture/model"}`
	endpoint := startPinnedServer(t, ctx, binary, []string{"OPENCODE_CONFIG_CONTENT=" + config})
	adapter, err := New(integrationConfig(endpoint, &native.ModelRef{ID: "model", ProviderID: "fixture"}))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := adapter.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "integration"}})
	if err != nil {
		t.Fatalf("open against real server: %v", err)
	}
	defer opened.Close(context.Background())
	response, stream, err := opened.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("ping")}}}})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 90*time.Second)
	adaptertest.AssertRunTrace(t, response, CapabilityRevision, events)
	last := events[len(events)-1]
	if last.Type != protocol.TypeRunCompleted {
		t.Fatalf("the turn ended %s: %v", last.Type, types(events))
	}
	var completed protocol.RunCompletedPayload
	if err := last.DecodePayload(&completed); err != nil {
		t.Fatal(err)
	}
	parts, ok := completed.FinalResponse.Content.Parts()
	if !ok || len(parts) != 1 || parts[0].Type != protocol.ContentText || parts[0].Text != "pong" {
		t.Fatalf("final response = %s, want the provider's one text part", completed.FinalResponse.Content)
	}
	nativeID := opened.(interface{ NativeSessionID() string }).NativeSessionID()
	read, err := adapter.NativeRead(ctx, base.NativeReadRequest{NativeID: nativeID})
	if err != nil {
		t.Fatal(err)
	}
	if len(read) != 2 || read[0].Role != "user" || read[0].Text != "ping" || read[1].Role != "assistant" || read[1].Text != "pong" || read[0].AtMS <= 0 || read[1].AtMS < read[0].AtMS {
		t.Fatalf("the server's own record of the turn read %+v", read)
	}
}
