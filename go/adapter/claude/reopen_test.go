package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func runClaudeReopenCorpus(t *testing.T, dir string, definition ccCorpusCase) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, definition.Native))
	if err != nil {
		t.Fatal(err)
	}
	var frames []ccFrame
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var frame ccFrame
		ccDecodeStrict(t, line, &frame, "native reopen exchange")
		if _, err := rpc.ParseMessage(frame.Raw); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	if len(frames) != 4 {
		t.Fatalf("native reopen frames=%d", len(frames))
	}
	peer := newWirePeer(t)
	id := definition.IdentityMap["native_session_id"]
	var argv []string
	a, err := New(Config{Executable: "/fixture/claude", Tools: UnrestrictedTools(), Model: "loader-default", ProcessFactory: ProcessFactoryFunc(func(_ context.Context, c rpc.ProcessConfig) (ProcessBridge, error) {
		argv = c.Args
		return recorderBridge{peer.client}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := a.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ccAssertCapabilities(t, definition, descriptor)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type opened struct {
		session base.Session
		err     error
	}
	done := make(chan opened, 1)
	go func() {
		s, err := a.Open(ctx, base.OpenRequest{SessionID: "session", Reopen: true, NativeSessionID: id, Participant: protocol.Participant{ID: "user"}})
		done <- opened{s, err}
	}()
	for i := 0; i < len(frames); i += 2 {
		actual, raw := peer.written()
		wanted := ccDecodeFrameMap(t, frames[i].Raw)
		wanted["request_id"] = actual.RequestID
		data, _ := json.Marshal(wanted)
		var got, want any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, &want); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("request %d mismatch: %s", i, raw)
		}
		reply := ccDecodeFrameMap(t, frames[i+1].Raw)["response"].(map[string]any)
		payload, _ := json.Marshal(reply["response"])
		peer.answerControl(actual.RequestID, string(payload))
	}
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	s := result.session
	if s == nil {
		t.Fatal("reopen returned no session")
	}
	defer s.Close(ctx)
	if len(argv) < 2 || !slices.Equal(argv[len(argv)-2:], []string{"--resume", id}) {
		t.Fatalf("resume argv=%v", argv)
	}
	state, err := s.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := ccLoadJSON[protocol.SessionState](t, filepath.Join(dir, definition.ExpectedOAP))
	if state.Status != protocol.SessionIdle || len(state.ActiveRuns) != 0 || state.ActiveRunID != "" || state.CurrentModelID != expected.CurrentModelID || state.ReasoningLevel != expected.ReasoningLevel || !reflect.DeepEqual(state.Recovery, expected.Recovery) || !reflect.DeepEqual(state.CompactionPolicy, expected.CompactionPolicy) {
		t.Fatalf("state=%+v", state)
	}
	var actualID string
	if err := json.Unmarshal(state.Metadata["claude_native_session_id"], &actualID); err != nil || actualID != id {
		t.Fatalf("binding=%s err=%v", actualID, err)
	}
	if _, _, err := s.Resume(ctx, base.ResumeRequest{RunID: "pre-close-run"}); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatalf("old run resume=%v", err)
	}
	if err := peer.client.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeReopenRefusesAnUnboundOrUnselectableConversation(t *testing.T) {
	peer := newWirePeer(t)
	a, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) {
		t.Error("factory started for an unsatisfiable binding")
		return peer.client, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "invalid", "9d992266-63b1-4a69-8000-3aaf8b854e5c"} {
		_, err := a.Open(context.Background(), base.OpenRequest{Reopen: true, NativeSessionID: id})
		var unsupported *base.UnsupportedControlError
		if !errors.As(err, &unsupported) || unsupported.Feature != protocol.FeatureOpenReopen || unsupported.Reason != base.ControlUnsatisfiable {
			t.Fatalf("reopen %q: %v", id, err)
		}
	}
}

func TestClaudeReopenAChildCannotLoadIsTyped(t *testing.T) {
	peer := newWirePeer(t)
	a, err := New(Config{Executable: "/fixture/claude", Tools: UnrestrictedTools(), ProcessFactory: ProcessFactoryFunc(func(context.Context, rpc.ProcessConfig) (ProcessBridge, error) {
		return recorderBridge{peer.client}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Open(context.Background(), base.OpenRequest{Reopen: true, NativeSessionID: "9d992266-63b1-4a69-8000-3aaf8b854e5c"})
		done <- err
	}()
	request, _ := peer.written()
	peer.send(`{"type":"control_response","response":{"subtype":"error","request_id":"` + request.RequestID + `","error":"No conversation found"}}`)
	var unsupported *base.UnsupportedControlError
	if err := <-done; !errors.As(err, &unsupported) || unsupported.Feature != protocol.FeatureOpenReopen || unsupported.Reason != base.ControlUnsatisfiable {
		t.Fatalf("reopen: %v", err)
	}
}

func TestClaudeRecoveredSettingsUseEffectiveValues(t *testing.T) {
	for _, fixture := range []struct {
		response string
		level    protocol.ReasoningLevel
		kind     protocol.CompactionKind
		tokens   int64
	}{
		{`{"applied":{"model":"applied","effort":"high"},"effective":{"autoCompactEnabled":false}}`, protocol.ReasoningHigh, protocol.CompactionOff, 0},
		{`{"applied":{"model":"applied","effort":null},"effective":{"autoCompactEnabled":true,"autoCompactWindow":120000}}`, "", protocol.CompactionTokens, 120000},
	} {
		peer := newWirePeer(t)
		s := &Session{client: peer.client, state: protocol.SessionState{CurrentModelID: "configured", ReasoningLevel: protocol.ReasoningMax}}
		done := make(chan error, 1)
		go func() { done <- s.reopened(context.Background(), "bound") }()
		request, _ := peer.written()
		go func() {
			in := <-peer.client.Inbound()
			if in.Barrier != nil {
				close(in.Barrier)
			}
		}()
		peer.answerControl(request.RequestID, fixture.response)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if s.state.CurrentModelID != "applied" || s.state.ReasoningLevel != fixture.level || s.state.CompactionPolicy.Kind != fixture.kind || s.state.CompactionPolicy.Tokens != fixture.tokens {
			t.Fatalf("state=%+v", s.state)
		}
	}
}
