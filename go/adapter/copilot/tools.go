package copilot

import (
	"encoding/json"

	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type tool struct {
	id        protocol.ToolCallID
	nativeID  string
	name      string
	arguments json.RawMessage
	started   protocol.EnvelopeID
	terminal  bool
}

func toolPayload(t *tool) map[string]any {
	return map[string]any{"tool_call_id": t.id, "requested_by": "copilot.headless", "execution_owner": "copilot-cli", "name": t.name}
}

func (r *reducer) observeTool(event native.Event) error {
	run := r.current
	if run == nil {
		return nil
	}
	id := native.Text(event.Data, "toolCallId")
	var found *tool
	for _, t := range run.tools {
		if t.nativeID == id {
			found = t
			break
		}
	}
	switch event.Type {
	case "tool.execution_start":
		name := native.Text(event.Data, "toolName")
		if id == "" || name == "" || found != nil {
			return r.fail(run, "copilot_invalid_tool_lifecycle", "a tool start that is unnamed or already started")
		}
		t := &tool{id: protocol.ToolCallID(r.ids.NewID("tool-call")), nativeID: id, name: name, arguments: event.Data["arguments"]}
		run.tools = append(run.tools, t)
		payload := toolPayload(t)
		payload["arguments_json"] = t.arguments
		asked, err := r.emit(run, "action.call.requested", payload, false, "")
		if err != nil {
			return err
		}
		t.started, err = r.emit(run, "action.call.started", toolPayload(t), false, asked)
		return err
	case "tool.execution_partial_result", "tool.shell_output":
		if found == nil || found.terminal {
			return nil
		}
		text := native.Text(event.Data, "partialOutput")
		if text == "" {
			text = native.Text(event.Data, "text")
		}
		payload := toolPayload(found)
		payload["progress"] = text
		_, err := r.emit(run, "action.call.progress", payload, false, found.started)
		return err
	case "tool.execution_complete":
		if found == nil {
			return r.fail(run, "copilot_invalid_tool_lifecycle", "a tool completion without a start in this run")
		}
		if found.terminal {
			return r.fail(run, "copilot_invalid_tool_lifecycle", "a second completion for one tool call")
		}
		found.terminal = true
		payload := toolPayload(found)
		if string(event.Data["success"]) == "true" {
			payload["result"] = event.Data["result"]
			_, err := r.emit(run, "action.call.completed", payload, false, found.started)
			return err
		}
		var problem map[string]json.RawMessage
		_ = json.Unmarshal(event.Data["error"], &problem)
		code, message := "copilot_tool_failed", native.Text(problem, "message")
		if native.Text(problem, "code") == "denied" {
			code = "copilot_tool_denied"
		}
		if message == "" {
			message = "Copilot reported the tool call failed"
		}
		payload["error"] = protocol.ProtocolError{Code: code, Message: message}
		_, err := r.emit(run, "action.call.failed", payload, false, found.started)
		return err
	}
	return nil
}

func (r *reducer) settleTools(run *run) error {
	for _, t := range run.tools {
		if t.terminal {
			continue
		}
		t.terminal = true
		payload := toolPayload(t)
		payload["error"] = protocol.ProtocolError{Code: "incomplete_tool", Message: "the Copilot run settled with an unfinished tool call"}
		if _, err := r.emit(run, "action.call.failed", payload, false, t.started); err != nil {
			return err
		}
	}
	return nil
}
