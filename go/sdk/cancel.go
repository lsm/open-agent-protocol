package sdk

import (
	"context"
	"errors"
	"time"
)

const (
	drainIdle   = 50 * time.Millisecond
	drainBudget = 250 * time.Millisecond
)

func cancelStream(t *transport, streamID string) {
	t.sendBestEffort(&frame{
		Type:      "abort_request",
		StreamID:  streamID,
		MessageID: newULID(),
		Sequence:  2,
		Timestamp: time.Now().UnixMilli(),
		Version:   envelopeVersion,
		Payload:   mustMarshal(map[string]any{"target_stream_id": streamID, "reason": "client aborted"}),
	})
}

func stopAgent(t *transport, sessionID string, sequence int64, reason string) string {
	envelope := newSessionEnvelope("agent_stop", sessionID, sequence, map[string]any{
		"session_id": sessionID,
		"reason":     reason,
	})
	t.sendBestEffort(envelope)
	return envelope.MessageID
}

func isAbort(err error) bool {
	var streamErr *StreamError
	if errors.As(err, &streamErr) && streamErr.Kind == KindAborted {
		return true
	}
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func asStreamError(err error, target **StreamError) bool { return errors.As(err, target) }
