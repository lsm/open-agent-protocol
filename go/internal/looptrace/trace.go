package looptrace

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lsm/open-agent-protocol/go/internal/agent"
	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type Ids interface {
	NewID(kind string) string
}

type Options struct {
	SessionID      protocol.SessionID
	RunID          protocol.RunID
	ModelID        string
	Revision       string
	Requester      protocol.ParticipantID
	Responder      protocol.ParticipantID
	ExecutionOwner protocol.ParticipantID
	Ids            Ids
	Now            func() int64
}

var ErrNoIds = errors.New("looptrace: an id generator is required")

type Trace struct {
	options  Options
	sequence uint64
	model    string
	accepted map[string]protocol.EnvelopeID
}

func NewTrace(options Options) (*Trace, error) {
	if options.Ids == nil {
		return nil, ErrNoIds
	}
	if options.SessionID == "" || options.RunID == "" {
		return nil, errors.New("looptrace: a session and a run are required")
	}
	if options.Requester == "" || options.Responder == "" || options.ExecutionOwner == "" {
		return nil, errors.New("looptrace: the requester, the responder and the execution owner are required: an action.call envelope names all three and the schema admits no empty one")
	}
	if options.Now == nil {
		options.Now = func() int64 { return 0 }
	}
	return &Trace{options: options, sequence: 1, model: options.ModelID, accepted: map[string]protocol.EnvelopeID{}}, nil
}

func (t *Trace) Accepted(toolCallID string, requestID protocol.EnvelopeID) {
	t.accepted[toolCallID] = requestID
}

func (t *Trace) ModelID() string { return t.model }

func (t *Trace) next(typ protocol.EnvelopeType, payload any) (protocol.Envelope, error) {
	envelope, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(t.options.Ids.NewID("event")), payload)
	if err != nil {
		return protocol.Envelope{}, err
	}
	sequence := t.sequence
	t.sequence++
	envelope.Sequence = &sequence
	timestamp := t.options.Now()
	envelope.TimestampMS = &timestamp
	envelope.SessionID = t.options.SessionID
	envelope.RunID = t.options.RunID
	envelope.CapabilityRevision = t.options.Revision
	return envelope, nil
}

func (t *Trace) callScope(call provider.ToolCall) protocol.ActionCallPayload {
	return protocol.ActionCallPayload{
		SessionID:      t.options.SessionID,
		RunID:          t.options.RunID,
		ToolCallID:     protocol.ToolCallID(call.ID),
		Name:           call.Name,
		RequestedBy:    t.options.Requester,
		RespondedBy:    t.options.Responder,
		ExecutionOwner: t.options.ExecutionOwner,
	}
}

func resultParts(result provider.ToolResult) []protocol.ContentPart {
	out := make([]protocol.ContentPart, 0, len(result.Parts))
	for _, part := range result.Parts {
		switch {
		case part.Text != nil:
			out = append(out, protocol.ContentPart{Type: protocol.ContentText, Text: part.Text.Text})
		case part.Thinking != nil:
			out = append(out, protocol.ContentPart{Type: protocol.ContentText, Text: part.Thinking.Thinking})
		case part.Image != nil:
			out = append(out, protocol.ContentPart{Type: protocol.ContentImage, Image: &protocol.ImageContent{URL: part.Image.DataURL()}})
		}
	}
	if len(out) == 0 {
		out = append(out, protocol.ContentPart{Type: protocol.ContentText})
	}
	return out
}

func failureTextOf(text string, result provider.ToolResult) string {
	if text != "" {
		return text
	}
	return fmt.Sprintf("the tool %q reported an error with no message", result.ToolName)
}

func resultText(result provider.ToolResult) string {
	for _, part := range result.Parts {
		if part.Text != nil && part.Text.Text != "" {
			return part.Text.Text
		}
	}
	return ""
}

func (t *Trace) Envelopes(event agent.Event) []protocol.Envelope {
	switch event.Kind {
	case agent.TextDelta:
		return t.one(protocol.TypeContentDelta, protocol.ContentDeltaPayload{
			SessionID: t.options.SessionID, RunID: t.options.RunID,
			Part: protocol.ContentPart{Type: protocol.ContentText, Text: event.Delta},
		})
	case agent.ReasoningDelta:
		return t.one(protocol.TypeContentDelta, protocol.ContentDeltaPayload{
			SessionID: t.options.SessionID, RunID: t.options.RunID,
			Part: protocol.ContentPart{Type: protocol.ContentReasoning, Reasoning: event.Delta},
		})
	case agent.ToolCallRequested:
		if event.Call == nil || event.Call.Name == "" {
			return nil
		}
		payload := t.callScope(*event.Call)
		payload.InteractionID = protocol.InteractionID(t.options.Ids.NewID("call"))
		payload.ArgumentsJSON = json.RawMessage("null")
		if json.Valid([]byte(event.Call.Arguments)) {
			payload.ArgumentsJSON = json.RawMessage(event.Call.Arguments)
		}
		return t.call(protocol.TypeActionCallRequested, payload)
	case agent.ToolCallCancelled:
		if event.Call == nil || event.Call.Name == "" {
			return nil
		}
		return t.call(protocol.TypeActionCallCancelled, t.callScope(*event.Call))
	case agent.ToolCallResolved:
		if event.Call == nil || event.ToolResult == nil || event.Call.Name == "" {
			return nil
		}
		payload := t.callScope(*event.Call)
		payload.RequestID = t.accepted[event.Call.ID]
		started := t.call(protocol.TypeActionCallStarted, payload)
		if event.ToolResult.IsError {
			payload.Error = &protocol.ProtocolError{Code: "tool_failed", Message: failureTextOf(resultText(*event.ToolResult), *event.ToolResult)}
			return append(started, t.call(protocol.TypeActionCallFailed, payload)...)
		}
		payload.Result = json.RawMessage(protocol.PartsContent(resultParts(*event.ToolResult)))
		return append(started, t.call(protocol.TypeActionCallCompleted, payload)...)
	case agent.AgentEnd:
		return t.settle(event)
	case agent.RunFailed:
		return t.one(protocol.TypeRunFailed, protocol.RunFailedPayload{
			SessionID: t.options.SessionID, RunID: t.options.RunID,
			Error: protocol.ProtocolError{Code: "internal_error", Message: event.Reason},
		})
	}
	return nil
}

func (t *Trace) one(typ protocol.EnvelopeType, payload any) []protocol.Envelope {
	envelope, err := t.next(typ, payload)
	if err != nil {
		return nil
	}
	return []protocol.Envelope{envelope}
}

func (t *Trace) call(typ protocol.EnvelopeType, payload protocol.ActionCallPayload) []protocol.Envelope {
	envelope, err := t.next(typ, payload)
	if err != nil {
		return nil
	}
	envelope.ToolCallID = payload.ToolCallID
	return []protocol.Envelope{envelope}
}

func (t *Trace) settle(event agent.Event) []protocol.Envelope {
	final := event.Result.FinalMessage
	if final.Model != "" {
		t.model = final.Model
	}
	switch event.Result.Termination {
	case agent.TerminationCanceled:
		return t.one(protocol.TypeRunCancelled, protocol.RunCancelledPayload{
			SessionID: t.options.SessionID, RunID: t.options.RunID, Reason: "the run was cancelled",
		})
	case agent.TerminationMaxTurns:
		return t.completed(string(final.StopReason), finalText(final))
	}
	if final.StopReason == provider.StopError {
		return t.one(protocol.TypeRunFailed, protocol.RunFailedPayload{
			SessionID: t.options.SessionID, RunID: t.options.RunID,
			Error: protocol.ProtocolError{Code: "provider_error", Message: failureText(final)},
		})
	}
	return t.completed(string(final.StopReason), finalText(final))
}

func (t *Trace) completed(stopReason, body string) []protocol.Envelope {
	return t.one(protocol.TypeRunCompleted, protocol.RunCompletedPayload{
		SessionID:     t.options.SessionID,
		RunID:         t.options.RunID,
		StopReason:    stopReason,
		ModelID:       t.model,
		FinalResponse: protocol.Message{Role: protocol.RoleAssistant, Content: protocol.TextContent(body)},
	})
}

func failureText(assistant provider.AssistantContent) string {
	if text := finalText(assistant); text != "" {
		return text
	}
	return "the provider ended the reply with stop reason " + string(assistant.StopReason)
}

func finalText(assistant provider.AssistantContent) string {
	for _, part := range assistant.Parts {
		if part.Text != nil && part.Text.Text != "" {
			return part.Text.Text
		}
	}
	return ""
}
