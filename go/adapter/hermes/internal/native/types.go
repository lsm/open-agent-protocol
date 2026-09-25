package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
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

	RequestApproval = "approval"
	RequestClarify  = "clarify"
	RequestSudo     = "sudo"
	RequestSecret   = "secret"
)

var ErrInvalid = errors.New("hermes native: invalid pinned message")

type Event struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Seq       int64           `json:"seq,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type ReadyPayload struct {
	Skin         json.RawMessage `json:"skin"`
	ChangeEvents bool            `json:"change_events"`
	ReplayEpoch  string          `json:"replay_epoch"`
}

type DeltaPayload struct {
	Text     string `json:"text"`
	Rendered string `json:"rendered,omitempty"`
	Verbose  bool   `json:"verbose,omitempty"`
}

type InterimPayload struct {
	Text            string `json:"text"`
	AlreadyStreamed bool   `json:"already_streamed"`
}

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
	PersistedTurn     json.RawMessage `json:"persisted_turn,omitempty"`
}

type ErrorSurface struct {
	Layer     string `json:"layer"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`

	Provider string   `json:"provider,omitempty"`
	Model    string   `json:"model,omitempty"`
	ResetsAt *float64 `json:"resets_at,omitempty"`
}

func (e *ErrorSurface) UnmarshalJSON(data []byte) error {
	type surfaceAlias ErrorSurface
	var value surfaceAlias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*e = ErrorSurface(value)
	return nil
}

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

	ActiveSubagents *int64 `json:"active_subagents,omitempty"`
}

func (u *Usage) UnmarshalJSON(data []byte) error {
	type usageAlias Usage
	var value usageAlias
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*u = Usage(value)
	return nil
}

type UsageTickPayload struct {
	Usage Usage `json:"usage"`
}

type ToolStartPayload struct {
	ToolID   string          `json:"tool_id"`
	Name     string          `json:"name"`
	Context  string          `json:"context"`
	Args     map[string]any  `json:"args,omitempty"`
	ArgsText string          `json:"args_text,omitempty"`
	Preview  string          `json:"preview,omitempty"`
	Labels   json.RawMessage `json:"labels,omitempty"`
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
	Todos      json.RawMessage `json:"todos,omitempty"`
	Revision   *int64          `json:"revision,omitempty"`
	Labels     json.RawMessage `json:"labels,omitempty"`
}

type ApprovalRequestParams struct {
	SessionID      string   `json:"session_id"`
	RequestID      string   `json:"request_id"`
	Command        string   `json:"command"`
	Description    string   `json:"description,omitempty"`
	Choices        []string `json:"choices"`
	AllowPermanent *bool    `json:"allow_permanent,omitempty"`
	AllowSession   *bool    `json:"allow_session,omitempty"`
	SmartDenied    *bool    `json:"smart_denied,omitempty"`
	ToolName       string   `json:"tool_name,omitempty"`
}

type ClarifyQuestion struct {
	Qid         string   `json:"qid"`
	Question    string   `json:"question"`
	Choices     []string `json:"choices"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}

type ClarifyRequestParams struct {
	SessionID   string            `json:"session_id"`
	Question    string            `json:"question,omitempty"`
	Choices     []string          `json:"choices,omitempty"`
	MultiSelect bool              `json:"multi_select,omitempty"`
	Questions   []ClarifyQuestion `json:"questions,omitempty"`
	Answers     map[string]string `json:"answers,omitempty"`
}

type SudoRequestParams struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command,omitempty"`
}

type SecretRequestParams struct {
	SessionID string          `json:"session_id"`
	EnvVar    string          `json:"env_var"`
	Prompt    string          `json:"prompt"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

type ApprovalResult struct {
	Choice string `json:"choice"`
}

type ClarifyAnswerResult struct {
	Answer string `json:"answer"`
}

type ClarifyAnswersResult struct {
	Answers map[string]string `json:"answers"`
}

type ValueResult struct {
	Value string `json:"value"`
}

type RequestCancelPayload struct {
	ID     string `json:"id"`
	Method string `json:"method"`
	Reason string `json:"reason"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}

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
	DelegationID    string          `json:"delegation_id,omitempty"`
}

type TaskCompletePayload struct {
	TaskID   string `json:"task_id"`
	Question string `json:"question,omitempty"`
	Text     string `json:"text"`
}

type SessionInfoPayload struct {
	Model           string   `json:"model"`
	Provider        string   `json:"provider"`
	Running         bool     `json:"running"`
	TurnStartedAt   *float64 `json:"turn_started_at"`
	Title           string   `json:"title"`
	StoredSessionID string   `json:"stored_session_id"`
}

type SessionCreateParams struct {
	Title string `json:"title,omitempty"`
	Cwd   string `json:"cwd,omitempty"`
	Model string `json:"model,omitempty"`
}

type SessionCreateResult struct {
	SessionID       string          `json:"session_id"`
	StoredSessionID string          `json:"stored_session_id"`
	MessageCount    int64           `json:"message_count"`
	Messages        json.RawMessage `json:"messages"`
	Info            json.RawMessage `json:"info"`
}

type PromptSubmitParams struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

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
	Events       []Event           `json:"events"`
	LatestSeq    int64             `json:"latest_seq"`
	Truncated    bool              `json:"truncated"`
	Count        int64             `json:"count"`
	Epoch        string            `json:"epoch"`
	OpenRequests []json.RawMessage `json:"open_requests"`
}

type SessionCloseResult struct {
	Closed bool `json:"closed"`
}

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
	EventRequestCancel      = "request.cancel"
	EventSubagentComplete   = "subagent.complete"
	EventBTWComplete        = "btw.complete"
	EventBackgroundComplete = "background.complete"
)

var observedEvents = map[string]bool{
	"agent.terminal.output": true, "billing.step_up.verification": true, "bot_relay.outbox.pending": true,
	"browser.controller.cancel": true, "browser.controller.command": true, "browser.progress": true,
	"connection.request": true, "connection.update": true, "cron.changed": true,
	"display.install.done": true, "display.install.log": true, "display.lease": true, "display.status": true,
	"layout.apply": true, "message.reaction": true, "moa.aggregating": true, "moa.phase": true,
	"moa.progress": true, "moa.reference": true, "notice": true, "notification.clear": true,
	"notification.show": true, "pairing.changed": true, "pane.reveal": true, "pet.changed": true,
	"pet.generate.progress": true, "pet.hatch.progress": true, "platforms.changed": true,
	"preview.close": true, "preview.open": true, "preview.restart.complete": true,
	"preview.restart.progress": true, "reaction": true, "reasoning.available": true,
	"review.summary": true, "session.control.update": true, "session.reclaimed": true,
	"session.resume_progress": true, "session.title": true, "sessions.changed": true,
	"setup.ready": true, "skin.changed": true, "status.update": true, "subagent.progress": true,
	"subagent.spawn_requested": true, "subagent.start": true, "subagent.thinking": true,
	"subagent.tool": true, "terminal.close": true, "tip.show": true, "todo.updated": true,
	"tool.generating": true, "tool.output_risk": true, "voice.interrupted": true,
	"voice.status": true, "voice.transcript": true, "wake.detected": true,
}

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

		var info SessionInfoPayload
		if err := json.Unmarshal(event.Payload, &info); err != nil {
			return fmt.Errorf("%w: invalid session.info payload: %v", ErrInvalid, err)
		}
		return nil
	}
	target, _ := payloadTarget(event.Type)
	if target == nil {
		return nil
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
	case EventRequestCancel:
		cancel, _ := target.(*RequestCancelPayload)
		if cancel.ID == "" || cancel.Method == "" {
			return fmt.Errorf("%w: request.cancel requires id and method", ErrInvalid)
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

var runScopedEvents = map[string]bool{
	EventMessageStart: true, EventMessageDelta: true, EventReasoningDelta: true, EventThinkingDelta: true,
	EventMessageComplete: true, EventToolStart: true, EventToolComplete: true,
}

func IsRunScoped(eventType string) bool { return runScopedEvents[eventType] }

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
		return nil, true
	case EventRequestCancel:
		return &RequestCancelPayload{}, true
	case EventSubagentComplete:
		return &SubagentPayload{}, true
	case EventBTWComplete, EventBackgroundComplete:
		return &TaskCompletePayload{}, true
	}
	return nil, false
}

func DecodeServerRequest(method string, params []byte) (any, error) {
	switch method {
	case RequestApproval:
		var request ApprovalRequestParams
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, fmt.Errorf("%w: invalid approval request: %v", ErrInvalid, err)
		}
		if request.SessionID == "" || request.RequestID == "" || (request.Command == "" && request.Description == "") || len(request.Choices) == 0 {
			return nil, fmt.Errorf("%w: approval request requires session_id, request_id, a command and choices", ErrInvalid)
		}
		for _, choice := range request.Choices {
			switch choice {
			case "once", "session", "always", "deny":
			default:
				return nil, fmt.Errorf("%w: approval choice %q", ErrInvalid, choice)
			}
		}
		return &request, nil
	case RequestClarify:
		var request ClarifyRequestParams
		if err := DecodeStrict(params, &request); err != nil {
			return nil, fmt.Errorf("%w: invalid clarify request: %v", ErrInvalid, err)
		}
		single := request.Question != "" || len(request.Choices) > 0
		batch := len(request.Questions) > 0
		if request.SessionID == "" || single == batch {
			return nil, fmt.Errorf("%w: clarify request needs a session and exactly one form", ErrInvalid)
		}
		for _, question := range request.Questions {
			if question.Qid == "" || question.Question == "" {
				return nil, fmt.Errorf("%w: invalid clarify batch question", ErrInvalid)
			}
		}
		return &request, nil
	case RequestSudo:
		var request SudoRequestParams
		if err := DecodeStrict(params, &request); err != nil || request.SessionID == "" {
			return nil, fmt.Errorf("%w: invalid sudo request", ErrInvalid)
		}
		return &request, nil
	case RequestSecret:
		var request SecretRequestParams
		if err := DecodeStrict(params, &request); err != nil || request.SessionID == "" || request.EnvVar == "" || request.Prompt == "" {
			return nil, fmt.Errorf("%w: invalid secret request", ErrInvalid)
		}
		return &request, nil
	}
	return nil, nil
}

func RequestSessionID(params []byte) string {
	var probe struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(params, &probe)
	return probe.SessionID
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
