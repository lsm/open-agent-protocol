package native

import (
	"encoding/json"
	"errors"
	"fmt"
)

const ReleaseTag = "v2.1.280"

const (
	TypeUser             = "user"
	TypeAssistant        = "assistant"
	TypeSystem           = "system"
	TypeResult           = "result"
	TypeStreamEvent      = "stream_event"
	TypeToolProgress     = "tool_progress"
	TypeCommandLifecycle = "command_lifecycle"
	TypeKeepAlive        = "keep_alive"
	TypeConversation     = "conversation_reset"
	TypeRateLimit        = "rate_limit_event"
)

const (
	SystemInit              = "init"
	SystemStatus            = "status"
	SystemSessionState      = "session_state_changed"
	SystemTaskStarted       = "task_started"
	SystemTaskProgress      = "task_progress"
	SystemTaskNotification  = "task_notification"
	SystemTaskUpdated       = "task_updated"
	SystemPermissionDenied  = "permission_denied"
	SystemInformational     = "informational"
	SystemCompactBoundary   = "compact_boundary"
	SystemBackgroundChanged = "background_tasks_changed"
)

const (
	ResultSuccess               = "success"
	ResultErrorDuringExecution  = "error_during_execution"
	ResultErrorMaxTurns         = "error_max_turns"
	ResultErrorMaxBudgetUSD     = "error_max_budget_usd"
	ResultErrorStructuredOutput = "error_max_structured_output_retries"
)

const (
	TerminalAbortedStreaming = "aborted_streaming"
	TerminalAbortedTools     = "aborted_tools"
	TerminalCompleted        = "completed"
	TerminalMaxTurns         = "max_turns"
)

var TerminalTaskStatuses = map[string]bool{
	"completed": true, "failed": true, "stopped": true, "killed": true,
}

const (
	CommandQueued    = "queued"
	CommandStarted   = "started"
	CommandCompleted = "completed"
	CommandCancelled = "cancelled"
)

const (
	ControlInitialize = "initialize"
	ControlInterrupt  = "interrupt"
	ControlCanUseTool = "can_use_tool"
)

var ErrInvalidFrame = errors.New("claude native: invalid frame for a known type")

type Origin struct {
	Kind string `json:"kind"`
	From string `json:"from,omitempty"`
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   *bool           `json:"is_error,omitempty"`
}

type UserFrame struct {
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	ToolUseResult   json.RawMessage `json:"tool_use_result"`
	Origin          *Origin         `json:"origin"`
	UUID            string          `json:"uuid"`
	SessionID       string          `json:"session_id"`
}

func (frame *UserFrame) Blocks() ([]ContentBlock, bool) {
	var blocks []ContentBlock
	if json.Unmarshal(frame.Message.Content, &blocks) != nil {
		return nil, false
	}
	return blocks, true
}

func (frame *UserFrame) TextContent() (string, bool) {
	var text string
	if json.Unmarshal(frame.Message.Content, &text) != nil {
		return "", false
	}
	return text, true
}

type AssistantFrame struct {
	Message struct {
		ID         string          `json:"id"`
		Model      string          `json:"model"`
		Content    []ContentBlock  `json:"content"`
		StopReason *string         `json:"stop_reason"`
		Usage      json.RawMessage `json:"usage"`
	} `json:"message"`
	ParentToolUseID   *string  `json:"parent_tool_use_id"`
	Error             string   `json:"error"`
	UserMessageUUID   string   `json:"user_message_uuid"`
	UserMessageUUIDs  []string `json:"user_message_uuids"`
	IsAPIErrorMessage bool     `json:"is_api_error_message"`
	UUID              string   `json:"uuid"`
	SessionID         string   `json:"session_id"`
}

type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

type ResultFrame struct {
	Subtype           string          `json:"subtype"`
	DurationMS        int64           `json:"duration_ms"`
	DurationAPIMS     int64           `json:"duration_api_ms"`
	IsError           bool            `json:"is_error"`
	NumTurns          int             `json:"num_turns"`
	SessionID         string          `json:"session_id"`
	StopReason        *string         `json:"stop_reason"`
	TotalCostUSD      *float64        `json:"total_cost_usd"`
	Usage             Usage           `json:"usage"`
	ModelUsage        json.RawMessage `json:"modelUsage"`
	PermissionDenials json.RawMessage `json:"permission_denials"`
	QueuedTurnCount   *int            `json:"queued_turn_count"`
	Errors            []string        `json:"errors"`
	APIErrorStatus    *int            `json:"api_error_status"`
	UserMessageUUID   string          `json:"user_message_uuid"`
	UserMessageUUIDs  []string        `json:"user_message_uuids"`
	TerminalReason    string          `json:"terminal_reason"`
	Result            string          `json:"result"`
	Origin            *Origin         `json:"origin"`
	UUID              string          `json:"uuid"`
}

func (frame *ResultFrame) Cancelled() bool {
	return frame.TerminalReason == TerminalAbortedStreaming || frame.TerminalReason == TerminalAbortedTools
}

func (frame *ResultFrame) MaxTurns() bool {
	return frame.Subtype == ResultErrorMaxTurns || frame.TerminalReason == TerminalMaxTurns
}

type StreamEventFrame struct {
	Event            json.RawMessage `json:"event"`
	ParentToolUseID  *string         `json:"parent_tool_use_id"`
	UserMessageUUID  string          `json:"user_message_uuid"`
	UserMessageUUIDs []string        `json:"user_message_uuids"`
	UUID             string          `json:"uuid"`
	SessionID        string          `json:"session_id"`
}

func (frame *StreamEventFrame) StreamDelta() (kind, text string, ok bool) {
	var event struct {
		Type  string `json:"type"`
		Delta struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Thinking string `json:"thinking"`
		} `json:"delta"`
	}
	if json.Unmarshal(frame.Event, &event) != nil || event.Type != "content_block_delta" {
		return "", "", false
	}
	switch event.Delta.Type {
	case "text_delta":
		return "text", event.Delta.Text, true
	case "thinking_delta":
		return "thinking", event.Delta.Thinking, true
	default:
		return "", "", false
	}
}

type InitFrame struct {
	SessionID  string   `json:"session_id"`
	Model      string   `json:"model"`
	Tools      []string `json:"tools"`
	MCPServers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"mcp_servers"`
	PermissionMode    string   `json:"permissionMode"`
	Capabilities      []string `json:"capabilities"`
	ClaudeCodeVersion string   `json:"claude_code_version"`
	APIKeySource      string   `json:"apiKeySource"`
	Cwd               string   `json:"cwd"`
	UUID              string   `json:"uuid"`
}

type StatusFrame struct {
	Status    *string `json:"status"`
	SessionID string  `json:"session_id"`
	UUID      string  `json:"uuid"`
}

type SessionStateFrame struct {
	State     string `json:"state"`
	SessionID string `json:"session_id"`
	UUID      string `json:"uuid"`
}

type TaskUsage struct {
	TotalTokens int64 `json:"total_tokens"`
	ToolUses    int   `json:"tool_uses"`
	DurationMS  int64 `json:"duration_ms"`
}

type TaskStartedFrame struct {
	TaskID         string `json:"task_id"`
	Description    string `json:"description"`
	UUID           string `json:"uuid"`
	SessionID      string `json:"session_id"`
	ToolUseID      string `json:"tool_use_id"`
	TaskType       string `json:"task_type"`
	SubagentType   string `json:"subagent_type"`
	IsBackgrounded *bool  `json:"is_backgrounded"`
}

type TaskProgressFrame struct {
	TaskID      string    `json:"task_id"`
	Description string    `json:"description"`
	Usage       TaskUsage `json:"usage"`
	UUID        string    `json:"uuid"`
	SessionID   string    `json:"session_id"`
	ToolUseID   string    `json:"tool_use_id"`
}

type TaskNotificationFrame struct {
	TaskID     string     `json:"task_id"`
	Status     string     `json:"status"`
	OutputFile string     `json:"output_file"`
	Summary    string     `json:"summary"`
	UUID       string     `json:"uuid"`
	SessionID  string     `json:"session_id"`
	ToolUseID  string     `json:"tool_use_id"`
	Usage      *TaskUsage `json:"usage"`
}

type TaskUpdatedFrame struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id"`
	UUID      string `json:"uuid"`
	Patch     struct {
		Status         string `json:"status"`
		Description    string `json:"description"`
		EndTime        *int64 `json:"end_time"`
		Error          string `json:"error"`
		IsBackgrounded *bool  `json:"is_backgrounded"`
	} `json:"patch"`
}

func (frame *TaskUpdatedFrame) Terminal() bool {
	return TerminalTaskStatuses[frame.Patch.Status]
}

type CommandLifecycleFrame struct {
	CommandUUID string `json:"command_uuid"`
	State       string `json:"state"`
	SessionID   string `json:"session_id"`
	UUID        string `json:"uuid"`
}

type ToolProgressFrame struct {
	ToolUseID          string  `json:"tool_use_id"`
	ToolName           string  `json:"tool_name"`
	ParentToolUseID    *string `json:"parent_tool_use_id"`
	ElapsedTimeSeconds float64 `json:"elapsed_time_seconds"`
	TaskID             string  `json:"task_id"`
	SessionID          string  `json:"session_id"`
	UUID               string  `json:"uuid"`
}

type ConversationResetFrame struct {
	NewConversationID string `json:"new_conversation_id"`
	UUID              string `json:"uuid"`
	SessionID         string `json:"session_id"`
}

type SystemNotice struct {
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	UUID      string          `json:"uuid"`
	Raw       json.RawMessage `json:"-"`
}

type UnknownFrame struct {
	Type    string
	Subtype string
}

func DecodeObservation(frameType, subtype string, raw []byte) (any, error) {
	switch frameType {
	case TypeUser:
		var frame UserFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.Message.Role == "" || len(frame.Message.Content) == 0 {
			return nil, fmt.Errorf("%w: user frame requires message.role and message.content", ErrInvalidFrame)
		}
		return &frame, nil
	case TypeAssistant:
		var frame AssistantFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.Message.Model == "" || frame.Message.Content == nil {
			return nil, fmt.Errorf("%w: assistant frame requires message.model and message.content", ErrInvalidFrame)
		}
		return &frame, nil
	case TypeResult:
		var frame ResultFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}

		if frame.Subtype == "" {
			return nil, fmt.Errorf("%w: result frame requires subtype", ErrInvalidFrame)
		}
		if frame.SessionID == "" {
			return nil, fmt.Errorf("%w: result frame requires session_id", ErrInvalidFrame)
		}
		return &frame, nil
	case TypeStreamEvent:
		var frame StreamEventFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if len(frame.Event) == 0 || frame.UUID == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("%w: stream_event requires event, uuid, and session_id", ErrInvalidFrame)
		}
		return &frame, nil
	case TypeToolProgress:
		var frame ToolProgressFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.ToolUseID == "" || frame.ToolName == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("%w: tool_progress requires tool_use_id, tool_name, and session_id", ErrInvalidFrame)
		}
		return &frame, nil
	case TypeCommandLifecycle:
		var frame CommandLifecycleFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.CommandUUID == "" || frame.State == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("%w: command_lifecycle requires command_uuid, state, and session_id", ErrInvalidFrame)
		}
		return &frame, nil
	case TypeKeepAlive:
		return &UnknownFrame{Type: TypeKeepAlive}, nil
	case TypeConversation:
		var frame ConversationResetFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.NewConversationID == "" || frame.UUID == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("%w: conversation_reset requires new_conversation_id, uuid, and session_id", ErrInvalidFrame)
		}
		return &frame, nil
	case TypeSystem:
		return decodeSystem(subtype, raw)
	default:
		return &UnknownFrame{Type: frameType, Subtype: subtype}, nil
	}
}

func decodeSystem(subtype string, raw []byte) (any, error) {
	switch subtype {
	case SystemInit:
		var frame InitFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}

		if frame.SessionID == "" || frame.Model == "" || frame.Tools == nil {
			return nil, fmt.Errorf("%w: init frame requires session_id, model, and tools", ErrInvalidFrame)
		}
		return &frame, nil
	case SystemStatus:
		var frame StatusFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		return &frame, nil
	case SystemSessionState:
		var frame SessionStateFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.State == "" {
			return nil, fmt.Errorf("%w: session_state_changed requires state", ErrInvalidFrame)
		}
		return &frame, nil
	case SystemTaskStarted:
		var frame TaskStartedFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.TaskID == "" || frame.Description == "" || frame.UUID == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("%w: task_started requires task_id, description, uuid, and session_id", ErrInvalidFrame)
		}
		return &frame, nil
	case SystemTaskProgress:
		var frame TaskProgressFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.TaskID == "" || frame.Description == "" || frame.UUID == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("%w: task_progress requires task_id, description, uuid, and session_id", ErrInvalidFrame)
		}
		return &frame, nil
	case SystemTaskNotification:
		var frame TaskNotificationFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.TaskID == "" || frame.Status == "" || frame.OutputFile == "" || frame.Summary == "" || frame.UUID == "" || frame.SessionID == "" {
			return nil, fmt.Errorf("%w: task_notification requires task_id, status, output_file, summary, uuid, and session_id", ErrInvalidFrame)
		}
		if !TerminalTaskStatuses[frame.Status] {
			return nil, fmt.Errorf("%w: task_notification status %q is not completed, failed, or stopped", ErrInvalidFrame, frame.Status)
		}
		return &frame, nil
	case SystemTaskUpdated:
		var frame TaskUpdatedFrame
		if err := unmarshal(raw, &frame); err != nil {
			return nil, err
		}
		if frame.TaskID == "" {
			return nil, fmt.Errorf("%w: task_updated requires task_id", ErrInvalidFrame)
		}
		return &frame, nil
	default:
		var notice SystemNotice
		if err := unmarshal(raw, &notice); err != nil {
			return nil, err
		}
		notice.Raw = append(json.RawMessage(nil), raw...)
		return &notice, nil
	}
}

type CanUseToolRequest struct {
	ToolName              string          `json:"tool_name"`
	Input                 json.RawMessage `json:"input"`
	PermissionSuggestions json.RawMessage `json:"permission_suggestions"`
	BlockedPath           string          `json:"blocked_path"`
	DecisionReason        string          `json:"decision_reason"`
	Title                 string          `json:"title"`
	DisplayName           string          `json:"display_name"`
	Description           string          `json:"description"`
	ToolUseID             string          `json:"tool_use_id"`
	AgentID               string          `json:"agent_id"`
}

func DecodeControlRequest(subtype string, raw []byte) (any, error) {
	switch subtype {
	case ControlCanUseTool:
		var envelope struct {
			Request CanUseToolRequest `json:"request"`
		}
		if err := unmarshal(raw, &envelope); err != nil {
			return nil, err
		}
		request := envelope.Request
		if request.ToolName == "" || request.ToolUseID == "" || len(request.Input) == 0 {
			return nil, fmt.Errorf("%w: can_use_tool requires tool_name, input, and tool_use_id", ErrInvalidFrame)
		}
		return &request, nil
	default:

		return &UnknownFrame{Type: "control_request", Subtype: subtype}, nil
	}
}

type InitializeRequest struct {
	Subtype string `json:"subtype"`
	Hooks   any    `json:"hooks"`
}

type InterruptRequest struct {
	Subtype string `json:"subtype"`
}

type InterruptResult struct {
	StillQueued []string `json:"still_queued"`
	Cancelled   []string `json:"cancelled,omitempty"`
}

type PermissionAllow struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput"`
}

type PermissionDeny struct {
	Behavior  string `json:"behavior"`
	Message   string `json:"message"`
	Interrupt bool   `json:"interrupt,omitempty"`
}

func NewUserTurn(uuid, text string) (json.RawMessage, error) {
	frame := map[string]any{
		"type":               TypeUser,
		"message":            map[string]any{"role": "user", "content": text},
		"parent_tool_use_id": nil,
		"session_id":         "default",
		"uuid":               uuid,
		"origin":             map[string]string{"kind": "human"},
	}
	data, err := json.Marshal(frame)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func ValidateTurnUUID(uuid string) error {
	if len(uuid) == 0 || len(uuid) > 128 {
		return fmt.Errorf("%w: turn uuid must be 1-128 bytes", ErrInvalidFrame)
	}
	for _, char := range uuid {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '-':
		default:
			return fmt.Errorf("%w: turn uuid must be alphanumeric with hyphens", ErrInvalidFrame)
		}
	}
	return nil
}

func unmarshal(raw []byte, value any) error {
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	return nil
}
