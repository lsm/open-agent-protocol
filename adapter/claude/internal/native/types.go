// Package native models the pinned Claude Code 2.1.263 stream-json frame
// vocabulary: the typed subset the adapter reduces, plus tolerant holders for
// the documented forward-compatible surface (unknown types and system
// subtypes are observations, not violations — both reference hosts ignore
// them by design).
package native

import (
	"encoding/json"
	"errors"
	"fmt"
)

const ReleaseTag = "v2.1.263"

// Message-stream frame types (see rpc.Type* constants for the full set).
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

// Modeled system subtypes. Everything else reduces as a generic system
// observation.
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

// Result subtypes (SDKResultSuccess | SDKResultError).
const (
	ResultSuccess               = "success"
	ResultErrorDuringExecution  = "error_during_execution"
	ResultErrorMaxTurns         = "error_max_turns"
	ResultErrorMaxBudgetUSD     = "error_max_budget_usd"
	ResultErrorStructuredOutput = "error_max_structured_output_retries"
)

// Terminal reasons that prove cancellation (the interrupt receipt never
// settles anything by itself).
const (
	TerminalAbortedStreaming = "aborted_streaming"
	TerminalAbortedTools     = "aborted_tools"
	TerminalCompleted        = "completed"
	TerminalMaxTurns         = "max_turns"
)

// Terminal task statuses span both lifecycle vocabularies:
// task_notification reports "stopped" (the CLI's mapped form of a killed
// task) while task_updated reports the raw "killed".
var TerminalTaskStatuses = map[string]bool{
	"completed": true, "failed": true, "stopped": true, "killed": true,
}

// Command lifecycle states observed on the pinned build (the family is
// untyped in both reference SDKs; states beyond these are still recorded).
const (
	CommandQueued    = "queued"
	CommandStarted   = "started"
	CommandCompleted = "completed"
	CommandCancelled = "cancelled"
)

// Control request subtypes this adapter exercises.
const (
	ControlInitialize = "initialize"
	ControlInterrupt  = "interrupt"
	ControlCanUseTool = "can_use_tool"
)

var ErrInvalidFrame = errors.New("claude native: invalid frame for a known type")

// Origin is message provenance. Only kind is always present; absent origin
// or a non-string kind means unattributed (parsed as nil, like the
// reference _parse_origin).
type Origin struct {
	Kind string `json:"kind"`
	From string `json:"from,omitempty"`
}

// ContentBlock is one Messages-API content block. Exactly the members the
// adapter reduces are typed; the rest stay in Raw for evidence.
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

// UserFrame is the CLI's own user-role output (tool results, synthetic
// interrupt markers, injected turns).
type UserFrame struct {
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"` // string or []ContentBlock
	} `json:"message"`
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	ToolUseResult   json.RawMessage `json:"tool_use_result"`
	Origin          *Origin         `json:"origin"`
	UUID            string          `json:"uuid"`
	SessionID       string          `json:"session_id"`
}

// Blocks decodes the content as a block list; ok is false for plain string
// content.
func (frame *UserFrame) Blocks() ([]ContentBlock, bool) {
	var blocks []ContentBlock
	if json.Unmarshal(frame.Message.Content, &blocks) != nil {
		return nil, false
	}
	return blocks, true
}

// TextContent decodes plain string content.
func (frame *UserFrame) TextContent() (string, bool) {
	var text string
	if json.Unmarshal(frame.Message.Content, &text) != nil {
		return "", false
	}
	return text, true
}

// AssistantFrame is one completed content block (or a synthetic
// API-error message).
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

// Usage is the turn usage block on result frames.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
}

// ResultFrame is the one turn-terminal candidate.
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

// Cancelled is the frozen cancellation rule: only terminal_reason proves it.
func (frame *ResultFrame) Cancelled() bool {
	return frame.TerminalReason == TerminalAbortedStreaming || frame.TerminalReason == TerminalAbortedTools
}

// MaxTurns is the native error subtype this contract projects as a graceful
// limit stop rather than a failure.
func (frame *ResultFrame) MaxTurns() bool {
	return frame.Subtype == ResultErrorMaxTurns || frame.TerminalReason == TerminalMaxTurns
}

// StreamEventFrame wraps one raw Anthropic streaming event.
type StreamEventFrame struct {
	Event            json.RawMessage `json:"event"`
	ParentToolUseID  *string         `json:"parent_tool_use_id"`
	UserMessageUUID  string          `json:"user_message_uuid"`
	UserMessageUUIDs []string        `json:"user_message_uuids"`
	UUID             string          `json:"uuid"`
	SessionID        string          `json:"session_id"`
}

// StreamDelta extracts a projected text or thinking delta from the wrapped
// event, if this event carries one.
func (frame *StreamEventFrame) StreamDelta() (kind, text string, ok bool) {
	var event struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(frame.Event, &event) != nil || event.Type != "content_block_delta" {
		return "", "", false
	}
	switch event.Delta.Type {
	case "text_delta":
		return "text", event.Delta.Text, true
	case "thinking_delta":
		return "thinking", event.Delta.Text, true
	default:
		return "", "", false
	}
}

// InitFrame is the per-turn capability truth refresh.
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

// StatusFrame is the compacting/requesting liveness signal.
type StatusFrame struct {
	Status    *string `json:"status"`
	SessionID string  `json:"session_id"`
	UUID      string  `json:"uuid"`
}

// SessionStateFrame carries the authoritative idle/running/requires_action
// signal.
type SessionStateFrame struct {
	State     string `json:"state"`
	SessionID string `json:"session_id"`
	UUID      string `json:"uuid"`
}

// TaskUsage is the task lifecycle usage block.
type TaskUsage struct {
	TotalTokens int64 `json:"total_tokens"`
	ToolUses    int   `json:"tool_uses"`
	DurationMS  int64 `json:"duration_ms"`
}

// TaskStartedFrame marks a background task in flight.
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

// TaskProgressFrame reports interim task usage.
type TaskProgressFrame struct {
	TaskID      string    `json:"task_id"`
	Description string    `json:"description"`
	Usage       TaskUsage `json:"usage"`
	UUID        string    `json:"uuid"`
	SessionID   string    `json:"session_id"`
	ToolUseID   string    `json:"tool_use_id"`
}

// TaskNotificationFrame is one legal child terminal shape.
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

// TaskUpdatedFrame is the second legal child terminal shape.
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

// Terminal reports whether the patch settles the task.
func (frame *TaskUpdatedFrame) Terminal() bool {
	return TerminalTaskStatuses[frame.Patch.Status]
}

// CommandLifecycleFrame is the untyped admission corroboration family.
type CommandLifecycleFrame struct {
	CommandUUID string `json:"command_uuid"`
	State       string `json:"state"`
	SessionID   string `json:"session_id"`
	UUID        string `json:"uuid"`
}

// ToolProgressFrame is the long-running tool heartbeat.
type ToolProgressFrame struct {
	ToolUseID          string  `json:"tool_use_id"`
	ToolName           string  `json:"tool_name"`
	ParentToolUseID    *string `json:"parent_tool_use_id"`
	ElapsedTimeSeconds float64 `json:"elapsed_time_seconds"`
	TaskID             string  `json:"task_id"`
	SessionID          string  `json:"session_id"`
	UUID               string  `json:"uuid"`
}

// ConversationResetFrame reports a replaced conversation.
type ConversationResetFrame struct {
	NewConversationID string `json:"new_conversation_id"`
	UUID              string `json:"uuid"`
	SessionID         string `json:"session_id"`
}

// SystemNotice is any unmodeled system subtype.
type SystemNotice struct {
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	UUID      string          `json:"uuid"`
	Raw       json.RawMessage `json:"-"`
}

// UnknownFrame is a tolerated forward-compatibility holder.
type UnknownFrame struct {
	Type    string
	Subtype string
}

// DecodeObservation types one message-stream frame. Known types validate the
// fields the reference parser requires (a known discriminator with a
// violated shape is fatal there); unknown types decode tolerantly.
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
		switch frame.Subtype {
		case ResultSuccess, ResultErrorDuringExecution, ResultErrorMaxTurns, ResultErrorMaxBudgetUSD, ResultErrorStructuredOutput:
		default:
			return nil, fmt.Errorf("%w: unknown result subtype %q", ErrInvalidFrame, frame.Subtype)
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
		// Narrower than the reference parser (which types init as a generic
		// system message): the descriptor refresh projects these three.
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

// CanUseToolRequest is the CLI's permission ask (reverse control request).
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

// DecodeControlRequest types one reverse control request. The raw bytes are
// the complete control_request frame.
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
		// Every other reverse subtype is unconfigured in v1 (no hooks, no
		// SDK MCP servers, no dialogs); keep the frame as evidence.
		return &UnknownFrame{Type: "control_request", Subtype: subtype}, nil
	}
}

// Host write and result shapes -------------------------------------------------

// InitializeRequest is the minimal verified initialize payload.
type InitializeRequest struct {
	Subtype string `json:"subtype"`
	Hooks   any    `json:"hooks"`
}

// InterruptRequest aborts the running turn. cancel_queued stays unset in v1:
// the adapter renders no per-uuid queue control.
type InterruptRequest struct {
	Subtype string `json:"subtype"`
}

// InterruptResult is the interrupt_receipt_v1 receipt.
type InterruptResult struct {
	StillQueued []string `json:"still_queued"`
	Cancelled   []string `json:"cancelled,omitempty"`
}

// PermissionAllow answers a can_use_tool ask with allow; the updated input
// becomes what the tool executes.
type PermissionAllow struct {
	Behavior     string          `json:"behavior"`
	UpdatedInput json.RawMessage `json:"updatedInput"`
}

// PermissionDeny answers a can_use_tool ask with deny.
type PermissionDeny struct {
	Behavior  string `json:"behavior"`
	Message   string `json:"message"`
	Interrupt bool   `json:"interrupt,omitempty"`
}

// NewUserTurn builds one inbound user frame: the host-minted uuid is the
// submission identity the CLI echoes, the logical stream label is the
// reference SDK's "default", and origin is stamped human because absent
// origin is treated as unattributed at trust gates.
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

// ValidateTurnUUID rejects degenerate submission identities before they are
// written to the wire.
func ValidateTurnUUID(uuid string) error {
	if len(uuid) < 8 || len(uuid) > 128 {
		return fmt.Errorf("%w: turn uuid must be 8-128 bytes", ErrInvalidFrame)
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

// unmarshal decodes leniently: the CLI vocabulary grows over time and the
// reference parser tolerates unknown members on known types.
func unmarshal(raw []byte, value any) error {
	if err := json.Unmarshal(raw, value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	return nil
}
