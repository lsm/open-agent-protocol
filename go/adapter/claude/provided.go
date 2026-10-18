package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/claude/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	providedServer     = "oap"
	providedPrefix     = mcpToolPrefix + providedServer + "__"
	maxProvidedTools   = 64
	providedNameLimit  = 64 - len(providedPrefix)
	providedDialect    = "https://json-schema.org/draft/2020-12/schema"
	mcpProtocolVersion = "2025-11-25"
)

var builtinToolNames = []string{"Agent", "Artifact", "AskUserQuestion", "Bash", "ClaudeDesign", "CronCreate", "CronDelete", "CronList", "Edit", "EndConversation", "EnterPlanMode", "EnterWorktree", "ExitPlanMode", "ExitWorktree", "Glob", "Grep", "ListAgents", "ListMcpResourcesTool", "LSP", "Monitor", "NotebookEdit", "OfferChromeSetup", "PowerShell", "Projects", "propose_skills", "ProposeGoal", "PushNotification", "Read", "ReadMcpResourceDirTool", "ReadMcpResourceTool", "ReadNotifications", "RemoteTrigger", "ReportFindings", "ScheduleWakeup", "SendFeedback", "SendMessage", "SendUserFile", "SendUserMessage", "ShareOnboardingGuide", "ShowOnboardingRolePicker", "Skill", "SubagentHandback", "TaskCreate", "TaskGet", "TaskList", "TaskStop", "TaskUpdate", "TodoWrite", "ToolSearch", "WaitForMcpServers", "WebFetch", "WebSearch", "Workflow", "Write", "Task"}

func admitProvided(req base.OpenRequest) ([]protocol.ToolDefinition, error) {
	if len(req.Tools) > maxProvidedTools {
		return nil, refuseTool(req.Tools[maxProvidedTools].Name, fmt.Sprintf("at most %d tools may be provided", maxProvidedTools))
	}
	opener := participant(req.Participant)
	provided := make([]protocol.ToolDefinition, 0, len(req.Tools))
	for _, tool := range req.Tools {
		name := tool.Name
		switch {
		case name == "":
			return nil, refuseTool("", "a provided tool needs a name")
		case tool.ExecutionOwner != opener:
			return nil, refuseTool(name, "execution_owner must be the opening participant")
		case tool.Source != nativeToolSource:
			return nil, &base.UnsupportedControlError{Feature: protocol.FeatureToolsProvide, Reason: base.ControlUnsatisfiable, Tool: name, Source: tool.Source, Detail: "a provided tool names the source " + nativeToolSource + ", the one this adapter declares"}
		case slices.ContainsFunc(provided, func(earlier protocol.ToolDefinition) bool { return earlier.Name == name }):
			return nil, refuseTool(name, "the name is provided twice")
		case strings.HasPrefix(name, mcpToolPrefix):
			return nil, refuseTool(name, "the name is in the CLI's MCP tool namespace")
		case slices.Contains(builtinToolNames, name):
			return nil, refuseTool(name, "the name is one of the pinned CLI's built-in tools")
		case !providedNameFits(name):
			return nil, refuseTool(name, fmt.Sprintf("the name is outside the disclosed name_pattern ^[a-zA-Z0-9_-]{1,%d}$", providedNameLimit))
		case !admissibleDialect(tool.InputSchema):
			return nil, refuseTool(name, "the input schema declares a dialect outside the disclosed "+providedDialect)
		}
		schema := json.RawMessage(`{"type":"object"}`)
		if trimmed := strings.TrimSpace(string(tool.InputSchema)); trimmed != "" && trimmed != "null" {
			schema = cloneRaw(tool.InputSchema)
		}
		provided = append(provided, protocol.ToolDefinition{Name: name, Description: tool.Description, InputSchema: schema, ExecutionOwner: opener, Source: nativeToolSource})
	}
	return provided, nil
}

func refuseTool(tool, detail string) error {
	return &base.UnsupportedControlError{Feature: protocol.FeatureToolsProvide, Reason: base.ControlUnsatisfiable, Tool: tool, Detail: detail}
}

func providedNameFits(name string) bool {
	if name == "" || len(name) > providedNameLimit {
		return false
	}
	for _, c := range []byte(name) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func admissibleDialect(schema json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(schema))
	if trimmed == "" || trimmed == "null" {
		return true
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(schema, &object) != nil || object == nil {
		return false
	}
	declared, ok := object["$schema"]
	if !ok || string(declared) == "null" {
		return true
	}
	var dialect string
	if json.Unmarshal(declared, &dialect) != nil {
		return false
	}
	return dialect == "" || dialect == providedDialect
}

func providedArgv(names []string, unrestricted bool) []string {
	if len(names) == 0 {
		return nil
	}
	var argv []string
	if unrestricted {
		argv = append(argv, "--allowedTools")
	}
	for _, name := range names {
		argv = append(argv, providedPrefix+name)
	}
	return argv
}

func providedServers(provided []protocol.ToolDefinition) []string {
	if len(provided) == 0 {
		return nil
	}
	return []string{providedServer}
}

func (s *Session) withProvidedLocked(catalog []protocol.ToolDefinition, name string) []protocol.ToolDefinition {
	index := slices.IndexFunc(s.provided, func(tool protocol.ToolDefinition) bool { return tool.Name == name })
	served := s.provided[index]
	served.InputSchema = cloneRaw(served.InputSchema)
	if at := slices.IndexFunc(catalog, func(tool protocol.ToolDefinition) bool { return tool.Name == name }); at >= 0 {
		catalog[at] = served
		return catalog
	}
	return append(catalog, served)
}

func (s *Session) providedName(nativeName string) (string, bool) {
	name, found := strings.CutPrefix(nativeName, providedPrefix)
	if !found {
		return "", false
	}
	for _, tool := range s.provided {
		if tool.Name == name {
			return name, true
		}
	}
	return "", false
}

func (s *Session) bareName(nativeName string) string {
	if name, ok := s.providedName(nativeName); ok {
		return name
	}
	return nativeName
}

func (s *Session) answerMCP(control *rpc.IncomingControl, request *native.MCPMessageRequest) {
	ctx := context.Background()
	if len(s.provided) == 0 || request.ServerName != providedServer {
		_ = control.RespondError(ctx, "claude adapter: no such SDK MCP server")
		return
	}
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Params struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"params"`
	}
	_ = json.Unmarshal(request.Message, &message)
	if len(message.ID) == 0 {
		_ = control.Respond(ctx, struct{}{})
		return
	}
	switch message.Method {
	case "initialize":
		version := message.Params.ProtocolVersion
		if version == "" {
			version = mcpProtocolVersion
		}
		s.answerRPC(control, message.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": struct{}{}},
			"serverInfo":      map[string]any{"name": providedServer, "version": protocol.Version},
		})
	case "ping":
		s.answerRPC(control, message.ID, struct{}{})
	case "tools/call":
		s.holdCall(control, message.ID, request.Message)
	case "tools/list":
		listed := make([]map[string]any, 0, len(s.provided))
		for _, tool := range s.provided {
			entry := map[string]any{"name": tool.Name, "inputSchema": tool.InputSchema}
			if tool.Description != "" {
				entry["description"] = tool.Description
			}
			listed = append(listed, entry)
		}
		s.answerRPC(control, message.ID, map[string]any{"tools": listed})
	default:
		s.replyRPC(control, map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
	}
}

func (s *Session) answerRPC(control *rpc.IncomingControl, id json.RawMessage, result any) {
	s.replyRPC(control, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *Session) replyRPC(control *rpc.IncomingControl, reply map[string]any) {
	encoded, err := json.Marshal(reply)
	if err != nil {
		_ = control.RespondError(context.Background(), "claude adapter: "+err.Error())
		return
	}
	_ = control.Respond(context.Background(), native.MCPResponse{MCPResponse: encoded})
}

func (s *Session) requestProvided(run *runState, nativeID, name string, input json.RawMessage) {
	if run.excludes(name) {
		s.tools[nativeID] = &toolState{nativeID: nativeID, run: run, name: name, suppressed: true, terminal: true}
		return
	}
	args, _ := json.Marshal(input)
	tool := &toolState{
		nativeID: nativeID, id: protocol.ToolCallID(s.ids.NewID("tool-call")), run: run,
		name: name, source: nativeToolSource, args: args, order: s.nextOrder,
		interaction: protocol.InteractionID(s.ids.NewID("interaction")),
	}
	s.nextOrder++
	s.tools[nativeID] = tool
	requested, _ := s.emitEnvelope(run, protocol.TypeActionCallRequested, s.toolPayload(tool), false, "")
	tool.requested = requested.ID
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunWaitingForInput, PendingUserInputID: tool.interaction, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
}

func (s *Session) cancelProvided(tool *toolState) {
	tool.terminal = true
	payload := s.toolPayload(tool)
	payload.ArgumentsJSON = nil
	cancelled, _ := s.emitEnvelope(tool.run, protocol.TypeActionCallCancelled, payload, false, tool.started)
	tool.settlement = cancelled.ID
	if tool.held != nil {
		s.replyRPC(tool.held, map[string]any{"jsonrpc": "2.0", "id": tool.heldID, "error": map[string]any{"code": -32603, "message": "the call was cancelled"}})
		tool.held = nil
	}
}

func (s *Session) emitRunning(run *runState) {
	_ = s.emit(run, protocol.TypeRunStatusUpdated, protocol.RunStatusUpdatedPayload{SessionID: s.state.SessionID, RunID: run.id, Status: protocol.RunRunning, UpdatedAtMS: s.clock.Now().UnixMilli()}, false)
}

func (s *Session) holdCall(control *rpc.IncomingControl, id json.RawMessage, message json.RawMessage) {
	var call struct {
		Params struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		} `json:"params"`
	}
	_ = json.Unmarshal(message, &call)
	var toolUseID string
	_ = json.Unmarshal(call.Params.Meta["claudecode/toolUseId"], &toolUseID)
	tool := s.tools[toolUseID]
	if tool == nil || tool.interaction == "" || tool.held != nil || tool.terminal && tool.reply == nil {
		s.replyRPC(control, map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32602, "message": "no provided call matches this tools/call"}})
		return
	}
	if tool.reply != nil {
		s.answerRPC(control, id, tool.reply)
		tool.reply = nil
		return
	}
	tool.held, tool.heldID = control, cloneRaw(id)
}

func (s *Session) answerCall(tool *toolState, result json.RawMessage) {
	if tool.held == nil {
		tool.reply = result
		return
	}
	s.answerRPC(tool.held, tool.heldID, result)
	tool.held = nil
}

func (s *Session) ResolveCall(ctx context.Context, resolution base.CallResolution) (protocol.ActionCallResolveResponse, error) {
	if err := ctx.Err(); err != nil {
		return protocol.ActionCallResolveResponse{}, err
	}
	request := resolution.Request
	answer := protocol.ActionCallResolveResponse{InteractionID: request.InteractionID, SessionID: request.SessionID, RunID: request.RunID, ToolCallID: request.ToolCallID}
	refuse := func(reason protocol.ResolveReason, settlement protocol.EnvelopeID) (protocol.ActionCallResolveResponse, error) {
		answer.Reason = reason
		if reason == protocol.ReasonAlreadyResolved {
			answer.Details = &protocol.ActionCallResolveDetails{SettlementID: settlement}
		}
		return answer, nil
	}
	s.reduceMu.Lock()
	defer s.reduceMu.Unlock()
	var tool *toolState
	for _, candidate := range s.tools {
		if candidate.interaction != "" && candidate.interaction == request.InteractionID {
			tool = candidate
		}
	}
	if tool == nil || request.SessionID != s.state.SessionID || request.RunID != tool.run.id {
		return refuse(protocol.ReasonUnknownInteraction, "")
	}
	if request.RespondedBy != s.participant || request.RequestedBy != endpointID || request.ToolCallID != tool.id {
		return refuse(protocol.ReasonWrongResponder, "")
	}
	arm := request.Arm()
	switch {
	case arm == "":
		return refuse(protocol.ReasonUnknownInteraction, "")
	case tool.terminal:
		return refuse(protocol.ReasonAlreadyResolved, tool.settlement)
	case arm == protocol.ResolveArmAcknowledge && tool.acknowledged:
		return refuse(protocol.ReasonRepeatedAcknowledgement, "")
	case tool.run.terminal:
		return refuse(protocol.ReasonUnknownInteraction, "")
	}
	run := tool.run
	answer.Accepted = true
	if arm == protocol.ResolveArmAcknowledge || !tool.acknowledged {
		started := s.toolPayload(tool)
		started.ArgumentsJSON, started.RequestID = nil, resolution.RequestID
		emitted, _ := s.emitEnvelope(run, protocol.TypeActionCallStarted, started, false, tool.requested)
		tool.started, tool.acknowledged = emitted.ID, true
		if arm == protocol.ResolveArmAcknowledge {
			return answer, nil
		}
	}
	settled := s.toolPayload(tool)
	settled.ArgumentsJSON, settled.RequestID = nil, resolution.RequestID
	kind := protocol.TypeActionCallCompleted
	if arm == protocol.ResolveArmError {
		kind, settled.Error = protocol.TypeActionCallFailed, request.Error
	} else {
		settled.Result = cloneRaw(request.Result)
	}
	emitted, _ := s.emitEnvelope(run, kind, settled, false, tool.started)
	tool.settlement, tool.terminal = emitted.ID, true
	s.emitRunning(run)
	s.answerCall(tool, toolResult(request))
	return answer, nil
}

func toolResult(request protocol.ActionCallResolveRequest) json.RawMessage {
	text := string(request.Result)
	if request.Error != nil {
		text = request.Error.Message
	} else if json.Unmarshal(request.Result, &text) != nil {
		text = string(request.Result)
	}
	encoded, _ := json.Marshal(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}, "isError": request.Error != nil})
	return encoded
}

var _ base.CallResolver = (*Session)(nil)
