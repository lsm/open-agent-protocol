package acp_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/acp"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestACPHubReopensFromItsRecordedNativeBindingAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the owned fixture child uses a POSIX shell")
	}
	root := t.TempDir()
	executable := filepath.Join(root, "fixture-agent")
	script := `#!/bin/sh
while IFS= read -r line; do
 id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
 case "$line" in
  *initialize*) response='{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}';;
  *session/new*) printf '%s' fixture-bound > native-id; response='{"sessionId":"fixture-bound"}';;
  *session/load*)
   case "$line" in *'"sessionId":"fixture-bound"'*) ;; *) exit 3;; esac
   [ "$(cat native-id)" = fixture-bound ] || exit 4
   response='{"configOptions":[{"id":"model","category":"model","currentValue":"restored"},{"id":"effort","category":"thought_level","currentValue":"high"}]}';;
  *) exit 5;;
 esac
 printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$response"
done
`
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	makeRegistry := func() *serve.Registry {
		t.Helper()
		implementation, err := acp.New(acp.Config{Executable: executable, WorkingDirectory: root, Environment: []string{"HOME=" + root, "PATH=/usr/bin:/bin"}})
		if err != nil {
			t.Fatal(err)
		}
		registry := serve.NewRegistry()
		if err := registry.Register("acp", implementation); err != nil {
			t.Fatal(err)
		}
		return registry
	}
	path := filepath.Join(root, "bindings.jsonl")
	store, err := binding.File(path)
	if err != nil {
		t.Fatal(err)
	}
	hub := serve.New(makeRegistry(), serve.Options{Bindings: store})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer hub.CloseSessions(ctx)
	opened, _, err := hub.Open(ctx, "acp", base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	recorded, found, err := store.Latest(ctx, "s")
	if err != nil || !found || recorded.Record.NativeSessionID != "fixture-bound" {
		t.Fatalf("binding=%+v found=%t err=%v", recorded, found, err)
	}
	if err := opened.Close(ctx); err != nil {
		t.Fatal(err)
	}
	store, err = binding.File(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := serve.New(makeRegistry(), serve.Options{Bindings: store})
	defer restarted.CloseSessions(ctx)
	_, state, err := restarted.Open(ctx, "acp", base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true})
	if err != nil || state.Recovery == nil || !state.Recovery.Recovered || state.CurrentModelID != "restored" || state.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
