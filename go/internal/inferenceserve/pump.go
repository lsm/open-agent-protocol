package inferenceserve

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

const (
	CodeCredentialRejected = "credential_rejected"
	CodeRateLimited        = "rate_limited"
	CodeEndpointError      = "endpoint_error"
	CodeAborted            = "aborted"

	ReasonCancelled = "request cancelled"
)

var (
	ErrTerminalEmitted         = errors.New("inferenceserve: the inference already settled")
	ErrPartAlreadyOpen         = errors.New("inferenceserve: a part is already open")
	ErrNoOpenPart              = errors.New("inferenceserve: no part is open")
	ErrPartIndexMismatch       = errors.New("inferenceserve: the event names a part that is not the open one")
	ErrToolCallIdentityNeeded  = errors.New("inferenceserve: a tool_call part needs an id and a name")
	ErrToolCallIdentityRefused = errors.New("inferenceserve: only a tool_call part carries an id and a name")
	ErrToolCallNeedsCall       = errors.New("inferenceserve: a tool_call part ends with its complete call")
	ErrPartKindMismatch        = errors.New("inferenceserve: the event ends a part of another kind")
)

type Usage struct {
	InputTokens           int `json:"input_tokens"`
	CacheReadInputTokens  int `json:"cache_read_input_tokens,omitempty"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens,omitempty"`
	OutputTokens          int `json:"output_tokens"`
	TotalTokens           int `json:"total_tokens"`
}

type partDelta struct {
	PartIndex int    `json:"part_index"`
	Delta     string `json:"delta"`
}

type openPart struct {
	index int
	kind  string
}

func (s *State) Pump(event provider.Event, nowMillis int64) ([]Envelope, error) {
	if s.terminal && event.Kind != provider.EventKeepalive {
		return nil, ErrTerminalEmitted
	}
	switch event.Kind {
	case provider.EventKeepalive:
		return nil, nil
	case provider.EventStart:
		return one(s.Started(nowMillis))
	case provider.EventTextStart:
		return one(s.partStarted(event.ContentIndex, "text", event.ID, event.Name))
	case provider.EventThinkingStart:
		return one(s.partStarted(event.ContentIndex, "reasoning", event.ID, event.Name))
	case provider.EventToolCallStart:
		return one(s.partStarted(event.ContentIndex, "tool_call", event.ID, event.Name))
	case provider.EventTextDelta, provider.EventThinkingDelta, provider.EventToolCallDelta:
		return one(s.partDelta(event.ContentIndex, event.Delta))
	case provider.EventTextEnd:
		return one(s.partEndedText(event.ContentIndex, "text", event.Delta, ""))
	case provider.EventThinkingEnd:
		return one(s.partEndedText(event.ContentIndex, "reasoning", event.Delta, thinkingSignature(event.Partial, event.ContentIndex)))
	case provider.EventToolCallEnd:
		return one(s.partEndedToolCall(event.ContentIndex, event.ToolCall))
	case provider.EventDone:
		return one(s.settleDone(event.Message))
	case provider.EventError:
		return one(s.settleError(event.Reason))
	}
	return nil, nil
}

func one(envelope Envelope, err error) ([]Envelope, error) {
	if err != nil {
		return nil, err
	}
	return []Envelope{envelope}, nil
}

func (s *State) partStarted(index int, kind, toolCallID, name string) (Envelope, error) {
	if s.open != nil {
		return Envelope{}, ErrPartAlreadyOpen
	}
	if kind == "tool_call" && (toolCallID == "" || name == "") {
		return Envelope{}, ErrToolCallIdentityNeeded
	}
	if kind != "tool_call" && (toolCallID != "" || name != "") {
		return Envelope{}, ErrToolCallIdentityRefused
	}
	s.open = &openPart{index: index, kind: kind}
	return s.emit("inference.part.started", "", PartStarted{PartIndex: index, PartKind: kind, ToolCallID: toolCallID, Name: name})
}

func (s *State) checkOpen(index int) error {
	if s.open == nil {
		return ErrNoOpenPart
	}
	if s.open.index != index {
		return ErrPartIndexMismatch
	}
	return nil
}

func (s *State) partDelta(index int, delta string) (Envelope, error) {
	if err := s.checkOpen(index); err != nil {
		return Envelope{}, err
	}
	return s.emit("inference.part.delta", "", partDelta{PartIndex: index, Delta: delta})
}

func (s *State) partEndedText(index int, kind, text, carry string) (Envelope, error) {
	if err := s.checkOpen(index); err != nil {
		return Envelope{}, err
	}
	if s.open.kind == "tool_call" {
		return Envelope{}, ErrToolCallNeedsCall
	}
	if s.open.kind != kind {
		return Envelope{}, ErrPartKindMismatch
	}
	s.open = nil
	held := text
	envelope, err := s.emit("inference.part.ended", "", PartEnded{PartIndex: index, PartKind: kind, Text: &held, Carry: carry})
	if err == nil {
		block := TerminalBlock{Type: kind, Text: &held}
		if kind == "reasoning" {
			block = TerminalBlock{Type: kind, Reasoning: &held, Carry: carry}
		}
		s.ended = append(s.ended, block)
	}
	return envelope, err
}

func (s *State) partEndedToolCall(index int, call *provider.ToolCall) (Envelope, error) {
	if err := s.checkOpen(index); err != nil {
		return Envelope{}, err
	}
	if s.open.kind != "tool_call" {
		return Envelope{}, ErrPartKindMismatch
	}
	if call == nil || call.ID == "" || call.Name == "" {
		return Envelope{}, ErrToolCallNeedsCall
	}
	arguments := call.Arguments
	if arguments == "" {
		arguments = "{}"
	}
	if !json.Valid([]byte(arguments)) {
		return s.Failed(CodeProtocolViolation, "the provider streamed a tool call whose arguments_json is not json")
	}
	s.open = nil
	envelope, err := s.emit("inference.part.ended", "", PartEnded{
		PartIndex: index,
		PartKind:  "tool_call",
		Carry:     call.ThoughtSig,
		ToolCall:  &EndedToolCall{ToolCallID: call.ID, Name: call.Name, ArgumentsJSON: json.RawMessage(arguments)},
	})
	if err == nil {
		s.ended = append(s.ended, TerminalBlock{Type: "tool_call", ToolCallID: call.ID, Name: call.Name, ArgumentsJSON: json.RawMessage(arguments), Carry: call.ThoughtSig})
	}
	return envelope, err
}

func (s *State) settleDone(message *provider.AssistantMessage) (Envelope, error) {
	if s.open != nil || message == nil {
		s.open = nil
		return s.Failed(CodeEndpointError, "the endpoint could not assemble a terminal for this inference")
	}
	usage := usageOf(message.Usage)
	if message.StopReason == provider.StopError {
		return s.FailedWithUsage(CodeProviderUnavailable, "the provider reported a failure", &usage)
	}
	content := partsOf(message.Content)
	if !sameBlocks(content, s.ended) && !(len(s.ended) == 0 && emptyText(content)) {
		return s.FailedWithUsage(CodeEndpointError, "the endpoint could not assemble a terminal for this inference", &usage)
	}
	return s.CompletedWithUsage(string(message.StopReason), content, &usage)
}

func emptyText(blocks []TerminalBlock) bool {
	for _, block := range blocks {
		if block.Type != "text" || block.Text == nil || *block.Text != "" {
			return false
		}
	}
	return true
}

func sameBlocks(a, b []TerminalBlock) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func (s *State) settleError(reason string) (Envelope, error) {
	s.open = nil
	if reason == ReasonCancelled {
		return s.Failed(CodeAborted, "the caller cancelled this inference")
	}
	if reason == "" {
		reason = "the provider stream failed"
	}
	return s.Failed(FailureCode(reason), reason)
}

func FailureCode(message string) string {
	const marker = "request failed: HTTP "
	start := strings.Index(message, marker)
	if start < 0 {
		return CodeProviderUnavailable
	}
	digits := message[start+len(marker):]
	end := 0
	for end < len(digits) && digits[end] >= '0' && digits[end] <= '9' {
		end++
	}
	status, err := strconv.Atoi(digits[:end])
	if err != nil {
		return CodeProviderUnavailable
	}
	switch status {
	case 401, 403:
		return CodeCredentialRejected
	case 429:
		return CodeRateLimited
	}
	return CodeProviderUnavailable
}

func usageOf(usage provider.Usage) Usage {
	total := usage.TotalTokens
	if total <= 0 {
		total = usage.InputTokens + usage.CacheReadTokens + usage.CacheWriteTokens + usage.OutputTokens
	}
	return Usage{InputTokens: usage.InputTokens, CacheReadInputTokens: usage.CacheReadTokens, CacheWriteInputTokens: usage.CacheWriteTokens, OutputTokens: usage.OutputTokens, TotalTokens: total}
}

func thinkingSignature(partial provider.PartialMessage, index int) string {
	if index < 0 || index >= len(partial.Content) {
		return ""
	}
	if thinking := partial.Content[index].Thinking; thinking != nil {
		return thinking.Signature
	}
	return ""
}
