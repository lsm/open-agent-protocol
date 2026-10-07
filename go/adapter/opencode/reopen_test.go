package opencode

import (
	"context"
	"errors"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func reopenAdapter(t *testing.T, client *fakeClient) *Adapter {
	t.Helper()
	adapter, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }), Clock: &fakeClock{}, IDs: &fakeIDs{}})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestReopenAttachesToTheBoundServerSessionWithoutCreatingOne(t *testing.T) {
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "fixture", ProviderID: "fixture"}
	session, err := reopenAdapter(t, client).Open(context.Background(), base.OpenRequest{SessionID: "after", Reopen: true, NativeSessionID: "ses_fake00000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered || state.CurrentModelID != "fixture/fixture" {
		t.Fatalf("recovered state = %+v", state)
	}
	if got := session.(base.NativeSession).NativeSessionID(); got != "ses_fake00000000000000" {
		t.Fatalf("binding = %q", got)
	}
	client.mu.Lock()
	creates, subscribed := client.creates, client.subscribeCtx != nil
	client.mu.Unlock()
	if creates != 0 || !subscribed {
		t.Fatalf("creates = %d, subscribed = %v; a reopen follows the bound session without creating one", creates, subscribed)
	}
}

func TestAFreshOpenBindsTheSessionTheServerCreated(t *testing.T) {
	client := newFakeClient()
	session, err := reopenAdapter(t, client).Open(context.Background(), base.OpenRequest{SessionID: "fresh"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	if got := session.(base.NativeSession).NativeSessionID(); got != "ses_fake00000000000000" {
		t.Fatalf("binding = %q", got)
	}
	client.mu.Lock()
	creates := client.creates
	client.mu.Unlock()
	if creates != 1 {
		t.Fatalf("creates = %d", creates)
	}
}

func TestReopenRefusesASessionItCannotAttach(t *testing.T) {
	cases := []struct {
		name    string
		binding string
		prepare func(*fakeClient)
		detail  string
	}{
		{name: "no binding", binding: "  ", detail: "names no session"},
		{name: "unknown session", binding: "ses_gone", prepare: func(f *fakeClient) {
			f.sessionErr = &native.APIError{Status: 404, Tag: "SessionNotFoundError"}
		}, detail: "holds no session ses_gone"},
		{name: "still running", binding: "ses_fake00000000000000", prepare: func(f *fakeClient) {
			f.activeFor = 1
		}, detail: "still running"},
		{name: "active set unreadable", binding: "ses_fake00000000000000", prepare: func(f *fakeClient) {
			f.activeErr = errors.New("active down")
		}, detail: "active down"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeClient()
			if tc.prepare != nil {
				tc.prepare(client)
			}
			_, err := reopenAdapter(t, client).Open(context.Background(), base.OpenRequest{SessionID: "after", Reopen: true, NativeSessionID: tc.binding})
			var refusal *base.UnsupportedControlError
			if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != base.ControlUnsatisfiable || !strings.Contains(refusal.Detail, tc.detail) {
				t.Fatalf("refusal = %v", err)
			}
			client.mu.Lock()
			closed, creates := client.closed, client.creates
			client.mu.Unlock()
			if !closed || creates != 0 {
				t.Fatalf("closed = %v, creates = %d", closed, creates)
			}
		})
	}
}

func TestReopenTakesAReasoningLevelOnTheModelTheSessionRecords(t *testing.T) {
	client := newFakeClient()
	client.model = &native.ModelRef{ID: "fixture", ProviderID: "fixture"}
	session, err := reopenAdapter(t, client).Open(context.Background(), base.OpenRequest{SessionID: "after", Reopen: true, NativeSessionID: "ses_fake00000000000000", ReasoningLevel: protocol.ReasoningHigh})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close(context.Background())
	client.mu.Lock()
	switches := append([]native.ModelRef(nil), client.switches...)
	client.mu.Unlock()
	if len(switches) != 1 || switches[0].ID != "fixture" || switches[0].Variant != "high" {
		t.Fatalf("switches = %+v, want the recorded model at high", switches)
	}
}
