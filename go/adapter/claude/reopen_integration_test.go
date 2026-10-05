package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/internal/providertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestClaudeProcessReopensItsBoundConversation(t *testing.T) {
	if os.Getenv("OAP_CLAUDE_INTEGRATION") != "1" {
		t.Skip("set OAP_CLAUDE_INTEGRATION=1 with absolute OAP_CLAUDE_BIN to run the pinned reload gate")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work")
	mock := providertest.New(t, providertest.Config{AnthropicKey: claudeMockSecret})
	mock.Enqueue(providertest.AnthropicMessages, providertest.Success)
	transcript := newCaptureTranscript()
	a := newPinnedClaude(t, Config{WorkingDirectory: work, Environment: claudeEnvironment(t, root, mock.AnthropicBaseURL()), Tools: UnrestrictedTools(), ProcessFactory: recordingFactory(transcript, os.Stderr)})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := a.Open(ctx, base.OpenRequest{SessionID: "bound", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	submit := func(s base.Session, text string) {
		t.Helper()
		_, stream, err := s.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: "bound", Delivery: protocol.DeliveryAuto, Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(text)}}}})
		if err != nil {
			t.Fatal(err)
		}
		adaptertest.Drain(t, stream, 20*time.Second)
	}
	bound := s.(base.NativeSession).NativeSessionID()
	if !validSessionUUID(bound) {
		t.Fatalf("open supplied no native binding: %q", bound)
	}
	submit(s, "fixture conversation before detach")
	state, err := s.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := json.Unmarshal(state.Metadata["claude_native_session_id"], &id); err != nil {
		t.Fatal(err)
	}
	if id != bound {
		t.Fatalf("the CLI changed its selected native session: %s -> %s", bound, id)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	transcript.note("resume " + id)
	s, err = a.Open(ctx, base.OpenRequest{SessionID: "bound", Reopen: true, NativeSessionID: id, Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	state, err = s.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered || state.Recovery.Reason != reopenReason || state.CurrentModelID != claudeLoopbackModel || state.Status != protocol.SessionIdle || state.ActiveRunID != "" {
		t.Fatalf("state=%+v", state)
	}
	if directory := os.Getenv("OAP_CLAUDE_CAPTURE_DIR"); directory != "" {
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal(err)
		}
		transcript.save(t, filepath.Join(directory, "bound-session-reopen.jsonl"))
	}
	mock.Enqueue(providertest.AnthropicMessages, providertest.Success)
	submit(s, "fixture conversation after reattach")
	requests := mock.RequestsFor(providertest.AnthropicMessages)
	encoded, err := json.Marshal(requests[len(requests)-1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "fixture conversation before detach") || !strings.Contains(string(encoded), "fixture conversation after reattach") {
		t.Fatal("resumed native request lost the prior conversation")
	}
	_, err = a.Open(ctx, base.OpenRequest{SessionID: "missing", Reopen: true, NativeSessionID: "9d992266-63b1-4a69-8000-3aaf8b854e5c"})
	var unsupported *base.UnsupportedControlError
	if !errors.As(err, &unsupported) || unsupported.Feature != protocol.FeatureOpenReopen || unsupported.Reason != base.ControlUnsatisfiable {
		t.Fatalf("missing conversation reopen=%v", err)
	}
}
