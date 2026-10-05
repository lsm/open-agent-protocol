package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/claude"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestClaudeHubRecordsTheNativeUUIDBeforeATurnAndReopensFromThatBinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture child uses a POSIX shell")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "fixture-cli")
	script := `#!/bin/sh
previous=''
native=''
reopen=''
for argument in "$@"; do
 if [ "$previous" = '--session-id' ]; then native="$argument"; fi
 if [ "$previous" = '--resume' ]; then native="$argument"; reopen=yes; fi
 previous="$argument"
done
if [ "$reopen" = yes ]; then
 [ "$(cat fixture-native-id)" = "$native" ] || exit 3
else
 printf '%s' "$native" > fixture-native-id
fi
while IFS= read -r line; do
 id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
 case "$line" in
  *get_settings*) response='{"applied":{"model":"resumed","effort":"high"},"effective":{}}';;
  *) response='{}';;
 esac
 printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s","response":%s}}\n' "$id" "$response"
done
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	makeRegistry := func() *serve.Registry {
		t.Helper()
		implementation, err := claude.New(claude.Config{Executable: executable, WorkingDirectory: root, Environment: []string{"HOME=" + root, "PATH=/usr/bin:/bin"}, Tools: claude.UnrestrictedTools()})
		if err != nil {
			t.Fatal(err)
		}
		registry := serve.NewRegistry()
		if err := registry.Register("claude", implementation); err != nil {
			t.Fatal(err)
		}
		return registry
	}
	registry := makeRegistry()
	path := filepath.Join(root, "bindings.jsonl")
	store, err := binding.File(path)
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(registry, serve.Options{Bindings: store})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer hub.CloseSessions(ctx)
	session, _, err := hub.Open(ctx, "claude", base.OpenRequest{SessionID: "conversation", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	recorded, found, err := store.Latest(ctx, "conversation")
	if err != nil || !found || recorded.Record.NativeSessionID == "" {
		t.Fatalf("open binding=%+v found=%t err=%v", recorded, found, err)
	}
	native := recorded.Record.NativeSessionID
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	restartedStore, err := binding.File(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := serve.New(makeRegistry(), serve.Options{Bindings: restartedStore})
	defer restarted.CloseSessions(ctx)
	_, state, err := restarted.Open(ctx, "claude", base.OpenRequest{SessionID: "conversation", Reopen: true, Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Recovery == nil || !state.Recovery.Recovered || !strings.Contains(state.Recovery.Reason, "loader") || state.CurrentModelID != "resumed" || state.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("reopened state=%+v", state)
	}
	var returned string
	if err := json.Unmarshal(state.Metadata["claude_native_session_id"], &returned); err != nil || returned != native {
		t.Fatalf("native binding changed: %s -> %s (%v)", native, returned, err)
	}
}
