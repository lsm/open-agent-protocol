package serveendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

const (
	controlReplay         = "replay"
	controlReplayAccepted = "replay.accepted"
	controlReplayGap      = "replay.gap"
	controlReplayError    = "replay.error"
	controlStreamLost     = "stream.lost"
)

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

func (s *Server) writeControl(ctx context.Context, frame controlFrame) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return s.send(ctx, append(data, '\n'))
}

func (s *Server) handleControl(streams context.Context, frame controlFrame) error {
	if frame.Control != controlReplay {
		return s.writeControl(streams, controlFrame{
			Control: controlReplayError, ID: frame.ID, Code: "unsupported_control",
			Message: fmt.Sprintf("this endpoint serves no %q control", frame.Control),
		})
	}
	return s.replay(streams, frame)
}

func (s *Server) replay(streams context.Context, frame controlFrame) error {
	if frame.SessionID == "" {
		return s.writeControl(streams, controlFrame{
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
		return s.writeControl(streams, s.replayFailure(frame, err))
	}
	subscription, err := s.hub.Subscribe(streams, entry.ID(), serve.After(frame.RunID, after))
	if err != nil {
		return s.writeControl(streams, s.replayFailure(frame, err))
	}

	runID := subscription.RunID()
	if runID == "" {
		runID = frame.RunID
	}
	if err := s.writeControl(streams, controlFrame{
		Control: controlReplayAccepted, ID: frame.ID, SessionID: entry.ID(), RunID: runID, After: &after,
	}); err != nil {
		subscription.Close()
		return err
	}
	s.pumps.Add(1)
	go func() {
		defer s.pumps.Done()
		s.pump(streams, subscription, runID)
	}()
	return nil
}

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
