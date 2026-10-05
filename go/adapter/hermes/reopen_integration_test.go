package hermes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type hermesTrace struct {
	mu     sync.Mutex
	frames []json.RawMessage
}

type hermesTraceWriter struct {
	trace     *hermesTrace
	direction string
	pending   []byte
}

func (w *hermesTraceWriter) Write(data []byte) (int, error) {
	w.trace.mu.Lock()
	defer w.trace.mu.Unlock()
	w.pending = append(w.pending, data...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			return len(data), nil
		}
		line := bytes.TrimSpace(w.pending[:end])
		w.pending = w.pending[end+1:]
		if len(line) == 0 {
			continue
		}
		raw, err := json.Marshal(struct {
			Direction string `json:"direction"`
			Raw       string `json:"raw"`
		}{w.direction, string(line)})
		if err != nil {
			return 0, err
		}
		w.trace.frames = append(w.trace.frames, raw)
	}
}

func (t *hermesTrace) mark() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.frames)
}

func (t *hermesTrace) since(start int) []json.RawMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]json.RawMessage(nil), t.frames[start:]...)
}

func newTracedHermes(t *testing.T, root string, environment []string, model string, trace *hermesTrace) *Adapter {
	t.Helper()
	implementation, err := New(Config{
		Executable: verifiedHermesPython(t), Args: []string{"-m", "tui_gateway.entry"},
		Environment: environment, WorkingDirectory: root, Model: model,
		ExitTimeout: 15 * time.Second,
		ProcessFactory: ProcessFactoryFunc(func(ctx context.Context, config rpc.ProcessConfig) (ProcessBridge, error) {
			config.TraceToGateway = &hermesTraceWriter{trace: trace, direction: "host-to-gateway"}
			config.TraceFromGateway = &hermesTraceWriter{trace: trace, direction: "gateway-to-host"}
			process, err := rpc.Start(ctx, config)
			if err != nil {
				return nil, err
			}
			return &rpcProcess{process}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func TestHermesProcessReopensItsStoredSessionWithTheConversation(t *testing.T) {
	if testing.Short() || os.Getenv("OAP_HERMES_INTEGRATION") != "1" {
		t.Skip("set OAP_HERMES_INTEGRATION=1 with absolute OAP_HERMES_BIN (python interpreter) and OAP_HERMES_ROOT (pinned hermes-agent checkout) to run; optionally set OAP_HERMES_CAPTURE_DIR to record the native reload")
	}
	mock := providertest.New(t, providertest.Config{OpenAIKey: hermesMockSecret})
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)
	mock.Enqueue(providertest.OpenAIChatCompletion, providertest.Success)
	root := verifiedHermesRoot(t)
	isolated := t.TempDir()
	environment := hermesEnvironment(t, isolated, mock.OpenAIBaseURL())
	writeHermesLoopbackConfig(t, isolated, mock.OpenAIBaseURL())
	trace := &hermesTrace{}
	implementation := newTracedHermes(t, root, environment, hermesLoopbackModel, trace)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	run := func(session base.Session, id protocol.SessionID, text string) {
		t.Helper()
		_, stream, err := session.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
			SessionID: id, Delivery: protocol.DeliveryAuto,
			Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}},
		}})
		if err != nil {
			t.Fatal(err)
		}
		if events := adaptertest.Drain(t, stream, 60*time.Second); events[len(events)-1].Type != protocol.TypeRunCompleted {
			t.Fatalf("terminal=%s", events[len(events)-1].Type)
		}
	}

	first, err := implementation.Open(ctx, base.OpenRequest{SessionID: "hermes-before", Participant: protocol.Participant{ID: "integration-user"}})
	if err != nil {
		t.Fatalf("open pinned gateway: %v", err)
	}
	run(first, "hermes-before", "Remember the word lantern-7319.")
	binding := first.(base.NativeSession).NativeSessionID()
	if binding == "" {
		t.Fatal("a fresh session named no stored session to bind")
	}
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	reloadStart := trace.mark()
	reopened, err := implementation.Open(ctx, base.OpenRequest{SessionID: "hermes-after", Participant: protocol.Participant{ID: "integration-user"}, Reopen: true, NativeSessionID: binding})
	if err != nil {
		t.Fatalf("reopen the bound stored session: %v", err)
	}
	reload := trace.since(reloadStart)
	state, err := reopened.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered || state.CurrentModelID != hermesLoopbackModel {
		t.Fatalf("recovered state = %+v", state)
	}
	if got := reopened.(base.NativeSession).NativeSessionID(); got != binding {
		t.Fatalf("reopened binding = %q, want %q", got, binding)
	}
	run(reopened, "hermes-after", "Which word did I ask you to remember?")
	requests := mock.RequestsFor(providertest.OpenAIChatCompletion)
	if len(requests) != 2 || !strings.Contains(string(requests[1].Body), "lantern-7319") {
		t.Fatalf("provider requests = %d; the reloaded conversation did not reach the model", len(requests))
	}
	if err := reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}

	marker, err := json.Marshal(map[string]any{binding: map[string]any{"attempts": 0, "prompt": "Finish the interrupted task.", "started_at": float64(time.Now().UnixNano()) / 1e9, "auto_continue": true}})
	if err != nil {
		t.Fatal(err)
	}
	markers := filepath.Join(isolated, "home", ".hermes", "desktop")
	if err := os.MkdirAll(markers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markers, "interrupted_turns.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	restartStart := trace.mark()
	_, err = implementation.Open(ctx, base.OpenRequest{SessionID: "hermes-interrupted", Reopen: true, NativeSessionID: binding})
	restart := trace.since(restartStart)
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || !strings.Contains(refusal.Detail, "restart") {
		t.Fatalf("reopen of a session Hermes would auto-continue = %v", err)
	}
	time.Sleep(5 * time.Second)
	if got := len(mock.RequestsFor(providertest.OpenAIChatCompletion)); got != 2 {
		t.Fatalf("provider requests = %d after the refused reopen; the interrupted turn restarted", got)
	}

	_, err = implementation.Open(ctx, base.OpenRequest{SessionID: "hermes-missing", Reopen: true, NativeSessionID: "no-such-stored-session"})
	refusal = nil
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("reopen of an unknown stored session = %v", err)
	}

	if dir := os.Getenv("OAP_HERMES_CAPTURE_DIR"); dir != "" {
		var out bytes.Buffer
		for _, frame := range reload {
			out.Write(frame)
			out.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, "session-reopen.jsonl"), out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		out.Reset()
		for _, frame := range restart {
			out.Write(frame)
			out.WriteByte('\n')
		}
		if err := os.WriteFile(filepath.Join(dir, "session-reopen-auto-continue.jsonl"), out.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "captured %d reload frames and %d refused-restart frames\n", len(reload), len(restart))
	}
}
