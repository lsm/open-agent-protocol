package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type reloadCapture struct {
	mu      sync.Mutex
	frames  []acpCorpusFrame
	pending map[string][]byte
}

type reloadTap struct {
	capture   *reloadCapture
	direction string
}

func (w reloadTap) Write(data []byte) (int, error) {
	w.capture.mu.Lock()
	defer w.capture.mu.Unlock()
	buffer := append(w.capture.pending[w.direction], data...)
	for {
		end := bytes.IndexByte(buffer, '\n')
		if end < 0 {
			break
		}
		line := buffer[:end]
		if len(line) > 0 {
			w.capture.frames = append(w.capture.frames, acpCorpusFrame{Direction: w.direction, Classification: "native", Fidelity: "native", Raw: append(json.RawMessage(nil), line...)})
		}
		buffer = buffer[end+1:]
	}
	w.capture.pending[w.direction] = append([]byte(nil), buffer...)
	return len(data), nil
}

type capturedACPClient struct {
	*rpc.Client
	command   *exec.Cmd
	stdin     io.Closer
	stdout    io.Closer
	wait      chan error
	closeOnce sync.Once
}

func (c *capturedACPClient) Close() error {
	c.closeOnce.Do(func() {
		_ = c.Client.Close()
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		select {
		case <-c.wait:
		case <-time.After(time.Second):
			_ = c.command.Process.Kill()
			<-c.wait
		}
	})
	return nil
}

func recordingACPFactory(binary, root string, environment []string, capture *reloadCapture) ClientFactory {
	return ClientFactoryFunc(func(ctx context.Context) (Client, rpc.InitializeResponse, error) {
		command := exec.Command(binary, acpArgs(root)...)
		command.Dir = filepath.Join(root, "workspace")
		command.Env = environment
		stdin, err := command.StdinPipe()
		if err != nil {
			return nil, rpc.InitializeResponse{}, err
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			_ = stdin.Close()
			return nil, rpc.InitializeResponse{}, err
		}
		if err = command.Start(); err != nil {
			_ = stdin.Close()
			_ = stdout.Close()
			return nil, rpc.InitializeResponse{}, err
		}
		client := &capturedACPClient{command: command, stdin: stdin, stdout: stdout, wait: make(chan error, 1)}
		client.Client = rpc.NewClient(io.TeeReader(stdout, reloadTap{capture, "agent-to-host"}), io.MultiWriter(reloadTap{capture, "host-to-agent"}, stdin), rpc.ClientOptions{StrictResponseIDs: true})
		go func() { client.wait <- command.Wait() }()
		var initialized rpc.InitializeResponse
		if err := client.Call(ctx, "initialize", rpc.InitializeRequest{ProtocolVersion: 1, ClientCapabilities: rpc.ClientCapabilities{}, ClientInfo: &rpc.Implementation{Name: "open-agent-protocol", Version: protocol.Version}}, &initialized); err != nil {
			_ = client.Close()
			return nil, initialized, err
		}
		return client, initialized, nil
	})
}

func TestACPProcessReopensItsBoundConversation(t *testing.T) {
	if os.Getenv("OAP_ACP_INTEGRATION") != "1" {
		t.Skip("set OAP_ACP_INTEGRATION=1 and OAP_ACP_BIN to run the pinned agent against a loopback mock")
	}
	binary := verifiedACPServerBinary(t)
	mock := providertest.New(t, providertest.Config{OpenAIKey: acpMockSecret})
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)
	root := t.TempDir()
	writeACPAgentConfig(t, root, mock.OpenAIBaseURL())
	for _, dir := range []string{filepath.Join(root, "workspace"), filepath.Join(root, "data")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	capture := &reloadCapture{pending: map[string][]byte{}}
	factory := recordingACPFactory(binary, root, acpEnvironment(t, root), capture)
	implementation, err := New(Config{WorkingDirectory: filepath.Join(root, "workspace"), Factory: factory})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	opened, err := implementation.Open(ctx, base.OpenRequest{SessionID: "bound", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	nativeID := opened.(base.NativeSession).NativeSessionID()
	if nativeID == "" {
		t.Fatal("open reported no native binding")
	}
	admission, stream, err := opened.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "bound", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Remember the fixture history.")}}}})
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, adaptertest.Drain(t, stream, 30*time.Second))
	if err := opened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := implementation.Open(ctx, base.OpenRequest{SessionID: "bound", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: nativeID})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	state, err := reopened.State(ctx)
	if err != nil || state.Recovery == nil || !state.Recovery.Recovered || state.Recovery.Reason != reopenReason || state.ActiveRunID != "" || reopened.(base.NativeSession).NativeSessionID() != nativeID {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if len(reopened.(*session).journal) != 0 {
		t.Fatal("load replay became new OAP history")
	}
	admission, stream, err = reopened.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "bound", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("Continue the fixture conversation.")}}}})
	if err != nil {
		t.Fatal(err)
	}
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, adaptertest.Drain(t, stream, 30*time.Second))
	requests := mock.RequestsFor(providertest.OpenAIChatCompletion)
	if len(requests) != 2 || !strings.Contains(string(requests[1].Body), "Remember the fixture history.") || !strings.Contains(string(requests[1].Body), "Continue the fixture conversation.") {
		t.Fatal("reopen did not restore the provider conversation")
	}
	_, err = implementation.Open(ctx, base.OpenRequest{SessionID: "absent", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: "00000000-0000-4000-8000-000000000001"})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("absent reload=%v", err)
	}
	if directory := os.Getenv("OAP_ACP_CAPTURE_DIR"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(filepath.Join(directory, "bound-session-reopen.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		capture.mu.Lock()
		defer capture.mu.Unlock()
		for _, frame := range capture.frames {
			if err := json.NewEncoder(file).Encode(frame); err != nil {
				t.Fatal(err)
			}
		}
	}
}
