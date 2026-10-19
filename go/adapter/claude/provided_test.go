package claude

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/adaptertest"
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

func providedToolUse(id string) string {
	return `{"type":"assistant","message":{"id":"msg_1","model":"claude-test","content":[{"type":"tool_use","id":"` + id + `","name":"mcp__oap__echo","input":{"text":"hi"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"a-` + id + `"}`
}

func providedCall(peer *wirePeer, requestID, toolUseID string) {
	peer.send(`{"type":"control_request","request_id":"` + requestID + `","request":{"subtype":"mcp_message","server_name":"oap","message":{"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"},"_meta":{"claudecode/toolUseId":"` + toolUseID + `","progressToken":2}},"jsonrpc":"2.0","id":7}}}`)
}

type providedRun struct {
	session base.Session
	peer    *wirePeer
	uuid    string
	outcome submitOutcome
	seen    []protocol.Envelope
}

func startProvided(t *testing.T, toolChoice string) *providedRun {
	t.Helper()
	session, peer := openProvided(t, providedTool("echo"))
	request := protocol.MessageSubmitRequest{SessionID: "session", Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("go")}}, Delivery: protocol.DeliveryAuto}
	if toolChoice != "" {
		request.ToolChoice = json.RawMessage(toolChoice)
	}
	uuid, outcome := admitWith(t, session, peer, request)
	if outcome.err != nil {
		t.Fatal(outcome.err)
	}
	run := &providedRun{session: session, peer: peer, uuid: uuid, outcome: outcome}
	peer.send(providedToolUse("toolu_p"))
	return run
}

func (r *providedRun) until(t *testing.T, kind protocol.EnvelopeType) protocol.Envelope {
	t.Helper()
	for {
		event := adaptertest.Next(t, r.outcome.stream, 5*time.Second)
		r.seen = append(r.seen, event)
		if event.Type == kind {
			return event
		}
	}
}

func (r *providedRun) finish(t *testing.T) []protocol.Envelope {
	t.Helper()
	r.peer.send(resultFrame(r.uuid, "success", false, "completed", "done", 0))
	return append(r.seen, adaptertest.Drain(t, r.outcome.stream, 5*time.Second)...)
}

func (r *providedRun) answerFor(t *testing.T, requested protocol.Envelope) protocol.ActionCallResolveRequest {
	t.Helper()
	var payload protocol.ActionCallPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	return protocol.ActionCallResolveRequest{InteractionID: payload.InteractionID, SessionID: payload.SessionID, RunID: payload.RunID, ToolCallID: payload.ToolCallID, RequestedBy: payload.RequestedBy, RespondedBy: payload.RespondedBy}
}

func (r *providedRun) resolve(t *testing.T, request protocol.ActionCallResolveRequest) protocol.ActionCallResolveResponse {
	t.Helper()
	response, err := r.session.(base.CallResolver).ResolveCall(context.Background(), base.CallResolution{RequestID: "resolve-1", Request: request})
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func assertTraceWithoutOpen(t *testing.T, request protocol.MessageSubmitRequest, admission protocol.MessageSubmitResponse, events []protocol.Envelope) {
	t.Helper()
	adaptertest.AssertProtocolValidWithSubmit(t, request, admission, testDescriptor(t), events)
}

func countType(events []protocol.Envelope, kind protocol.EnvelopeType) int {
	count := 0
	for _, event := range events {
		if event.Type == kind {
			count++
		}
	}
	return count
}

const heldReply = `{"id":7,"jsonrpc":"2.0","result":{"content":[{"text":"hi back","type":"text"}],"isError":false}}`

func TestAProvidedToolsCallReachesTheOpenerAsAnInteractionAndItsResolutionAnswersTheCLIsHeldToolsCall(t *testing.T) {
	run := startProvided(t, "")
	requested := run.until(t, protocol.TypeActionCallRequested)
	var payload protocol.ActionCallPayload
	if err := requested.DecodePayload(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.ExecutionOwner != "user" || payload.RespondedBy != "user" || payload.Name != "echo" || payload.InteractionID == "" {
		t.Fatalf("requested = %+v", payload)
	}
	waiting := run.until(t, protocol.TypeRunStatusUpdated)
	var status protocol.RunStatusUpdatedPayload
	if err := waiting.DecodePayload(&status); err != nil || status.Status != protocol.RunWaitingForInput || status.PendingUserInputID != payload.InteractionID {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	providedCall(run.peer, "call-1", "toolu_p")
	answer := run.answerFor(t, requested)
	answer.Result = json.RawMessage(`"hi back"`)
	if response := run.resolve(t, answer); !response.Accepted {
		t.Fatalf("response = %+v", response)
	}
	if got := mcpAnswer(t, run.peer, "call-1"); got != heldReply {
		t.Fatalf("held tools/call answered %s", got)
	}
	completed := run.until(t, protocol.TypeActionCallCompleted)
	again := run.resolve(t, answer)
	if again.Accepted || again.Reason != protocol.ReasonAlreadyResolved || again.Details == nil || again.Details.SettlementID != completed.ID {
		t.Fatalf("second resolution = %+v", again)
	}
	events := run.finish(t)
	assertTraceWithoutOpen(t, run.outcome.request, run.outcome.admission, events)
	if countType(events, protocol.TypeActionCallStarted) != 1 || countType(events, protocol.TypeActionCallCompleted) != 1 {
		t.Fatalf("events = %v", callSettlementsOf(events))
	}
}

func TestAResolutionThatComesBeforeTheCLIsToolsCallAnswersItWhenItArrives(t *testing.T) {
	run := startProvided(t, "")
	requested := run.until(t, protocol.TypeActionCallRequested)
	answer := run.answerFor(t, requested)
	answer.Result = json.RawMessage(`"hi back"`)
	if response := run.resolve(t, answer); !response.Accepted {
		t.Fatalf("response = %+v", response)
	}
	providedCall(run.peer, "call-1", "toolu_p")
	if got := mcpAnswer(t, run.peer, "call-1"); got != heldReply {
		t.Fatalf("late tools/call answered %s", got)
	}
	assertTraceWithoutOpen(t, run.outcome.request, run.outcome.admission, run.finish(t))
}

func TestAProvidedCallsResolutionsAreRefusedByRankAcknowledgedOnceAndAnErrorSettlesWithItsDetails(t *testing.T) {
	run := startProvided(t, "")
	requested := run.until(t, protocol.TypeActionCallRequested)
	answer := run.answerFor(t, requested)
	foreign := answer
	foreign.RespondedBy, foreign.Started = "someone", &protocol.ResolveArmStarted{}
	unknown := answer
	unknown.InteractionID, unknown.Started = "interaction-x", &protocol.ResolveArmStarted{}
	ack := answer
	ack.Started = &protocol.ResolveArmStarted{}
	for _, testCase := range []struct {
		request protocol.ActionCallResolveRequest
		reason  protocol.ResolveReason
	}{
		{unknown, protocol.ReasonUnknownInteraction},
		{foreign, protocol.ReasonWrongResponder},
		{answer, protocol.ReasonUnknownInteraction},
	} {
		if response := run.resolve(t, testCase.request); response.Accepted || response.Reason != testCase.reason {
			t.Fatalf("%+v: response = %+v", testCase.request, response)
		}
	}
	if response := run.resolve(t, ack); !response.Accepted {
		t.Fatalf("acknowledgement = %+v", response)
	}
	if response := run.resolve(t, ack); response.Accepted || response.Reason != protocol.ReasonRepeatedAcknowledgement {
		t.Fatalf("second acknowledgement = %+v", response)
	}
	failure := answer
	failure.Error = &protocol.ProtocolError{Code: "tool_failed", Message: "failed", Details: map[string]any{"path": "x"}}
	if response := run.resolve(t, failure); !response.Accepted {
		t.Fatalf("error resolution = %+v", response)
	}
	failed := run.until(t, protocol.TypeActionCallFailed)
	var settled protocol.ActionCallPayload
	if err := failed.DecodePayload(&settled); err != nil || settled.Error == nil || settled.Error.Code != "tool_failed" || settled.Error.Details["path"] != "x" {
		t.Fatalf("failed = %s, err = %v", failed.Payload, err)
	}
	events := run.finish(t)
	assertTraceWithoutOpen(t, run.outcome.request, run.outcome.admission, events)
	if countType(events, protocol.TypeActionCallStarted) != 1 {
		t.Fatalf("events = %v", callSettlementsOf(events))
	}
}

func TestAProvidedCallTheCLISettlesFirstOrARunLeavesOpenIsCancelledAndStaysResolved(t *testing.T) {
	settledFirst := startProvided(t, "")
	requested := settledFirst.until(t, protocol.TypeActionCallRequested)
	settledFirst.peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_p","type":"tool_result","content":"MCP tool call timed out","is_error":true}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u1"}`)
	cancelled := settledFirst.until(t, protocol.TypeActionCallCancelled)
	answer := settledFirst.answerFor(t, requested)
	answer.Result = json.RawMessage(`"late"`)
	if late := settledFirst.resolve(t, answer); late.Accepted || late.Reason != protocol.ReasonAlreadyResolved || late.Details.SettlementID != cancelled.ID {
		t.Fatalf("late resolution = %+v", late)
	}
	settledEvents := settledFirst.finish(t)
	assertTraceWithoutOpen(t, settledFirst.outcome.request, settledFirst.outcome.admission, settledEvents)
	if countType(settledEvents, protocol.TypeActionCallFailed) != 0 || countType(settledEvents, protocol.TypeActionCallCancelled) != 1 {
		t.Fatalf("events = %v", callSettlementsOf(settledEvents))
	}

	leftOpen := startProvided(t, "")
	requested = leftOpen.until(t, protocol.TypeActionCallRequested)
	providedCall(leftOpen.peer, "call-1", "toolu_p")
	events := leftOpen.finish(t)
	assertTraceWithoutOpen(t, leftOpen.outcome.request, leftOpen.outcome.admission, events)
	if countType(events, protocol.TypeActionCallCancelled) != 1 {
		t.Fatalf("events = %v", callSettlementsOf(events))
	}
	if got := mcpAnswer(t, leftOpen.peer, "call-1"); !strings.Contains(got, `"error"`) {
		t.Fatalf("the held tools/call was answered %s", got)
	}
	answer = leftOpen.answerFor(t, requested)
	answer.Result = json.RawMessage(`"late"`)
	if late := leftOpen.resolve(t, answer); late.Accepted || late.Reason != protocol.ReasonAlreadyResolved {
		t.Fatalf("late resolution = %+v", late)
	}
}

func TestAProvidedCallTheRunsToolChoiceExcludesIsNeverRequestedOfTheOpener(t *testing.T) {
	run := startProvided(t, `{"disallowed":["echo"]}`)
	run.peer.send(`{"type":"control_request","request_id":"hook-p","request":{"subtype":"hook_callback","callback_id":"` + native.ToolSelectionHook + `","input":{"hook_event_name":"PreToolUse","tool_name":"mcp__oap__echo","tool_input":{},"tool_use_id":"toolu_p"},"tool_use_id":"toolu_p"}}`)
	message, _ := run.peer.written()
	var decision native.HookDeny
	if json.Unmarshal(message.Response.Response, &decision) != nil || decision.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("hook answer = %+v", message)
	}
	run.peer.send(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_p","type":"tool_result","content":"echo is excluded","is_error":true}]},"parent_tool_use_id":null,"session_id":"` + peerSession + `","uuid":"u1"}`)
	events := run.finish(t)
	assertTraceWithoutOpen(t, run.outcome.request, run.outcome.admission, events)
	for _, event := range events {
		var status protocol.RunStatusUpdatedPayload
		if strings.HasPrefix(string(event.Type), "action.call.") || event.Type == protocol.TypeRunStatusUpdated && event.DecodePayload(&status) == nil && status.Status == protocol.RunWaitingForInput {
			t.Fatalf("an excluded provided call reached the trace: %v", callSettlementsOf(events))
		}
	}
	if events[len(events)-1].Type != protocol.TypeRunCompleted {
		t.Fatalf("events = %v", callSettlementsOf(events))
	}
}

func callSettlementsOf(events []protocol.Envelope) []protocol.EnvelopeType {
	kinds := make([]protocol.EnvelopeType, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Type)
	}
	return kinds
}
