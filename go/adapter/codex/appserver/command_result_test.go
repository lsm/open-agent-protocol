package appserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func TestCommandResultPreservesNativeExitCode(t *testing.T) {
	for _, code := range []string{"0", "1", "-7", "null", "absent"} {
		t.Run(code, func(t *testing.T) {
			client, session, descriptor := openFake(t)
			admission, stream := submitFake(t, session)
			client.notify("turn/started", json.RawMessage(`{"threadId":"native-thread","turn":{"id":"native-turn","status":"inProgress"}}`))
			client.notify("item/started", json.RawMessage(`{"threadId":"native-thread","turnId":"native-turn","item":{"type":"commandExecution","id":"read-1","command":"git diff HEAD~1","cwd":"/workspace/repo","status":"inProgress"}}`))
			completed := `{"threadId":"native-thread","turnId":"native-turn","item":{"type":"commandExecution","id":"read-1","command":"git diff HEAD~1","cwd":"/workspace/repo","status":"completed","aggregatedOutput":"bwrap: No permissions to create a new namespace","exitCode":CODE}}`
			if code == "absent" {
				completed = strings.Replace(completed, `,"exitCode":CODE`, "", 1)
			} else {
				completed = strings.Replace(completed, "CODE", code, 1)
			}
			client.notify("item/completed", json.RawMessage(completed))
			client.notify("turn/completed", json.RawMessage(`{"threadId":"native-thread","turn":{"id":"native-turn","status":"completed"}}`))
			events := drainClosed(t, stream)
			adaptertest.AssertProtocolValidWithSubmit(t, fakeSubmit, admission, descriptor, events)
			var payload protocol.ActionCallPayload
			if len(events) != 5 || events[3].Type != protocol.TypeActionCallCompleted || events[3].DecodePayload(&payload) != nil {
				t.Fatalf("unexpected tool lifecycle: %+v", events)
			}
			var result map[string]json.RawMessage
			if err := json.Unmarshal(payload.Result, &result); err != nil {
				t.Fatal(err)
			}
			want := code
			if want == "absent" {
				want = "null"
			}
			if string(result["exitCode"]) != want || string(result["command"]) != `"git diff HEAD~1"` || string(result["cwd"]) != `"/workspace/repo"` || string(result["output"]) != `"bwrap: No permissions to create a new namespace"` {
				t.Fatalf("native exitCode=%s: result=%s", code, payload.Result)
			}
		})
	}
}
