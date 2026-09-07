// Package protocol defines the Open Agent Protocol agent-control-core wire model.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	Protocol = "open-agent-protocol"
	Version  = "0.1"
	Profile  = "open-agent-protocol.agent-control-core"
)

type EnvelopeID string
type ParticipantID string
type EndpointID string
type SessionID string
type SubmissionID string
type RunID string
type MessageID string
type ToolCallID string
type InteractionID string

type EnvelopeType string

const (
	TypeProtocolInitializeRequest       EnvelopeType = "protocol.initialize.request"
	TypeProtocolInitializeResponse      EnvelopeType = "protocol.initialize.response"
	TypeCapabilitiesRequest             EnvelopeType = "capabilities.request"
	TypeCapabilitiesResponse            EnvelopeType = "capabilities.response"
	TypeCapabilitiesUpdated             EnvelopeType = "capabilities.updated"
	TypeSessionOpenRequest              EnvelopeType = "session.open.request"
	TypeSessionOpenResponse             EnvelopeType = "session.open.response"
	TypeSessionStateRequest             EnvelopeType = "session.state.request"
	TypeSessionStateResponse            EnvelopeType = "session.state.response"
	TypeSessionStateUpdated             EnvelopeType = "session.state.updated"
	TypeSessionMessageSubmitRequest     EnvelopeType = "session.message.submit.request"
	TypeSessionMessageSubmitResponse    EnvelopeType = "session.message.submit.response"
	TypeRunCancelRequest                EnvelopeType = "run.cancel.request"
	TypeRunCancelResponse               EnvelopeType = "run.cancel.response"
	TypeRunStarted                      EnvelopeType = "run.started"
	TypeRunStatusUpdated                EnvelopeType = "run.status.updated"
	TypeContentDelta                    EnvelopeType = "content.delta"
	TypeRunCompleted                    EnvelopeType = "run.completed"
	TypeRunFailed                       EnvelopeType = "run.failed"
	TypeRunCancelled                    EnvelopeType = "run.cancelled"
	TypeActionToolsListRequest          EnvelopeType = "action.tools.list.request"
	TypeActionToolsListResponse         EnvelopeType = "action.tools.list.response"
	TypeActionCallRequested             EnvelopeType = "action.call.requested"
	TypeActionCallStarted               EnvelopeType = "action.call.started"
	TypeActionCallProgress              EnvelopeType = "action.call.progress"
	TypeActionCallCompleted             EnvelopeType = "action.call.completed"
	TypeActionCallFailed                EnvelopeType = "action.call.failed"
	TypeActionCallCancelled             EnvelopeType = "action.call.cancelled"
	TypeActionPermissionRequested       EnvelopeType = "action.permission.requested"
	TypeActionPermissionResolveRequest  EnvelopeType = "action.permission.resolve.request"
	TypeActionPermissionResolveResponse EnvelopeType = "action.permission.resolve.response"
	TypeActionPermissionResolved        EnvelopeType = "action.permission.resolved"
	TypeUserInputRequested              EnvelopeType = "user.input.requested"
	TypeUserInputResolveRequest         EnvelopeType = "user.input.resolve.request"
	TypeUserInputResolveResponse        EnvelopeType = "user.input.resolve.response"
	TypeUserInputResolved               EnvelopeType = "user.input.resolved"
	TypeUserInputCancelRequest          EnvelopeType = "user.input.cancel.request"
	TypeUserInputCancelResponse         EnvelopeType = "user.input.cancel.response"
	TypeErrorResponse                   EnvelopeType = "error.response"
)

// Envelope retains its payload and any additive top-level fields as raw JSON.
type Envelope struct {
	Protocol           string                     `json:"protocol"`
	Version            string                     `json:"version"`
	Profile            string                     `json:"profile"`
	Type               EnvelopeType               `json:"type"`
	ID                 EnvelopeID                 `json:"id"`
	Payload            json.RawMessage            `json:"payload"`
	Sequence           *uint64                    `json:"sequence,omitempty"`
	TimestampMS        *int64                     `json:"timestamp_ms,omitempty"`
	InReplyTo          EnvelopeID                 `json:"in_reply_to,omitempty"`
	SessionID          SessionID                  `json:"session_id,omitempty"`
	RunID              RunID                      `json:"run_id,omitempty"`
	TurnID             InteractionID              `json:"turn_id,omitempty"`
	ToolCallID         ToolCallID                 `json:"tool_call_id,omitempty"`
	CapabilityRevision string                     `json:"capability_revision,omitempty"`
	Extensions         map[string]json.RawMessage `json:"extensions,omitempty"`
	Unknown            map[string]json.RawMessage `json:"-"`
}

var envelopeFields = map[string]struct{}{
	"protocol": {}, "version": {}, "profile": {}, "type": {}, "id": {}, "payload": {},
	"sequence": {}, "timestamp_ms": {}, "in_reply_to": {}, "session_id": {}, "run_id": {},
	"turn_id": {}, "tool_call_id": {}, "capability_revision": {}, "extensions": {},
}

func NewEnvelope(typ EnvelopeType, id EnvelopeID, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("marshal payload: %w", err)
	}
	return Envelope{Protocol: Protocol, Version: Version, Profile: Profile, Type: typ, ID: id, Payload: raw}, nil
}

func (e *Envelope) DecodePayload(dst any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("decode payload: missing payload")
	}
	if err := json.Unmarshal(e.Payload, dst); err != nil {
		return fmt.Errorf("decode %s payload: %w", e.Type, err)
	}
	return nil
}

func (e *Envelope) UnmarshalJSON(data []byte) error {
	type plain Envelope
	var known plain
	if err := json.Unmarshal(data, &known); err != nil {
		return err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return err
	}
	unknown := make(map[string]json.RawMessage)
	for key, value := range all {
		if _, ok := envelopeFields[key]; !ok {
			unknown[key] = append(json.RawMessage(nil), value...)
		}
	}
	*e = Envelope(known)
	if len(unknown) != 0 {
		e.Unknown = unknown
	}
	return nil
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	type plain Envelope
	known, err := json.Marshal(plain(e))
	if err != nil {
		return nil, err
	}
	if len(e.Unknown) == 0 {
		return known, nil
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(known, &all); err != nil {
		return nil, err
	}
	for key, value := range e.Unknown {
		if _, reserved := envelopeFields[key]; reserved {
			continue
		}
		all[key] = value
	}
	return json.Marshal(all)
}

func ParseEnvelope(data []byte) (Envelope, error) {
	var envelope Envelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode envelope: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}
