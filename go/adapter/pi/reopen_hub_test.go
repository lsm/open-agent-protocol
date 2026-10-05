package pi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/pi"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

func TestPiHubReopensItsRecordedFileBindingAfterRestart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owned fixture child uses a POSIX shell")
	}
	root := t.TempDir()
	nativeFile := filepath.Join(root, "native.jsonl")
	if err := os.WriteFile(nativeFile, []byte("{\"type\":\"session\",\"id\":\"fixture-bound\",\"version\":3}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := json.Marshal(map[string]any{"sessionId": "fixture-bound", "sessionFile": nativeFile, "thinkingLevel": "high", "steeringMode": "all", "followUpMode": "one-at-a-time", "messageCount": 3, "pendingMessageCount": 0, "isStreaming": false, "isCompacting": false, "autoCompactionEnabled": true, "model": map[string]string{"id": "restored", "provider": "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r line; do
 id=${line#*'"id":"'}; id=${id%%%%'"'*}
 case "$line" in
  *get_state*) kind=get_state; response='%s';;
  *switch_session*)
   case "$line" in *'"sessionPath":"%s"'*) ;; *) exit 3;; esac
   [ -s '%s' ] || exit 4
   kind=switch_session; response='{"cancelled":false}';;
  *) exit 5;;
 esac
 printf '{"id":"%%s","type":"response","command":"%%s","success":true,"data":%%s}\n' "$id" "$kind" "$response"
done
`, state, nativeFile, nativeFile)
	executable := filepath.Join(root, "fixture-pi")
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	makeRegistry := func() *serve.Registry {
		t.Helper()
		implementation, err := pi.New(pi.Config{Executable: executable, WorkingDirectory: root, Environment: []string{"HOME=" + root, "PATH=/usr/bin:/bin"}})
		if err != nil {
			t.Fatal(err)
		}
		registry := serve.NewRegistry()
		if err := registry.Register("pi", implementation); err != nil {
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
	opened, _, err := hub.Open(ctx, "pi", base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	recorded, found, err := store.Latest(ctx, "s")
	if err != nil || !found || !strings.Contains(recorded.Record.NativeSessionID, nativeFile) || !strings.Contains(recorded.Record.NativeSessionID, "fixture-bound") {
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
	_, resumed, err := restarted.Open(ctx, "pi", base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Reopen: true})
	if err != nil || resumed.Recovery == nil || !resumed.Recovery.Recovered || resumed.CurrentModelID != "fixture/restored" || resumed.ReasoningLevel != protocol.ReasoningHigh {
		t.Fatalf("state=%+v err=%v", resumed, err)
	}
}
