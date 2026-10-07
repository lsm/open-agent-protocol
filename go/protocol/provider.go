package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const ProviderProfile = "open-agent-protocol.model-provider-core"

type InferenceID string

type ProviderEnvelope struct {
	Protocol           string                     `json:"protocol"`
	Version            string                     `json:"version"`
	Profile            string                     `json:"profile"`
	Type               EnvelopeType               `json:"type"`
	ID                 EnvelopeID                 `json:"id"`
	Payload            json.RawMessage            `json:"payload"`
	Sequence           *uint64                    `json:"sequence,omitempty"`
	TimestampMS        *int64                     `json:"timestamp_ms,omitempty"`
	InReplyTo          EnvelopeID                 `json:"in_reply_to,omitempty"`
	InferenceID        InferenceID                `json:"inference_id,omitempty"`
	CapabilityRevision string                     `json:"capability_revision,omitempty"`
	Extensions         map[string]json.RawMessage `json:"extensions,omitempty"`
}

func NewProviderEnvelope(typ EnvelopeType, id EnvelopeID, payload any) (ProviderEnvelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return ProviderEnvelope{}, fmt.Errorf("marshal payload: %w", err)
	}
	return ProviderEnvelope{Protocol: Protocol, Version: Version, Profile: ProviderProfile, Type: typ, ID: id, Payload: raw}, nil
}

func (e *ProviderEnvelope) DecodePayload(dst any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("decode payload: missing payload")
	}
	if err := json.Unmarshal(e.Payload, dst); err != nil {
		return fmt.Errorf("decode %s payload: %w", e.Type, err)
	}
	return nil
}

func ParseProviderEnvelope(data []byte) (ProviderEnvelope, error) {
	var envelope ProviderEnvelope
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&envelope); err != nil {
		return ProviderEnvelope{}, fmt.Errorf("decode provider envelope: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return ProviderEnvelope{}, err
	}
	return envelope, nil
}
