package inferenceserve

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

const (
	Protocol = "open-agent-protocol"
	Version  = "0.1"
	Profile  = "open-agent-protocol.model-provider-core"
)

type Envelope struct {
	Protocol    string          `json:"protocol"`
	Version     string          `json:"version"`
	Profile     string          `json:"profile"`
	Type        string          `json:"type"`
	ID          string          `json:"id"`
	InReplyTo   string          `json:"in_reply_to,omitempty"`
	InferenceID string          `json:"inference_id,omitempty"`
	Sequence    int             `json:"sequence,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

type PartStarted struct {
	PartIndex  int    `json:"part_index"`
	PartKind   string `json:"part_kind"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"`
}

type EndedToolCall struct {
	ToolCallID    string          `json:"tool_call_id"`
	Name          string          `json:"name"`
	ArgumentsJSON json.RawMessage `json:"arguments_json"`
}

type PartEnded struct {
	PartIndex int            `json:"part_index"`
	PartKind  string         `json:"part_kind"`
	Text      *string        `json:"text,omitempty"`
	ToolCall  *EndedToolCall `json:"tool_call,omitempty"`
	Carry     string         `json:"carry,omitempty"`
}

type TerminalBlock struct {
	Type          string          `json:"type"`
	Text          *string         `json:"text,omitempty"`
	Reasoning     *string         `json:"reasoning,omitempty"`
	Image         *ImagePart      `json:"image,omitempty"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
	Name          string          `json:"name,omitempty"`
	ArgumentsJSON json.RawMessage `json:"arguments_json,omitempty"`
	Carry         string          `json:"carry,omitempty"`
}

type ImagePart struct {
	URL       string `json:"url,omitempty"`
	Data      string `json:"data,omitempty"`
	MediaType string `json:"media_type,omitempty"`
}

type Content []TerminalBlock

func (c Content) MarshalJSON() ([]byte, error) {
	if len(c) == 0 {
		return json.Marshal("")
	}
	return json.Marshal([]TerminalBlock(c))
}

type TerminalMessage struct {
	Role    string  `json:"role"`
	Content Content `json:"content"`
}

type Honoured struct {
	IncludeSnapshot string `json:"include_snapshot"`
}

const (
	CodeProtocolViolation   = "protocol_violation"
	CodeProviderUnavailable = "provider_unavailable"
)

var ErrRefused = fmt.Errorf("inferenceserve: a refused inference allocates nothing to scope to")

type Ids struct {
	mu  sync.Mutex
	seq int
}

func (i *Ids) next() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.seq++
	return fmt.Sprintf("e%d", i.seq)
}

type State struct {
	ids         *Ids
	inferenceID string
	modelRef    string
	sequence    int
	refused     bool
	lastSettled bool
	open        *openPart
	ended       []TerminalBlock
}

func NewState(ids *Ids, inferenceID, modelRef string) *State {
	return &State{ids: ids, inferenceID: inferenceID, modelRef: modelRef}
}

func (s *State) nextID() string {
	return s.ids.next()
}

func (s *State) emit(typ string, inReplyTo string, payload any) (Envelope, error) {
	return s.emitAllocated(typ, inReplyTo, payload, scoped(typ))
}

func (s *State) emitAllocated(typ string, inReplyTo string, payload any, allocated bool) (Envelope, error) {
	if s.refused && allocated {
		return Envelope{}, ErrRefused
	}
	held, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	envelope := Envelope{
		Protocol:  Protocol,
		Version:   Version,
		Profile:   Profile,
		Type:      typ,
		ID:        s.nextID(),
		InReplyTo: inReplyTo,
		Payload:   held,
	}
	if allocated {
		envelope.InferenceID = s.inferenceID
	}
	if scoped(typ) {
		s.sequence++
		envelope.Sequence = s.sequence
	}
	return envelope, nil
}

func scoped(typ string) bool {
	switch typ {
	case "inference.started", "inference.part.started", "inference.part.delta",
		"inference.part.ended", "inference.completed", "inference.failed":
		return true
	}
	return false
}

func (s *State) Started(nowMillis int64) (Envelope, error) {
	return s.emit("inference.started", "", struct {
		ModelRef    string `json:"model_ref"`
		StartedAtMS int64  `json:"started_at_ms"`
	}{ModelRef: s.modelRef, StartedAtMS: nowMillis})
}

func (s *State) Accepted(requestID string, honoured Honoured) (Envelope, error) {
	return s.emitAllocated("inference.create.response", requestID, struct {
		Accepted bool     `json:"accepted"`
		Honoured Honoured `json:"honoured"`
	}{Accepted: true, Honoured: honoured}, true)
}

func (s *State) Refused(requestID, code, message string) (Envelope, error) {
	envelope, err := s.emitAllocated("inference.create.response", requestID, struct {
		Accepted bool `json:"accepted"`
		Error    struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}}, false)
	if err != nil {
		return Envelope{}, err
	}
	s.refused = true
	return envelope, nil
}

func (s *State) settled() bool {
	return s.lastSettled
}

func (s *State) Completed(stopReason string, content []TerminalBlock) (Envelope, error) {
	for _, block := range content {
		if block.ArgumentsJSON == nil {
			continue
		}
		if !json.Valid(block.ArgumentsJSON) {
			return s.Failed(CodeProtocolViolation, "the provider streamed a tool call whose arguments_json is not json")
		}
	}
	envelope, err := s.emit("inference.completed", "", struct {
		StopReason string          `json:"stop_reason"`
		Message    TerminalMessage `json:"message"`
	}{StopReason: stopReason, Message: TerminalMessage{Role: "assistant", Content: content}})
	if err == nil {
		s.lastSettled = true
	}
	return envelope, err
}

func (s *State) Failed(code, message string) (Envelope, error) {
	envelope, err := s.emitFailed(code, message)
	if err == nil {
		s.lastSettled = true
	}
	return envelope, err
}

func (s *State) emitFailed(code, message string) (Envelope, error) {
	return s.emit("inference.failed", "", struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}

func partsOf(blocks []provider.AssistantBlock) []TerminalBlock {
	out := []TerminalBlock{}
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			held := block.Text.Text
			out = append(out, TerminalBlock{Type: "text", Text: &held})
		case block.Thinking != nil:
			held := block.Thinking.Thinking
			out = append(out, TerminalBlock{Type: "reasoning", Reasoning: &held, Carry: block.Thinking.Signature})
		case block.ToolCall != nil:
			arguments := json.RawMessage("{}")
			if block.ToolCall.Arguments != "" {
				arguments = json.RawMessage(block.ToolCall.Arguments)
			}
			out = append(out, TerminalBlock{
				Type:          "tool_call",
				ToolCallID:    block.ToolCall.ID,
				Name:          block.ToolCall.Name,
				ArgumentsJSON: arguments,
				Carry:         block.ToolCall.ThoughtSig,
			})
		}
	}
	return out
}
