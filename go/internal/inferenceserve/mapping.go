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

type Part struct {
	PartIndex        int             `json:"part_index"`
	PartKind         string          `json:"part_kind"`
	Text             string          `json:"text,omitempty"`
	Carry            string          `json:"carry,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	ArgumentsJSON    json.RawMessage `json:"arguments_json,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

type TerminalMessage struct {
	Role    string          `json:"role"`
	Content []TerminalBlock `json:"content"`
}

type TerminalBlock struct {
	Type             string          `json:"type"`
	Text             string          `json:"text,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	Carry            string          `json:"carry,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	Name             string          `json:"name,omitempty"`
	ArgumentsJSON    json.RawMessage `json:"arguments_json,omitempty"`
	ThoughtSignature string          `json:"thought_signature,omitempty"`
}

var ErrPartIndexMismatch = fmt.Errorf("inferenceserve: a part arrived that is not the one open")

type openPart struct {
	index int
	kind  string
}

type State struct {
	inferenceID string
	modelRef    string
	sequence    int
	open        *openPart
	settled     bool
	ids         int
}

func NewState(inferenceID, modelRef string) *State {
	return &State{inferenceID: inferenceID, modelRef: modelRef}
}

func (s *State) nextID(prefix string) string {
	s.ids++
	return fmt.Sprintf("%s%d", prefix, s.ids)
}

func (s *State) emit(typ string, inReplyTo string, payload any) (Envelope, error) {
	held, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	envelope := Envelope{
		Protocol:  Protocol,
		Version:   Version,
		Profile:   Profile,
		Type:      typ,
		ID:        s.nextID("e"),
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

func partsOf(message *provider.AssistantMessage) []TerminalBlock {
	out := []TerminalBlock{}
	if message == nil {
		return out
	}
	for _, block := range message.Content {
		switch {
		case block.Text != nil:
			out = append(out, TerminalBlock{Type: "text", Text: block.Text.Text})
		case block.Thinking != nil:
			held := TerminalBlock{Type: "reasoning", Reasoning: block.Thinking.Thinking, Carry: block.Thinking.Signature}
			out = append(out, held)
		case block.ToolCall != nil:
			held := TerminalBlock{
				Type:             "tool_call",
				ToolCallID:       block.ToolCall.ID,
				Name:             block.ToolCall.Name,
				ThoughtSignature: block.ToolCall.ThoughtSig,
			}
			if block.ToolCall.Arguments != "" {
				held.ArgumentsJSON = json.RawMessage(block.ToolCall.Arguments)
			}
			out = append(out, held)
		}
	}
	return out
}

func (s *State) Accepted(requestID string, honoured map[string]string) (Envelope, error) {
	return s.createResponse(requestID, struct {
		Accepted bool              `json:"accepted"`
		Honoured map[string]string `json:"honoured,omitempty"`
	}{Accepted: true, Honoured: honoured})
}

func (s *State) Refused(requestID, code, message string) (Envelope, error) {
	return s.createResponse(requestID, struct {
		Accepted bool `json:"accepted"`
		Error    struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: code, Message: message}})
}

func (s *State) createResponse(requestID string, payload any) (Envelope, error) {
	return s.emit("inference.create.response", requestID, payload)
}

func (s *State) Started(nowMillis int64) (Envelope, error) {
	return s.emit("inference.started", "", struct {
		ModelRef    string `json:"model_ref"`
		StartedAtMS int64  `json:"started_at_ms"`
	}{ModelRef: s.modelRef, StartedAtMS: nowMillis})
}
