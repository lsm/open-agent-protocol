package sdk

type ProviderEvent interface {
	isProviderEvent()
}

type AgentEvent interface {
	isAgentEvent()
}

type MessageStart struct {
	ProviderID string
	API        string
	ModelID    string
}

type TextDelta struct {
	Delta string
}

type ThinkingDelta struct {
	Delta string
}

type ToolCallEvent struct {
	ToolCallID    string
	Name          string
	ArgumentsJSON string
}

type MessageEnd struct {
	Usage        *Usage
	StopReason   string
	ErrorMessage string
}

type ErrorEvent struct {
	Message    string
	Code       string
	ProviderID string
}

type AgentStart struct {
	SessionID string
}

type AgentEnd struct {
	StopReason string

	Usage *Usage

	ErrorMessage string
	ProviderID   string
	API          string
}

type TurnStart struct{}

type TurnEnd struct {
	StopReason   string
	ErrorMessage string
}

type ToolExecutionStart struct {
	ToolCallID string
	ToolName   string
}

type ToolExecutionEnd struct {
	ToolCallID string
	IsError    bool
}

func (*MessageStart) isProviderEvent()  {}
func (*TextDelta) isProviderEvent()     {}
func (*ThinkingDelta) isProviderEvent() {}
func (*ToolCallEvent) isProviderEvent() {}
func (*MessageEnd) isProviderEvent()    {}
func (*ErrorEvent) isProviderEvent()    {}

func (*MessageStart) isAgentEvent()       {}
func (*TextDelta) isAgentEvent()          {}
func (*ThinkingDelta) isAgentEvent()      {}
func (*ToolCallEvent) isAgentEvent()      {}
func (*MessageEnd) isAgentEvent()         {}
func (*ErrorEvent) isAgentEvent()         {}
func (*AgentStart) isAgentEvent()         {}
func (*AgentEnd) isAgentEvent()           {}
func (*TurnStart) isAgentEvent()          {}
func (*TurnEnd) isAgentEvent()            {}
func (*ToolExecutionStart) isAgentEvent() {}
func (*ToolExecutionEnd) isAgentEvent()   {}

type toolBuffer struct {
	entries map[int]*toolBufferEntry
}

type toolBufferEntry struct {
	id   string
	name string
	args string
}

func newToolBuffer() *toolBuffer { return &toolBuffer{entries: map[int]*toolBufferEntry{}} }

func (b *toolBuffer) start(index int, id, name string) {
	b.entries[index] = &toolBufferEntry{id: id, name: name}
}

func (b *toolBuffer) delta(index int, delta string) {
	entry := b.entries[index]
	if entry == nil {
		entry = &toolBufferEntry{}
		b.entries[index] = entry
	}
	entry.args += delta
}

func (b *toolBuffer) end(index int) *toolBufferEntry {
	entry := b.entries[index]
	delete(b.entries, index)
	return entry
}

func (b *toolBuffer) size() int { return len(b.entries) }

func normalizeProviderFrame(f *frame, tools *toolBuffer) ProviderEvent {
	switch f.Type {
	case "event":
		payload := f.payload()
		if inner := payload.obj("event"); inner != nil {
			return normalizeProviderPayload(inner, tools)
		}
		return normalizeProviderPayload(payload, tools)
	case "stream_error", "error":
		return errorEventFrom(f.payload())
	case "start", "message_start":
		return messageStartFrom(f.payload())
	case "text_delta":
		return &TextDelta{Delta: f.payload().str("delta")}
	case "thinking_delta", "reasoning_delta", "reasoning":
		return &ThinkingDelta{Delta: f.payload().str("delta", "reasoning")}
	case "tool_call":
		return toolCallFrom(f.payload())
	case "toolcall_start", "toolcall_delta", "toolcall_end":
		return normalizeProviderPayload(f.payload(), tools)
	case "message_end", "done", "result":
		return messageEndFrom(f.payload())
	default:
		return nil
	}
}

func normalizeProviderPayload(payload jsonObject, tools *toolBuffer) ProviderEvent {
	kind := eventKind(payload)
	switch kind {
	case "start", "message_start":
		if message := payload.obj("message"); message != nil {
			return messageStartFrom(message)
		}
		return messageStartFrom(payload)
	case "text_delta":
		return &TextDelta{Delta: payload.str("delta")}
	case "thinking_delta", "reasoning_delta", "reasoning":
		return &ThinkingDelta{Delta: payload.str("delta", "reasoning")}
	case "toolcall_start":
		tools.start(payload.intOr(tools.size(), "content_index"), payload.str("id", "tool_call_id"), payload.str("name"))
		return nil
	case "toolcall_delta":
		tools.delta(payload.intOr(0, "content_index"), payload.str("delta"))
		return nil
	case "toolcall_end":
		return bufferedToolCall(payload, tools)
	case "tool_call":
		return toolCallFrom(payload)
	case "done", "message_end":
		return messageEndFrom(payload)
	case "stream_error", "error":
		return errorEventFrom(payload)
	default:
		return nil
	}
}

var agentEventKinds = map[string]bool{
	"agent_start":           true,
	"agent_end":             true,
	"turn_start":            true,
	"turn_end":              true,
	"tool_execution_start":  true,
	"tool_execution_end":    true,
	"tool_execution_update": true,
	"context_usage":         true,
	"prompt_segment_usage":  true,
}

func normalizeAgentFrame(f *frame, tools *toolBuffer) ([]AgentEvent, error) {
	switch f.Type {
	case "agent_result":
		payload, err := f.jsonPayload("result_json")
		if err != nil {
			return nil, err
		}
		return []AgentEvent{agentEndFrom(payload)}, nil
	case "agent_error":
		return []AgentEvent{errorEventFrom(f.payload())}, nil
	case "agent_event":
		payload, err := f.jsonPayload("event_json")
		if err != nil {
			return nil, err
		}
		return normalizeAgentPayload(payload, tools), nil
	case "event":
		payload := f.mergedPayload()
		inner := payload
		if nested := payload.obj("event"); nested != nil {
			inner = nested
		}
		if kind := eventKind(inner); agentEventKinds[kind] {
			return normalizeAgentPayload(inner, tools), nil
		}
	}
	if event := normalizeProviderFrame(f, tools); event != nil {
		return []AgentEvent{event.(AgentEvent)}, nil
	}
	return nil, nil
}

func normalizeAgentPayload(event jsonObject, tools *toolBuffer) []AgentEvent {
	kind := eventKind(event)

	data := event
	if event.str("type", "event_type") == "" {
		if nested := event.obj(kind); nested != nil {
			data = nested
		}
	}

	switch kind {
	case "agent_start":
		return []AgentEvent{&AgentStart{SessionID: data.str("session_id")}}
	case "turn_start":
		return []AgentEvent{&TurnStart{}}
	case "turn_end":
		return []AgentEvent{&TurnEnd{
			StopReason:   data.str("stop_reason"),
			ErrorMessage: data.str("error_message"),
		}}
	case "tool_execution_start":
		return []AgentEvent{&ToolExecutionStart{
			ToolCallID: data.str("tool_call_id"),
			ToolName:   data.str("tool_name"),
		}}
	case "tool_execution_end":
		isError, _ := data.boolean("is_error")
		return []AgentEvent{&ToolExecutionEnd{
			ToolCallID: data.str("tool_call_id"),
			IsError:    isError,
		}}
	case "tool_execution_update", "context_usage", "prompt_segment_usage":

		return nil
	case "agent_end":
		return []AgentEvent{agentEndFrom(data)}
	case "message_start":
		if message := data.obj("message"); message != nil {
			return []AgentEvent{messageStartFrom(message)}
		}
		return []AgentEvent{messageStartFrom(data)}
	case "message_end":
		if message := data.obj("message"); message != nil {
			return []AgentEvent{messageEndFrom(message)}
		}
		return []AgentEvent{messageEndFrom(data)}
	case "message_update":
		inner := data
		if nested := data.obj("event"); nested != nil {
			inner = nested
		}
		if event := normalizeProviderPayload(inner, tools); event != nil {
			return []AgentEvent{event.(AgentEvent)}
		}
		return nil
	case "error":
		return []AgentEvent{errorEventFrom(data)}
	default:
		if event := normalizeProviderPayload(event, tools); event != nil {
			return []AgentEvent{event.(AgentEvent)}
		}
		return nil
	}
}

func eventKind(event jsonObject) string {
	explicit := event.str("type")
	eventType := event.str("event_type")
	if explicit == "event" && eventType != "" {
		return eventType
	}
	if explicit != "" {
		return explicit
	}
	if eventType != "" {
		return eventType
	}
	return event.soleKey()
}

func messageStartFrom(data jsonObject) *MessageStart {
	return &MessageStart{
		ProviderID: data.str("provider_id", "provider"),
		API:        data.str("api"),
		ModelID:    data.str("model_id", "model"),
	}
}

func messageEndFrom(data jsonObject) *MessageEnd {
	message := data
	if nested := data.obj("message"); nested != nil {
		message = nested
	}
	usage := usageFrom(message.obj("usage"))
	if usage == nil {
		usage = usageFrom(data.obj("usage"))
	}
	if usage == nil {
		usage = usageFrom(message)
	}
	return &MessageEnd{
		Usage:        usage,
		StopReason:   firstNonEmpty(data.str("stop_reason", "reason"), message.str("stop_reason")),
		ErrorMessage: firstNonEmpty(data.str("error_message"), message.str("error_message")),
	}
}

func agentEndFrom(data jsonObject) *AgentEnd {
	usage := usageFrom(data.obj("usage"))
	if usage == nil {
		usage = usageFrom(data)
	}
	return &AgentEnd{
		StopReason:   data.str("stop_reason", "reason"),
		Usage:        usage,
		ErrorMessage: data.str("error_message"),
		ProviderID:   data.str("provider_id", "provider"),
		API:          data.str("api"),
	}
}

func toolCallFrom(data jsonObject) *ToolCallEvent {
	return &ToolCallEvent{
		ToolCallID:    data.str("tool_call_id", "id"),
		Name:          data.str("name"),
		ArgumentsJSON: data.str("arguments_json"),
	}
}

func bufferedToolCall(data jsonObject, tools *toolBuffer) *ToolCallEvent {
	buffered := tools.end(data.intOr(0, "content_index"))
	event := &ToolCallEvent{
		ToolCallID:    data.str("tool_call_id", "id"),
		Name:          data.str("name"),
		ArgumentsJSON: data.str("arguments_json"),
	}
	if buffered != nil {
		event.ToolCallID = firstNonEmpty(event.ToolCallID, buffered.id)
		event.Name = firstNonEmpty(event.Name, buffered.name)
		event.ArgumentsJSON = firstNonEmpty(event.ArgumentsJSON, buffered.args)
	}
	return event
}

func errorEventFrom(data jsonObject) *ErrorEvent {
	return &ErrorEvent{
		Message:    data.strOrDefault("stream error", "message", "error_message", "reason"),
		Code:       data.str("code", "error_code"),
		ProviderID: data.str("provider_id"),
	}
}

func usageFrom(data jsonObject) *Usage {
	if data == nil {
		return nil
	}
	input, hasInput := data.num("input", "input_tokens")
	output, hasOutput := data.num("output", "output_tokens")
	if !hasInput || !hasOutput {
		return nil
	}
	usage := &Usage{Input: int64(input), Output: int64(output)}
	if value, ok := data.num("cache_read"); ok {
		usage.CacheRead = int64(value)
	}
	if value, ok := data.num("cache_write"); ok {
		usage.CacheWrite = int64(value)
	}
	return usage
}
