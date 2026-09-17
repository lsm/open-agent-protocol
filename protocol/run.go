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

// SettledBy values say how an endpoint learned a run reached its terminal.
// SettledByObserved is a run-scoped native terminal the endpoint saw;
// SettledByInferred is one the endpoint concluded from other evidence, such as
// a session-scoped stop or transport loss, having never observed a terminal for
// the run itself. The member is optional on all three terminals and its
// omission asserts observation, so an endpoint that always observes its
// terminals never writes it. It is provenance about the endpoint's own
// knowledge, not a second status: an inferred terminal is as absorbing and as
// final as an observed one.
const (
	SettledByObserved = "observed"
	SettledByInferred = "inferred"
)

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

// IsToolSourceKind reports whether a value names one of them. The schema holds
// `kind` and every disclosed transport to this set, and the Go decoders hold
// the same line for a descriptor that never passed through the schema.
func IsToolSourceKind(kind string) bool {
	switch kind {
	case ToolSourceNative, ToolSourceLocal, ToolSourceProcess, ToolSourceRemote, ToolSourceHosted:
		return true
	}
	return false
}

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

// ActionCallPayload is every action.call.* event. RequestID names the
// action.call.resolve.request an event was derived from, and is carried by a
// control-owned call's action.call.started and its result-derived terminal.
// It exists because a client reads the resolve response on one stream and the
// event it authorized on another: two resolutions of one call can be
// outstanding at once — a result and its retry — so `tool_call_id` alone
// cannot say which request authorized the event being held, and the
// correlation has to be at the request.
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

// ResolveArmStarted is the acknowledgement arm's payload: an empty object.
// It is a struct rather than a bool so the wire shape stays extensible and so
// presence, not truth, is what the arm means.
type ResolveArmStarted struct{}

// The three arms of one action.call.resolve.request, and the names the
// validator and the adapters use for them.
const (
	ResolveArmAcknowledge = "started"
	ResolveArmResult      = "result"
	ResolveArmError       = "error"
)

// ResolveReason is why a control-owned call's resolution was refused. The five
// are ranked, most informative first, and the endpoint reports — and the
// validator requires — the highest reason the request satisfies, because one
// request can satisfy several at once and one response carries one reason.
//
// The order asks what the sender most needs to know. Whether the interaction
// exists comes first; then whether this sender may speak for it at all, since
// the state of a call it does not own is not its business; and only then how
// far the call has progressed, most advanced first.
type ResolveReason string

const (
	ReasonUnknownInteraction      ResolveReason = "unknown_interaction"
	ReasonWrongResponder          ResolveReason = "wrong_responder"
	ReasonAlreadyResolved         ResolveReason = "already_resolved"
	ReasonRepeatedAcknowledgement ResolveReason = "repeated_acknowledgement"
	ReasonLateAcknowledgement     ResolveReason = "late_acknowledgement"
)

// resolveReasonLadder is the ranking, highest first. Index 0 outranks index 1.
var resolveReasonLadder = []ResolveReason{
	ReasonUnknownInteraction,
	ReasonWrongResponder,
	ReasonAlreadyResolved,
	ReasonRepeatedAcknowledgement,
	ReasonLateAcknowledgement,
}

// ResolveReasonRank reports a reason's position on the ladder and whether it
// is on it at all. A lower rank outranks a higher one.
func ResolveReasonRank(reason ResolveReason) (int, bool) {
	for rank, known := range resolveReasonLadder {
		if known == reason {
			return rank, true
		}
	}
	return 0, false
}

// HighestResolveReason picks the reason a refusal must carry from the set of
// conditions a request satisfies.
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

// ActionCallResolveRequest is the control participant's answer to a call it
// owns, in exactly one of three arms: Started acknowledges that execution has
// begun, Result or Error resolves the call. An acknowledgement is not a
// resolution — it may appear at most once and only before one.
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

// Arm reports which of the three the request carries, or "" when it carries
// none or more than one — a shape the schema rejects, and one the validator
// must be able to name rather than guess at.
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

// ActionCallResolveDetails is the closed detail object an unaccepted
// resolution carries. SettlementID is the envelope id of the settlement an
// `already_resolved` refusal points at: required with that reason and absent
// otherwise.
//
// The member is declared here rather than left to a free-form object because
// over HTTP the refusal arrives on the POST body while the settlement travels
// the event stream, so a client can read the refusal first; carrying the id
// lets it wait for, or look up, the event that justifies the refusal instead
// of reading a valid rejection as an arbitrary one. A field the validator and
// the client recovery path both depend on cannot be an assumption.
type ActionCallResolveDetails struct {
	SettlementID EnvelopeID `json:"settlement_id,omitempty"`
}

// ActionCallResolveResponse answers one resolution. Reason is present only
// with Accepted false, and Details only alongside Reason.
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
