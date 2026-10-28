package copilot

import (
	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const maxQueued = 8

type run struct {
	id                protocol.RunID
	messageID         protocol.MessageID
	nativeMessage     string
	status            protocol.RunStatus
	started, terminal bool
	next              uint64
	parts             []protocol.ContentPart
	usage             usageState
	tools             []*tool
}

type reducer struct {
	sessionID protocol.SessionID
	nativeID  string
	ids       base.IDGenerator
	clock     base.Clock
	runs      []*run
	current   *run
	events    []protocol.Envelope
}

func newReducer(sessionID protocol.SessionID, nativeID string, ids base.IDGenerator, clock base.Clock) *reducer {
	return &reducer{sessionID: sessionID, nativeID: nativeID, ids: ids, clock: clock}
}

func (r *reducer) admit(queue bool) (*run, error) {
	live := 0
	for _, run := range r.runs {
		if !run.terminal {
			live++
		}
	}
	if live > maxQueued {
		return nil, base.ErrRunActive
	}
	status := protocol.RunRunning
	if queue || live > 0 {
		status = protocol.RunQueued
	}
	admitted := &run{id: protocol.RunID(r.ids.NewID("run")), messageID: protocol.MessageID(r.ids.NewID("message")), status: status, next: 1}
	r.runs = append(r.runs, admitted)
	return admitted, nil
}

func (r *reducer) observe(event native.Event) error {
	if event.SessionID != r.nativeID {
		if r.current != nil {
			return r.fail(r.current, "copilot_foreign_session", "an event for another Copilot session reached this one")
		}
		return nil
	}
	switch event.Type {
	case "user.message":
		id := native.Text(event.Data, "messageId")
		if id == "" {
			return nil
		}
		for _, run := range r.runs {
			if run.started || run.terminal || run.nativeMessage != id {
				continue
			}
			if r.current != nil {
				if err := r.complete(r.current); err != nil {
					return err
				}
			}
			run.started = true
			run.status = protocol.RunRunning
			r.current = run
			_, err := r.emit(run, "run.started", map[string]any{"status": "running"}, false, "")
			return err
		}
	case "assistant.message_delta":
		if r.current == nil {
			return nil
		}
		text := native.Text(event.Data, "deltaContent")
		if text == "" {
			return nil
		}
		_, err := r.emit(r.current, "content.delta", map[string]any{"message_id": r.current.messageID, "part": protocol.ContentPart{Type: protocol.ContentText, Text: text}}, false, "")
		return err
	case "assistant.message":
		if r.current != nil {
			if text := native.Text(event.Data, "content"); text != "" {
				r.current.parts = append(r.current.parts, protocol.ContentPart{Type: protocol.ContentText, Text: text})
			}
		}
	case "session.idle":
		if r.current == nil {
			return nil
		}
		if string(event.Data["aborted"]) == "true" {
			return r.fail(r.current, "copilot_aborted", "Copilot ended the busy period as aborted, without a cancel for this run")
		}
		return r.complete(r.current)
	case "session.error":
		if r.current == nil {
			return nil
		}
		text := native.Text(event.Data, "message")
		if text == "" {
			text = "Copilot reported a session error"
		}
		return r.fail(r.current, "copilot_session_error", text)
	case "assistant.usage":
		if r.current != nil {
			r.current.usage.observe(event.Data)
		}
	case "tool.execution_start", "tool.execution_partial_result", "tool.shell_output", "tool.execution_complete":
		return r.observeTool(event)
	}
	return nil
}

func (r *reducer) complete(run *run) error {
	var content any = ""
	if len(run.parts) > 0 {
		content = run.parts
	}
	_, err := r.emit(run, "run.completed", map[string]any{"final_response": map[string]any{"id": run.messageID, "role": "assistant", "content": content}, "stop_reason": "end_turn"}, true, "")
	return err
}

func (r *reducer) fail(run *run, code, message string) error {
	_, err := r.emit(run, "run.failed", map[string]any{"error": protocol.ProtocolError{Code: code, Message: message}}, true, "")
	return err
}

func (r *reducer) emit(run *run, kind string, payload map[string]any, terminal bool, reply protocol.EnvelopeID) (protocol.EnvelopeID, error) {
	if run.terminal {
		return "", nil
	}
	if terminal {
		if err := r.settleTools(run); err != nil {
			return "", err
		}
	}
	payload["session_id"] = r.sessionID
	payload["run_id"] = run.id
	if terminal && run.usage.measured {
		payload["usage"] = run.usage.portable()
	}
	envelope, err := protocol.NewEnvelope(protocol.EnvelopeType(kind), protocol.EnvelopeID(r.ids.NewID("event")), payload)
	if err != nil {
		return "", err
	}
	seq, at := run.next, r.clock.Now().UnixMilli()
	envelope.Sequence = &seq
	envelope.TimestampMS = &at
	envelope.SessionID = r.sessionID
	envelope.RunID = run.id
	envelope.CapabilityRevision = CapabilityRevision
	envelope.InReplyTo = reply
	if id, ok := payload["tool_call_id"].(protocol.ToolCallID); ok {
		envelope.ToolCallID = id
	}
	run.next++
	if terminal {
		envelope.Extensions = run.usage.extensions()
		run.terminal = true
		run.status = protocol.RunCompleted
		if kind == "run.failed" {
			run.status = protocol.RunFailed
		}
		if r.current == run {
			r.current = nil
		}
	}
	r.events = append(r.events, envelope)
	return envelope.ID, nil
}
