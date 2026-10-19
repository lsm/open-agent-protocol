package servestdio

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

const opShutdown = "shutdown"

const drainPoll = 20 * time.Millisecond

type shutdownState struct {
	admission   sync.RWMutex
	mu          sync.Mutex
	mode        protocol.ShutdownMode
	watching    bool
	flushed     bool
	finished    bool
	served      bool
	done        chan struct{}
	signalsSeen int
}

func newShutdownState() *shutdownState {
	return &shutdownState{done: make(chan struct{})}
}

func (st *shutdownState) accepted() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.mode != ""
}

func (st *shutdownState) drained() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.finished && st.flushed && st.mode == protocol.ShutdownGraceful
}

func (st *shutdownState) finishLocked() {
	if !st.finished {
		st.finished = true
		close(st.done)
	}
}

func admitsWork(op string) bool {
	switch op {
	case opSubmit, opOpen, opWorkStart, opWorkSend:
		return true
	}
	return false
}

func (s *Server) shutdownOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	asked, ok := serve.AskedShutdown(request.Request)
	if !ok {
		return nil, &wireError{Code: "invalid_request", Message: "request is an object whose only member is mode, graceful or force"}
	}
	st := s.stopState()
	st.admission.Lock()
	defer st.admission.Unlock()
	runs := s.hub.NonterminalRuns(ctx)
	st.mu.Lock()
	defer st.mu.Unlock()
	mode := asked
	if mode == "" {
		mode = st.mode
	}
	if mode == "" {
		if len(runs) > 0 {
			return marshalResult(protocol.EndpointShutdownResponse{Outcome: protocol.ShutdownChoose, ActiveRuns: runs})
		}
		mode = protocol.ShutdownGraceful
	}
	if st.mode != protocol.ShutdownForce {
		st.mode = mode
	}
	var lost []protocol.ShutdownQueuedRun
	if st.mode == protocol.ShutdownGraceful {
		kept := []protocol.ShutdownRun{}
		for _, run := range runs {
			if run.Status == protocol.RunQueued && s.hub.CancelQueued(ctx, run) {
				lost = append(lost, protocol.ShutdownQueuedRun{SessionID: run.SessionID, RunID: run.RunID})
				continue
			}
			kept = append(kept, run)
		}
		runs = kept
	}
	outcome := protocol.ShutdownStopping
	if st.mode == protocol.ShutdownGraceful && len(runs) > 0 {
		outcome = protocol.ShutdownDraining
		if !st.watching {
			st.watching = true
			go s.awaitDrained(ctx, st)
		}
	} else {
		if st.watching {
			st.flushed = true
		}
		st.finishLocked()
	}
	return marshalResult(protocol.EndpointShutdownResponse{Outcome: outcome, ActiveRuns: runs, Lost: lost})
}

func (s *Server) awaitDrained(ctx context.Context, st *shutdownState) {
	ticker := time.NewTicker(drainPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-st.done:
			return
		case <-ticker.C:
		}
		runs := s.hub.NonterminalRuns(ctx)
		if len(runs) == 0 {
			st.mu.Lock()
			st.flushed = true
			st.finishLocked()
			st.mu.Unlock()
			return
		}
		for _, run := range runs {
			if run.Status == protocol.RunQueued {
				s.hub.CancelQueued(ctx, run)
			}
		}
	}
}

func (s *Server) Signal(ctx context.Context, count int) (protocol.EndpointShutdownResponse, bool) {
	wanted := protocol.ShutdownGraceful
	if count >= 2 {
		wanted = protocol.ShutdownForce
	}
	ctx = context.WithoutCancel(ctx)
	st := s.stopState()
	st.mu.Lock()
	current := st.mode
	fresh := count > st.signalsSeen
	if fresh {
		st.signalsSeen = count
	}
	st.mu.Unlock()
	if current == protocol.ShutdownForce {
		wanted = protocol.ShutdownForce
	}
	if !fresh {
		return protocol.EndpointShutdownResponse{}, false
	}
	result, werr := s.shutdownOp(ctx, requestLine{Op: opShutdown, Request: json.RawMessage(`{"mode":"` + string(wanted) + `"}`)})
	if werr != nil {
		return protocol.EndpointShutdownResponse{}, false
	}
	var answer protocol.EndpointShutdownResponse
	if err := json.Unmarshal(result, &answer); err != nil {
		return protocol.EndpointShutdownResponse{}, false
	}
	st.mu.Lock()
	overtaken := st.mode != wanted
	st.mu.Unlock()
	return answer, !overtaken
}

func (s *Server) stopState() *shutdownState {
	s.stoppingMu.Lock()
	defer s.stoppingMu.Unlock()
	return s.stopping
}
