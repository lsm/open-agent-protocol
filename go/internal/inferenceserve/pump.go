package inferenceserve

import (
	"encoding/json"
	"fmt"

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
	index    int
	kind     string
	implicit bool
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
			var out []Envelope
			if state.open != nil && state.open.kind == "tool_call" {
				state.open = nil
			}
			if state.open != nil {
				held := state.takeImplicit(state.open.index, state.open.kind)
				if held == nil {
					empty := ""
					held = &PartEnded{PartIndex: state.open.index, PartKind: state.open.kind, Text: &empty}
				}
				state.ended = append(state.ended, blockOf(*held))
				envelope, err := state.emit("inference.part.ended", "", *held)
				if err != nil {
					return nil, err
				}
				out = append(out, envelope)
				state.open = nil
			}
			envelope, err := state.Completed("aborted", state.ended)
			if err != nil {
				return nil, err
			}
			return append(out, envelope), nil
		}
		code, message := classify(event.Reason)
		envelope, err := settleFailed(state, code, message)
		if err != nil {
			return nil, err
		}
		return []Envelope{envelope}, nil
	case provider.EventDone:
		if state.settled() {
			return nil, nil
		}
		if state.open != nil {
			if !state.open.implicit {
				envelope, err := settleFailed(state, CodeEndpointError,
					"the endpoint could not deliver the terminal for this inference")
				if err != nil {
					return nil, err
				}
				return []Envelope{envelope}, nil
			}
			held := state.takeImplicit(state.open.index, state.open.kind)
			state.open = nil
			if held != nil {
				state.ended = append(state.ended, blockOf(*held))
				ended, err := state.emit("inference.part.ended", "", *held)
				if err != nil {
					return nil, err
				}
				envelope, err := settleCompleted(state, event)
				if err != nil {
					return nil, err
				}
				return []Envelope{ended, envelope}, nil
			}
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
			if !state.open.implicit {
				return nil, ErrPartAlreadyOpen
			}
			held := state.takeImplicit(state.open.index, state.open.kind)
			state.open = nil
			if held == nil {
				return nil, ErrPartAlreadyOpen
			}
			state.ended = append(state.ended, blockOf(*held))
			closed, err := state.emit("inference.part.ended", "", *held)
			if err != nil {
				return nil, err
			}
			started, err := state.emit("inference.part.started", "", PartStarted{
				PartIndex: event.ContentIndex, PartKind: partKindOf(event.Kind),
				ToolCallID: event.ID, Name: event.Name,
			})
			if err != nil {
				return nil, err
			}
			state.open = &openPart{index: event.ContentIndex, kind: partKindOf(event.Kind)}
			return []Envelope{closed, started}, nil
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
		if state.open == nil {
			opened, err := state.openImplicitPart(event)
			if err != nil {
				return nil, err
			}
			return opened, nil
		}
		if state.open.index != event.ContentIndex {
			return nil, ErrPartIndexMismatch
		}
		envelope, err := state.emit("inference.part.delta", "", struct {
			PartIndex int    `json:"part_index"`
			Delta     string `json:"delta"`
		}{PartIndex: event.ContentIndex, Delta: event.Delta})
		if err != nil {
			return nil, err
		}
		state.appendImplicit(event.ContentIndex, event.Delta)
		return []Envelope{envelope}, nil
	}

	if isPartEnd(event.Kind) {
		if state.open == nil {
			if held := state.takeImplicit(event.ContentIndex, partKindOf(event.Kind)); held != nil {
				state.ended = append(state.ended, blockOf(*held))
				envelope, err := state.emit("inference.part.ended", "", *held)
				if err != nil {
					return nil, err
				}
				return []Envelope{envelope}, nil
			}
			return nil, ErrPartIndexMismatch
		}
		if state.open.index != event.ContentIndex {
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
	content := state.ended
	if len(content) == 0 {
		content = state.implicitContent()
	}
	return state.Completed(stopReason, content)
}

func (s *State) implicitContent() []TerminalBlock {
	out := []TerminalBlock{}
	for _, held := range s.accumulates {
		block := TerminalBlock{Type: held.kind}
		text := held.text
		switch held.kind {
		case "text":
			block.Text = &text
		case "reasoning":
			block.Reasoning = &text
		default:
			continue
		}
		out = append(out, block)
	}
	return out
}

func settleFailed(state *State, code, message string) (Envelope, error) {
	state.open = nil
	return state.Failed(code, message)
}

func isCancellation(reason string) bool {
	return reason == ReasonCancelled
}

const (
	ReasonCancelled  = "request cancelled"
	ReasonReadFailed = "read error"
)

func classify(reason string) (string, string) {
	switch reason {
	case "":
		return CodeProviderUnavailable, "the provider stream failed"
	case ReasonCancelled:
		return CodeAborted, "the request was cancelled"
	case ReasonReadFailed:
		return CodeEndpointError, "the endpoint could not read the provider stream"
	}
	return CodeProviderUnavailable, reason
}

func partKindOf(event provider.EventKind) string {
	switch event {
	case provider.EventTextStart, provider.EventTextDelta, provider.EventTextEnd:
		return "text"
	case provider.EventThinkingStart, provider.EventThinkingDelta, provider.EventThinkingEnd:
		return "reasoning"
	case provider.EventToolCallStart, provider.EventToolCallDelta, provider.EventToolCallEnd:
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

func (s *State) openImplicitPart(event provider.Event) ([]Envelope, error) {
	kind := partKindOf(event.Kind)
	if kind == "" {
		return nil, ErrPartIndexMismatch
	}
	if kind == "tool_call" {
		return nil, ErrPartIndexMismatch
	}
	part := PartStarted{PartIndex: event.ContentIndex, PartKind: kind}
	s.open = &openPart{index: event.ContentIndex, kind: kind, implicit: true}
	envelope, err := s.emit("inference.part.started", "", part)
	if err != nil {
		return nil, err
	}
	s.accumulates = append(s.accumulates, accumulated{index: event.ContentIndex, kind: kind, text: event.Delta})
	delta, err := s.emit("inference.part.delta", "", struct {
		PartIndex int    `json:"part_index"`
		Delta     string `json:"delta"`
	}{PartIndex: event.ContentIndex, Delta: event.Delta})
	if err != nil {
		return nil, err
	}
	return []Envelope{envelope, delta}, nil
}

func (s *State) takeImplicit(contentIndex int, kind string) *PartEnded {
	for i := range s.accumulates {
		if s.accumulates[i].index != contentIndex {
			continue
		}
		held := s.accumulates[i]
		s.accumulates = append(s.accumulates[:i], s.accumulates[i+1:]...)
		text := held.text
		if kind == "" {
			kind = held.kind
		}
		return &PartEnded{PartIndex: contentIndex, PartKind: kind, Text: &text}
	}
	return nil
}

func (s *State) appendImplicit(contentIndex int, delta string) {
	for i := range s.accumulates {
		if s.accumulates[i].index == contentIndex {
			s.accumulates[i].text += delta
			return
		}
	}
	s.accumulates = append(s.accumulates, accumulated{index: contentIndex, text: delta})
}
