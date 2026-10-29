package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var _ = flag.Bool("headless", false, "")
var _ = flag.Bool("stdio", false, "")
var _ = flag.Bool("no-auto-update", false, "")

func TestSyntheticCopilotChild(t *testing.T) {
	if os.Getenv("OAP_COPILOT_SESSION_CHILD") != "1" {
		return
	}
	wire := rpc.NewCodec(os.Stdin, os.Stdout, 0)
	say := func(value any) {
		body, err := json.Marshal(value)
		if err != nil || wire.Write(body) != nil {
			os.Exit(2)
		}
	}
	event := func(kind string, data any) {
		say(map[string]any{"jsonrpc": "2.0", "method": "session.event", "params": map[string]any{"sessionId": "ses", "event": map[string]any{"type": kind, "data": data}}})
	}
	count := 0
	for {
		body, err := wire.Read()
		if err != nil {
			os.Exit(0)
		}
		message, err := native.Parse(body)
		if err != nil {
			os.Exit(3)
		}
		if os.Getenv("OAP_COPILOT_SESSION_MODE") == "slow-handshake" && message.Method != "session.send" {
			time.Sleep(600 * time.Millisecond)
		}
		result := any(map[string]any{})
		prompt := "hello"
		switch message.Method {
		case "ping":
			result = map[string]any{"protocolVersion": 3}
			if os.Getenv("OAP_COPILOT_SESSION_MODE") == "bad-protocol" {
				result = map[string]any{"protocolVersion": 4}
			}
		case "status.get":
			result = map[string]any{"version": PinnedVersion, "protocolVersion": 3}
			if os.Getenv("OAP_COPILOT_SESSION_MODE") == "bad-version" {
				result = map[string]any{"version": "synthetic-other", "protocolVersion": 3}
			}
		case "session.create":
			var params map[string]json.RawMessage
			_ = json.Unmarshal(message.Params, &params)
			if native.Text(params, "clientName") != "open-agent-protocol" || string(params["requestPermission"]) != "false" || string(params["requestUserInput"]) != "false" || string(params["streaming"]) != "true" || native.Text(params, "workingDirectory") != os.Getenv("HOME") || flag.Lookup("headless").Value.String() != "true" || flag.Lookup("stdio").Value.String() != "true" || flag.Lookup("no-auto-update").Value.String() != "true" {
				say(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32602, "message": "invalid synthetic session configuration"}})
				continue
			}
			result = map[string]any{"sessionId": "ses"}
		case "session.send":
			var params map[string]json.RawMessage
			_ = json.Unmarshal(message.Params, &params)
			prompt = native.Text(params, "prompt")
			count++
			id := fmt.Sprintf("m%d", count)
			if os.Getenv("OAP_COPILOT_SESSION_MODE") == "refuse" {
				say(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32602, "message": "synthetic refusal"}})
				continue
			}
			if os.Getenv("OAP_COPILOT_SESSION_MODE") != "queued" || count == 1 {
				event("user.message", map[string]any{"messageId": id, "content": "hello"})
			}
			if os.Getenv("OAP_COPILOT_SESSION_MODE") == "early" {
				time.Sleep(25 * time.Millisecond)
			}
			result = map[string]any{"messageId": id}
			if os.Getenv("OAP_COPILOT_SESSION_MODE") == "missing-id" {
				result = map[string]any{}
			}
		}
		say(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result})
		if message.Method != "session.send" {
			continue
		}
		if os.Getenv("OAP_COPILOT_SESSION_MODE") == "exit" {
			os.Exit(0)
		}
		if os.Getenv("OAP_COPILOT_SESSION_MODE") == "burst-exit" {
			for range 100 {
				event("assistant.message_delta", map[string]any{"deltaContent": "x"})
			}
			os.Exit(0)
		}
		if os.Getenv("OAP_COPILOT_SESSION_MODE") == "hold" || os.Getenv("OAP_COPILOT_SESSION_MODE") == "queued" {
			continue
		}
		event("assistant.message_delta", map[string]any{"deltaContent": prompt})
		event("assistant.message", map[string]any{"content": prompt})
		event("assistant.usage", map[string]any{"inputTokens": 8, "outputTokens": 3, "cost": 0})
		event("session.idle", map[string]any{})
	}
}

func syntheticProcessConfig(t *testing.T, mode string) rpc.ProcessConfig {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	return rpc.ProcessConfig{Path: path, Args: []string{"-test.run=^TestSyntheticCopilotChild$"}, Dir: home, Env: []string{"HOME=" + home, "OAP_COPILOT_SESSION_CHILD=1", "OAP_COPILOT_SESSION_MODE=" + mode, "GORACE=atexit_sleep_ms=0"}, QueueCapacity: 256}
}

func syntheticSession(t *testing.T, mode string, capacity int) (*session, *run) {
	t.Helper()
	p, err := rpc.Start(syntheticProcessConfig(t, mode))
	if err != nil {
		t.Fatal(err)
	}
	r := newReducer("s1", "ses", &corpusIDs{}, &corpusClock{})
	run, err := r.admit(false)
	if err != nil {
		_ = p.Close()
		t.Fatal(err)
	}
	run.nativeMessage = "m1"
	s := newSession(r, p, capacity, time.Second)
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s, run
}

func TestSessionStateAndReplayUseNativeObservations(t *testing.T) {
	s, run := syntheticSession(t, "", 64)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	state, err := s.State(ctx)
	if err != nil || state.ActiveRunID != run.id {
		t.Fatalf("initial state %+v %v", state, err)
	}
	var result any
	if err := s.process.Client.Call(ctx, "session.send", map[string]any{"sessionId": "ses", "prompt": "hello"}, &result); err != nil {
		t.Fatal(err)
	}
	_, stream, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id})
	if err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	ack := protocol.MessageSubmitResponse{SessionID: "s1", Accepted: true, RunID: run.id, SubmissionID: "m1", RequestedDelivery: protocol.DeliveryAuto, EffectiveDelivery: protocol.DeliveryStart, Admission: protocol.AdmissionStarted, Status: protocol.RunRunning}
	adaptertest.AssertRunTrace(t, ack, CapabilityRevision, events)
	state, err = s.State(ctx)
	if err != nil || state.Status != protocol.SessionIdle {
		t.Fatalf("settled state %+v %v", state, err)
	}
	recovery, stream, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id, AfterSequence: 1})
	if err != nil {
		t.Fatal(err)
	}
	if replayed := adaptertest.Drain(t, stream, time.Second); len(replayed) != len(events)-1 || recovery.ReplayedFrom != 2 {
		t.Fatalf("recovery %+v, replayed %d", recovery, len(replayed))
	}
	if _, _, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id, AfterSequence: 999}); !errors.Is(err, base.ErrReplayCursorFuture) {
		t.Fatal(err)
	}
	if _, _, err := s.Resume(ctx, base.ResumeRequest{RunID: "foreign"}); !errors.Is(err, base.ErrRunNotFound) {
		t.Fatal(err)
	}
}

func TestProcessExitFailsTheAdmittedRun(t *testing.T) {
	s, run := syntheticSession(t, "exit", 64)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, stream, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id})
	if err != nil {
		t.Fatal(err)
	}
	var result any
	if err := s.process.Client.Call(ctx, "session.send", map[string]any{"prompt": "hello"}, &result); err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) == 0 || events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("unterminated events %+v", events)
	}
	select {
	case <-s.process.Done():
	case <-ctx.Done():
		t.Fatal("child not reaped")
	}
	state, err := s.State(ctx)
	if err != nil || state.SessionID != "s1" || state.Status != protocol.SessionIdle || state.ActiveRunID != "" || len(state.ActiveRuns) != 0 {
		t.Fatalf("settled state became unreachable after native exit: %+v, %v", state, err)
	}
	_, again, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id})
	if err != nil {
		t.Fatalf("retained replay became unreachable after native exit: %v", err)
	}
	replayed := adaptertest.Drain(t, again, time.Second)
	want, _ := json.Marshal(events)
	got, _ := json.Marshal(replayed)
	if string(want) != string(got) {
		t.Fatalf("crash replay changed events: %s", got)
	}
}

func TestSessionCloseSettlesAndStopsItsProcess(t *testing.T) {
	s, run := syntheticSession(t, "hold", 64)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, stream, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.process.Client.Call(ctx, "session.send", map[string]any{"prompt": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, time.Second)
	if len(events) == 0 || events[len(events)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("close left the admitted run open: %+v", events)
	}
	select {
	case <-s.process.Done():
	default:
		t.Fatal("child remains live")
	}
	if _, err := s.State(ctx); !errors.Is(err, base.ErrSessionClosed) {
		t.Fatal(err)
	}
}

func TestSessionReplayReportsAnEvictedPrefix(t *testing.T) {
	s, run := syntheticSession(t, "", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.process.Client.Call(ctx, "session.send", map[string]any{"prompt": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		state, err := s.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if state.Status == protocol.SessionIdle {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	_, _, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id})
	var gap *base.ReplayGap
	if !errors.As(err, &gap) || gap.RequestedAfter != 0 || gap.OldestAvailable == 0 {
		t.Fatalf("missing replay gap: %v", err)
	}
}

func TestCrashReplayRetainsMoreThanOneFollowerBuffer(t *testing.T) {
	s, run := syntheticSession(t, "burst-exit", 256)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, stream, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.process.Client.Call(ctx, "session.send", map[string]any{"prompt": "hello"}, nil); err != nil {
		t.Fatal(err)
	}
	first := adaptertest.Drain(t, stream, time.Second)
	if len(first) != 102 || first[len(first)-1].Type != protocol.TypeRunFailed {
		t.Fatalf("crash lost original observations: %d", len(first))
	}
	_, again, err := s.Resume(ctx, base.ResumeRequest{RunID: run.id})
	if err != nil {
		t.Fatal(err)
	}
	replayed := adaptertest.Drain(t, again, time.Second)
	if len(replayed) != len(first) {
		t.Fatalf("replay truncated: %d of %d", len(replayed), len(first))
	}
	for index, event := range replayed {
		if event.ID != first[index].ID || *event.Sequence != *first[index].Sequence {
			t.Fatalf("replay changed event %d", index)
		}
	}
}
