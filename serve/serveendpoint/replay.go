package serveendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

// The control frames this binding defines. A cursor is a fact about one
// consumer's position in a stream rather than about the agent loop's state,
// which is why it rides the transport here exactly as it rides the query
// string on the HTTP binding, and why v0.1 has no envelope for it.
const (
	controlReplay         = "replay"
	controlReplayAccepted = "replay.accepted"
	controlReplayGap      = "replay.gap"
	controlReplayError    = "replay.error"
	controlStreamLost     = "stream.lost"
)

// controlFrame is one binding control line in either direction. A frame is
// distinguished from an envelope by carrying Control and no protocol member.
type controlFrame struct {
	Control   string             `json:"control"`
	ID        string             `json:"id,omitempty"`
	SessionID protocol.SessionID `json:"session_id,omitempty"`
	RunID     protocol.RunID     `json:"run_id,omitempty"`
	After     *uint64            `json:"after,omitempty"`

	RequestedAfter  uint64 `json:"requested_after,omitempty"`
	OldestAvailable uint64 `json:"oldest_available,omitempty"`
	LatestAvailable uint64 `json:"latest_available,omitempty"`

	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// writeControl serialises one control frame through the same single writer the
// envelopes use, so a frame and an envelope cannot interleave mid-line.
func (s *Server) writeControl(frame controlFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.out.Write(append(data, '\n')); err != nil {
		return err
	}
	return s.out.Flush()
}

// handleControl serves one control frame. Unlike an envelope request, a
// control frame is answered with a control frame: the two vocabularies stay
// separate so a host can route a line on its shape alone.
func (s *Server) handleControl(streams context.Context, frame controlFrame) error {
	if frame.Control != controlReplay {
		return s.writeControl(controlFrame{
			Control: controlReplayError, ID: frame.ID, Code: "unsupported_control",
			Message: fmt.Sprintf("this endpoint serves no %q control", frame.Control),
		})
	}
	return s.replay(streams, frame)
}

// replay re-delivers one run's retained events after a cursor and then
// continues live.
//
// A cursor the endpoint no longer retains is reported as a gap carrying the
// window that is still available, never as a partial stream: an endpoint that
// silently started later would hand the host a sequence hole it has no way to
// detect.
func (s *Server) replay(streams context.Context, frame controlFrame) error {
	if frame.SessionID == "" {
		return s.writeControl(controlFrame{
			Control: controlReplayError, ID: frame.ID, Code: "invalid_request",
			Message: "a replay must name its session",
		})
	}
	after := uint64(0)
	if frame.After != nil {
		after = *frame.After
	}
	entry, err := s.hub.Session(frame.SessionID)
	if err != nil {
		return s.writeControl(s.replayFailure(frame, err))
	}
	subscription, err := s.hub.Subscribe(streams, entry.ID(), serve.After(frame.RunID, after))
	if err != nil {
		return s.writeControl(s.replayFailure(frame, err))
	}
	// The acknowledgement names the run the subscription actually resolved
	// onto, not the session's active run. They differ exactly when replay
	// matters most: a settled run is no longer active, so reading the state
	// here would answer with no run at all for every replay after a terminal.
	runID := subscription.RunID()
	if runID == "" {
		runID = frame.RunID
	}
	if err := s.writeControl(controlFrame{
		Control: controlReplayAccepted, ID: frame.ID, SessionID: entry.ID(), RunID: runID, After: &after,
	}); err != nil {
		subscription.Close()
		return err
	}
	s.pumps.Add(1)
	go func() {
		defer s.pumps.Done()
		s.pump(subscription)
	}()
	return nil
}

// replayFailure maps a refused replay onto the frame that reports it. A gap
// keeps its own shape because its window is the actionable part.
func (s *Server) replayFailure(frame controlFrame, err error) controlFrame {
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		return controlFrame{
			Control: controlReplayGap, ID: frame.ID, SessionID: frame.SessionID, RunID: frame.RunID,
			RequestedAfter: gap.RequestedAfter, OldestAvailable: gap.OldestAvailable, LatestAvailable: gap.LatestAvailable,
			Message: "the requested replay cursor is no longer retained; ask again from oldest_available - 1",
		}
	}
	code := "internal"
	switch {
	case errors.Is(err, serve.ErrUnknownSession):
		code = "unknown_session"
	case errors.Is(err, base.ErrSessionClosed):
		code = "session_closed"
	case errors.Is(err, serve.ErrNoRunToResume):
		code = "no_run_to_resume"
	case errors.Is(err, base.ErrRunNotFound):
		code = "run_not_found"
	case errors.Is(err, base.ErrReplayCursorFuture):
		code = "replay_cursor_future"
	}
	return controlFrame{
		Control: controlReplayError, ID: frame.ID, SessionID: frame.SessionID, RunID: frame.RunID,
		Code: code, Message: err.Error(),
	}
}
