package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/pi/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func boundPiFile(t *testing.T) sessionBinding {
	t.Helper()
	binding := sessionBinding{SessionID: "bound-session", SessionFile: filepath.Join(t.TempDir(), "session.jsonl")}
	if err := os.WriteFile(binding.SessionFile, []byte("{\"type\":\"session\",\"id\":\"bound-session\",\"version\":3,\"cwd\":\"/workspace\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return binding
}

func piBindingText(t *testing.T, binding sessionBinding) string {
	t.Helper()
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func reopeningPi(t *testing.T, binding sessionBinding) (*Adapter, *fakeClient) {
	t.Helper()
	client := newFakeClient()
	client.state.SessionID = binding.SessionID
	client.state.SessionFile = binding.SessionFile
	client.state.Model = json.RawMessage(`{"id":"restored","provider":"fixture"}`)
	client.state.ThinkingLevel = native.ThinkingHigh
	client.state.AutoCompactionEnabled = true
	client.state.MessageCount = 3
	client.replies = map[native.CommandType]json.RawMessage{native.CommandSwitchSession: json.RawMessage(`{"cancelled":false}`)}
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) {
		initial := validState(false)
		initial.SessionFile = filepath.Join(t.TempDir(), "fresh.jsonl")
		return client, initial, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return implementation, client
}

func TestPiReopensTheBoundFileAndReportsResumedSettings(t *testing.T) {
	binding := boundPiFile(t)
	implementation, client := reopeningPi(t, binding)
	session, err := implementation.Open(context.Background(), base.OpenRequest{SessionID: "oap-session", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: piBindingText(t, binding)})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.SessionID != "oap-session" || state.Status != protocol.SessionIdle || state.Recovery == nil || !state.Recovery.Recovered || state.Recovery.Reason != reopenRecoveryReason || state.CurrentModelID != "fixture/restored" || state.ReasoningLevel != protocol.ReasoningHigh || state.CompactionPolicy == nil || state.CompactionPolicy.Kind != protocol.CompactionAuto || state.ActiveRunID != "" || state.TranscriptCursor != "" {
		t.Fatalf("state=%+v", state)
	}
	if session.(base.NativeSession).NativeSessionID() != piBindingText(t, binding) {
		t.Fatal("binding changed")
	}
	client.mu.Lock()
	calls := append([]native.Command(nil), client.calls...)
	client.mu.Unlock()
	if len(calls) != 3 || calls[0].Type != native.CommandSwitchSession || calls[0].SessionPath == nil || *calls[0].SessionPath != binding.SessionFile || calls[1].Type != native.CommandGetState {
		t.Fatalf("calls=%+v", calls)
	}
	if _, _, err := session.Resume(context.Background(), base.ResumeRequest{RunID: "old-run"}); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("old OAP run survived: %v", err)
	}
}

func TestPiRefusesAnUnusableBindingBeforeStartingOrWriting(t *testing.T) {
	for _, kind := range []string{"absent-binding", "malformed-binding", "relative-path", "missing-file", "empty-file", "other-session", "wrong-type", "duplicate-id", "malformed-header", "directory"} {
		t.Run(kind, func(t *testing.T) {
			binding := boundPiFile(t)
			raw := ""
			switch kind {
			case "malformed-binding":
				raw = "{"
			case "relative-path":
				binding.SessionFile = "session.jsonl"
			case "missing-file":
				os.Remove(binding.SessionFile)
			case "empty-file":
				os.WriteFile(binding.SessionFile, nil, 0600)
			case "other-session":
				os.WriteFile(binding.SessionFile, []byte(`{"type":"session","id":"someone-else"}`), 0600)
			case "wrong-type":
				os.WriteFile(binding.SessionFile, []byte(`{"type":"message","id":"bound-session"}`), 0600)
			case "duplicate-id":
				os.WriteFile(binding.SessionFile, []byte(`{"type":"session","id":"bound-session","id":"bound-session"}`), 0600)
			case "malformed-header":
				os.WriteFile(binding.SessionFile, []byte(`{"type":"session"`), 0600)
			case "directory":
				binding.SessionFile = t.TempDir()
			}
			if kind != "absent-binding" && kind != "malformed-binding" {
				raw = piBindingText(t, binding)
			}
			started := false
			implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) {
				started = true
				return newFakeClient(), validState(false), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			_, err = implementation.Open(context.Background(), base.OpenRequest{Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: raw})
			assertPiReloadRefusal(t, err)
			if started {
				t.Fatal("unusable binding started a native process")
			}
		})
	}
}

func TestPiRefusesAnUnconfirmedNativeReload(t *testing.T) {
	for _, kind := range []string{"cancelled", "omitted-cancelled", "null", "remote-error", "other-session", "other-file", "streaming", "compacting", "malformed-state"} {
		t.Run(kind, func(t *testing.T) {
			binding := boundPiFile(t)
			implementation, client := reopeningPi(t, binding)
			switch kind {
			case "cancelled":
				client.replies[native.CommandSwitchSession] = json.RawMessage(`{"cancelled":true}`)
			case "omitted-cancelled":
				client.replies[native.CommandSwitchSession] = json.RawMessage(`{}`)
			case "null":
				client.replies[native.CommandSwitchSession] = json.RawMessage(`null`)
			case "remote-error":
				client.refusals = map[native.CommandType]error{native.CommandSwitchSession: errors.New("cannot load")}
			case "other-session":
				client.state.SessionID = "wrong-session"
			case "other-file":
				client.state.SessionFile = "/different/file"
			case "streaming":
				client.state.IsStreaming = true
			case "compacting":
				client.state.IsCompacting = true
			case "malformed-state":
				client.replies[native.CommandGetState] = json.RawMessage(`{"sessionId":"bound-session"}`)
			}
			_, err := implementation.Open(context.Background(), base.OpenRequest{Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: piBindingText(t, binding)})
			assertPiReloadRefusal(t, err)
			client.mu.Lock()
			closed := client.closed
			client.mu.Unlock()
			if !closed {
				t.Fatal("failed reload left process open")
			}
		})
	}
}

func assertPiReloadRefusal(t *testing.T, err error) {
	t.Helper()
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable {
		t.Fatalf("reload refusal=%v", err)
	}
}

func TestPiRecordsTheNativeFileBeforeItsFirstTurn(t *testing.T) {
	client := newFakeClient()
	client.state.SessionFile = filepath.Join(t.TempDir(), "not-yet-written.jsonl")
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, native.SessionState, error) { return client, client.state, nil })})
	if err != nil {
		t.Fatal(err)
	}
	session, err := implementation.Open(context.Background(), base.OpenRequest{Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	binding := session.(base.NativeSession).NativeSessionID()
	if !strings.Contains(binding, client.state.SessionFile) || !strings.Contains(binding, client.state.SessionID) {
		t.Fatalf("binding=%s", binding)
	}
	if _, err := os.Stat(client.state.SessionFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected native write: %v", err)
	}
}

func TestPiAppliesRequestedSettingsAfterLoadingTheBinding(t *testing.T) {
	binding := boundPiFile(t)
	implementation, client := reopeningPi(t, binding)
	client.onCall = func(command native.Command) {
		client.mu.Lock()
		defer client.mu.Unlock()
		if command.Type == native.CommandSetThinkingLevel {
			client.state.ThinkingLevel = command.Level
		}
		if command.Type == native.CommandSetAutoCompaction {
			client.state.AutoCompactionEnabled = *command.Enabled
		}
	}
	opened, err := implementation.Open(context.Background(), base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: piBindingText(t, binding), ReasoningLevel: protocol.ReasoningLow, CompactionPolicy: &protocol.CompactionPolicy{Kind: protocol.CompactionOff}})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close(context.Background())
	state, err := opened.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.ReasoningLevel != protocol.ReasoningLow || state.CompactionPolicy.Kind != protocol.CompactionOff || state.Recovery == nil || !state.Recovery.Recovered {
		t.Fatalf("settings=%+v", state)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.calls) != 6 || client.calls[0].Type != native.CommandSwitchSession || client.calls[2].Type != native.CommandSetAutoCompaction || client.calls[3].Type != native.CommandSetThinkingLevel {
		t.Fatalf("native order=%+v", client.calls)
	}
}
