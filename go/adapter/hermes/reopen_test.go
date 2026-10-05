package hermes

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type fakeBridge struct{ client *fakeClient }

func (b fakeBridge) ClientHandle() Client        { return b.client }
func (b fakeBridge) Done() <-chan struct{}       { return b.client.done }
func (b fakeBridge) WaitError() error            { return nil }
func (b fakeBridge) Close(context.Context) error { return b.client.Close() }

func processBackedAdapter(t *testing.T, f *fakeClient, launches *atomic.Int32) *Adapter {
	t.Helper()
	implementation, err := New(Config{Model: "hermes-test", Clock: &testClock{}, IDs: &testIDs{}, ProcessFactory: ProcessFactoryFunc(func(context.Context, rpc.ProcessConfig) (ProcessBridge, error) {
		launches.Add(1)
		return fakeBridge{client: f}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return implementation
}

func TestReopenResumesTheBoundStoredSessionAndReportsItsModel(t *testing.T) {
	f := newFake()
	f.queue(native.MethodSessionResume, reply{result: map[string]any{"session_id": "rt000001", "resumed": "stored-1", "message_count": 2, "info": map[string]any{"model": "resumed-model"}, "running": false, "status": "idle"}})
	var launches atomic.Int32
	session, err := processBackedAdapter(t, f, &launches).Open(context.Background(), base.OpenRequest{SessionID: "after", Reopen: true, NativeSessionID: "stored-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered || state.CurrentModelID != "resumed-model" {
		t.Fatalf("recovered state = %+v", state)
	}
	if got := session.(base.NativeSession).NativeSessionID(); got != "stored-1" {
		t.Fatalf("binding = %q, want the stored session it reopened", got)
	}
	f.callsMu.Lock()
	params := f.calls[0].params.(native.SessionResumeParams)
	f.callsMu.Unlock()
	if params.SessionID != "stored-1" || f.callCount(native.MethodSessionCreate) != 0 {
		t.Fatalf("resume params = %+v, create calls = %d", params, f.callCount(native.MethodSessionCreate))
	}
}

func TestAFreshOpenBindsTheStoredSessionIDSessionCreateReports(t *testing.T) {
	f := newFake()
	f.queue(native.MethodSessionCreate, reply{result: map[string]any{"session_id": "rt000001", "stored_session_id": "stored-new"}})
	var launches atomic.Int32
	session, err := processBackedAdapter(t, f, &launches).Open(context.Background(), base.OpenRequest{SessionID: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	if got := session.(base.NativeSession).NativeSessionID(); got != "stored-new" {
		t.Fatalf("binding = %q, want session.create's stored_session_id", got)
	}
}

func TestReopenRefusesWhatItCannotRestoreWithoutRestartingWork(t *testing.T) {
	cases := []struct {
		name     string
		binding  string
		reply    reply
		launched bool
		detail   string
	}{
		{name: "no binding", binding: " ", detail: "names no stored session"},
		{name: "unknown stored session", binding: "gone", reply: reply{err: &rpc.RemoteError{Object: rpc.ErrorObject{Code: hermesSessionNotFound, Message: "session not found"}}}, launched: true, detail: "no stored session"},
		{name: "still running", binding: "busy", reply: reply{result: map[string]any{"session_id": "rt", "resumed": "busy", "running": true, "status": "streaming"}}, launched: true, detail: "while it was running"},
		{name: "auto-continue pending", binding: "crashed", reply: reply{result: map[string]any{"session_id": "rt", "resumed": "crashed", "running": false, "status": "idle", "auto_continue": map[string]any{"attempt": 1, "interrupted_at": 1.5}}}, launched: true, detail: "restart the turn a crash interrupted"},
		{name: "no session id", binding: "blank", reply: reply{result: map[string]any{"resumed": "blank", "status": "idle"}}, launched: true, detail: "omitted the reloaded session id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			if tc.launched {
				f.queue(native.MethodSessionResume, tc.reply)
			}
			var launches atomic.Int32
			_, err := processBackedAdapter(t, f, &launches).Open(context.Background(), base.OpenRequest{SessionID: "after", Reopen: true, NativeSessionID: tc.binding})
			var refusal *base.UnsupportedControlError
			if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable || !strings.Contains(refusal.Detail, tc.detail) {
				t.Fatalf("refusal = %v", err)
			}
			if launched := launches.Load() == 1; launched != tc.launched {
				t.Fatalf("launched a gateway = %v, want %v", launched, tc.launched)
			}
			if tc.launched && (!f.closed || f.callCount(native.MethodPromptSubmit) != 0) {
				t.Fatalf("refused gateway closed = %v, prompts = %d", f.closed, f.callCount(native.MethodPromptSubmit))
			}
		})
	}
}

func TestReopenRefusesAFactoryThatCannotReload(t *testing.T) {
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, string, error) {
		return nil, "", errors.New("a reopen must not create a fresh session")
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = implementation.Open(context.Background(), base.OpenRequest{SessionID: "after", Reopen: true, NativeSessionID: "stored-1"})
	var refusal *base.UnsupportedControlError
	if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || !strings.Contains(refusal.Detail, "cannot reload") {
		t.Fatalf("refusal = %v", err)
	}
}
