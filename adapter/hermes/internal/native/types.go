// Package native defines the pinned Hermes tui_gateway wire vocabulary at
// release v2026.8.31 (commit 29112bef099274229cadff79cdff7bf7b99c4b77).
package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ReleaseTag    = "v2026.8.31"
	ReleaseCommit = "29112bef099274229cadff79cdff7bf7b99c4b77"

	// The only notification method the pinned gateway emits.
	NotifyEvent = "event"

	MethodSessionCreate     = "session.create"
	MethodSessionClose      = "session.close"
	MethodSessionSteer      = "session.steer"
	MethodSessionInterrupt  = "session.interrupt"
	MethodSessionEventsSinc = "session.events.since"
	MethodSessionEventsStat = "session.events.stats"
	MethodPromptSubmit      = "prompt.submit"
	MethodPromptBTW         = "prompt.btw"
	MethodPromptBackground  = "prompt.background"
	MethodSubagentSteer     = "subagent.steer"
	MethodSubagentInterrupt = "subagent.interrupt"
	MethodApprovalRespond   = "approval.respond"
	MethodClarifyRespond    = "clarify.respond"
	MethodSudoRespond       = "sudo.respond"
	MethodSecretRespond     = "secret.respond"
)

var ErrInvalid = errors.New("hermes native: invalid pinned message")

// Event is one wire event frame: the params object of an "event"
// notification. Session-scoped frames carry a per-session monotonic seq;
// session-less frames (gateway.ready omits the member, _emit globals carry
// "") never do.
type Event struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// ReadyPayload is the gateway.ready payload emitted before any input is read.
type ReadyPayload struct {
	Skin         json.RawMessage `json:"skin"`
	ChangeEvents bool            `json:"change_events"`
	ReplayEpoch  string          `json:"replay_epoch"`
}

// DeltaPayload covers message.delta {text[, rendered]}, reasoning.delta
// {text[, verbose]}, and thinking.delta {text}.
type DeltaPayload struct {
	Text     string `json:"text"`
	Rendered string `json:"rendered,omitempty"`
	Verbose  bool   `json:"verbose,omitempty"`
}

type InterimPayload struct {
	Text            string `json:"text"`
	AlreadyStreamed bool   `json:"already_streamed"`
}

// MessageCompletePayload is the settlement frame. Status is interrupted |
// error | complete on the parent session stream; the child-mirror variant
// carries text only (empty status) and is never a parent settlement.
type MessageCompletePayload struct {
	Text              string          `json:"text"`
	Usage             Usage           `json:"usage"`
	Status            string          `json:"status"`
	Reasoning         string          `json:"reasoning,omitempty"`
	Warning           string          `json:"warning,omitempty"`
	ResponsePreviewed bool            `json:"response_previewed,omitempty"`
	Billing           json.RawMessage `json:"billing,omitempty"`
	FailureReason     string          `json:"failure_reason,omitempty"`
	Rendered          string          `json:"rendered,omitempty"`
	Error             string          `json:"error,omitempty"`
	Recoverable       bool            `json:"recoverable,omitempty"`
	ErrorSurface      *ErrorSurface   `json:"error_surface,omitempty"`
	Partial           bool            `json:"partial,omitempty"`
}

type ErrorSurface struct {
	Layer     string `json:"layer"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// Usage is the pinned usage dict; the context fields appear only when a
// context compressor reports occupancy.
type Usage struct {
	Model          string `json:"model"`
	Input          int64  `json:"input"`
	Output         int64  `json:"output"`
	Reasoning      int64  `json:"reasoning"`
	Prompt         int64  `json:"prompt"`
	Completion     int64  `json:"completion"`
	Total          int64  `json:"total"`
	Calls          int64  `json:"calls"`
	ContextUsed    *int64 `json:"context_used,omitempty"`
	ContextMax     *int64 `json:"context_max,omitempty"`
	ContextPercent *int64 `json:"context_percent,omitempty"`
	Compressions   *int64 `json:"compressions,omitempty"`
}

type UsageTickPayload struct {
	Usage Usage `json:"usage"`
}

type ToolStartPayload struct {
	ToolID   string         `json:"tool_id"`
	Name     string         `json:"name"`
	Context  string         `json:"context"`
	Args     map[string]any `json:"args,omitempty"`
	ArgsText string         `json:"args_text,omitempty"`
}

type ToolCompletePayload struct {
	ToolID     string          `json:"tool_id"`
	Name       string          `json:"name"`
	Args       map[string]any  `json:"args"`
	DurationS  *float64        `json:"duration_s,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	ResultText string          `json:"result_text,omitempty"`
	InlineDiff string          `json:"inline_diff,omitempty"`
}

// ApprovalRequestPayload is a passthrough of the producer's approval dict
// plus the gateway-synthesized choices. It deliberately carries no
// request_id: approvals resolve through the approval.respond registry.
type ApprovalRequestPayload struct {
	Command        string   `json:"command"`
	PatternKey     string   `json:"pattern_key,omitempty"`
	PatternKeys    []string `json:"pattern_keys,omitempty"`
	Description    string   `json:"description,omitempty"`
	AllowPermanent bool     `json:"allow_permanent,omitempty"`
	AllowSession   bool     `json:"allow_session,omitempty"`
	SmartDenied    bool     `json:"smart_denied,omitempty"`
	Choices        []string `json:"choices"`
}

// ClarifyQuestion is one element of the batch form.
type ClarifyQuestion struct {
	Qid         string   `json:"qid"`
	Question    string   `json:"question"`
	Choices     []string `json:"choices"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}

type ClarifyRequestPayload struct {
	RequestID   string            `json:"request_id"`
	Question    string            `json:"question,omitempty"`
	Choices     []string          `json:"choices,omitempty"`
	MultiSelect bool              `json:"multi_select,omitempty"`
	Questions   []ClarifyQuestion `json:"questions,omitempty"`
}

type SudoRequestPayload struct {
	RequestID string `json:"request_id"`
}

type SecretRequestPayload struct {
	RequestID string          `json:"request_id"`
	Prompt    string          `json:"prompt"`
	EnvVar    string          `json:"env_var"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

type ExpirePayload struct {
	RequestID string `json:"request_id"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}

// SubagentPayload is the parent-sid subagent frame family. Identity fields
// are producer-optional; the reducer keys on subagent_id/child_session_id
// when present.
type SubagentPayload struct {
	Goal            string          `json:"goal"`
	TaskCount       int64           `json:"task_count"`
	TaskIndex       int64           `json:"task_index"`
	SubagentID      string          `json:"subagent_id,omitempty"`
	ParentID        string          `json:"parent_id,omitempty"`
	ChildSessionID  string          `json:"child_session_id,omitempty"`
	Depth           *int64          `json:"depth,omitempty"`
	Model           string          `json:"model,omitempty"`
	ToolCount       *int64          `json:"tool_count,omitempty"`
	Toolsets        []string        `json:"toolsets,omitempty"`
	Status          string          `json:"status,omitempty"`
	Summary         string          `json:"summary,omitempty"`
	DurationSeconds *float64        `json:"duration_seconds,omitempty"`
	InputTokens     *int64          `json:"input_tokens,omitempty"`
	OutputTokens    *int64          `json:"output_tokens,omitempty"`
	ReasoningTokens *int64          `json:"reasoning_tokens,omitempty"`
	APICalls        *int64          `json:"api_calls,omitempty"`
	FilesRead       []string        `json:"files_read,omitempty"`
	FilesWritten    []string        `json:"files_written,omitempty"`
	OutputTail      json.RawMessage `json:"output_tail,omitempty"`
	ToolName        string          `json:"tool_name,omitempty"`
	ToolPreview     string          `json:"tool_preview,omitempty"`
	Text            string          `json:"text,omitempty"`
}

type TaskCompletePayload struct {
	TaskID   string `json:"task_id"`
	Question string `json:"question,omitempty"`
	Text     string `json:"text"`
}

// SessionInfoPayload is the reconciliation subset of session.info. The frame
// is observed-only: the pinned payload is large and best-effort in several
// members, so the envelope is decoded leniently and only the fields the
// reducer relies on are typed.
type SessionInfoPayload struct {
	Model           string   `json:"model"`
	Provider        string   `json:"provider"`
	Running         bool     `json:"running"`
	TurnStartedAt   *float64 `json:"turn_started_at"`
	Title           string   `json:"title"`
	StoredSessionID string   `json:"stored_session_id"`
}

// SessionCreateParams seeds one runtime session. Only the identity-relevant
// members are modeled; the native coerces everything else.
type SessionCreateParams struct {
	Title string `json:"title,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
	Model string `json:"model,omitempty"`
}

type SessionCreateResult struct {
	SessionID       string          `json:"session_id"`
	StoredSessionID string          `json:"stored_session_id"`
	MessageCount    int64           `json:"message_count"`
	Info            json.RawMessage `json:"info"`
}

// PromptSubmitParams is the conservative v1 surface: text on a session.
type PromptSubmitParams struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// Submit result statuses. The success path is always "streaming"; the busy
// trio reports the busy_input_mode outcome.
const (
	SubmitStreaming  = "streaming"
	SubmitSteered    = "steered"
	SubmitRedirected = "redirected"
	SubmitQueued     = "queued"
)

type PromptSubmitResult struct {
	Status             string         `json:"status"`
	SurvivorUserRowIDs []*int64       `json:"survivor_user_row_ids,omitempty"`
	SurvivorRowIDMap   map[string]any `json:"survivor_row_id_map,omitempty"`
	VoiceStopped       bool           `json:"voice_stopped,omitempty"`
	TurnIsolation      bool           `json:"turn_isolation,omitempty"`
}

type TaskRequestParams struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type TaskRequestResult struct {
	TaskID string `json:"task_id"`
}

type SteerParams struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type SteerResult struct {
	Status string `json:"status"`
	Text   string `json:"text,omitempty"`
}

type InterruptParams struct {
	SessionID string `json:"session_id"`
}

type InterruptResult struct {
	Status        string `json:"status"`
	Interrupted   bool   `json:"interrupted,omitempty"`
	TurnIsolation bool   `json:"turn_isolation,omitempty"`
}

type SubagentTargetParams struct {
	SubagentID string `json:"subagent_id"`
	SessionID  string `json:"session_id,omitempty"`
	Text       string `json:"text,omitempty"`
}

type SubagentInterruptResult struct {
	Found      bool   `json:"found"`
	SubagentID string `json:"subagent_id"`
}

type SubagentSteerResult struct {
	Status     string `json:"status"`
	SubagentID string `json:"subagent_id"`
	Text       string `json:"text,omitempty"`
}

type EventsSinceParams struct {
	SessionID string `json:"session_id"`
	LastSeen  int64  `json:"last_seen"`
}

type EventsSinceResult struct {
	Events    []Event `json:"events"`
	LatestSeq int64   `json:"latest_seq"`
	Truncated bool    `json:"truncated"`
	Count     int64   `json:"count"`
	Epoch     string  `json:"epoch"`
}

type SessionCloseResult struct {
	Closed bool `json:"closed"`
}

// ApprovalRespondParams resolves through the session's approval registry;
// choice defaults to deny server-side when omitted.
type ApprovalRespondParams struct {
	SessionID string `json:"session_id"`
	Choice    string `json:"choice"`
	All       bool   `json:"all,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

type ApprovalRespondResult struct {
	Resolved bool `json:"resolved"`
}

// RespondParams is the shared _block answer shape: request_id plus the
// per-kind value member (answer for clarify, password for sudo, value for
// secret) and the optional batch question selector.
type RespondParams struct {
	RequestID  string `json:"request_id"`
	QuestionID string `json:"question_id,omitempty"`
	Answer     string `json:"answer,omitempty"`
	Password   string `json:"password,omitempty"`
	Value      string `json:"value,omitempty"`
}

type RespondResult struct {
	Status    string   `json:"status"`
	Remaining []string `json:"remaining,omitempty"`
}

// Modeled event types: the envelope is validated and the payload strictly
// decoded into its pinned struct (session.info is the lenient exception).
const (
	EventGatewayReady       = "gateway.ready"
	EventMessageStart       = "message.start"
	EventMessageDelta       = "message.delta"
	EventReasoningDelta     = "reasoning.delta"
	EventThinkingDelta      = "thinking.delta"
	EventMessageInterim     = "message.interim"
	EventMessageComplete    = "message.complete"
	EventError              = "error"
	EventToolStart          = "tool.start"
	EventToolComplete       = "tool.complete"
	EventSessionUsage       = "session.usage"
	EventSessionInfo        = "session.info"
	EventApprovalRequest    = "approval.request"
	EventClarifyRequest     = "clarify.request"
	EventSudoRequest        = "sudo.request"
	EventSecretRequest      = "secret.request"
	EventSecretExpire       = "secret.expire"
	EventSudoExpire         = "sudo.expire"
	EventClarifyExpire      = "clarify.expire"
	EventSubagentComplete   = "subagent.complete"
	EventBTWComplete        = "btw.complete"
	EventBackgroundComplete = "background.complete"
)

// observedEvents is the closed set of pinned event types the adapter accepts
// without reducer-relevant projection. A type outside this set and the
// modeled set is a protocol violation: the pin is frozen, so an unknown type
// is drift, not extensibility.
var observedEvents = map[string]bool{
	"reasoning.available": true, "tool.generating": true, "tool.output_risk": true,
	"todo.updated": true, "session.title": true, "session.resume_progress": true,
	"status.update": true, "notification.show": true, "notification.clear": true,
	"notice": true, "review.summary": true, "reaction": true,
	"mcp.setup.request": true, "terminal.read.request": true,
	"preview.read.request": true, "preview.act.request": true,
	"window.read.request": true, "tour.request": true,
	"terminal.read.expire": true, "preview.read.expire": true,
	"preview.act.expire": true, "window.read.expire": true,
	"mcp.setup.expire": true, "tour.expire": true,
	"subagent.spawn_requested": true, "subagent.progress": true,
	"subagent.thinking": true, "subagent.tool": true,
	"skin.changed": true, "pet.changed": true, "cron.changed": true,
	"sessions.changed": true, "platforms.changed": true, "pairing.changed": true,
	"bot_relay.outbox.pending": true, "session.reclaimed": true,
	"agent.terminal.output": true, "terminal.close": true,
	"preview.restart.progress": true, "preview.restart.complete": true,
	"browser.progress": true, "voice.interrupted": true, "voice.transcript": true,
	"voice.status": true, "wake.detected": true, "preview.open": true,
	"preview.close": true, "pane.reveal": true, "layout.apply": true,
	"tip.show": true, "message.reaction": true, "pet.generate.progress": true,
	"pet.hatch.progress": true, "billing.step_up.verification": true,
}

// DecodeNotification types one inbound notification. The pinned gateway
// emits only method "event".
func DecodeNotification(method string, data []byte) (any, error) {
	if method != NotifyEvent {
		return nil, fmt.Errorf("%w: unknown notification method %q", ErrInvalid, method)
	}
	var event Event
	if err := DecodeStrict(data, &event); err != nil {
		return nil, fmt.Errorf("%w: invalid event envelope: %v", ErrInvalid, err)
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	return &event, nil
}

func (event Event) Validate() error {
	if event.Type == "" {
		return fmt.Errorf("%w: event type is required", ErrInvalid)
	}
	if event.SessionID != "" {
		if event.Seq < 1 {
			return fmt.Errorf("%w: session event %q requires a positive seq", ErrInvalid, event.Type)
		}
	} else if event.Seq != 0 {
		return fmt.Errorf("%w: session-less event %q must not carry seq", ErrInvalid, event.Type)
	}
	if !knownEvent(event.Type) {
		return fmt.Errorf("%w: unknown event type %q", ErrInvalid, event.Type)
	}
	if event.Type == EventMessageStart && len(event.Payload) > 0 {
		return fmt.Errorf("%w: message.start carries no payload", ErrInvalid)
	}
	if event.Type == EventSessionInfo {
		// Observed-only reconciliation frame: lenient members, typed subset.
		var info SessionInfoPayload
		if err := json.Unmarshal(event.Payload, &info); err != nil {
			return fmt.Errorf("%w: invalid session.info payload: %v", ErrInvalid, err)
		}
		return nil
	}
	target, _ := payloadTarget(event.Type)
	if target == nil {
		return nil // observed-only
	}
	if err := DecodeStrict(event.Payload, target); err != nil {
		return fmt.Errorf("%w: invalid %s payload: %v", ErrInvalid, event.Type, err)
	}
	switch event.Type {
	case EventGatewayReady:
		ready, _ := target.(*ReadyPayload)
		if !ready.ChangeEvents || len(ready.ReplayEpoch) != 32 || !hexOnly(ready.ReplayEpoch) || len(ready.Skin) == 0 {
			return fmt.Errorf("%w: invalid gateway.ready payload", ErrInvalid)
		}
		if event.SessionID != "" {
			return fmt.Errorf("%w: gateway.ready must be session-less", ErrInvalid)
		}
	case EventMessageComplete:
		complete, _ := target.(*MessageCompletePayload)
		switch complete.Status {
		case "interrupted", "error", "complete", "":
		default:
			return fmt.Errorf("%w: message.complete status %q", ErrInvalid, complete.Status)
		}
		if complete.Status == "error" && complete.Error == "" {
			return fmt.Errorf("%w: error settlement requires an error message", ErrInvalid)
		}
	case EventMessageDelta, EventReasoningDelta, EventThinkingDelta:
		delta, _ := target.(*DeltaPayload)
		if delta.Rendered != "" && event.Type != EventMessageDelta {
			return fmt.Errorf("%w: rendered is message.delta-only", ErrInvalid)
		}
		if delta.Verbose && event.Type != EventReasoningDelta {
			return fmt.Errorf("%w: verbose is reasoning.delta-only", ErrInvalid)
		}
	case EventApprovalRequest:
		approval, _ := target.(*ApprovalRequestPayload)
		if approval.Command == "" || len(approval.Choices) == 0 {
			return fmt.Errorf("%w: approval.request requires command and choices", ErrInvalid)
		}
		for _, choice := range approval.Choices {
			switch choice {
			case "once", "session", "always", "deny":
			default:
				return fmt.Errorf("%w: approval choice %q", ErrInvalid, choice)
			}
		}
	case EventClarifyRequest:
		clarify, _ := target.(*ClarifyRequestPayload)
		if clarify.RequestID == "" {
			return fmt.Errorf("%w: clarify.request requires request_id", ErrInvalid)
		}
		single := clarify.Question != "" || len(clarify.Choices) > 0
		batch := len(clarify.Questions) > 0
		if single == batch {
			return fmt.Errorf("%w: clarify.request needs exactly one form", ErrInvalid)
		}
		if batch {
			for _, question := range clarify.Questions {
				if question.Qid == "" || question.Question == "" || len(question.Choices) == 0 {
					return fmt.Errorf("%w: invalid clarify batch question", ErrInvalid)
				}
			}
		}
	case EventSudoRequest:
		if sudo, _ := target.(*SudoRequestPayload); sudo.RequestID == "" {
			return fmt.Errorf("%w: sudo.request requires request_id", ErrInvalid)
		}
	case EventSecretRequest:
		if secret, _ := target.(*SecretRequestPayload); secret.RequestID == "" || secret.Prompt == "" || secret.EnvVar == "" {
			return fmt.Errorf("%w: invalid secret.request", ErrInvalid)
		}
	case EventSecretExpire, EventSudoExpire, EventClarifyExpire:
		if expire, _ := target.(*ExpirePayload); expire.RequestID == "" {
			return fmt.Errorf("%w: expire requires request_id", ErrInvalid)
		}
	case EventError:
		if failure, _ := target.(*ErrorPayload); failure.Message == "" {
			return fmt.Errorf("%w: error event requires a message", ErrInvalid)
		}
	case EventBTWComplete, EventBackgroundComplete:
		task, _ := target.(*TaskCompletePayload)
		if task.TaskID == "" {
			return fmt.Errorf("%w: task completion requires task_id", ErrInvalid)
		}
	}
	return nil
}

func knownEvent(eventType string) bool {
	if _, ok := payloadTarget(eventType); ok {
		return true
	}
	return observedEvents[eventType]
}

// runScopedEvents are the types whose frames belong to one owned run: turn
// grammar, tools, and interaction gates. Every other known type — modeled
// corroboration (message.interim, session.usage, session.info, error,
// subagent/task completions) and the observed-only closure — may legally
// arrive on an idle session, because the pin guarantees post-settlement
// corroboration after message.complete.
var runScopedEvents = map[string]bool{
	EventMessageStart: true, EventMessageDelta: true, EventReasoningDelta: true, EventThinkingDelta: true,
	EventMessageComplete: true, EventToolStart: true, EventToolComplete: true,
	EventApprovalRequest: true, EventClarifyRequest: true, EventSudoRequest: true, EventSecretRequest: true,
	EventSecretExpire: true, EventSudoExpire: true, EventClarifyExpire: true,
}

// IsRunScoped reports whether an event type carries run semantics and is
// therefore illegal on an idle, run-less session.
func IsRunScoped(eventType string) bool { return runScopedEvents[eventType] }

// payloadTarget returns the strict decode target for a modeled type, nil for
// an observed-only type, and ok=false when the type is not in the pin.
func payloadTarget(eventType string) (any, bool) {
	switch eventType {
	case EventGatewayReady:
		return &ReadyPayload{}, true
	case EventMessageStart:
		return nil, true
	case EventMessageDelta, EventReasoningDelta, EventThinkingDelta:
		return &DeltaPayload{}, true
	case EventMessageInterim:
		return &InterimPayload{}, true
	case EventMessageComplete:
		return &MessageCompletePayload{}, true
	case EventError:
		return &ErrorPayload{}, true
	case EventToolStart:
		return &ToolStartPayload{}, true
	case EventToolComplete:
		return &ToolCompletePayload{}, true
	case EventSessionUsage:
		return &UsageTickPayload{}, true
	case EventSessionInfo:
		return nil, true // decoded leniently in Validate
	case EventApprovalRequest:
		return &ApprovalRequestPayload{}, true
	case EventClarifyRequest:
		return &ClarifyRequestPayload{}, true
	case EventSudoRequest:
		return &SudoRequestPayload{}, true
	case EventSecretRequest:
		return &SecretRequestPayload{}, true
	case EventSecretExpire, EventSudoExpire, EventClarifyExpire:
		return &ExpirePayload{}, true
	case EventSubagentComplete:
		return &SubagentPayload{}, true
	case EventBTWComplete, EventBackgroundComplete:
		return &TaskCompletePayload{}, true
	}
	return nil, false
}

func hexOnly(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			return false
		}
	}
	return true
}

// ValidateReady pins the handshake payload identity: a uuid4-hex epoch and
// the change-events contract flag.
func ValidateReady(payload *ReadyPayload) error {
	if len(payload.ReplayEpoch) != 32 || !hexOnly(payload.ReplayEpoch) {
		return fmt.Errorf("%w: replay_epoch must be a uuid4 hex", ErrInvalid)
	}
	if !payload.ChangeEvents {
		return fmt.Errorf("%w: gateway.ready must announce change_events", ErrInvalid)
	}
	return nil
}

func DecodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
