package serveendpoint

import (
	"context"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

const drainPoll = 20 * time.Millisecond

type stopState struct {
	admission   sync.RWMutex
	mu          sync.Mutex
	mode        protocol.ShutdownMode
	watching    bool
	finished    bool
	withdrawn   map[protocol.RunID]bool
	done        chan struct{}
	signalsSeen int
}

func newStopState() *stopState {
	return &stopState{done: make(chan struct{}), withdrawn: map[protocol.RunID]bool{}}
}

func (st *stopState) accepted() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.mode != ""
}

func (st *stopState) forced() bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.mode == protocol.ShutdownForce
}

func (st *stopState) finishLocked() {
	if !st.finished {
		st.finished = true
		close(st.done)
	}
}

func admitsWork(t protocol.EnvelopeType) bool {
	switch t {
	case protocol.TypeSessionOpenRequest, protocol.TypeSessionMessageSubmitRequest, protocol.TypeSessionCompactRequest:
		return true
	}
	return false
}

func (s *Server) shutdownRequest(ctx context.Context, e protocol.Envelope) (protocol.Envelope, error) {
	asked, ok := serve.AskedShutdown(e.Payload)
	if !ok {
		return protocol.Envelope{}, &refusal{code: "invalid_payload", message: "endpoint.shutdown.request carries only mode, graceful or force"}
	}
	envelope, err := protocol.NewEnvelope(protocol.TypeEndpointShutdownResponse, s.nextID("response"), s.acceptShutdown(ctx, asked))
	if err != nil {
		return protocol.Envelope{}, err
	}
	envelope.InReplyTo = e.ID
	envelope.CapabilityRevision = e.CapabilityRevision
	return envelope, nil
}

func (s *Server) acceptShutdown(ctx context.Context, asked protocol.ShutdownMode) protocol.EndpointShutdownResponse {
	st := s.stopping
	st.admission.Lock()
	defer st.admission.Unlock()
	runs := s.hub.NonterminalRuns(ctx)
	st.mu.Lock()
	defer st.mu.Unlock()
	mode := asked
	if mode == "" {
		mode = st.mode
	}
	if mode == "" && len(runs) > 0 {
		return protocol.EndpointShutdownResponse{Outcome: protocol.ShutdownChoose, ActiveRuns: runs}
	}
	if mode == "" {
		mode = protocol.ShutdownGraceful
	}
	if st.mode != protocol.ShutdownForce {
		st.mode = mode
	}
	answer := protocol.EndpointShutdownResponse{Outcome: protocol.ShutdownStopping, ActiveRuns: runs}
	if st.mode == protocol.ShutdownGraceful {
		kept := []protocol.ShutdownRun{}
		for _, run := range runs {
			if st.withdrawn[run.RunID] {
				continue
			}
			if run.Status == protocol.RunQueued && s.hub.CancelQueued(ctx, run) {
				st.withdrawn[run.RunID] = true
				answer.Lost = append(answer.Lost, protocol.ShutdownQueuedRun{SessionID: run.SessionID, RunID: run.RunID})
				continue
			}
			kept = append(kept, run)
		}
		answer.ActiveRuns = kept
	}
	if st.mode == protocol.ShutdownGraceful && len(answer.ActiveRuns) > 0 {
		answer.Outcome = protocol.ShutdownDraining
		if !st.watching {
			st.watching = true
			go s.awaitDrained(ctx, st)
		}
		return answer
	}
	st.finishLocked()
	return answer
}

func (s *Server) Signal(ctx context.Context, count int) (protocol.EndpointShutdownResponse, bool) {
	ctx = context.WithoutCancel(ctx)
	wanted := protocol.ShutdownGraceful
	if count >= 2 {
		wanted = protocol.ShutdownForce
	}
	s.stopping.mu.Lock()
	current := s.stopping.mode
	fresh := count > s.stopping.signalsSeen
	if fresh {
		s.stopping.signalsSeen = count
	}
	s.stopping.mu.Unlock()
	if current == protocol.ShutdownForce {
		wanted = protocol.ShutdownForce
	}
	if !fresh {
		return protocol.EndpointShutdownResponse{}, false
	}
	answer := s.acceptShutdown(ctx, wanted)
	s.stopping.mu.Lock()
	overtaken := s.stopping.mode != wanted
	s.stopping.mu.Unlock()
	return answer, !overtaken
}

func (s *Server) awaitDrained(ctx context.Context, st *stopState) {
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
		st.mu.Lock()
		left := 0
		for _, run := range s.hub.NonterminalRuns(ctx) {
			if st.withdrawn[run.RunID] {
				continue
			}
			if run.Status == protocol.RunQueued && s.hub.CancelQueued(ctx, run) {
				st.withdrawn[run.RunID] = true
				continue
			}
			left++
		}
		if left == 0 {
			st.finishLocked()
		}
		st.mu.Unlock()
		if left == 0 {
			return
		}
	}
}
