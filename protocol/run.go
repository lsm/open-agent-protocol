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

// RunCompletedPayload reports a completed run. ModelID names the model that
// produced the final response, so a consumer need not correlate back to the
// admission to see it; under an admitted model_id it may not name another
// model. Result is raw so presence is preserved exactly as the endpoint
// emitted it: a map with omitempty drops a valid empty object `{}`, which is a
// conforming structured result under a schema that requires nothing.
type RunCompletedPayload struct {
	SessionID     SessionID       `json:"session_id"`
	RunID         RunID           `json:"run_id"`
	FinalResponse Message         `json:"final_response"`
	StopReason    string          `json:"stop_reason"`
	ModelID       string          `json:"model_id,omitempty"`
	Result        json.RawMessage `json:"result,omitempty"`
	Usage         *Usage          `json:"usage,omitempty"`
	DurationMS    int64           `json:"duration_ms,omitempty"`
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

// ToolDefinition is one catalog entry. Source names the ToolSourceDescriptor
// the tool comes from — a source id, never an inline copy of the descriptor —
// so a consumer can attribute a tool to an MCP server without parsing its
// name, and a harness that namespaces MCP tools (Claude's
// `mcp__<server>__<tool>`) exposes the namespaced string as Name while Source
// carries the attribution. Features is the per-tool effective support map, so
// one entry can report that this tool is executable but has no progress.
type ToolDefinition struct {
	Name           string                     `json:"name"`
	Description    string                     `json:"description,omitempty"`
	InputSchema    json.RawMessage            `json:"input_schema"`
	ExecutionOwner ParticipantID              `json:"execution_owner"`
	Source         string                     `json:"source,omitempty"`
	Features       map[string]FeatureSupport  `json:"features,omitempty"`
	Annotations    map[string]json.RawMessage `json:"annotations,omitempty"`
}

// The tool-source kinds. A source says where its tools are executed from;
// an MCP source is a process or remote kind whose Protocol is ToolSourceMCP.
const (
	ToolSourceNative  = "native"
	ToolSourceLocal   = "local"
	ToolSourceProcess = "process"
	ToolSourceRemote  = "remote"
	ToolSourceHosted  = "hosted"
)

// ToolSourceMCP is the `protocol` value an MCP source declares.
const ToolSourceMCP = "mcp"

// ToolSourceDescriptor is the published shape of one tool source: what
// `action.tools.list.response`, `session.state`, and the capability descriptor
// report back to clients. It deliberately carries no `command`, `args`, or
// `environment` — those belong to ToolSourceAttachment, the open-time shape —
// because an attachment's environment can hold a literal credential and one
// schema serving both would make a leak into a published catalog valid.
type ToolSourceDescriptor struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DisplayName string `json:"display_name,omitempty"`
	Protocol    string `json:"protocol,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
}

// ToolsListRequest asks for a catalog. SessionID scopes the request to one
// session's effective catalog; an empty one asks for the endpoint-level
// catalog a static adapter serves. AllowDegradedFeatures is the consent
// carrier every other capability-electing request has, because
// `action.tools.list` can be advertised `degraded` like any key and a list is
// a request of its own.
type ToolsListRequest struct {
	SessionID             SessionID `json:"session_id,omitempty"`
	AllowDegradedFeatures []string  `json:"allow_degraded_features,omitempty"`
}

// AllowsDegraded reports whether the list request opted into the degraded
// application of one capability key.
func (r ToolsListRequest) AllowsDegraded(key string) bool {
	for _, allowed := range r.AllowDegradedFeatures {
		if allowed == key {
			return true
		}
	}
	return false
}

// ToolsListResponse is one catalog. It repeats the request's SessionID when
// the catalog is a session's effective catalog, so a trace can tie a catalog
// to the attachment it reflects.
type ToolsListResponse struct {
	SessionID SessionID              `json:"session_id,omitempty"`
	Sources   []ToolSourceDescriptor `json:"sources,omitempty"`
	Tools     []ToolDefinition       `json:"tools"`
}

type ActionCallPayload struct {
	InteractionID  InteractionID   `json:"interaction_id,omitempty"`
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
