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

const (
	SettledByObserved = "observed"
	SettledByInferred = "inferred"
)

type RunCompletedPayload struct {
	SessionID     SessionID       `json:"session_id"`
	RunID         RunID           `json:"run_id"`
	FinalResponse Message         `json:"final_response"`
	StopReason    string          `json:"stop_reason"`
	ModelID       string          `json:"model_id,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
	DurationMS    int64           `json:"duration_ms,omitempty"`
	SettledBy     string          `json:"settled_by,omitempty"`
}

type RunFailedPayload struct {
	SessionID  SessionID         `json:"session_id"`
	RunID      RunID             `json:"run_id"`
	Error      ProtocolError     `json:"error"`
	Usage      *Usage            `json:"usage,omitempty"`
	DurationMS int64             `json:"duration_ms,omitempty"`
	Recovery   *RecoveryMetadata `json:"recovery,omitempty"`
	SettledBy  string            `json:"settled_by,omitempty"`
}

type RunCancelledPayload struct {
	SessionID  SessionID `json:"session_id"`
	RunID      RunID     `json:"run_id"`
	Reason     string    `json:"reason,omitempty"`
	Usage      *Usage    `json:"usage,omitempty"`
	DurationMS int64     `json:"duration_ms,omitempty"`
	SettledBy  string    `json:"settled_by,omitempty"`
}

type ToolDefinition struct {
	Name           string                     `json:"name"`
	Description    string                     `json:"description,omitempty"`
	InputSchema    json.RawMessage            `json:"input_schema"`
	ExecutionOwner ParticipantID              `json:"execution_owner"`
	Source         string                     `json:"source,omitempty"`
	Features       map[string]FeatureSupport  `json:"features,omitempty"`
	Annotations    map[string]json.RawMessage `json:"annotations,omitempty"`
}

const (
	ToolSourceNative  = "native"
	ToolSourceLocal   = "local"
	ToolSourceProcess = "process"
	ToolSourceRemote  = "remote"
	ToolSourceHosted  = "hosted"
)

func IsToolSourceKind(kind string) bool {
	switch kind {
	case ToolSourceNative, ToolSourceLocal, ToolSourceProcess, ToolSourceRemote, ToolSourceHosted:
		return true
	}
	return false
}

const ToolSourceMCP = "mcp"

type ToolSourceDescriptor struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

type ToolsListRequest struct {
	SessionID             SessionID `json:"session_id,omitempty"`
	AllowDegradedFeatures []string  `json:"allow_degraded_features,omitempty"`
}

func (r ToolsListRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

type ToolsListResponse struct {
	SessionID SessionID              `json:"session_id,omitempty"`
	Sources   []ToolSourceDescriptor `json:"sources,omitempty"`
	Tools     []ToolDefinition       `json:"tools"`
}

type ActionCallPayload struct {
	InteractionID  InteractionID   `json:"interaction_id,omitempty"`
	RequestID      EnvelopeID      `json:"request_id,omitempty"`
	SessionID      SessionID       `json:"session_id"`
	RunID          RunID           `json:"run_id"`
	ToolCallID     ToolCallID      `json:"tool_call_id"`
	RequestedBy    ParticipantID   `json:"requested_by,omitempty"`
	RespondedBy    ParticipantID   `json:"responded_by,omitempty"`
	ExecutionOwner ParticipantID   `json:"execution_owner"`
	Source         string          `json:"source,omitempty"`
	Name           string          `json:"name,omitempty"`
	ArgumentsJSON  json.RawMessage `json:"arguments_json,omitempty"`
	Progress       json.RawMessage `json:"progress,omitempty"`
	Result         json.RawMessage `json:"result,omitempty"`
	Error          *ProtocolError  `json:"error,omitempty"`
}

type ResolveArmStarted struct{}

const (
	ResolveArmAcknowledge = "started"
	ResolveArmResult      = "result"
	ResolveArmError       = "error"
)

type ResolveReason string

const (
	ReasonUnknownInteraction      ResolveReason = "unknown_interaction"
	ReasonWrongResponder          ResolveReason = "wrong_responder"
	ReasonAlreadyResolved         ResolveReason = "already_resolved"
	ReasonRepeatedAcknowledgement ResolveReason = "repeated_acknowledgement"
	ReasonLateAcknowledgement     ResolveReason = "late_acknowledgement"
)

var resolveReasonLadder = []ResolveReason{
	ReasonUnknownInteraction,
	ReasonWrongResponder,
	ReasonAlreadyResolved,
	ReasonRepeatedAcknowledgement,
	ReasonLateAcknowledgement,
}

func ResolveReasonRank(reason ResolveReason) (int, bool) {
	for rank, known := range resolveReasonLadder {
		if known == reason {
			return rank, true
		}
	}
	return 0, false
}

func HighestResolveReason(reasons ...ResolveReason) ResolveReason {
	best := ResolveReason("")
	bestRank := len(resolveReasonLadder)
	for _, reason := range reasons {
		rank, ok := ResolveReasonRank(reason)
		if !ok || rank >= bestRank {
			continue
		}
		best, bestRank = reason, rank
	}
	return best
}

type ActionCallResolveRequest struct {
	InteractionID InteractionID      `json:"interaction_id"`
	SessionID     SessionID          `json:"session_id"`
	RunID         RunID              `json:"run_id"`
	ToolCallID    ToolCallID         `json:"tool_call_id"`
	RequestedBy   ParticipantID      `json:"requested_by"`
	RespondedBy   ParticipantID      `json:"responded_by"`
	Started       *ResolveArmStarted `json:"started,omitempty"`
	Result        json.RawMessage    `json:"result,omitempty"`
	Error         *ProtocolError     `json:"error,omitempty"`
}

func (r ActionCallResolveRequest) Arm() string {
	arm, count := "", 0
	if r.Started != nil {
		arm, count = ResolveArmAcknowledge, count+1
	}
	if r.Result != nil {
		arm, count = ResolveArmResult, count+1
	}
	if r.Error != nil {
		arm, count = ResolveArmError, count+1
	}
	if count != 1 {
		return ""
	}
	return arm
}

type ActionCallResolveDetails struct {
	SettlementID EnvelopeID `json:"settlement_id,omitempty"`
}

type ActionCallResolveResponse struct {
	InteractionID InteractionID             `json:"interaction_id"`
	SessionID     SessionID                 `json:"session_id"`
	RunID         RunID                     `json:"run_id"`
	ToolCallID    ToolCallID                `json:"tool_call_id"`
	Accepted      bool                      `json:"accepted"`
	Reason        ResolveReason             `json:"reason,omitempty"`
	Details       *ActionCallResolveDetails `json:"details,omitempty"`
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
