package serve

import (
	"context"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type Admitted struct {
	Type      protocol.EnvelopeType
	Payload   any
	SessionID protocol.SessionID
	RunID     protocol.RunID
}

type Admit func(context.Context, *Session) (Admitted, error)

func Admission(envelope protocol.Envelope) (Admit, error) {
	if envelope.Type == protocol.TypeSessionCompactRequest {
		var request protocol.SessionCompactRequest
		if err := envelope.DecodePayload(&request); err != nil {
			return nil, err
		}
		return func(ctx context.Context, entry *Session) (Admitted, error) {
			admission, err := entry.Compact(ctx, base.CompactRequest{Request: request, EnvelopeID: envelope.ID})
			return Admitted{Type: protocol.TypeSessionCompactResponse, Payload: admission, SessionID: admission.SessionID, RunID: admission.RunID}, err
		}, nil
	}
	var request protocol.MessageSubmitRequest
	if err := envelope.DecodePayload(&request); err != nil {
		return nil, err
	}
	return func(ctx context.Context, entry *Session) (Admitted, error) {
		admission, err := entry.Submit(ctx, base.SubmitRequest{Request: request, EnvelopeID: envelope.ID})
		return Admitted{Type: protocol.TypeSessionMessageSubmitResponse, Payload: admission, SessionID: admission.SessionID, RunID: admission.RunID}, err
	}, nil
}
