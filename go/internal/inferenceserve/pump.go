package inferenceserve

import (
	"encoding/json"
	"fmt"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

var (
	ErrPartAlreadyOpen      = fmt.Errorf("inferenceserve: a part is already open")
	ErrPartIndexMismatch    = fmt.Errorf("inferenceserve: a part arrived that is not the one open")
	ErrToolCallIdentity     = fmt.Errorf("inferenceserve: a tool_call part needs an id and a name")
	ErrToolCallIdentityOnly = fmt.Errorf("inferenceserve: only a tool_call part may carry an id and a name")
)

type openPart struct {
	index int
	kind  string
}

func carriedSignature(partial provider.PartialMessage) string {
	for _, block := range partial.Content {
		if block.Thinking != nil {
			return block.Thinking.Signature
		}
	}
	return ""
}

func Pump(state *State, event provider.Event) ([]Envelope, error) {
	switch event.Kind {
	case provider.EventKeepalive:
		return nil, nil
	case provider.EventStart:
		return nil, nil
	case provider.EventError:
		if state.settled() {
			return nil, nil
		}
		return []Envelope{settleFailed(state, CodeProviderUnavailable, "the provider stream failed")}, nil
	case provider.EventDone:
		if state.settled() {
			return nil, nil
		}
		if state.open != nil {
			return []Envelope{settleFailed(state, CodeProtocolViolation,
				"the provider settled with a part still open")}, nil
		}
		return []Envelope{settleCompleted(state, event)}, nil
	}

	if isPartStart(event.Kind) {
		if state.open != nil {
			return nil, ErrPartAlreadyOpen
		}
		part := PartStarted{PartIndex: event.ContentIndex, PartKind: partKindOf(event.Kind)}
		if part.PartKind == "tool_call" {
			if event.ID == "" || event.Name == "" {
				return nil, ErrToolCallIdentity
			}
			part.ToolCallID = event.ID
			part.Name = event.Name
		} else if event.ID != "" || event.Name != "" {
			return nil, ErrToolCallIdentityOnly
		}
		state.open = &openPart{index: event.ContentIndex, kind: part.PartKind}
		envelope, err := state.emit("inference.part.started", "", part)
		if err != nil {
			return nil, err
		}
		return []Envelope{envelope}, nil
	}

	if isDelta(event.Kind) {
		if state.open == nil || state.open.index != event.ContentIndex {
			return nil, ErrPartIndexMismatch
		}
		envelope, err := state.emit("inference.part.delta", "", struct {
			PartIndex int    `json:"part_index"`
			Delta     string `json:"delta"`
		}{PartIndex: event.ContentIndex, Delta: event.Delta})
		if err != nil {
			return nil, err
		}
		return []Envelope{envelope}, nil
	}

	if isPartEnd(event.Kind) {
		if state.open == nil || state.open.index != event.ContentIndex {
			return nil, ErrPartIndexMismatch
		}
		part := PartEnded{PartIndex: event.ContentIndex, PartKind: state.open.kind}
		switch part.PartKind {
		case "text", "reasoning":
			held := event.Delta
			part.Text = &held
			if part.PartKind == "reasoning" {
				part.Carry = carriedSignature(event.Partial)
			}
		case "tool_call":
			call := event.ToolCall
			if call == nil || call.ID == "" || call.Name == "" {
				return nil, ErrToolCallIdentity
			}
			part.Carry = call.ThoughtSig
			arguments := "{}"
			if call.Arguments != "" {
				if !json.Valid([]byte(call.Arguments)) {
					state.open = nil
					if state.settled() {
						return nil, nil
					}
					return []Envelope{settleFailed(state, CodeProtocolViolation,
						"the provider streamed a tool call whose arguments_json is not json")}, nil
				}
				arguments = call.Arguments
			}
			part.ToolCall = &EndedToolCall{ToolCallID: call.ID, Name: call.Name, ArgumentsJSON: json.RawMessage(arguments)}
		}
		state.open = nil
		state.ended = append(state.ended, blockOf(part))
		envelope, err := state.emit("inference.part.ended", "", part)
		if err != nil {
			return nil, err
		}
		return []Envelope{envelope}, nil
	}
	return nil, nil
}

func blockOf(part PartEnded) TerminalBlock {
	switch part.PartKind {
	case "text":
		return TerminalBlock{Type: "text", Text: part.Text}
	case "reasoning":
		return TerminalBlock{Type: "reasoning", Reasoning: part.Text, Carry: part.Carry}
	default:
		call := part.ToolCall
		held := TerminalBlock{Type: "tool_call", Carry: part.Carry}
		if call != nil {
			held.ToolCallID = call.ToolCallID
			held.Name = call.Name
			held.ArgumentsJSON = call.ArgumentsJSON
		}
		return held
	}
}

func settleCompleted(state *State, event provider.Event) Envelope {
	stopReason := "stop"
	if event.Message != nil && event.Message.StopReason != "" {
		stopReason = event.Message.StopReason
	}
	content := state.ended
	if len(content) == 0 {
		return settleFailed(state, CodeProtocolViolation, "the provider settled with no content")
	}
	envelope, err := state.Completed(stopReason, content)
	if err != nil {
		return settleFailed(state, CodeProtocolViolation, err.Error())
	}
	return envelope
}

func settleFailed(state *State, code, message string) Envelope {
	envelope, err := state.Failed(code, message)
	if err != nil {
		return Envelope{Type: "inference.failed", Payload: json.RawMessage(`{}`)}
	}
	return envelope
}

func partKindOf(event provider.EventKind) string {
	switch event {
	case provider.EventTextStart, provider.EventTextEnd:
		return "text"
	case provider.EventThinkingStart, provider.EventThinkingEnd:
		return "reasoning"
	case provider.EventToolCallStart, provider.EventToolCallEnd:
		return "tool_call"
	}
	return ""
}

func isDelta(event provider.EventKind) bool {
	switch event {
	case provider.EventTextDelta, provider.EventThinkingDelta, provider.EventToolCallDelta:
		return true
	}
	return false
}

func isPartStart(event provider.EventKind) bool {
	switch event {
	case provider.EventTextStart, provider.EventThinkingStart, provider.EventToolCallStart:
		return true
	}
	return false
}

func isPartEnd(event provider.EventKind) bool {
	switch event {
	case provider.EventTextEnd, provider.EventThinkingEnd, provider.EventToolCallEnd:
		return true
	}
	return false
}
