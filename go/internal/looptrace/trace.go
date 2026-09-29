package looptrace

import (
	"encoding/json"
	"errors"

	"github.com/lsm/open-agent-protocol/go/internal/agent"
	"github.com/lsm/open-agent-protocol/go/internal/provider"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type Ids interface {
	NewID(kind string) string
}

type Options struct {
	SessionID protocol.SessionID
	RunID     protocol.RunID
	ModelID   string
	Revision  string
	Ids       Ids
	NowMS     int64
}

var ErrNoIds = errors.New("looptrace: an id generator is required")

type Trace struct {
	options  Options
	sequence uint64
	model    string
}

func NewTrace(options Options) (*Trace, error) {
	if options.Ids == nil {
		return nil, ErrNoIds
	}
	if options.SessionID == "" || options.RunID == "" {
		return nil, errors.New("looptrace: a session and a run are required")
	}
	return &Trace{options: options, sequence: 1, model: options.ModelID}, nil
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
	timestamp := t.options.NowMS
	envelope.TimestampMS = &timestamp
	envelope.SessionID = t.options.SessionID
	envelope.RunID = t.options.RunID
	envelope.CapabilityRevision = t.options.Revision
	return envelope, nil
}

func (t *Trace) callPayload(call provider.ToolCall, result *provider.ToolResult) protocol.ActionCallPayload {
	payload := protocol.ActionCallPayload{
		SessionID:  t.options.SessionID,
		RunID:      t.options.RunID,
		ToolCallID: protocol.ToolCallID(call.ID),
		Name:       call.Name,
	}
	if json.Valid([]byte(call.Arguments)) {
		payload.ArgumentsJSON = json.RawMessage(call.Arguments)
	}
	if result != nil {
		payload.Result = json.RawMessage(protocol.PartsContent([]protocol.ContentPart{resultPart(*result)}))
	}
	return payload
}

func resultPart(result provider.ToolResult) protocol.ContentPart {
	out := protocol.ContentPart{Type: protocol.ContentText}
	if len(result.Parts) > 0 && result.Parts[0].Text != nil {
		out.Text = result.Parts[0].Text.Text
	}
	if result.IsError {
		isError := true
		out.IsError = &isError
	}
	return out
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
		if event.Call == nil {
			return nil
		}
		payload := t.callPayload(*event.Call, nil)
		payload.InteractionID = protocol.InteractionID(t.options.Ids.NewID("call"))
		return t.call(protocol.TypeActionCallRequested, payload)
	case agent.ToolCallResolved:
		if event.Call == nil || event.ToolResult == nil {
			return nil
		}
		payload := t.callPayload(*event.Call, event.ToolResult)
		if event.ToolResult.IsError {
			payload.Error = &protocol.ProtocolError{Code: "tool_failed", Message: resultPart(*event.ToolResult).Text}
			return t.call(protocol.TypeActionCallFailed, payload)
		}
		return t.call(protocol.TypeActionCallCompleted, payload)
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
			Error: protocol.ProtocolError{Code: "provider_error", Message: finalText(final)},
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

func finalText(assistant provider.AssistantContent) string {
	for _, part := range assistant.Parts {
		if part.Text != nil && part.Text.Text != "" {
			return part.Text.Text
		}
	}
	return ""
}
