package native

import (
	"encoding/json"
	"testing"
)

const (
	sessionID   = "3b926aac-d113-4b86-9dc1-0c36b2013f93"
	turnUUID    = "aaaa1111-0000-4000-8000-000000000001"
	toolUseID   = "toolu_probe01"
	commandUUID = "aaaa1111-0000-4000-8000-000000000001"
)

func decode(t *testing.T, frameType, subtype, frame string) (any, error) {
	t.Helper()
	return DecodeObservation(frameType, subtype, []byte(frame))
}

func mustDecode(t *testing.T, frameType, subtype, frame string) any {
	t.Helper()
	value, err := decode(t, frameType, subtype, frame)
	if err != nil {
		t.Fatalf("%s: %v", frameType, err)
	}
	return value
}

func TestCommandLifecycleStates(t *testing.T) {
	for _, state := range []string{CommandQueued, CommandStarted, CommandCompleted, CommandCancelled} {
		frame := mustDecode(t, TypeCommandLifecycle, "", `{"type":"command_lifecycle","command_uuid":"`+commandUUID+`","state":"`+state+`","session_id":"`+sessionID+`","uuid":"c1"}`)
		lifecycle, ok := frame.(*CommandLifecycleFrame)
		if !ok || lifecycle.CommandUUID != commandUUID || lifecycle.State != state {
			t.Fatalf("state %s: %#v", state, frame)
		}
	}
	for name, frame := range map[string]string{
		"no uuid":    `{"type":"command_lifecycle","state":"queued","session_id":"s"}`,
		"no state":   `{"type":"command_lifecycle","command_uuid":"u","session_id":"s"}`,
		"no session": `{"type":"command_lifecycle","command_uuid":"u","state":"queued"}`,
	} {
		if _, err := decode(t, TypeCommandLifecycle, "", frame); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestAssistantEchoAndBlocks(t *testing.T) {
	frame := mustDecode(t, TypeAssistant, "", `{"type":"assistant","message":{"id":"msg_1","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"`+toolUseID+`","name":"Bash","input":{"command":"ls"}}],"stop_reason":null,"usage":{"input_tokens":7}},"parent_tool_use_id":null,"session_id":"`+sessionID+`","uuid":"a1","user_message_uuid":"`+turnUUID+`","user_message_uuids":["`+turnUUID+`"],"error":"rate_limit"}`)
	assistant, ok := frame.(*AssistantFrame)
	if !ok {
		t.Fatalf("frame = %T", frame)
	}
	if assistant.UserMessageUUID != turnUUID || len(assistant.UserMessageUUIDs) != 1 {
		t.Fatalf("echo = %+v", assistant.UserMessageUUIDs)
	}
	if assistant.Error != "rate_limit" {
		t.Fatalf("error = %q", assistant.Error)
	}
	if len(assistant.Message.Content) != 1 || assistant.Message.Content[0].Name != "Bash" {
		t.Fatalf("blocks = %+v", assistant.Message.Content)
	}
	// The synthetic auth-failure shape decodes with its model marker.
	frame = mustDecode(t, TypeAssistant, "", `{"type":"assistant","message":{"id":"m","model":"<synthetic>","content":[{"type":"text","text":"Not logged in"}]},"parent_tool_use_id":null,"session_id":"`+sessionID+`","uuid":"a2","error":"authentication_failed","is_api_error_message":true,"user_message_uuid":"`+turnUUID+`"}`)
	assistant = frame.(*AssistantFrame)
	if assistant.Message.Model != "<synthetic>" || !assistant.IsAPIErrorMessage {
		t.Fatalf("synthetic = %+v", assistant)
	}
	for name, frame := range map[string]string{
		"no model":   `{"type":"assistant","message":{"content":[]}}`,
		"no content": `{"type":"assistant","message":{"model":"m"}}`,
	} {
		if _, err := decode(t, TypeAssistant, "", frame); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
}

func TestUserToolResultAndSyntheticInterrupt(t *testing.T) {
	frame := mustDecode(t, TypeUser, "", `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"`+toolUseID+`","type":"tool_result","content":"probe","is_error":false}]},"parent_tool_use_id":null,"session_id":"`+sessionID+`","uuid":"u1","tool_use_result":{"stdout":"probe","stderr":"","interrupted":false}}`)
	user, ok := frame.(*UserFrame)
	if !ok {
		t.Fatalf("frame = %T", frame)
	}
	blocks, isBlocks := user.Blocks()
	if !isBlocks || len(blocks) != 1 || blocks[0].ToolUseID != toolUseID || blocks[0].IsError == nil || *blocks[0].IsError {
		t.Fatalf("blocks = %+v", blocks)
	}

	frame = mustDecode(t, TypeUser, "", `{"type":"user","message":{"role":"user","content":"[Request interrupted by user]"},"parent_tool_use_id":null,"session_id":"`+sessionID+`","uuid":"u2"}`)
	user = frame.(*UserFrame)
	if text, ok := user.TextContent(); !ok || text != "[Request interrupted by user]" {
		t.Fatalf("text = %q", text)
	}

	// Injected turns carry origin; the reducer discriminates on it.
	frame = mustDecode(t, TypeUser, "", `{"type":"user","message":{"role":"user","content":"task done"},"parent_tool_use_id":null,"session_id":"`+sessionID+`","uuid":"u3","origin":{"kind":"task-notification"}}`)
	user = frame.(*UserFrame)
	if user.Origin == nil || user.Origin.Kind != "task-notification" {
		t.Fatalf("origin = %+v", user.Origin)
	}
	if _, err := decode(t, TypeUser, "", `{"type":"user","parent_tool_use_id":null}`); err == nil {
		t.Fatal("user frame without message accepted")
	}
}

func TestResultArbitrationInputs(t *testing.T) {
	base := `{"type":"result","subtype":"success","duration_ms":130,"duration_api_ms":75,"is_error":IS_ERROR,"num_turns":1,"session_id":"` + sessionID + `","total_cost_usd":0.000096,"usage":{"input_tokens":7,"output_tokens":5},"permission_denials":[],"terminal_reason":"TERMINAL","result":"fixture response","user_message_uuid":"` + turnUUID + `","user_message_uuids":["` + turnUUID + `"],"queued_turn_count":0,"uuid":"r1","new_future_field":{"x":1}}`
	completed := mustDecode(t, TypeResult, ResultSuccess, replace(base, "IS_ERROR", "false", "TERMINAL", TerminalCompleted)).(*ResultFrame)
	if completed.IsError || completed.Cancelled() || completed.Result != "fixture response" || completed.QueuedTurnCount == nil || *completed.QueuedTurnCount != 0 {
		t.Fatalf("completed = %+v", completed)
	}

	apiError := mustDecode(t, TypeResult, ResultSuccess, replace(base, "IS_ERROR", "true", "TERMINAL", "api_error")).(*ResultFrame)
	if !apiError.IsError || apiError.Cancelled() {
		t.Fatalf("api error = %+v", apiError)
	}

	interrupted := mustDecode(t, TypeResult, ResultErrorDuringExecution, `{"type":"result","subtype":"error_during_execution","duration_ms":66,"duration_api_ms":0,"is_error":true,"num_turns":2,"session_id":"`+sessionID+`","stop_reason":null,"usage":{"input_tokens":0,"output_tokens":0},"modelUsage":{},"permission_denials":[],"terminal_reason":"aborted_streaming","errors":["[ede_diagnostic] result_type=user"],"user_message_uuid":"`+turnUUID+`","user_message_uuids":["`+turnUUID+`"],"queued_turn_count":0,"uuid":"r2"}`).(*ResultFrame)
	if !interrupted.Cancelled() || interrupted.IsError != true || interrupted.NumTurns != 2 {
		t.Fatalf("interrupted = %+v", interrupted)
	}

	maxTurns := mustDecode(t, TypeResult, ResultErrorMaxTurns, `{"type":"result","subtype":"error_max_turns","duration_ms":1,"duration_api_ms":1,"is_error":true,"num_turns":3,"session_id":"`+sessionID+`","errors":["Max turns reached"],"uuid":"r3"}`).(*ResultFrame)
	if !maxTurns.MaxTurns() {
		t.Fatal("max turns not detected")
	}

	if _, err := decode(t, TypeResult, "error_new_future", `{"type":"result","subtype":"error_new_future","duration_ms":1,"duration_api_ms":1,"is_error":true,"num_turns":1,"session_id":"s","errors":["future failure"]}`); err != nil {
		t.Fatalf("unknown result subtype rejected: %v", err)
	}
	if _, err := decode(t, TypeResult, "", `{"type":"result","subtype":"","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":1,"session_id":"s"}`); err == nil {
		t.Fatal("result without subtype accepted")
	}
	if _, err := decode(t, TypeResult, ResultSuccess, `{"type":"result","subtype":"success","duration_ms":1,"duration_api_ms":1,"is_error":false,"num_turns":1}`); err == nil {
		t.Fatal("result without session_id accepted")
	}
}

func TestStreamEventDeltas(t *testing.T) {
	frame := mustDecode(t, TypeStreamEvent, "", `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"fixture response"}},"session_id":"`+sessionID+`","parent_tool_use_id":null,"uuid":"e1","user_message_uuid":"`+turnUUID+`","user_message_uuids":["`+turnUUID+`"]}`)
	event := frame.(*StreamEventFrame)
	kind, text, ok := event.StreamDelta()
	if !ok || kind != "text" || text != "fixture response" {
		t.Fatalf("delta = %q %q %v", kind, text, ok)
	}
	if event.UserMessageUUID != turnUUID {
		t.Fatalf("echo = %q", event.UserMessageUUID)
	}

	frame = mustDecode(t, TypeStreamEvent, "", `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}},"session_id":"`+sessionID+`","uuid":"e2"}`)
	kind, thinking, ok := frame.(*StreamEventFrame).StreamDelta()
	if !ok || kind != "thinking" || thinking != "hmm" {
		t.Fatalf("thinking delta = %q %q %v", kind, thinking, ok)
	}

	frame = mustDecode(t, TypeStreamEvent, "", `{"type":"stream_event","event":{"type":"message_stop"},"session_id":"`+sessionID+`","uuid":"e3"}`)
	if _, _, ok := frame.(*StreamEventFrame).StreamDelta(); ok {
		t.Fatal("message_stop projected as a delta")
	}
	if _, err := decode(t, TypeStreamEvent, "", `{"type":"stream_event","session_id":"`+sessionID+`","uuid":"e4"}`); err == nil {
		t.Fatal("stream event without event accepted")
	}
}

func TestSystemVocabulary(t *testing.T) {
	init := mustDecode(t, TypeSystem, SystemInit, `{"type":"system","subtype":"init","cwd":"/w","session_id":"`+sessionID+`","tools":["Task","Bash"],"mcp_servers":[],"model":"claude-sonnet-4-5","permissionMode":"default","slash_commands":[],"apiKeySource":"ANTHROPIC_API_KEY","claude_code_version":"2.1.263","capabilities":["interrupt_receipt_v1","interrupt_cancel_queued_v1","msg_lifecycle_v1"],"uuid":"i1"}`).(*InitFrame)
	if init.SessionID != sessionID || len(init.Capabilities) != 3 || init.ClaudeCodeVersion != "2.1.263" {
		t.Fatalf("init = %+v", init)
	}
	if _, err := decode(t, TypeSystem, SystemInit, `{"type":"system","subtype":"init","session_id":"s"}`); err == nil {
		t.Fatal("init without model/tools accepted")
	}

	state := mustDecode(t, TypeSystem, SystemSessionState, `{"type":"system","subtype":"session_state_changed","state":"idle","session_id":"`+sessionID+`","uuid":"x1"}`).(*SessionStateFrame)
	if state.State != "idle" {
		t.Fatalf("state = %q", state.State)
	}

	started := mustDecode(t, TypeSystem, SystemTaskStarted, `{"type":"system","subtype":"task_started","task_id":"task1","description":"research","uuid":"t1","session_id":"`+sessionID+`","tool_use_id":"`+toolUseID+`","task_type":"local_agent","is_backgrounded":true}`).(*TaskStartedFrame)
	if started.TaskID != "task1" || started.TaskType != "local_agent" || started.IsBackgrounded == nil || !*started.IsBackgrounded {
		t.Fatalf("task_started = %+v", started)
	}

	notification := mustDecode(t, TypeSystem, SystemTaskNotification, `{"type":"system","subtype":"task_notification","task_id":"task1","status":"completed","output_file":"/out","summary":"done","uuid":"t2","session_id":"`+sessionID+`","usage":{"total_tokens":10,"tool_uses":2,"duration_ms":100}}`).(*TaskNotificationFrame)
	if notification.Status != "completed" {
		t.Fatalf("notification = %+v", notification)
	}
	if _, err := decode(t, TypeSystem, SystemTaskNotification, `{"type":"system","subtype":"task_notification","task_id":"task1","status":"running","output_file":"/o","summary":"s","uuid":"t3","session_id":"s"}`); err == nil {
		t.Fatal("non-terminal task_notification status accepted")
	}

	updated := mustDecode(t, TypeSystem, SystemTaskUpdated, `{"type":"system","subtype":"task_updated","task_id":"task1","session_id":"`+sessionID+`","patch":{"status":"killed","end_time":123}}`).(*TaskUpdatedFrame)
	if !updated.Terminal() || updated.Patch.Status != "killed" {
		t.Fatalf("task_updated = %+v", updated)
	}

	notice := mustDecode(t, TypeSystem, "future_subtype", `{"type":"system","subtype":"future_subtype","data":{"anything":true},"uuid":"n1"}`)
	if _, ok := notice.(*SystemNotice); !ok {
		t.Fatalf("unknown system subtype = %T", notice)
	}
}

func TestUnknownTypesAreTolerated(t *testing.T) {
	for _, frameType := range []string{"future_frame", "auth_status", "prompt_suggestion", "tool_use_summary", "active_goal"} {
		value, err := DecodeObservation(frameType, "", []byte(`{"type":"`+frameType+`","payload":{"x":1}}`))
		if err != nil {
			t.Fatalf("%s: %v", frameType, err)
		}
		unknown, ok := value.(*UnknownFrame)
		if !ok || unknown.Type != frameType {
			t.Fatalf("%s: %T", frameType, value)
		}
	}
	if value, err := DecodeObservation(TypeKeepAlive, "", []byte(`{"type":"keep_alive"}`)); err != nil {
		t.Fatal(err)
	} else if _, ok := value.(*UnknownFrame); !ok {
		t.Fatalf("keep_alive = %T", value)
	}
}

func TestCanUseToolRequestDecode(t *testing.T) {
	value, err := DecodeControlRequest(ControlCanUseTool, []byte(`{"type":"control_request","request_id":"b6efb009","request":{"subtype":"can_use_tool","tool_name":"Bash","display_name":"Bash","input":{"command":"touch /tmp/x"},"description":"touch /tmp/x","permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}],"blocked_path":"/tmp/x","tool_use_id":"`+toolUseID+`"}}`))
	if err != nil {
		t.Fatal(err)
	}
	ask, ok := value.(*CanUseToolRequest)
	if !ok || ask.ToolName != "Bash" || ask.ToolUseID != toolUseID || ask.BlockedPath != "/tmp/x" || len(ask.Input) == 0 {
		t.Fatalf("ask = %#v", value)
	}
	if _, err := DecodeControlRequest(ControlCanUseTool, []byte(`{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{}}}`)); err == nil {
		t.Fatal("ask without tool_use_id accepted")
	}
	value, err = DecodeControlRequest("hook_callback", []byte(`{"type":"control_request","request_id":"r","request":{"subtype":"hook_callback","callback_id":"h1"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if unknown, ok := value.(*UnknownFrame); !ok || unknown.Subtype != "hook_callback" {
		t.Fatalf("unconfigured reverse request = %T", value)
	}
}

func TestHostWriteShapes(t *testing.T) {
	frame, err := NewUserTurn(turnUUID, "hello one")
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(frame, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["session_id"] != "default" || decoded["uuid"] != turnUUID {
		t.Fatalf("frame = %s", frame)
	}
	origin, ok := decoded["origin"].(map[string]any)
	if !ok || origin["kind"] != "human" {
		t.Fatalf("origin = %+v", decoded["origin"])
	}
	message, ok := decoded["message"].(map[string]any)
	if !ok || message["content"] != "hello one" || message["role"] != "user" {
		t.Fatalf("message = %+v", decoded["message"])
	}
	if err := ValidateTurnUUID(turnUUID); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "sp ace-uuid", "uuid/with/slashes", "uuid_with_underscore"} {
		if err := ValidateTurnUUID(bad); err == nil {
			t.Fatalf("uuid %q accepted", bad)
		}
	}
}

func replace(value string, pairs ...string) string {
	for i := 0; i+1 < len(pairs); i += 2 {
		value = replaceAll(value, pairs[i], pairs[i+1])
	}
	return value
}

func replaceAll(value, old, new string) string {
	for i := 0; i+len(old) <= len(value); i++ {
		if value[i:i+len(old)] == old {
			return value[:i] + new + value[i+len(old):]
		}
	}
	return value
}
