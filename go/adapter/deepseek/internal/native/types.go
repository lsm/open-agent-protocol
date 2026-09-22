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

var pinnedObservedOnlyEvents = map[string]bool{
	"agent-preset/selected":                  true,
	"approval/asked":                         true,
	"approval/decided":                       true,
	"approval/policy":                        true,
	"command/done":                           true,
	"command/run":                            true,
	"compaction/end":                         true,
	"compaction/prune":                       true,
	"compaction/start":                       true,
	"compaction/summary":                     true,
	"deliverables/presented":                 true,
	"feedback/message-delete":                true,
	"feedback/message-put":                   true,
	"feedback/record":                        true,
	"goal/change":                            true,
	"hook/invoked":                           true,
	"hook/result":                            true,
	"llm/retry":                              true,
	"llm/retry-started":                      true,
	"model/selection":                        true,
	"permission/preset":                      true,
	"plan/mode":                              true,
	"sandbox/mode":                           true,
	"schedule/change":                        true,
	"session-log-deepseek/delivery-accepted": true,
	"session/title":                          true,
	"session/title-llm-request":              true,
	"subagent/catalog":                       true,
	"subagent/descriptor":                    true,
	"subagent/model-selection-policy":        true,
	"system/message":                         true,
	"team/member":                            true,
	"team/message/delivered":                 true,
	"team/message/queued":                    true,
	"team/task":                              true,
	"tool-workflow/agent-end":                true,
	"tool-workflow/agent-start":              true,
	"tool-workflow/run-end":                  true,
	"tool-workflow/run-start":                true,
	"tool/ptc-dispatch":                      true,
	"tool/ptc-dispatch-start":                true,
	"web/deepseek-search-llm-request":        true,
	"workspace/changes":                      true,
}

func ObservedOnly(eventType string) bool { return pinnedObservedOnlyEvents[eventType] }

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
	Offloaded  json.RawMessage `json:"offloaded,omitempty"`
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

type AssistantAttempt struct {
	Turn   int64                   `json:"turn"`
	Step   int64                   `json:"step"`
	Stream []AssistantStreamRecord `json:"stream"`
}

type AssistantStreamRecord struct {
	Type  string
	Index int64
	Texts []string
	ID    string
	Name  string
	Args  []string
	Chunk json.RawMessage
}

func (r *AssistantStreamRecord) UnmarshalJSON(data []byte) error {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	switch head.Type {
	case "text-chunks", "reasoning-chunks":
		var value struct {
			Type  string   `json:"type"`
			Time0 int64    `json:"time0"`
			Index int64    `json:"index"`
			DT    []int64  `json:"dt"`
			Texts []string `json:"texts"`
		}
		if err := DecodeStrict(data, &value); err != nil {
			return err
		}

		if value.Time0 < 0 || value.Index < 0 || len(value.Texts) == 0 || len(value.DT) != len(value.Texts)-1 {
			return fmt.Errorf("%w: invalid %s record", ErrInvalid, head.Type)
		}
		r.Type, r.Index, r.Texts = head.Type, value.Index, value.Texts
		return nil
	case "tool-call-chunks":
		var value struct {
			Type  string   `json:"type"`
			Time0 int64    `json:"time0"`
			Index int64    `json:"index"`
			DT    []int64  `json:"dt"`
			ID    string   `json:"id"`
			Name  string   `json:"name,omitempty"`
			Args  []string `json:"args"`
		}
		if err := DecodeStrict(data, &value); err != nil {
			return err
		}
		if value.Time0 < 0 || value.Index < 0 || value.ID == "" || len(value.Args) == 0 || len(value.DT) != len(value.Args)-1 {
			return fmt.Errorf("%w: invalid tool-call-chunks record", ErrInvalid)
		}
		r.Type, r.Index, r.ID, r.Name, r.Args = head.Type, value.Index, value.ID, value.Name, value.Args
		return nil
	case "chunk":
		var value struct {
			Type  string          `json:"type"`
			Time  int64           `json:"time"`
			Chunk json.RawMessage `json:"chunk"`
		}
		if err := DecodeStrict(data, &value); err != nil {
			return err
		}
		if value.Time < 0 || !validStreamChunk(value.Chunk) {
			return fmt.Errorf("%w: invalid chunk record", ErrInvalid)
		}
		r.Type, r.Chunk = head.Type, value.Chunk
		return nil
	default:
		return fmt.Errorf("%w: unknown assistant stream record %q", ErrInvalid, head.Type)
	}
}

type AssistantMessageEvent struct {
	Turn    int64                   `json:"turn"`
	Step    int64                   `json:"step"`
	Message AssistantMessage        `json:"message"`
	Stream  []AssistantStreamRecord `json:"stream"`
	Usage   *TokenUsage             `json:"usage,omitempty"`
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
	TotalTokens      *int64 `json:"totalTokens,omitempty"`
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
	Name   string          `json:"name"`
	Code   string          `json:"code"`
	Reason json.RawMessage `json:"reason,omitempty"`
}

func (e *ToolError) ReasonText() (string, bool) {
	trimmed := bytes.TrimSpace(e.Reason)
	if len(trimmed) == 0 {
		return "", true
	}
	if trimmed[0] != '"' {
		return "", false
	}
	var text string
	if json.Unmarshal(trimmed, &text) != nil {
		return "", false
	}
	return text, true
}

func validToolError(e *ToolError) bool {
	if e == nil {
		return true
	}
	if e.Name == "" || e.Code == "" {
		return false
	}
	_, ok := e.ReasonText()
	return ok
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
	Header EpochHeader `json:"header"`
	Reason string      `json:"reason"`
}

type EpochHeader struct {
	Config          json.RawMessage `json:"config"`
	AdapterDefaults json.RawMessage `json:"adapterDefaults,omitempty"`
	System          *string         `json:"system,omitempty"`
	Tools           json.RawMessage `json:"tools,omitempty"`
}

func (header EpochHeader) valid() bool {
	if len(header.Config) == 0 {
		return false
	}
	var config map[string]json.RawMessage
	if DecodeStrict(header.Config, &config) != nil {
		return false
	}
	provider, providerErr := rawString(config["provider"])
	model, modelErr := rawString(config["model"])
	if providerErr != nil || modelErr != nil || provider == "" || model == "" {
		return false
	}
	if len(header.Tools) > 0 {
		var tools []json.RawMessage
		if DecodeStrict(header.Tools, &tools) != nil {
			return false
		}
		for _, tool := range tools {
			var fields map[string]json.RawMessage

			if DecodeStrict(tool, &fields) != nil || !blockHasKeys(fields, "name", "description", "parameters") {
				return false
			}
		}
	}
	return true
}

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
	if err := validateNotification(value, data); err != nil {
		return nil, err
	}
	return value, nil
}

func validateNotification(value any, data []byte) error {
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
		if err := require(validStopReason(v.StopReason), "invalid subagent stopReason"); err != nil {
			return err
		}
		var object map[string]json.RawMessage
		if err := DecodeStrict(data, &object); err != nil {
			return fmt.Errorf("%w: invalid subagent.finished: %v", ErrInvalid, err)
		}
		if raw, ok := object["lastAssistantMessage"]; ok && !validBlocksRaw(raw) {
			return fmt.Errorf("%w: invalid subagent.finished lastAssistantMessage", ErrInvalid)
		}
		return nil
	}
	return ErrInvalid
}

func (event Event) Validate() error {
	if event.Type == "" || event.Seq < 0 || event.Time < 0 || len(event.Data) == 0 || (event.Ignorable != nil && !*event.Ignorable) {
		return fmt.Errorf("%w: invalid event envelope", ErrInvalid)
	}

	if pinnedObservedOnlyEvents[event.Type] {
		return nil
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
	case "assistant/attempt":
		target = &AssistantAttempt{}
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

		if pinnedObservedOnlyEvents[event.Type] {
			return nil
		}
		if event.Ignorable != nil && *event.Ignorable {
			return nil
		}
		return fmt.Errorf("%w: unknown required event %q", ErrInvalid, event.Type)
	}
	if err := DecodeStrict(event.Data, target); err != nil {
		return fmt.Errorf("%w: %s data: %v", ErrInvalid, event.Type, err)
	}
	object := func() map[string]json.RawMessage {
		var fields map[string]json.RawMessage
		if DecodeStrict(event.Data, &fields) != nil {
			return nil
		}
		return fields
	}
	blocksOf := func(raw json.RawMessage) bool { return validBlocksRaw(raw) }
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
	case *AssistantAttempt:
		if data.Turn <= 0 || data.Step <= 0 {
			return fmt.Errorf("%w: invalid assistant/attempt", ErrInvalid)
		}
	case *UserMessage:
		fields := object()
		if data.ID == "" || data.Role != "user" || !validSource(data.Source) || fields == nil || !blocksOf(fields["content"]) {
			return fmt.Errorf("%w: invalid user/message", ErrInvalid)
		}
	case *InboxSpliced:
		fields := object()
		if (data.Target != "next-turn" && data.Target != "next-step") || data.Start < 0 || (data.Outcome != "" && data.Outcome != "canceled") || fields == nil {
			return fmt.Errorf("%w: invalid agent/inbox/spliced", ErrInvalid)
		}
		for _, message := range data.Inserted {
			if message.ID == "" || message.Role != "user" || !validSource(message.Source) {
				return fmt.Errorf("%w: invalid inserted user message", ErrInvalid)
			}
		}
		var inserted []json.RawMessage
		if DecodeStrict(fields["inserted"], &inserted) != nil {
			return fmt.Errorf("%w: invalid agent/inbox/spliced inserted", ErrInvalid)
		}
		for _, element := range inserted {
			var messageFields map[string]json.RawMessage
			if DecodeStrict(element, &messageFields) != nil || !blocksOf(messageFields["content"]) {
				return fmt.Errorf("%w: invalid inserted user message content", ErrInvalid)
			}
		}
	case *AssistantMessageEvent:
		fields := object()
		validContent := false
		if fields != nil {
			var messageFields map[string]json.RawMessage
			if DecodeStrict(fields["message"], &messageFields) == nil {
				validContent = blocksOf(messageFields["content"])
			}
		}

		_, hasStream := fields["stream"]
		if data.Turn <= 0 || data.Step <= 0 || !hasStream || data.Message.ID == "" || data.Message.Role != "assistant" || data.Message.Source.Kind != "model" || !validSource(data.Message.Source) || !validContent || (data.Usage != nil && !validUsage(*data.Usage)) {
			return fmt.Errorf("%w: invalid assistant/message", ErrInvalid)
		}
	case *ToolCall:
		if data.Turn <= 0 || data.Step <= 0 || data.CallID == "" || data.Name == "" || !carriesIntoATrace(data.Arguments) {
			return fmt.Errorf("%w: invalid tool/call", ErrInvalid)
		}
	case *ToolResult:
		fields := object()
		validContent := false
		if fields != nil {
			var messageFields map[string]json.RawMessage
			if DecodeStrict(fields["message"], &messageFields) == nil {
				validContent = blocksOf(messageFields["content"])
			}
		}

		singleMatchingBlock := len(data.Message.Content) == 1 && data.Message.Content[0].Type == "tool-result" && data.Message.Content[0].ToolCallID == data.Message.Source.CallID
		if data.Turn <= 0 || data.Step <= 0 || data.Message.ID == "" || data.Message.Role != "user" || data.Message.Source.Kind != "tool" || !validSource(data.Message.Source) || !validContent || !singleMatchingBlock || !validToolError(data.Error) || (len(data.Meta) > 0 && !json.Valid(data.Meta)) {
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
		if !data.Header.valid() || (data.Reason != "initial" && data.Reason != "resume" && data.Reason != "change") {
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
		if !validBlock(block) {
			return false
		}
		if block.Type == "tool-result" && !validBlocks(block.Content) {
			return false
		}
	}
	return true
}

func validBlocksRaw(raw json.RawMessage) bool {
	var elements []json.RawMessage
	if len(raw) == 0 || DecodeStrict(raw, &elements) != nil || elements == nil {
		return false
	}
	for _, element := range elements {
		if !validBlockRaw(element) {
			return false
		}
	}
	return true
}

func validBlockRaw(element json.RawMessage) bool {
	var object map[string]json.RawMessage
	if DecodeStrict(element, &object) != nil {
		return false
	}
	kind, err := rawString(object["type"])
	if err != nil {
		return false
	}
	var block ContentBlock
	if DecodeStrict(element, &block) != nil || !validBlock(block) {
		return false
	}
	switch kind {
	case "text", "reasoning":
		return blockHasKeys(object, "text")
	case "image":
		return blockHasKeys(object, "attachment") && validAttachment(object["attachment"])
	case "tool-call":
		return blockHasKeys(object, "id", "name", "arguments")
	case "tool-result":
		return blockHasKeys(object, "toolCallId", "content") && validBlocksRaw(object["content"])
	default:
		return false
	}
}

func offloadedAbsent(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0
}

func offloadedRidesImage(raw json.RawMessage) bool {
	return offloadedAbsent(raw) || bytes.Equal(bytes.TrimSpace(raw), []byte("true"))
}

func validBlock(block ContentBlock) bool {
	switch block.Type {
	case "text", "reasoning":
		return block.Attachment == nil && block.ID == "" && block.Name == "" && block.Arguments == "" && block.ToolCallID == "" && block.Content == nil && block.IsError == nil && offloadedAbsent(block.Offloaded)
	case "image":
		return block.Text == "" && block.ID == "" && block.Name == "" && block.Arguments == "" && block.ToolCallID == "" && block.Content == nil && block.IsError == nil && offloadedRidesImage(block.Offloaded)
	case "tool-call":
		return block.Text == "" && block.Attachment == nil && block.ToolCallID == "" && block.Content == nil && block.IsError == nil && offloadedAbsent(block.Offloaded) && block.ID != "" && block.Name != "" && carriesIntoATrace(block.Arguments)
	case "tool-result":
		return block.Text == "" && block.Attachment == nil && block.ID == "" && block.Name == "" && block.Arguments == "" && block.ToolCallID != "" && offloadedAbsent(block.Offloaded) && validBlocks(block.Content)
	default:
		return false
	}
}

func validAttachment(raw json.RawMessage) bool {
	var attachment struct {
		AttachmentID string `json:"attachmentId"`
		MediaType    string `json:"mediaType"`
	}
	return DecodeStrict(raw, &attachment) == nil && attachment.AttachmentID != "" && attachment.MediaType != ""
}

func blockHasKeys(object map[string]json.RawMessage, keys ...string) bool {
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func rawString(raw json.RawMessage) (string, error) {
	var value string
	if len(raw) == 0 {
		return "", errors.New("missing value")
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	return value, nil
}

func validUsage(usage TokenUsage) bool {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 {
		return false
	}
	for _, value := range []*int64{usage.TotalTokens, usage.CacheReadTokens, usage.CacheWriteTokens, usage.ReasoningTokens} {
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
		return validCancelCause(reason.Reason)
	case "error":
		return validLlmFailure(reason.Error)
	}
	return false
}

func validCancelCause(raw json.RawMessage) bool {
	var cause struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason,omitempty"`
	}
	if DecodeStrict(raw, &cause) != nil {
		return false
	}
	switch cause.Kind {
	case "user", "parent", "disposed", "legacy":
		return cause.Reason == ""
	case "hook":
		return cause.Reason != ""
	}
	return false
}

func validLlmFailure(raw json.RawMessage) bool {
	var failure struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	return DecodeStrict(raw, &failure) == nil && failure.Message != "" && failure.Code != ""
}

func validStreamChunk(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if DecodeStrict(raw, &object) != nil {
		return false
	}
	kind, err := rawString(object["type"])
	if err != nil || kind == "" {
		return false
	}
	nonNegativeIndex := func() bool {
		var index int64
		return json.Unmarshal(object["index"], &index) == nil && index >= 0
	}
	switch kind {
	case "block-start":
		return blockHasKeys(object, "index", "blockType") && nonNegativeIndex() && func() bool {
			value, err := rawString(object["blockType"])
			return err == nil && value != ""
		}()
	case "text-delta", "reasoning-delta":
		return blockHasKeys(object, "index", "text") && nonNegativeIndex()
	case "tool-call-delta":
		return blockHasKeys(object, "index", "id", "argumentsDelta") && nonNegativeIndex() && func() bool {
			value, err := rawString(object["id"])
			return err == nil && value != ""
		}()
	case "block-end":
		if !blockHasKeys(object, "index", "block") || !nonNegativeIndex() {
			return false
		}
		return validBlockRaw(object["block"])
	case "usage":
		if !blockHasKeys(object, "usage") {
			return false
		}
		var value struct {
			Type  string     `json:"type"`
			Usage TokenUsage `json:"usage"`
		}
		return DecodeStrict(raw, &value) == nil && validUsage(value.Usage)
	case "finish":
		return blockHasKeys(object, "reason") && validFinishReason(object["reason"])
	default:
		return false
	}
}

func validFinishReason(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if DecodeStrict(raw, &object) != nil {
		return false
	}
	kind, err := rawString(object["kind"])
	if err != nil {
		return false
	}
	switch kind {
	case "stop", "tool-calls", "max-tokens":
		return len(object) == 1
	case "aborted", "error":
		return len(object) == 2 && blockHasKeys(object, "failure") && validLlmFailure(object["failure"])
	default:
		return false
	}
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
		return v.Plugin == "" && v.Provider == "" && v.Model == "" && v.CallID == "" && v.Form == "" && v.Summary == "" && v.Sections == nil && v.ReplayState == nil
	case "plugin":

		return v.Plugin != "" && v.Provider == "" && v.Model == "" && v.CallID == "" && v.ReplayState == nil
	case "model":
		return v.Provider != "" && v.Model != "" && v.Plugin == "" && v.CallID == "" && v.Form == "" && v.Summary == "" && v.Sections == nil
	case "tool":
		return v.CallID != "" && v.Plugin == "" && v.Provider == "" && v.Model == "" && v.Form == "" && v.Summary == "" && v.Sections == nil && v.ReplayState == nil
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

const maxSafeInteger = int64(9007199254740991)

func ValidateInitializeParams(v InitializeParams) error {
	if v.Cwd == "" || v.Provider == "" || v.Model == "" {
		return fmt.Errorf("%w: initialize cwd, provider, and model are required", ErrInvalid)
	}
	if v.MaxTokens != nil && (*v.MaxTokens <= 0 || *v.MaxTokens > maxSafeInteger) {
		return fmt.Errorf("%w: maxTokens must be a positive safe integer", ErrInvalid)
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
	if !validBlocks(v.ContentBlocks) {
		return fmt.Errorf("%w: prompt contentBlocks are invalid", ErrInvalid)
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
func carriesIntoATrace(raw string) bool {
	return rejectDuplicateKeys([]byte(raw)) == nil
}
func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
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
