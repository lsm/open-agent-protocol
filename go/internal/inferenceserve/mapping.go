package inferenceserve

import (
	"encoding/json"
	"fmt"

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
	Reasoning     string          `json:"reasoning,omitempty"`
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

type TerminalMessage struct {
	Role    string          `json:"role"`
	Content []TerminalBlock `json:"content"`
}

type Honoured struct {
	IncludeSnapshot string `json:"include_snapshot"`
}

var ErrRefused = fmt.Errorf("inferenceserve: a refused inference allocates nothing to scope to")

type State struct {
	inferenceID string
	modelRef    string
	sequence    int
	ids         int
	refused     bool
}

func NewState(inferenceID, modelRef string) *State {
	return &State{inferenceID: inferenceID, modelRef: modelRef}
}

func (s *State) nextID() string {
	s.ids++
	return fmt.Sprintf("e%d", s.ids)
}

func (s *State) emit(typ string, inReplyTo string, payload any) (Envelope, error) {
	if s.refused && scoped(typ) {
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
	if scoped(typ) {
		s.sequence++
		envelope.Sequence = s.sequence
		envelope.InferenceID = s.inferenceID
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
	return s.emit("inference.create.response", requestID, struct {
		Accepted bool     `json:"accepted"`
		Honoured Honoured `json:"honoured"`
	}{Accepted: true, Honoured: honoured})
}

func (s *State) Refused(requestID, code, message string) (Envelope, error) {
	envelope, err := s.emit("inference.create.response", requestID, struct {
		Accepted bool `json:"accepted"`
		Error    struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
	if err != nil {
		return Envelope{}, err
	}
	s.refused = true
	return envelope, nil
}

func (s *State) Completed(stopReason string, content []TerminalBlock) (Envelope, error) {
	return s.emit("inference.completed", "", struct {
		StopReason string          `json:"stop_reason"`
		Message    TerminalMessage `json:"message"`
	}{StopReason: stopReason, Message: TerminalMessage{Role: "assistant", Content: content}})
}

func (s *State) Failed(code, message string) (Envelope, error) {
	return s.emit("inference.failed", "", struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message})
}

func partsOf(blocks []provider.AssistantBlock) []TerminalBlock {
	out := []TerminalBlock{}
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			held := block.Text.Text
			out = append(out, TerminalBlock{Type: "text", Text: &held})
		case block.Thinking != nil:
			out = append(out, TerminalBlock{Type: "reasoning", Reasoning: block.Thinking.Thinking, Carry: block.Thinking.Signature})
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
