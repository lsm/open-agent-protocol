package copilot

import (
	"context"
	"sync"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/native"
	"github.com/lsm/open-agent-protocol/go/adapter/copilot/internal/rpc"
	"github.com/lsm/open-agent-protocol/go/adapter/internal/journal"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type session struct {
	mu       sync.Mutex
	reducer  *reducer
	process  *rpc.Process
	journal  *journal.Journal
	timeout  time.Duration
	closed   bool
	unusable bool
	done     chan struct{}
}

func newSession(r *reducer, p *rpc.Process, capacity int, timeout time.Duration) *session {
	s := &session{reducer: r, process: p, journal: journal.New(capacity), timeout: timeout, done: make(chan struct{})}
	go s.pump()
	return s
}

func (s *session) NativeSessionID() string { return s.reducer.nativeID }

func (s *session) State(ctx context.Context) (protocol.SessionState, error) {
	if err := ctx.Err(); err != nil {
		return protocol.SessionState{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unusable {
		return protocol.SessionState{}, base.ErrSessionClosed
	}
	return s.state(), nil
}

func (s *session) state() protocol.SessionState {
	state := protocol.SessionState{SessionID: s.reducer.sessionID, Status: protocol.SessionIdle}
	position := 0
	for _, run := range s.reducer.runs {
		if run.terminal {
			continue
		}
		seq := run.next - 1
		active := protocol.ActiveRun{RunID: run.id, Status: run.status, Relationship: protocol.RelationshipPrimary, AsOfSequence: &seq}
		if run.status == protocol.RunQueued {
			position++
			q := position
			active.QueuePosition = &q
		} else {
			state.ActiveRunID = run.id
		}
		state.ActiveRuns = append(state.ActiveRuns, active)
	}
	if state.ActiveRunID != "" {
		state.Status = protocol.SessionRunning
	} else if len(state.ActiveRuns) > 0 {
		state.Status = protocol.SessionQueued
	}
	return state
}

func (s *session) Resume(ctx context.Context, request base.ResumeRequest) (base.Recovery, base.EventStream, error) {
	if err := ctx.Err(); err != nil {
		return base.Recovery{}, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return base.Recovery{}, nil, base.ErrSessionClosed
	}
	for _, run := range s.reducer.runs {
		if run.id != request.RunID {
			continue
		}
		latest := run.next - 1
		if request.AfterSequence > latest {
			return base.Recovery{}, nil, base.ErrReplayCursorFuture
		}
		return s.journal.Resume(s.state(), run.id, request.AfterSequence, latest)
	}
	return base.Recovery{}, nil, base.ErrRunNotFound
}

func (s *session) Close(context.Context) error {
	s.mu.Lock()
	s.end("copilot_closed", "the Copilot session closed")
	s.closed = true
	s.mu.Unlock()
	_ = s.process.Close()
	<-s.done
	s.journal.Close()
	return nil
}

func (s *session) pump() {
	defer close(s.done)
	client := s.process.Client
	for {
		select {
		case message := <-client.Notifications():
			s.consume(message)
		case <-client.Done():
			_ = s.process.Close()
			for len(client.Notifications()) > 0 {
				s.consume(<-client.Notifications())
			}
			s.mu.Lock()
			s.end("copilot_process_exit", "the Copilot server closed its RPC connection")
			s.mu.Unlock()
			return
		}
	}
}

func (s *session) consume(message native.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.unusable {
		return
	}
	event, err := native.DecodeEvent(message)
	if err == nil && event != nil {
		err = s.reducer.observe(*event)
	}
	if err != nil {
		s.end("copilot_invalid_event", "the Copilot server sent an invalid session event")
		_ = s.process.Client.Close()
	}
	s.flush()
}

func (s *session) end(code, message string) {
	if s.closed || s.unusable {
		return
	}
	s.unusable = true
	for _, run := range s.reducer.runs {
		if !run.terminal {
			_ = s.reducer.fail(run, code, message)
		}
	}
	s.flush()
}

func (s *session) flush() {
	for _, event := range s.reducer.events {
		s.journal.Append(event, event.Type == protocol.TypeRunCompleted || event.Type == protocol.TypeRunFailed || event.Type == protocol.TypeRunCancelled)
	}
	s.reducer.events = nil
}
