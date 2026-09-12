package servehttp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// SSE stream names for daemon-side terminal signals. They are transport
// framing, not OAP envelopes: data carries a small JSON object documenting
// the condition and the reconnect cursor.
const (
	sseEventOverflow  = "oap-overflow"
	sseEventReplayGap = "oap-replay-gap"
)

// streamSubscription serves one SSE connection from a hub subscription until
// it ends at a run's terminal event, overflows, or the request context ends.
// The overflow signal names the run current at signal time and the last
// sequence this connection delivered, so a reconnect resumes exactly where
// the consumer stopped.
func (s *Server) streamSubscription(w io.Writer, flusher http.Flusher, subscription *serve.Subscription) {
	for {
		envelope, err := subscription.Next()
		if err != nil {
			var overflow *serve.OverflowError
			if errors.As(err, &overflow) {
				writeSSESignal(w, flusher, sseEventOverflow, map[string]any{
					"run_id":        string(overflow.RunID),
					"last_sequence": overflow.LastSequence,
					"message":       "event stream consumer fell behind; reconnect with a cursor after this sequence",
				})
			}
			// Any other end — the clean io.EOF at terminality, a client
			// write failure, the request context, or a stream error whose
			// documented recovery is the replay cursor — simply ends the
			// response.
			return
		}
		if err := writeSSE(w, envelope); err != nil {
			return
		}
		flusher.Flush()
	}
}

// writeSSEGap terminates an SSE response with the documented replay-gap
// signal after the adapter reported the requested cursor as expired.
func writeSSEGap(w io.Writer, flusher http.Flusher, gap *base.ReplayGap) {
	writeSSESignal(w, flusher, sseEventReplayGap, map[string]any{
		"requested_after":  gap.RequestedAfter,
		"oldest_available": gap.OldestAvailable,
		"latest_available": gap.LatestAvailable,
		"message":          "requested replay cursor is no longer retained; reconnect with a cursor at or after oldest_available - 1",
	})
}

func writeSSESignal(w io.Writer, flusher http.Flusher, event string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	flusher.Flush()
}

func writeSSE(w io.Writer, envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if envelope.Sequence != nil {
		if _, err := fmt.Fprintf(w, "id: %s\n", strconv.FormatUint(*envelope.Sequence, 10)); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func startSSE(w http.ResponseWriter, flusher http.Flusher) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
}
