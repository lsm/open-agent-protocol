package sdk

import (
	"bytes"
	"encoding/json"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

type routing struct {
	Type        string          `json:"type"`
	InReplyTo   string          `json:"in_reply_to"`
	SessionID   string          `json:"session_id"`
	RunID       string          `json:"run_id"`
	InferenceID string          `json:"inference_id"`
	Payload     json.RawMessage `json:"payload"`
}

type inbound struct {
	agent    *protocol.Envelope
	provider *protocol.ProviderEnvelope
	broken   error
	header   routing
}

func decodeInbound(line []byte) (*inbound, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, errMalformedFrame
	}
	var head struct {
		routing
		Protocol string `json:"protocol"`
		Version  any    `json:"version"`
		Profile  string `json:"profile"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, errMalformedFrame
	}
	if head.Protocol != protocol.Protocol || head.Version != protocol.Version || (head.Profile != protocol.Profile && head.Profile != protocol.ProviderProfile) {
		return &inbound{broken: transportErrorf(nil, "malformed OAP envelope %s: protocol %q, version %v and profile %q are not an OAP 0.1 profile", head.Type, head.Protocol, head.Version, head.Profile), header: head.routing}, nil
	}
	if head.Profile == protocol.ProviderProfile {
		envelope, err := protocol.ParseProviderEnvelope(line)
		if err != nil {
			return &inbound{broken: transportErrorf(err, "malformed OAP envelope %s: %v", head.Type, err), header: head.routing}, nil
		}
		return &inbound{provider: &envelope}, nil
	}
	envelope, err := protocol.ParseEnvelope(line)
	if err != nil {
		return &inbound{broken: transportErrorf(err, "malformed OAP envelope %s: %v", head.Type, err), header: head.routing}, nil
	}
	return &inbound{agent: &envelope}, nil
}

func (in *inbound) kind() string {
	switch {
	case in.agent != nil:
		return string(in.agent.Type)
	case in.provider != nil:
		return string(in.provider.Type)
	}
	return in.header.Type
}

func (in *inbound) replyTo() string {
	switch {
	case in.agent != nil:
		return string(in.agent.InReplyTo)
	case in.provider != nil:
		return string(in.provider.InReplyTo)
	}
	return in.header.InReplyTo
}

func (in *inbound) session() string {
	switch {
	case in.agent != nil:
		return string(in.agent.SessionID)
	case in.provider != nil:
		return ""
	}
	return in.header.SessionID
}

func (in *inbound) run() string {
	switch {
	case in.agent != nil:
		return string(in.agent.RunID)
	case in.provider != nil:
		return ""
	}
	return in.header.RunID
}

func (in *inbound) inference() string {
	switch {
	case in.agent != nil:
		return ""
	case in.provider != nil:
		return string(in.provider.InferenceID)
	}
	return in.header.InferenceID
}

func (in *inbound) flow() string {
	if in.agent != nil || in.provider != nil {
		return in.body().str("flow_id")
	}
	return payloadObject(in.header.Payload).str("flow_id")
}

func (in *inbound) sequence() int64 {
	var sequence *uint64
	switch {
	case in.agent != nil:
		sequence = in.agent.Sequence
	case in.provider != nil:
		sequence = in.provider.Sequence
	}
	if sequence == nil {
		return 0
	}
	return int64(*sequence)
}

func (in *inbound) body() jsonObject {
	var raw json.RawMessage
	switch {
	case in.agent != nil:
		raw = in.agent.Payload
	case in.provider != nil:
		raw = in.provider.Payload
	}
	payload, _ := decodeObject(raw)
	if payload == nil {
		return jsonObject{}
	}
	return payload
}

func (in *inbound) failure(providerID string) error {
	if in.broken != nil {
		return in.broken
	}
	return oapFailure(in.body(), providerID)
}
