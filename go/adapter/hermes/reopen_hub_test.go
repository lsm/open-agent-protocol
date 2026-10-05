package hermes_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/hermes"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestHermesHubReopensItsRecordedStoredSessionAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owned fixture child uses a POSIX shell")
	}
	root := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' '{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{"skin":{"name":"default"},"change_events":true,"replay_epoch":"0123456789abcdef0123456789abcdef"}}}'
while IFS= read -r line; do
 id=${line#*'"id":'}; id=${id%%,*}
 case "$line" in
  *'"session.create"'*) result='{"session_id":"rt000001","stored_session_id":"stored-fixture","message_count":0,"messages":[],"info":{"model":"fixture-model"}}';;
  *'"session.resume"'*)
   case "$line" in *'"session_id":"stored-fixture"'*) ;; *) exit 3;; esac
   result='{"session_id":"rt000002","resumed":"stored-fixture","message_count":2,"messages":[],"info":{"model":"fixture-model"},"inflight":null,"running":false,"status":"idle"}';;
  *) exit 5;;
 esac
 printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$result"
done
`
	executable := filepath.Join(root, "fixture-hermes")
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	makeRegistry := func() *serve.Registry {
		t.Helper()
		implementation, err := hermes.New(hermes.Config{Executable: executable, WorkingDirectory: root, Environment: []string{"HOME=" + root, "PATH=/usr/bin:/bin"}, ExitTimeout: 2 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		registry := serve.NewRegistry()
		if err := registry.Register("hermes", implementation); err != nil {
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
	opened, _, err := hub.Open(ctx, "hermes", base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	recorded, found, err := store.Latest(ctx, "s")
	if err != nil || !found || recorded.Record.NativeSessionID != "stored-fixture" {
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
	_, resumed, err := restarted.Open(ctx, "hermes", base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true})
	if err != nil || resumed.Recovery == nil || !resumed.Recovery.Recovered || resumed.CurrentModelID != "fixture-model" {
		t.Fatalf("state=%+v err=%v", resumed, err)
	}
}
