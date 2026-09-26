package opencode

import (
	"context"
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "serve", "--hostname", "127.0.0.1", "--port", fmt.Sprint(port))
	command.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "NO_COLOR=1"}
	stderr, err := os.Create(filepath.Join(home, "stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start opencode serve: %v", err)
	}
	defer func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	}()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(60 * time.Second)
	for {
		response, err := client.Get(endpoint + "/api/health")
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

	adapter, err := New(Config{Endpoint: endpoint, Clock: systemClock{}, IDs: &sequenceIDs{}})
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
