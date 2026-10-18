package claude

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

func providedTool(name string) protocol.ToolDefinition {
	return protocol.ToolDefinition{Name: name, Description: name + " it", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`), ExecutionOwner: "user", Source: nativeToolSource}
}

func providedArgvFor(t *testing.T, posture ToolPosture, tools ...protocol.ToolDefinition) []string {
	t.Helper()
	var captured []string
	implementation, err := New(Config{Executable: "/bin/claude", Tools: posture, ProcessFactory: ProcessFactoryFunc(func(_ context.Context, c rpc.ProcessConfig) (ProcessBridge, error) {
		captured = append([]string(nil), c.Args...)
		return nil, errors.New("not spawning in this test")
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = implementation.open(context.Background(), base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Tools: tools})
	return captured
}

func argvAfter(argv []string, flag string) []string {
	at := slices.Index(argv, flag)
	if at < 0 {
		return nil
	}
	end := slices.Index(argv, "--session-id")
	if end < at {
		end = len(argv)
	}
	return argv[at+1 : end]
}

func TestProvidedToolsArePreAllowedByTheirMCPNamesAfterAnyAllowlistAndUnderAnUnrestrictedPosture(t *testing.T) {
	allowed := providedArgvFor(t, AllowTools("Read"), providedTool("echo"), providedTool("look"))
	if got := argvAfter(allowed, "--allowedTools"); !slices.Equal(got, []string{"Read", "mcp__oap__echo", "mcp__oap__look"}) {
		t.Fatalf("allowed posture --allowedTools = %q in %q", got, allowed)
	}
	open := providedArgvFor(t, UnrestrictedTools(), providedTool("echo"))
	if got := argvAfter(open, "--allowedTools"); !slices.Equal(got, []string{"mcp__oap__echo"}) {
		t.Fatalf("unrestricted posture --allowedTools = %q in %q", got, open)
	}
	if none := providedArgvFor(t, UnrestrictedTools()); slices.Contains(none, "--allowedTools") {
		t.Fatalf("no provided tools still pre-allowed some: %q", none)
	}
}

func TestTheInitializeRequestNamesTheProvidedServerOnlyWhenToolsAreProvided(t *testing.T) {
	for _, testCase := range []struct {
		tools []protocol.ToolDefinition
		want  string
	}{
		{[]protocol.ToolDefinition{providedTool("echo")}, `,"sdkMcpServers":["oap"]}`},
		{nil, `]}]}}`},
	} {
		peer := newWirePeer(t)
		recorder := &initializeRecorder{Client: peer.client, request: make(chan any, 1)}
		implementation, err := New(Config{Executable: "/bin/claude", Tools: UnrestrictedTools(), ProcessFactory: ProcessFactoryFunc(func(context.Context, rpc.ProcessConfig) (ProcessBridge, error) {
			return recorderBridge{client: recorder}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = implementation.open(context.Background(), base.OpenRequest{SessionID: "s", Participant: protocol.Participant{ID: "user"}, Tools: testCase.tools})
		encoded, err := json.Marshal((<-recorder.request).(native.InitializeRequest))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(string(encoded), testCase.want) {
			t.Fatalf("initialize = %s, want it to end %s", encoded, testCase.want)
		}
	}
}

func openProvided(t *testing.T, tools ...protocol.ToolDefinition) (base.Session, *wirePeer) {
	t.Helper()
	peer := newWirePeer(t)
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return peer.client, nil }), Model: "claude-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	session, err := implementation.open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session, peer
}

func mcpAnswer(t *testing.T, peer *wirePeer, requestID string) string {
	t.Helper()
	message, _ := peer.written()
	if message.Kind != rpc.KindControlResponse || message.Response.RequestID != requestID {
		t.Fatalf("answer to %s = %+v", requestID, message)
	}
	if !message.Response.Success {
		return "error"
	}
	var reply struct {
		MCPResponse json.RawMessage `json:"mcp_response"`
	}
	if err := json.Unmarshal(message.Response.Response, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.MCPResponse == nil {
		return string(message.Response.Response)
	}
	return string(reply.MCPResponse)
}

func TestTheProvidedServerAnswersTheCLIsMCPInitializePingToolsListAndAnythingElse(t *testing.T) {
	_, peer := openProvided(t, providedTool("echo"))
	send := func(id, server, message string) {
		peer.send(`{"type":"control_request","request_id":"` + id + `","request":{"subtype":"mcp_message","server_name":"` + server + `","message":` + message + `}}`)
	}
	for _, testCase := range []struct{ id, server, message, want string }{
		{"m1", "oap", `{"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{}},"jsonrpc":"2.0","id":0}`, `{"id":0,"jsonrpc":"2.0","result":{"capabilities":{"tools":{}},"protocolVersion":"2025-06-18","serverInfo":{"name":"oap","version":"0.1"}}}`},
		{"m2", "oap", `{"jsonrpc":"2.0","method":"notifications/initialized"}`, `{}`},
		{"m3", "oap", `{"method":"tools/list","jsonrpc":"2.0","id":1}`, `{"id":1,"jsonrpc":"2.0","result":{"tools":[{"description":"echo it","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}},"name":"echo"}]}}`},
		{"m4", "oap", `{"method":"resources/list","jsonrpc":"2.0","id":2}`, `{"error":{"code":-32601,"message":"method not found"},"id":2,"jsonrpc":"2.0"}`},
		{"m5", "oap", `{"method":"ping","jsonrpc":"2.0","id":"p"}`, `{"id":"p","jsonrpc":"2.0","result":{}}`},
		{"m6", "other", `{"method":"tools/list","jsonrpc":"2.0","id":3}`, "error"},
	} {
		send(testCase.id, testCase.server, testCase.message)
		if got := mcpAnswer(t, peer, testCase.id); got != testCase.want {
			t.Fatalf("%s answered %s, want %s", testCase.id, got, testCase.want)
		}
	}
}

func TestAnOpenWithToolsIsRefusedWhileProvidingThemIsUnadvertised(t *testing.T) {
	implementation, err := New(Config{Factory: ClientFactoryFunc(func(context.Context) (Client, error) { return newWirePeer(t).client, nil }), Model: "claude-test", Clock: &testClock{}, IDs: &testIDs{}, JournalCapacity: 64})
	if err != nil {
		t.Fatal(err)
	}
	_, err = implementation.Open(context.Background(), base.OpenRequest{SessionID: "session", Participant: protocol.Participant{ID: "user"}, Tools: []protocol.ToolDefinition{providedTool("echo")}})
	var refused *base.UnsupportedControlError
	if !errors.As(err, &refused) || refused.Feature != protocol.FeatureToolsProvide || refused.Reason != base.ControlUnadvertised {
		t.Fatalf("err = %v", err)
	}
}

func TestAnSDKMCPServerIsRefusedWhenNoToolIsProvided(t *testing.T) {
	_, peer := openProvided(t)
	peer.send(`{"type":"control_request","request_id":"m1","request":{"subtype":"mcp_message","server_name":"oap","message":{"method":"tools/list","jsonrpc":"2.0","id":1}}}`)
	if got := mcpAnswer(t, peer, "m1"); got != "error" {
		t.Fatalf("answered %s", got)
	}
}

func TestAnOpenRefusesAProvidedToolAnotherParticipantOwnsUnderAnotherSourceWithAnUnfitNameOrSchemaTwiceOrUnderABuiltInOrMCPName(t *testing.T) {
	owned := providedTool("echo")
	owned.ExecutionOwner = "someone"
	foreign := providedTool("echo")
	foreign.Source = "elsewhere"
	draft := providedTool("echo")
	draft.InputSchema = json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#"}`)
	tooMany := make([]protocol.ToolDefinition, maxProvidedTools+1)
	for index := range tooMany {
		tooMany[index] = providedTool("t" + strings.Repeat("x", index))
	}
	for _, testCase := range []struct {
		tools  []protocol.ToolDefinition
		tool   string
		source string
	}{
		{[]protocol.ToolDefinition{owned}, "echo", ""},
		{[]protocol.ToolDefinition{foreign}, "echo", "elsewhere"},
		{[]protocol.ToolDefinition{providedTool("a.b")}, "a.b", ""},
		{[]protocol.ToolDefinition{providedTool(strings.Repeat("x", providedNameLimit+1))}, strings.Repeat("x", providedNameLimit+1), ""},
		{[]protocol.ToolDefinition{draft}, "echo", ""},
		{[]protocol.ToolDefinition{providedTool("echo"), providedTool("echo")}, "echo", ""},
		{[]protocol.ToolDefinition{providedTool("Bash")}, "Bash", ""},
		{[]protocol.ToolDefinition{providedTool("Task")}, "Task", ""},
		{[]protocol.ToolDefinition{providedTool("mcp__files__read")}, "mcp__files__read", ""},
		{tooMany, tooMany[maxProvidedTools].Name, ""},
	} {
		_, err := admitProvided(base.OpenRequest{Participant: protocol.Participant{ID: "user"}, Tools: testCase.tools})
		var refused *base.UnsupportedControlError
		if !errors.As(err, &refused) || refused.Feature != protocol.FeatureToolsProvide || refused.Reason != base.ControlUnsatisfiable || refused.Tool != testCase.tool || refused.Source != testCase.source {
			t.Fatalf("%q: err = %v", testCase.tool, err)
		}
	}
	fitting := providedTool(strings.Repeat("x", providedNameLimit))
	fitting.InputSchema = nil
	admitted, err := admitProvided(base.OpenRequest{Participant: protocol.Participant{ID: "user"}, Tools: []protocol.ToolDefinition{fitting}})
	if err != nil || len(admitted) != 1 || string(admitted[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("admitted = %+v, err = %v", admitted, err)
	}
}

func listedInit(t *testing.T, tools []string, servers ...string) *native.InitFrame {
	t.Helper()
	listed := make([]map[string]string, len(servers))
	for index, server := range servers {
		listed[index] = map[string]string{"name": server}
	}
	encoded, _ := json.Marshal(map[string]any{"tools": tools, "mcp_servers": listed})
	var frame native.InitFrame
	if err := json.Unmarshal(encoded, &frame); err != nil {
		t.Fatal(err)
	}
	return &frame
}

func servedCatalog(t *testing.T, session *Session) []protocol.ToolDefinition {
	t.Helper()
	catalog, err := session.Tools(context.Background(), protocol.ToolsListRequest{SessionID: "session", AllowDegradedFeatures: []string{protocol.FeatureToolsList}})
	if err != nil {
		t.Fatal(err)
	}
	return catalog.Tools.Tools
}

func TestAProvidedToolIsListedUnderItsOwnNameOwnedByTheOpenerAndTheServerThatCarriesItIsNotASource(t *testing.T) {
	session := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle}, provided: []protocol.ToolDefinition{providedTool("echo")}}
	session.projectCatalogLocked(listedInit(t, []string{"Bash", "mcp__oap__echo", "mcp__oap__other", "mcp__files__read"}, "oap", "files"))
	for _, source := range session.catalogSources {
		if source.ID == mcpSourcePrefix+providedServer {
			t.Fatalf("the provided server is a source: %+v", session.catalogSources)
		}
	}
	served := servedCatalog(t, session)
	names := make([]string, len(served))
	for index, tool := range served {
		names[index] = tool.Name + "/" + string(tool.ExecutionOwner)
	}
	if want := []string{"Bash/claude-code", "echo/user", "mcp__oap__other/claude-code", "mcp__files__read/claude-code"}; !slices.Equal(names, want) {
		t.Fatalf("catalog = %q, want %q", names, want)
	}
	if served[1].Description != "echo it" || served[1].Source != nativeToolSource {
		t.Fatalf("provided entry = %+v", served[1])
	}

	plain := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle}}
	plain.projectCatalogLocked(listedInit(t, []string{"mcp__oap__echo"}, "oap"))
	if unprovided := servedCatalog(t, plain); unprovided[0].Name != "mcp__oap__echo" || unprovided[0].ExecutionOwner != harnessOwner || unprovided[0].Source != mcpSourcePrefix+"oap" {
		t.Fatalf("an unprovided oap tool = %+v", unprovided[0])
	}
}

func TestAProvidedToolKeepsItsNameOverASameNamedNativeToolWhicheverInitListsFirst(t *testing.T) {
	for _, tools := range [][]string{{"Lookup", "mcp__oap__Lookup"}, {"mcp__oap__Lookup", "Lookup"}} {
		session := &Session{state: protocol.SessionState{SessionID: "session", Status: protocol.SessionIdle}, provided: []protocol.ToolDefinition{providedTool("Lookup")}}
		session.projectCatalogLocked(listedInit(t, tools, "oap"))
		served := servedCatalog(t, session)
		if len(served) != 1 || served[0].Name != "Lookup" || served[0].ExecutionOwner != "user" {
			t.Fatalf("%q: catalog = %+v", tools, served)
		}
	}
}

func TestTheRunsToolChoiceNamesAProvidedToolBare(t *testing.T) {
	session, peer := openProvided(t, providedTool("echo"))
	_, outcome := admitWith(t, session, peer, protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}, Delivery: protocol.DeliveryAuto, ToolChoice: json.RawMessage(`{"disallowed":["echo"]}`)})
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	hook := func(id, tool string) string {
		peer.send(`{"type":"control_request","request_id":"` + id + `","request":{"subtype":"hook_callback","callback_id":"` + native.ToolSelectionHook + `","input":{"hook_event_name":"PreToolUse","tool_name":"` + tool + `","tool_input":{},"tool_use_id":"` + id + `"},"tool_use_id":"` + id + `"}}`)
		message, _ := peer.written()
		var decision native.HookDeny
		_ = json.Unmarshal(message.Response.Response, &decision)
		return decision.HookSpecificOutput.PermissionDecision
	}
	if got := hook("toolu_p", "mcp__oap__echo"); got != "deny" {
		t.Fatalf("the provided tool's hook decided %q", got)
	}
	if got := hook("toolu_b", "Bash"); got != "" {
		t.Fatalf("an unexcluded tool's hook decided %q", got)
	}
}
