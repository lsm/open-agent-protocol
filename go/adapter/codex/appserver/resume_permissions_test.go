package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func reopenWithHostPermissions(t *testing.T, client *fakeClient) (adapter.Session, error) {
	t.Helper()
	implementation, err := New(Config{
		Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return client, nil }),
		Clock:   &fakeClock{}, IDs: &fakeIDs{}, Model: "glm-test", JournalCapacity: 32,
		Sandbox: "workspace-write", ApprovalPolicy: "on-request", WorkingDirectory: "/work/project",
	})
	if err != nil {
		t.Fatal(err)
	}
	return implementation.Open(context.Background(), adapter.OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}, Reopen: true, NativeSessionID: client.threadID})
}

func TestAReopenSendsTheHostsPermissionsAndKeepsTheThreadWhenCodexConfirmsThem(t *testing.T) {
	client := newFakeClient()
	client.resumedModel = "thread-model"
	client.resumedSandbox = json.RawMessage(`{"type":"workspaceWrite","writableRoots":[],"networkAccess":false}`)
	client.resumedApproval = json.RawMessage(`"on-request"`)
	client.resumedCwd = "/work/project/"
	session, err := reopenWithHostPermissions(t, client)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	client.mu.Lock()
	sent := client.resume
	client.mu.Unlock()
	if sent.Sandbox != "workspace-write" || sent.ApprovalPolicy != "on-request" || sent.Cwd != "/work/project" {
		t.Fatalf("thread/resume sent %+v, want the host's sandbox, approval policy and cwd", sent)
	}
	state, err := session.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.CurrentModelID != "thread-model" {
		t.Fatalf("model = %q, want the one the thread last ran with", state.CurrentModelID)
	}
}

func TestAReopenCodexAnswersWithWiderPermissionsIsRefused(t *testing.T) {
	cases := []struct {
		name     string
		sandbox  string
		approval string
		cwd      string
		detail   string
	}{
		{name: "full access", sandbox: `{"type":"dangerFullAccess"}`, approval: `"on-request"`, cwd: "/work/project", detail: "sandbox"},
		{name: "never asks", sandbox: `{"type":"workspaceWrite"}`, approval: `"never"`, cwd: "/work/project", detail: "approval policy"},
		{name: "elsewhere", sandbox: `{"type":"workspaceWrite"}`, approval: `"on-request"`, cwd: "/", detail: "configured \"/work/project\""},
		{name: "nothing said", detail: "sandbox"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeClient()
			client.resumedSandbox = json.RawMessage(tc.sandbox)
			client.resumedApproval = json.RawMessage(tc.approval)
			client.resumedCwd = tc.cwd
			_, err := reopenWithHostPermissions(t, client)
			var refusal *adapter.UnsupportedControlError
			if !errors.As(err, &refusal) || refusal.Feature != protocol.FeatureOpenReopen || refusal.Reason != adapter.ControlUnsatisfiable || !strings.Contains(refusal.Detail, tc.detail) {
				t.Fatalf("refusal = %v", err)
			}
		})
	}
}
