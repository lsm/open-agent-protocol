package pi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type piTrace struct {
	mu     sync.Mutex
	frames []json.RawMessage
}
type piTraceWriter struct {
	trace     *piTrace
	direction string
	pending   []byte
}

func (w *piTraceWriter) Write(data []byte) (int, error) {
	w.trace.mu.Lock()
	defer w.trace.mu.Unlock()
	w.pending = append(w.pending, data...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		line := bytes.TrimSpace(w.pending[:end])
		w.pending = w.pending[end+1:]
		if len(line) == 0 {
			continue
		}
		raw, err := json.Marshal(struct {
			Direction string          `json:"direction"`
			Raw       json.RawMessage `json:"raw"`
		}{w.direction, json.RawMessage(line)})
		if err != nil {
			return 0, err
		}
		w.trace.frames = append(w.trace.frames, raw)
	}
	return len(data), nil
}
func (t *piTrace) snapshot() []json.RawMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]json.RawMessage(nil), t.frames...)
}

type capturedPiBridge struct {
	client  *rpc.Client
	initial native.SessionState
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	done    chan struct{}
	mu      sync.Mutex
	err     error
	close   sync.Once
}

func (p *capturedPiBridge) ClientHandle() Client                     { return p.client }
func (p *capturedPiBridge) InitialSessionState() native.SessionState { return p.initial }
func (p *capturedPiBridge) Done() <-chan struct{}                    { return p.done }
func (p *capturedPiBridge) WaitError() error                         { p.mu.Lock(); defer p.mu.Unlock(); return p.err }
func (p *capturedPiBridge) Close(context.Context) error {
	p.close.Do(func() {
		p.client.Close()
		p.stdin.Close()
		p.stdout.Close()
		select {
		case <-p.done:
		case <-time.After(time.Second):
			p.command.Process.Kill()
			<-p.done
		}
	})
	return nil
}

func recordingPiFactory(trace *piTrace) ProcessFactory {
	return ProcessFactoryFunc(func(ctx context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
		command := exec.Command(config.Path, append(append([]string(nil), config.Args...), "--mode", "rpc")...)
		command.Dir = config.Dir
		command.Env = config.Env
		stdin, err := command.StdinPipe()
		if err != nil {
			return nil, err
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			stdin.Close()
			return nil, err
		}
		command.Stderr = io.Discard
		if err := command.Start(); err != nil {
			stdin.Close()
			stdout.Close()
			return nil, err
		}
		bridge := &capturedPiBridge{command: command, stdin: stdin, stdout: stdout, done: make(chan struct{})}
		bridge.client = rpc.NewClient(io.TeeReader(stdout, &piTraceWriter{trace: trace, direction: "pi-to-host"}), io.MultiWriter(stdin, &piTraceWriter{trace: trace, direction: "host-to-pi"}), rpc.ClientOptions{})
		go func() {
			err := command.Wait()
			bridge.mu.Lock()
			bridge.err = err
			bridge.mu.Unlock()
			close(bridge.done)
		}()
		if err := openingCall(ctx, bridge.client, native.Command{Type: native.CommandGetState}, &bridge.initial); err != nil {
			bridge.Close(context.Background())
			return nil, err
		}
		return bridge, nil
	})
}

func TestPiProcessReopensItsFileWithTheConversation(t *testing.T) {
	if testing.Short() || os.Getenv("OAP_PI_INTEGRATION") != "1" {
		t.Skip("set OAP_PI_INTEGRATION=1 and a verified pinned Pi binary to run")
	}
	binary := verifiedPiBinary(t)
	root := t.TempDir()
	environment := piEnvironment(t, root, piMockSecret)
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	mock := providertest.New(t, providertest.Config{OpenAIKey: piMockSecret})
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	mock.Enqueue(providertest.OpenAIResponses, providertest.Success)
	models := map[string]any{"providers": map[string]any{"oap-loopback": map[string]any{"baseUrl": mock.OpenAIBaseURL(), "api": "openai-responses", "apiKey": "$OAP_PI_MOCK_KEY", "models": []map[string]any{{"id": "fixture-model", "name": "OAP loopback fixture", "reasoning": true}}}}}
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agent", "models.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	trace := &piTrace{}
	config := Config{Executable: binary, Args: []string{"--provider", "oap-loopback", "--model", "fixture-model"}, Environment: environment, WorkingDirectory: workspace, ProcessFactory: recordingPiFactory(trace)}
	implementation, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	request := base.OpenRequest{SessionID: "pi-reopen-session", Participant: protocol.Participant{ID: "user"}, ReasoningLevel: protocol.ReasoningHigh, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}}
	first, err := implementation.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(context.Background())
	bindingText := first.(base.NativeSession).NativeSessionID()
	if bindingText == "" {
		t.Fatal("no binding before first turn")
	}
	submitPiReloadTurn(t, ctx, first, "Remember the marker river-stone.")
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	before := len(trace.snapshot())
	request.Reopen = true
	request.NativeSessionID = bindingText
	request.ReasoningLevel = ""
	request.CompactionPolicy = nil
	reopened, err := implementation.Open(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close(context.Background())
	state, err := reopened.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered || state.ReasoningLevel != protocol.ReasoningHigh || state.CompactionPolicy == nil || state.CompactionPolicy.Kind != protocol.CompactionOff || state.CurrentModelID != "oap-loopback/fixture-model" || state.TranscriptCursor != "" {
		t.Fatalf("reopened=%+v", state)
	}
	if reopened.(base.NativeSession).NativeSessionID() != bindingText {
		t.Fatal("reopened binding changed")
	}
	capture := trace.snapshot()[before:]
	submitPiReloadTurn(t, ctx, reopened, "What marker did I ask you to remember?")
	requests := mock.RequestsFor(providertest.OpenAIResponses)
	if len(requests) != 2 || !bytes.Contains(requests[1].Body, []byte("river-stone")) || !bytes.Contains(requests[1].Body, []byte("What marker")) {
		t.Fatalf("conversation was not restored: %+v", requests)
	}
	var binding sessionBinding
	if err := json.Unmarshal([]byte(bindingText), &binding); err != nil {
		t.Fatal(err)
	}
	header, err := os.ReadFile(binding.SessionFile)
	if err != nil {
		t.Fatal(err)
	}
	firstLine := bytes.SplitN(header, []byte("\n"), 2)[0]
	if dir := os.Getenv("OAP_PI_CAPTURE_DIR"); dir != "" {
		if !filepath.IsAbs(dir) {
			t.Fatal("capture path must be absolute")
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		for _, frame := range capture {
			output.Write(frame)
			output.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, "bound-session-reopen.jsonl"), output.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session-header.json"), append(firstLine, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(binding.SessionFile); err != nil {
		t.Fatal(err)
	}
	_, err = implementation.Open(ctx, request)
	assertPiReloadRefusal(t, err)
	if !errors.Is(osFileMissing(binding.SessionFile), os.ErrNotExist) {
		t.Fatal("refused reopen created a new file")
	}
}

func osFileMissing(path string) error { _, err := os.Stat(path); return err }
func submitPiReloadTurn(t *testing.T, ctx context.Context, session base.Session, text string) {
	t.Helper()
	admission, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "pi-reopen-session", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}}}})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 30*time.Second)
	adaptertest.AssertRunEvents(t, admission, CapabilityRevision, events)
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("terminal=%s", events[len(events)-1].Type)
	}
}
