package opencode

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func serveInHome(t *testing.T, ctx context.Context, binary, home string, environment []string) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	command := exec.CommandContext(ctx, binary, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	command.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "NO_COLOR=1"}, environment...)
	stderr, err := os.Create(filepath.Join(home, fmt.Sprintf("stderr-%d.log", port)))
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start opencode serve: %v", err)
	}
	stop := func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
		_ = stderr.Close()
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for {
		response, err := client.Get(endpoint + "/api/health")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return endpoint, stop
			}
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("opencode serve did not become healthy")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestOpenCodeServerReopensItsBoundSessionAfterARestart(t *testing.T) {
	if os.Getenv("OAP_OPENCODE_INTEGRATION") != "1" {
		t.Skip("set OAP_OPENCODE_INTEGRATION=1 and absolute OAP_OPENCODE_BIN to run the pinned server gate; optionally set OAP_OPENCODE_SHA256 (64 hex characters) for exact-artifact evidence")
	}
	binary := adaptertest.VerifiedBinary(t, "OAP_OPENCODE_BIN", "OAP_OPENCODE_SHA256", "an opencode "+PinnedTag+" binary")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	config := `{"provider":{"fixture":{"npm":"@ai-sdk/openai-compatible","name":"fixture","options":{"baseURL":"http://127.0.0.1:1/v1","apiKey":"fixture"},"models":{"model":{"name":"model"}}}}}`
	environment := []string{"OPENCODE_CONFIG_CONTENT=" + config}
	home := t.TempDir()
	endpoint, stop := serveInHome(t, ctx, binary, home, environment)
	first, err := New(Config{Endpoint: endpoint, Model: &native.ModelRef{ID: "model", ProviderID: "fixture"}, Clock: systemClock{}, IDs: &sequenceIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := first.Open(ctx, base.OpenRequest{SessionID: "before", Participant: protocol.Participant{ID: "integration"}})
	if err != nil {
		t.Fatalf("open against real server: %v", err)
	}
	if _, _, err := opened.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "before", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Remember lantern-7319.")}}}}); err != nil {
		t.Fatal(err)
	}
	binding := opened.(base.NativeSession).NativeSessionID()
	stored := waitForStoredEvents(t, ctx, opened.(*session).client, native.SessionID(binding))
	stop()
	_ = opened.Close(context.Background())

	restarted, stopRestarted := serveInHome(t, ctx, binary, home, environment)
	defer stopRestarted()
	second, err := New(Config{Endpoint: restarted, Model: &native.ModelRef{ID: "model", ProviderID: "fixture"}, Clock: systemClock{}, IDs: &sequenceIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := second.Open(ctx, base.OpenRequest{SessionID: "after", Participant: protocol.Participant{ID: "integration"}, Reopen: true, NativeSessionID: binding})
	if err != nil {
		t.Fatalf("reopen the bound session on the restarted server: %v", err)
	}
	defer reopened.Close(context.Background())
	time.Sleep(3 * time.Second)
	state, err := reopened.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered || state.CurrentModelID != "fixture/model" || state.Status != protocol.SessionIdle || state.ActiveRunID != "" || state.TranscriptCursor != formatSeq(stored) {
		t.Fatalf("recovered state = %+v; the stored history must not replay as a run", state)
	}
	if got := reopened.(base.NativeSession).NativeSessionID(); got != binding {
		t.Fatalf("binding = %q, want %q", got, binding)
	}

	_, err = second.Open(ctx, base.OpenRequest{SessionID: "missing", Reopen: true, NativeSessionID: "ses_doesnotexist000000000000"})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("reopen of an unknown session = %v", err)
	}
}

func waitForStoredEvents(t *testing.T, ctx context.Context, client Client, id native.SessionID) int64 {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		last, err := lastDurableSeq(ctx, client, id)
		if err == nil && last >= 2 {
			return last
		}
		if time.Now().After(deadline) {
			t.Fatalf("the first run stored no events: last=%d err=%v", last, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
