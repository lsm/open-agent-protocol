package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/acp/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestCloseActiveRunRequiresExplicitOptIn(t *testing.T) {
	for _, abort := range []bool{false, true} {
		t.Run(map[bool]string{false: "guard", true: "abort"}[abort], func(t *testing.T) {
			f := newFake()
			a, err := New(Config{WorkingDirectory: t.TempDir(), AbortActiveOnClose: abort, Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
				return f, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			s, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
			if err != nil {
				t.Fatal(err)
			}
			_, stream := submit(t, s)
			<-f.promptStarted
			if !abort {
				if err := s.Close(context.Background()); !errors.Is(err, base.ErrRunActive) {
					t.Fatalf("close=%v", err)
				}
				f.mu.Lock()
				closed := f.closed
				f.mu.Unlock()
				if closed {
					t.Fatal("guard closed transport")
				}
				s.(*session).abortActiveOnClose = true
			}
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertAbortedStream(t, stream)
			f.prompt <- promptOutcome{result: native.PromptResult{StopReason: "end_turn"}}
			s.(*session).settlePrompt(s.(*session).active, native.PromptResult{StopReason: "end_turn"}, nil)
			state, err := s.State(context.Background())
			if !errors.Is(err, base.ErrSessionClosed) || state.Status != protocol.SessionClosed || state.ActiveRunID != "" {
				t.Fatalf("state=%+v err=%v", state, err)
			}
			if err := s.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("late")}}}}); !errors.Is(err, base.ErrSessionClosed) {
				t.Fatalf("late submit=%v", err)
			}
		})
	}
}

func assertAbortedStream(t *testing.T, stream base.EventStream) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case event, ok := <-stream:
			if !ok {
				return
			}
			switch event.Envelope.Type {
			case protocol.TypeRunCompleted, protocol.TypeRunCancelled, protocol.TypeRunFailed:
				t.Fatalf("invented terminal observation: %s", event.Envelope.Type)
			}
		case <-deadline:
			t.Fatal("abandoned stream was not closed")
		}
	}
}

func TestCloseAbandonedACPProcess(t *testing.T) {
	for _, mode := range []string{"silent", "late-response"} {
		t.Run(mode, func(t *testing.T) { testCloseAbandonedACPProcess(t, mode) })
	}
}

func testCloseAbandonedACPProcess(t *testing.T, mode string) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var process *rpc.Process
	a, err := New(Config{WorkingDirectory: t.TempDir(), AbortActiveOnClose: true, Factory: ClientFactoryFunc(func(ctx context.Context) (Client, rpc.InitializeResponse, error) {
		process, err = rpc.Start(ctx, rpc.ProcessConfig{Path: binary, Args: []string{"-test.run=^TestAbandonedACPHelper$"}, Env: append(os.Environ(), "OAP_ABANDONED_ACP_HELPER="+mode), ShutdownTimeout: 100 * time.Millisecond})
		if err != nil {
			return nil, rpc.InitializeResponse{}, err
		}
		return &processClient{process}, process.Initialize, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := a.Open(ctx, base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Close(context.Background()) })
	_, stream := submit(t, s)
	select {
	case event := <-stream:
		if event.Envelope.Type != protocol.TypeRunStarted {
			t.Fatal(event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case event := <-stream:
		if event.Envelope.Type != protocol.TypeContentDelta {
			t.Fatal(event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	select {
	case err := <-closed:
		if err == nil || !strings.Contains(err.Error(), "shutdown timed out") {
			t.Fatalf("close=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("process teardown exceeded deadline")
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("child process still running")
	}
	assertAbortedStream(t, stream)
}

func TestAbandonedACPHelper(t *testing.T) {
	if os.Getenv("OAP_ABANDONED_ACP_HELPER") == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	var promptID json.RawMessage
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}}
		case "session/new":
			result = map[string]any{"sessionId": "native-session"}
		case "session/prompt":
			promptID = request.ID
			_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "native-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "admitted"}}}})
			continue
		default:
			continue
		}
		if encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}) != nil {
			os.Exit(3)
		}
	}
	if os.Getenv("OAP_ABANDONED_ACP_HELPER") == "late-response" {
		_ = encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": promptID, "result": map[string]any{"stopReason": "end_turn"}})
	}
	for {
		time.Sleep(time.Hour)
	}
}

type blockedAdmissionClient struct {
	*fakeClient
	writing chan struct{}
}

func (f *blockedAdmissionClient) CallStarted(_ context.Context, _ string, _ any, _ any, started chan<- error) error {
	close(f.writing)
	<-f.done
	err := errors.New("closed during admission")
	started <- err
	close(started)
	return err
}
func TestOptInClosePreemptsBlockedAdmission(t *testing.T) {
	f := &blockedAdmissionClient{fakeClient: newFake(), writing: make(chan struct{})}
	a, err := New(Config{WorkingDirectory: t.TempDir(), AbortActiveOnClose: true, Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return f, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	submitted := make(chan error, 1)
	go func() {
		_, stream, err := s.Submit(context.Background(), base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hello")}}}})
		if stream != nil {
			for range stream {
			}
		}
		submitted <- err
	}()
	<-f.writing
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background()) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close blocked behind admission")
	}
	select {
	case err := <-submitted:
		if !errors.Is(err, base.ErrSessionClosed) {
			t.Fatalf("submit=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("submit was not released")
	}
	state, err := s.State(context.Background())
	if !errors.Is(err, base.ErrSessionClosed) || state.Status != protocol.SessionClosed {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

type blockedCancelClient struct{ *fakeClient }

func (f *blockedCancelClient) Notify(ctx context.Context, _ string, _ any) error {
	<-ctx.Done()
	return ctx.Err()
}
func TestOptInCancelHasCleanupDeadline(t *testing.T) {
	f := &blockedCancelClient{newFake()}
	a, err := New(Config{WorkingDirectory: t.TempDir(), AbortActiveOnClose: true, ShutdownTimeout: 20 * time.Millisecond, Factory: ClientFactoryFunc(func(context.Context) (Client, rpc.InitializeResponse, error) {
		return f, rpc.InitializeResponse{ProtocolVersion: 1, AgentCapabilities: rpc.AgentCapabilities{}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	s, err := a.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	admission, stream := submit(t, s)
	defer func() { _ = s.Close(context.Background()); f.prompt <- promptOutcome{err: errors.New("closed")} }()
	cancelled := make(chan error, 1)
	go func() { _, err := s.Cancel(context.Background(), admission.RunID); cancelled <- err }()
	select {
	case err := <-cancelled:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel exceeded deadline")
	}
	if err := s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertAbortedStream(t, stream)
}
