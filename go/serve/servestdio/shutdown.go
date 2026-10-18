package servestdio

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const opShutdown = "shutdown"

const drainPoll = 20 * time.Millisecond

type shutdownState struct {
	admission sync.RWMutex
	mu        sync.Mutex
	mode      protocol.ShutdownMode
	watching  bool
	flushed   bool
	finished  bool
	done      chan struct{}
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

func askedShutdown(raw json.RawMessage) (protocol.ShutdownMode, bool) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true
	}
	var asked struct {
		Mode *protocol.ShutdownMode `json:"mode"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&asked); err != nil {
		return "", false
	}
	if asked.Mode == nil {
		return "", true
	}
	switch *asked.Mode {
	case protocol.ShutdownGraceful, protocol.ShutdownForce:
		return *asked.Mode, true
	}
	return "", false
}

func (s *Server) nonterminalRuns(ctx context.Context) []protocol.ShutdownRun {
	runs := []protocol.ShutdownRun{}
	for _, status := range s.hub.Sessions(ctx) {
		for _, run := range status.ActiveRuns {
			runs = append(runs, protocol.ShutdownRun{SessionID: status.SessionID, RunID: run.RunID, Status: run.Status, PendingInteractions: run.PendingInteractions})
		}
		if len(status.ActiveRuns) > 0 || status.ActiveRunID == "" {
			continue
		}
		state := protocol.RunRunning
		switch status.Status {
		case protocol.SessionQueued:
			state = protocol.RunQueued
		case protocol.SessionWaitingForInput:
			state = protocol.RunWaitingForInput
		}
		runs = append(runs, protocol.ShutdownRun{SessionID: status.SessionID, RunID: status.ActiveRunID, Status: state})
	}
	return runs
}

func (s *Server) shutdownOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	asked, ok := askedShutdown(request.Request)
	if !ok {
		return nil, &wireError{Code: "invalid_request", Message: "request is an object whose only member is mode, graceful or force"}
	}
	st := s.stopping
	st.admission.Lock()
	defer st.admission.Unlock()
	runs := s.nonterminalRuns(ctx)
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
			if run.Status == protocol.RunQueued && s.cancelQueued(ctx, run) {
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
		st.finishLocked()
	}
	return marshalResult(protocol.EndpointShutdownResponse{Outcome: outcome, ActiveRuns: runs, Lost: lost})
}

func (s *Server) cancelQueued(ctx context.Context, run protocol.ShutdownRun) bool {
	entry, err := s.hub.Session(run.SessionID)
	if err != nil {
		return false
	}
	_, err = entry.Cancel(ctx, run.RunID)
	return err == nil
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
		runs := s.nonterminalRuns(ctx)
		if len(runs) == 0 {
			st.mu.Lock()
			st.flushed = true
			st.finishLocked()
			st.mu.Unlock()
			return
		}
		for _, run := range runs {
			if run.Status == protocol.RunQueued {
				s.cancelQueued(ctx, run)
			}
		}
	}
}
