package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
	"github.com/lsm/open-agent-protocol/go/adapter/codex/appserver/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	conversationCaptureVariable = "OAP_CODEX_CONVERSATION_CAPTURE"
	conversationUpdateVariable  = "OAP_UPDATE_CODEX_CONVERSATION"
	conversationText            = "hello <codex> & friends\u2028\u00e9"
)

type conversationFrame struct {
	Direction string `json:"direction"`
	Frame     string `json:"frame"`
}

type conversationRecord struct {
	Version            int                 `json:"version"`
	CodexCommit        string              `json:"codex_commit"`
	CapabilityRevision string              `json:"capability_revision"`
	Capabilities       string              `json:"capabilities"`
	Frames             []conversationFrame `json:"frames"`
}

func TestConversationHelperProcess(t *testing.T) {
	capture := os.Getenv(conversationCaptureVariable)
	if capture == "" {
		return
	}
	os.Exit(serveConversation(capture))
}

func serveConversation(capture string) int {
	reader := bufio.NewReader(os.Stdin)
	var frames []conversationFrame
	read := func() rpc.Message {
		line, err := reader.ReadString('\n')
		if err != nil {
			return rpc.Message{}
		}
		line = strings.TrimSuffix(line, "\n")
		frames = append(frames, conversationFrame{Direction: "client_to_server", Frame: line})
		message, _ := rpc.ParseMessage([]byte(line))
		return message
	}
	send := func(frame string) {
		frames = append(frames, conversationFrame{Direction: "server_to_client", Frame: frame})
		_, _ = os.Stdout.WriteString(frame + "\n")
	}
	respond := func(request rpc.Message, result string) {
		var wire bytes.Buffer
		if err := rpc.NewEncoder(&wire).Encode(rpc.Response(request.ID, json.RawMessage(result))); err != nil {
			return
		}
		send(strings.TrimSuffix(wire.String(), "\n"))
	}

	respond(read(), `{"userAgent":"codex-fixture","codexHome":"/codex","platformFamily":"unix","platformOs":"linux"}`)
	read()
	respond(read(), `{"thread":{"id":"native-thread"}}`)
	respond(read(), `{"turn":{"id":"native-turn","status":"inProgress"}}`)
	send(`{"method":"turn/started","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"inProgress"}}}`)
	send(`{"method":"item/started","params":{"threadId":"native-thread","turnId":"native-turn","item":{"type":"commandExecution","id":"native-item","command":"true","status":"inProgress"}}}`)
	send(`{"id":7,"method":"item/commandExecution/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"native-item","kind":"command","startedAtMs":10,"reason":"needs approval","availableDecisions":["accept","decline","cancel"]}}`)
	read()
	send(`{"id":8,"method":"item/tool/requestUserInput","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"tool-item","isBlocking":true,"questions":[{"id":"mode","header":"Mode","question":"Choose mode","isOther":false,"isSecret":false,"options":[{"label":"Fast","description":"Lower latency"},{"label":"Safe","description":"More checks"}]},{"id":"note","header":"Note","question":"Add note","isOther":false,"isSecret":false,"options":null}]}}`)
	read()
	send(`{"id":9,"method":"item/permissions/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"native-item","permissions":{}}}`)
	read()
	send(`{"id":10,"method":"item/commandExecution/requestApproval","params":{"threadId":"other-thread","turnId":"native-turn","itemId":"native-item","kind":"command","startedAtMs":11}}`)
	read()
	send(`{"id":11,"method":"item/fileChange/requestApproval","params":{"threadId":"native-thread","turnId":"native-turn","itemId":"native-item","startedAtMs":12,"reason":"apply edit","grantRoot":"/workspace"}}`)
	respond(read(), `{}`)
	send(`{"method":"turn/completed","params":{"threadId":"native-thread","turn":{"id":"native-turn","status":"interrupted"}}}`)
	read()
	if _, err := reader.ReadString('\n'); !errors.Is(err, io.EOF) {
		return 21
	}
	data, err := json.Marshal(frames)
	if err != nil || os.WriteFile(capture, data, 0o600) != nil {
		return 22
	}
	return 0
}

func conversationPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "fixtures", "adapters", "codex-appserver-writes", "conversation.json")
}

func conversationEvent(t *testing.T, stream adapter.EventStream, want protocol.EnvelopeType) protocol.Envelope {
	t.Helper()
	event := adaptertest.Next(t, stream, 5*time.Second)
	if event.Type != want {
		t.Fatalf("got %s, want %s", event.Type, want)
	}
	return event
}

func TestCodexProcessWritesTheRecordedConversation(t *testing.T) {
	ctx := context.Background()
	capture := filepath.Join(t.TempDir(), "capture.json")
	implementation, err := New(Config{
		Executable:      os.Args[0],
		Args:            []string{"-test.run=^TestConversationHelperProcess$", "--"},
		Environment:     []string{conversationCaptureVariable + "=" + capture},
		Model:           "glm-test",
		ApprovalPolicy:  "on-request",
		Sandbox:         "workspace-write",
		Clock:           &fakeClock{},
		IDs:             &fakeIDs{},
		ShutdownTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := implementation.Probe(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session, err := implementation.Open(ctx, adapter.OpenRequest{SessionID: "session-1", Participant: protocol.Participant{ID: "user"}})
	if err != nil {
		t.Fatal(err)
	}
	model := "glm-per-turn"
	admission, stream, err := session.Submit(ctx, protocol.MessageSubmitRequest{
		SessionID: "session-1", Delivery: protocol.DeliveryAuto, ModelID: &model,
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent(conversationText)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationEvent(t, stream, protocol.TypeRunStarted)
	conversationEvent(t, stream, protocol.TypeActionCallRequested)
	conversationEvent(t, stream, protocol.TypeActionCallStarted)
	var permission protocol.PermissionRequestedPayload
	requested := conversationEvent(t, stream, protocol.TypeActionPermissionRequested)
	if err := requested.DecodePayload(&permission); err != nil {
		t.Fatal(err)
	}
	if err := session.Resolve(ctx, adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Permission: &protocol.PermissionResolveRequest{
		InteractionID: permission.InteractionID, RequestedBy: endpointID, RespondedBy: "user",
		SessionID: admission.SessionID, RunID: admission.RunID, ChoiceID: "decline", Granted: false,
	}}); err != nil {
		t.Fatal(err)
	}
	conversationEvent(t, stream, protocol.TypeActionPermissionResolved)
	var input protocol.UserInputRequestedPayload
	asked := conversationEvent(t, stream, protocol.TypeUserInputRequested)
	if err := asked.DecodePayload(&input); err != nil {
		t.Fatal(err)
	}
	conversationEvent(t, stream, protocol.TypeRunStatusUpdated)
	if err := session.Resolve(ctx, adapter.InteractionResolution{RunID: admission.RunID, RespondedBy: "user", Input: &protocol.UserInputResolveRequest{
		InteractionID: input.InteractionID, RequestedBy: endpointID, RespondedBy: "user",
		SessionID: admission.SessionID, RunID: admission.RunID,
		Answers: []protocol.InputAnswer{{QuestionID: "mode", SelectedOptionIDs: []string{"option-2"}}, {QuestionID: "note", Text: "ship it"}},
	}}); err != nil {
		t.Fatal(err)
	}
	conversationEvent(t, stream, protocol.TypeUserInputResolved)
	conversationEvent(t, stream, protocol.TypeRunStatusUpdated)
	conversationEvent(t, stream, protocol.TypeActionPermissionRequested)
	if _, err := session.Cancel(ctx, admission.RunID); err != nil {
		t.Fatal(err)
	}
	events := adaptertest.Drain(t, stream, 5*time.Second)
	if len(events) == 0 || events[len(events)-1].Type != protocol.TypeRunCancelled {
		t.Fatalf("conversation did not settle as cancelled: %+v", events)
	}
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatalf("the scripted app-server recorded no conversation: %v", err)
	}
	var frames []conversationFrame
	if err := json.Unmarshal(data, &frames); err != nil {
		t.Fatal(err)
	}
	capabilities, err := json.Marshal(descriptor.Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	got := conversationRecord{Version: 1, CodexCommit: CodexCommit, CapabilityRevision: CapabilityRevision, Capabilities: string(capabilities), Frames: frames}
	path := conversationPath(t)
	if os.Getenv(conversationUpdateVariable) == "1" {
		encoded, err := json.MarshalIndent(got, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want := loadJSON[conversationRecord](t, path)
	if want.Capabilities != got.Capabilities {
		t.Fatalf("capabilities differ\nwant: %s\ngot:  %s", want.Capabilities, got.Capabilities)
	}
	for index := range min(len(want.Frames), len(got.Frames)) {
		if want.Frames[index] != got.Frames[index] {
			t.Fatalf("frame %d differs\nwant: %+v\ngot:  %+v", index, want.Frames[index], got.Frames[index])
		}
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("recorded conversation differs; run %s=1 to rerecord\nwant: %+v\ngot:  %+v", conversationUpdateVariable, want, got)
	}
}
