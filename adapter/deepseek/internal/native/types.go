// Package native defines the pinned DeepSeek Harness SDK runtime wire vocabulary.
package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	ServerName    = "deepseek-harness-sdk-runtime"
	ServerVersion = "0.0.1"

	MethodInitialize       = "initialize"
	MethodSessionPrompt    = "session/prompt"
	MethodShutdown         = "shutdown"
	NotifySessionEvent     = "session.event"
	NotifySessionStatus    = "session.status"
	NotifySubagentStarted  = "subagent.started"
	NotifySubagentFinished = "subagent.finished"
)

var ErrInvalid = errors.New("deepseek native: invalid pinned message")

type InitializeParams struct {
	Cwd       string `json:"cwd"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	MaxTokens *int64 `json:"maxTokens,omitempty"`
}
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type InitializeResult struct {
	ServerInfo ServerInfo `json:"serverInfo"`
}

type SessionPromptParams struct {
	SessionID     string         `json:"sessionId"`
	ContentBlocks []ContentBlock `json:"contentBlocks"`
}
type SessionPromptResult struct {
	MessageID string `json:"messageId"`
}
type ShutdownResult struct{}

type ContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Attachment json.RawMessage `json:"attachment,omitempty"`
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name,omitempty"`
	Arguments  string          `json:"arguments,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Content    []ContentBlock  `json:"content,omitempty"`
	IsError    *bool           `json:"isError,omitempty"`
}

type SessionEventNotification struct {
	SessionID string `json:"sessionId"`
	Event     Event  `json:"event"`
}
type SessionStatusNotification struct {
	SessionID string `json:"sessionId"`
	Status    string `json:"status"`
}
type SubagentStartedNotification struct {
	ParentSessionID string `json:"parentSessionId"`
	ChildSessionID  string `json:"childSessionId"`
}
type SubagentFinishedNotification struct {
	Provider             string         `json:"provider"`
	AgentID              string         `json:"agentId"`
	ParentSessionID      string         `json:"parentSessionId"`
	ChildSessionID       string         `json:"childSessionId"`
	Status               string         `json:"status"`
	StopReason           string         `json:"stopReason"`
	LastAssistantMessage []ContentBlock `json:"lastAssistantMessage,omitempty"`
}

type Event struct {
	Type            string          `json:"type"`
	Seq             int64           `json:"seq"`
	Time            int64           `json:"time"`
	Data            json.RawMessage `json:"data"`
	Ignorable       *bool           `json:"ignorable,omitempty"`
	SourceEventSeqs []int64         `json:"sourceEventSeqs,omitempty"`
	SurfaceOp       json.RawMessage `json:"surfaceOp,omitempty"`
}
type TurnStart struct {
	Turn int64 `json:"turn"`
}
type TurnEnd struct {
	Turn   int64           `json:"turn"`
	Reason json.RawMessage `json:"reason"`
}
type StepBoundary struct {
	Turn int64 `json:"turn"`
	Step int64 `json:"step"`
}
type UserMessage struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
	Source  MessageSource  `json:"source"`
}
type MessageSource struct {
	Kind        string          `json:"kind"`
	Plugin      string          `json:"plugin,omitempty"`
	Provider    string          `json:"provider,omitempty"`
	Model       string          `json:"model,omitempty"`
	CallID      string          `json:"callId,omitempty"`
	Form        string          `json:"form,omitempty"`
	Summary     string          `json:"summary,omitempty"`
	Sections    json.RawMessage `json:"sections,omitempty"`
	ReplayState json.RawMessage `json:"replayState,omitempty"`
}
type InboxSpliced struct {
	Target       string        `json:"target"`
	Start        int64         `json:"start"`
	RemovedCount *int64        `json:"removedCount,omitempty"`
	Inserted     []UserMessage `json:"inserted"`
	Outcome      string        `json:"outcome,omitempty"`
}
type AssistantChunk struct {
	Turn  int64           `json:"turn"`
	Step  int64           `json:"step"`
	Chunk json.RawMessage `json:"chunk"`
}
type AssistantMessageEvent struct {
	Turn    int64            `json:"turn"`
	Step    int64            `json:"step"`
	Message AssistantMessage `json:"message"`
	Usage   *TokenUsage      `json:"usage,omitempty"`
}
type AssistantMessage struct {
	ID      string         `json:"id"`
	Role    string         `json:"role"`
	Content []ContentBlock `json:"content"`
	Source  MessageSource  `json:"source"`
}
type TokenUsage struct {
	InputTokens      int64  `json:"inputTokens"`
	OutputTokens     int64  `json:"outputTokens"`
	CacheReadTokens  *int64 `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens *int64 `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens  *int64 `json:"reasoningTokens,omitempty"`
}
type ToolCall struct {
	Turn      int64  `json:"turn"`
	Step      int64  `json:"step"`
	CallID    string `json:"callId"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ToolResult struct {
	Turn    int64           `json:"turn"`
	Step    int64           `json:"step"`
	Message UserMessage     `json:"message"`
	Error   *ToolError      `json:"error,omitempty"`
	Meta    json.RawMessage `json:"meta,omitempty"`
}
type ToolError struct {
	Name string `json:"name"`
	Code string `json:"code"`
}
type TodoWrite struct {
	Todos []Todo `json:"todos"`
}
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}
type RequestContext struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	ContextWindow *int64 `json:"contextWindow,omitempty"`
}
type RequestHeader struct {
	Header json.RawMessage `json:"header"`
	Reason string          `json:"reason"`
}

// DataAs strictly decodes the event payload into its pinned concrete type.
func (event Event) DataAs(dst any) error { return DecodeStrict(event.Data, dst) }

func DecodeNotification(method string, data []byte) (any, error) {
	var value any
	switch method {
	case NotifySessionEvent:
		value = &SessionEventNotification{}
	case NotifySessionStatus:
		value = &SessionStatusNotification{}
	case NotifySubagentStarted:
		value = &SubagentStartedNotification{}
	case NotifySubagentFinished:
		value = &SubagentFinishedNotification{}
	default:
		return nil, fmt.Errorf("%w: unknown notification %q", ErrInvalid, method)
	}
	if err := DecodeStrict(data, value); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := validateNotification(value); err != nil {
		return nil, err
	}
	return value, nil
}

func validateNotification(value any) error {
	require := func(ok bool, what string) error {
		if !ok {
			return fmt.Errorf("%w: %s", ErrInvalid, what)
		}
		return nil
	}
	switch v := value.(type) {
	case *SessionEventNotification:
		if err := require(v.SessionID != "", "session.event sessionId is required"); err != nil {
			return err
		}
		return v.Event.Validate()
	case *SessionStatusNotification:
		return require(v.SessionID != "" && (v.Status == "idle" || v.Status == "running"), "invalid session.status")
	case *SubagentStartedNotification:
		return require(v.ParentSessionID != "" && v.ChildSessionID != "", "invalid subagent.started")
	case *SubagentFinishedNotification:
		if err := require(v.Provider != "" && v.AgentID != "" && v.ParentSessionID != "" && v.ChildSessionID != "", "invalid subagent.finished identity"); err != nil {
			return err
		}
		if err := require(v.Status == "ok" || v.Status == "error", "invalid subagent status"); err != nil {
			return err
		}
		return require(validStopReason(v.StopReason), "invalid subagent stopReason")
	}
	return ErrInvalid
}

func (event Event) Validate() error {
	if event.Type == "" || event.Seq < 0 || event.Time < 0 || len(event.Data) == 0 || (event.Ignorable != nil && !*event.Ignorable) {
		return fmt.Errorf("%w: invalid event envelope", ErrInvalid)
	}
	surface := event.Type == "user/message" || event.Type == "assistant/message" || event.Type == "tool/result"
	if !surface && (event.SourceEventSeqs != nil || len(event.SurfaceOp) > 0) {
		return fmt.Errorf("%w: surface metadata on non-surface event", ErrInvalid)
	}
	for _, seq := range event.SourceEventSeqs {
		if seq < 0 {
			return fmt.Errorf("%w: invalid sourceEventSeqs", ErrInvalid)
		}
	}
	if len(event.SurfaceOp) > 0 && !validSurfaceOp(event.SurfaceOp) {
		return fmt.Errorf("%w: invalid surfaceOp", ErrInvalid)
	}
	var target any
	switch event.Type {
	case "turn/start":
		target = &TurnStart{}
	case "turn/end":
		target = &TurnEnd{}
	case "step/start", "step/end":
		target = &StepBoundary{}
	case "user/message":
		target = &UserMessage{}
	case "agent/inbox/spliced":
		target = &InboxSpliced{}
	case "assistant/chunk":
		target = &AssistantChunk{}
	case "assistant/message":
		target = &AssistantMessageEvent{}
	case "tool/call":
		target = &ToolCall{}
	case "tool/result":
		target = &ToolResult{}
	case "todo/write":
		target = &TodoWrite{}
	case "request/header":
		target = &RequestHeader{}
	case "request/context":
		target = &RequestContext{}
	case "session/end-seed":
		target = &struct{}{}
	default:
		if event.Ignorable != nil && *event.Ignorable {
			return nil
		}
		return fmt.Errorf("%w: unknown required event %q", ErrInvalid, event.Type)
	}
	if err := DecodeStrict(event.Data, target); err != nil {
		return fmt.Errorf("%w: %s data: %v", ErrInvalid, event.Type, err)
	}
	switch data := target.(type) {
	case *TurnStart:
		if data.Turn <= 0 {
			return fmt.Errorf("%w: invalid turn/start", ErrInvalid)
		}
	case *TurnEnd:
		if data.Turn <= 0 || !validTurnEndReason(data.Reason) {
			return fmt.Errorf("%w: invalid turn/end", ErrInvalid)
		}
	case *StepBoundary:
		if data.Turn <= 0 || data.Step <= 0 {
			return fmt.Errorf("%w: invalid step boundary", ErrInvalid)
		}
	case *AssistantChunk:
		if data.Turn <= 0 || data.Step <= 0 || !validStreamChunk(data.Chunk) {
			return fmt.Errorf("%w: invalid assistant/chunk", ErrInvalid)
		}
	case *UserMessage:
		if data.ID == "" || data.Role != "user" || !validSource(data.Source) {
			return fmt.Errorf("%w: invalid user/message", ErrInvalid)
		}
	case *InboxSpliced:
		if (data.Target != "next-turn" && data.Target != "next-step") || data.Start < 0 || (data.Outcome != "" && data.Outcome != "canceled") {
			return fmt.Errorf("%w: invalid agent/inbox/spliced", ErrInvalid)
		}
		for _, message := range data.Inserted {
			if message.ID == "" || message.Role != "user" || !validSource(message.Source) {
				return fmt.Errorf("%w: invalid inserted user message", ErrInvalid)
			}
		}
	case *AssistantMessageEvent:
		if data.Turn <= 0 || data.Step <= 0 || data.Message.ID == "" || data.Message.Role != "assistant" || data.Message.Source.Kind != "model" || !validSource(data.Message.Source) || !validBlocks(data.Message.Content) || (data.Usage != nil && !validUsage(*data.Usage)) {
			return fmt.Errorf("%w: invalid assistant/message", ErrInvalid)
		}
	case *ToolCall:
		if data.Turn <= 0 || data.Step <= 0 || data.CallID == "" || data.Name == "" || !json.Valid([]byte(data.Arguments)) {
			return fmt.Errorf("%w: invalid tool/call", ErrInvalid)
		}
	case *ToolResult:
		if data.Turn <= 0 || data.Step <= 0 || data.Message.ID == "" || data.Message.Role != "user" || data.Message.Source.Kind != "tool" || !validSource(data.Message.Source) || !validBlocks(data.Message.Content) || (data.Error != nil && (data.Error.Name == "" || data.Error.Code == "")) || (len(data.Meta) > 0 && !json.Valid(data.Meta)) {
			return fmt.Errorf("%w: invalid tool/result", ErrInvalid)
		}
	case *TodoWrite:
		if data.Todos == nil {
			return fmt.Errorf("%w: invalid todo/write", ErrInvalid)
		}
		for _, todo := range data.Todos {
			if todo.Content == "" || (todo.Status != "pending" && todo.Status != "in_progress" && todo.Status != "completed") {
				return fmt.Errorf("%w: invalid todo", ErrInvalid)
			}
		}
	case *RequestHeader:
		if len(data.Header) == 0 || (data.Reason != "initial" && data.Reason != "resume" && data.Reason != "change") {
			return fmt.Errorf("%w: invalid request/header", ErrInvalid)
		}
	case *RequestContext:
		if data.Provider == "" || data.Model == "" || (data.ContextWindow != nil && *data.ContextWindow <= 0) {
			return fmt.Errorf("%w: invalid request/context", ErrInvalid)
		}
	}
	return nil
}

func validBlocks(blocks []ContentBlock) bool {
	if blocks == nil {
		return false
	}
	for _, block := range blocks {
		switch block.Type {
		case "text", "reasoning":
		case "image":
			if len(block.Attachment) == 0 || !json.Valid(block.Attachment) {
				return false
			}
		case "tool-call":
			if block.ID == "" || block.Name == "" || !json.Valid([]byte(block.Arguments)) {
				return false
			}
		case "tool-result":
			if block.ToolCallID == "" || !validBlocks(block.Content) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validUsage(usage TokenUsage) bool {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return false
	}
	for _, value := range []*int64{usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens} {
		if value != nil && *value < 0 {
			return false
		}
	}
	return true
}

func validTurnEndReason(raw json.RawMessage) bool {
	var reason struct {
		Kind   string          `json:"kind"`
		Reason json.RawMessage `json:"reason,omitempty"`
		Error  json.RawMessage `json:"error,omitempty"`
	}
	if DecodeStrict(raw, &reason) != nil {
		return false
	}
	switch reason.Kind {
	case "completed", "blocked", "max-tokens", "interrupted":
		return len(reason.Reason) == 0 && len(reason.Error) == 0
	case "aborted":
		return len(reason.Reason) > 0 && json.Valid(reason.Reason)
	case "error":
		return len(reason.Error) > 0 && json.Valid(reason.Error)
	}
	return false
}

func validStreamChunk(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	if DecodeStrict(raw, &value) != nil {
		return false
	}
	var kind string
	return json.Unmarshal(value["type"], &kind) == nil && kind != ""
}

func validSurfaceOp(raw json.RawMessage) bool {
	var appendValue string
	if json.Unmarshal(raw, &appendValue) == nil {
		return appendValue == "append"
	}
	var replace struct {
		Op    string `json:"op"`
		Start int64  `json:"start"`
		End   int64  `json:"end"`
	}
	return DecodeStrict(raw, &replace) == nil && replace.Op == "replace" && replace.Start >= 0 && replace.End >= replace.Start
}

func validSource(v MessageSource) bool {
	switch v.Kind {
	case "user":
		return v.Plugin == "" && v.Provider == "" && v.Model == "" && v.CallID == ""
	case "plugin":
		return v.Plugin != ""
	case "model":
		return v.Provider != "" && v.Model != ""
	case "tool":
		return v.CallID != ""
	}
	return false
}
func validStopReason(v string) bool {
	switch v {
	case "completed", "aborted", "error", "max-tokens", "refusal":
		return true
	}
	return false
}

func ValidateInitializeParams(v InitializeParams) error {
	if v.Cwd == "" || v.Provider == "" || v.Model == "" {
		return fmt.Errorf("%w: initialize cwd, provider, and model are required", ErrInvalid)
	}
	if v.MaxTokens != nil && *v.MaxTokens <= 0 {
		return fmt.Errorf("%w: maxTokens must be positive", ErrInvalid)
	}
	return nil
}
func ValidateInitializeResult(v InitializeResult) error {
	if v.ServerInfo.Name != ServerName || v.ServerInfo.Version != ServerVersion {
		return fmt.Errorf("%w: unexpected serverInfo %q/%q", ErrInvalid, v.ServerInfo.Name, v.ServerInfo.Version)
	}
	return nil
}
func ValidatePrompt(v SessionPromptParams) error {
	if v.SessionID == "" || v.ContentBlocks == nil {
		return fmt.Errorf("%w: prompt sessionId and contentBlocks are required", ErrInvalid)
	}
	return nil
}

func DecodeStrict(data []byte, dst any) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
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
func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				kt, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := kt.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		}
		return errors.New("unexpected closing delimiter")
	}
	if err := walk(); err != nil {
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
