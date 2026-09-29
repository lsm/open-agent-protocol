package inferenceserve

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/provider"
)

var (
	ErrAfterTerminal        = fmt.Errorf("inferenceserve: a part arrived after the inference settled")
	ErrPartAlreadyOpen      = fmt.Errorf("inferenceserve: a part is already open")
	ErrPartIndexMismatch    = fmt.Errorf("inferenceserve: a part arrived that is not the one open")
	ErrToolCallIdentity     = fmt.Errorf("inferenceserve: a tool_call part needs an id and a name")
	ErrToolCallIdentityOnly = fmt.Errorf("inferenceserve: only a tool_call part may carry an id and a name")
)

type openPart struct {
	index int
	kind  string
}

func carriedSignature(partial provider.PartialMessage, contentIndex int) string {
	if contentIndex < 0 || contentIndex >= len(partial.Content) {
		return ""
	}
	block := partial.Content[contentIndex]
	if block.Thinking == nil {
		return ""
	}
	return block.Thinking.Signature
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
		if isCancellation(event.Reason) {
			envelope, err := state.Completed("aborted", state.ended)
			if err != nil {
				return nil, err
			}
			return []Envelope{envelope}, nil
		}
		envelope, err := settleFailed(state, CodeEndpointError, failureMessage(event.Reason))
		if err != nil {
			return nil, err
		}
		return []Envelope{envelope}, nil
	case provider.EventDone:
		if state.settled() {
			return nil, nil
		}
		if state.open != nil {
			envelope, err := settleFailed(state, CodeEndpointError,
				"the endpoint could not deliver the terminal for this inference")
			if err != nil {
				return nil, err
			}
			return []Envelope{envelope}, nil
		}
		envelope, err := settleCompleted(state, event)
		if err != nil {
			return nil, err
		}
		return []Envelope{envelope}, nil
	}

	if isPartStart(event.Kind) || isDelta(event.Kind) || isPartEnd(event.Kind) {
		if state.settled() {
			return nil, ErrAfterTerminal
		}
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
				part.Carry = carriedSignature(event.Partial, event.ContentIndex)
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
					envelope, err := settleFailed(state, CodeProtocolViolation,
						"the provider streamed a tool call whose arguments_json is not json")
					if err != nil {
						return nil, err
					}
					return []Envelope{envelope}, nil
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

func settleCompleted(state *State, event provider.Event) (Envelope, error) {
	stopReason := "stop"
	if event.Message != nil && event.Message.StopReason != "" {
		stopReason = string(event.Message.StopReason)
	}
	return state.Completed(stopReason, state.ended)
}

func settleFailed(state *State, code, message string) (Envelope, error) {
	state.open = nil
	return state.Failed(code, message)
}

func isCancellation(reason string) bool {
	return strings.Contains(reason, "cancel")
}

func failureMessage(reason string) string {
	if reason == "" {
		return "the provider stream failed"
	}
	return "the provider stream failed: " + reason
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
