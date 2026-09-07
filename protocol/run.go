package protocol

import "encoding/json"

type RunStatus string

const (
	RunQueued          RunStatus = "queued"
	RunRunning         RunStatus = "running"
	RunWaitingForInput RunStatus = "waiting_for_input"
	RunCancelling      RunStatus = "cancelling"
	RunCompleted       RunStatus = "completed"
	RunFailed          RunStatus = "failed"
	RunCancelled       RunStatus = "cancelled"
)

type RunCancelRequest struct {
	SessionID SessionID `json:"session_id"`
	RunID     RunID     `json:"run_id"`
	Reason    string    `json:"reason,omitempty"`
}

type RunCancelResponse struct {
	SessionID SessionID `json:"session_id"`
	RunID     RunID     `json:"run_id"`
	Accepted  bool      `json:"accepted"`
	Status    RunStatus `json:"status"`
}

type RunStartedPayload struct {
	SessionID   SessionID `json:"session_id"`
	RunID       RunID     `json:"run_id"`
	Status      RunStatus `json:"status"`
	ModelID     string    `json:"model_id,omitempty"`
	StartedAtMS int64     `json:"started_at_ms,omitempty"`
}

type RunStatusUpdatedPayload struct {
	SessionID          SessionID     `json:"session_id"`
	RunID              RunID         `json:"run_id"`
	Status             RunStatus     `json:"status"`
	PendingUserInputID InteractionID `json:"pending_user_input_id,omitempty"`
	UpdatedAtMS        int64         `json:"updated_at_ms,omitempty"`
}

type ContentDeltaPayload struct {
	SessionID SessionID   `json:"session_id"`
	RunID     RunID       `json:"run_id"`
	MessageID MessageID   `json:"message_id,omitempty"`
	Part      ContentPart `json:"part"`
}

type RunCompletedPayload struct {
	SessionID     SessionID      `json:"session_id"`
	RunID         RunID          `json:"run_id"`
	FinalResponse Message        `json:"final_response"`
	StopReason    string         `json:"stop_reason"`
	Result        map[string]any `json:"result,omitempty"`
	Usage         *Usage         `json:"usage,omitempty"`
	DurationMS    int64          `json:"duration_ms,omitempty"`
}

type RunFailedPayload struct {
	SessionID  SessionID         `json:"session_id"`
	RunID      RunID             `json:"run_id"`
	Error      ProtocolError     `json:"error"`
	Usage      *Usage            `json:"usage,omitempty"`
	DurationMS int64             `json:"duration_ms,omitempty"`
	Recovery   *RecoveryMetadata `json:"recovery,omitempty"`
}

type RunCancelledPayload struct {
	SessionID  SessionID `json:"session_id"`
	RunID      RunID     `json:"run_id"`
	Reason     string    `json:"reason,omitempty"`
	Usage      *Usage    `json:"usage,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
}

type ToolDefinition struct {
	Name           string                     `json:"name"`
	Description    string                     `json:"description,omitempty"`
	InputSchema    json.RawMessage            `json:"input_schema"`
	ExecutionOwner ParticipantID              `json:"execution_owner"`
	Annotations    map[string]json.RawMessage `json:"annotations,omitempty"`
}

type ToolsListRequest struct{}

type ToolsListResponse struct {
	Tools []ToolDefinition `json:"tools"`
}

type ActionCallPayload struct {
	InteractionID  InteractionID   `json:"interaction_id,omitempty"`
	SessionID      SessionID       `json:"session_id"`
	RunID          RunID           `json:"run_id"`
	ToolCallID     ToolCallID      `json:"tool_call_id"`
	RequestedBy    ParticipantID   `json:"requested_by,omitempty"`
	RespondedBy    ParticipantID   `json:"responded_by,omitempty"`
	ExecutionOwner ParticipantID   `json:"execution_owner"`
	Name           string          `json:"name,omitempty"`
	ArgumentsJSON  json.RawMessage `json:"arguments_json,omitempty"`
	Progress       json.RawMessage `json:"progress,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *ProtocolError  `json:"error,omitempty"`
}

type PermissionChoice struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type PermissionRequestedPayload struct {
	InteractionID InteractionID      `json:"interaction_id"`
	RequestedBy   ParticipantID      `json:"requested_by"`
	RespondedBy   ParticipantID      `json:"responded_by"`
	SessionID     SessionID          `json:"session_id"`
	RunID         RunID              `json:"run_id"`
	ToolCallID    ToolCallID         `json:"tool_call_id,omitempty"`
	Title         string             `json:"title"`
	Description   string             `json:"description,omitempty"`
	Choices       []PermissionChoice `json:"choices"`
	ArgumentsJSON json.RawMessage    `json:"arguments_json,omitempty"`
}

type PermissionResolveRequest struct {
	InteractionID        InteractionID   `json:"interaction_id"`
	RequestedBy          ParticipantID   `json:"requested_by"`
	RespondedBy          ParticipantID   `json:"responded_by"`
	SessionID            SessionID       `json:"session_id"`
	RunID                RunID           `json:"run_id"`
	ChoiceID             string          `json:"choice_id,omitempty"`
	Granted              bool            `json:"granted"`
	Reason               string          `json:"reason,omitempty"`
	UpdatedArgumentsJSON json.RawMessage `json:"updated_arguments_json,omitempty"`
}

type PermissionResolveResponse struct {
	InteractionID InteractionID `json:"interaction_id"`
	SessionID     SessionID     `json:"session_id"`
	RunID         RunID         `json:"run_id"`
	Accepted      bool          `json:"accepted"`
}

type InteractionOutcome string

const (
	InteractionResolved  InteractionOutcome = "resolved"
	InteractionRejected  InteractionOutcome = "rejected"
	InteractionCancelled InteractionOutcome = "cancelled"
	InteractionFailed    InteractionOutcome = "failed"
)

type PermissionResolvedPayload struct {
	InteractionID InteractionID      `json:"interaction_id"`
	RequestedBy   ParticipantID      `json:"requested_by"`
	RespondedBy   ParticipantID      `json:"responded_by"`
	SessionID     SessionID          `json:"session_id"`
	RunID         RunID              `json:"run_id"`
	ToolCallID    ToolCallID         `json:"tool_call_id,omitempty"`
	Outcome       InteractionOutcome `json:"outcome"`
	ChoiceID      string             `json:"choice_id,omitempty"`
	Granted       *bool              `json:"granted,omitempty"`
	Reason        *ProtocolError     `json:"reason,omitempty"`
}
