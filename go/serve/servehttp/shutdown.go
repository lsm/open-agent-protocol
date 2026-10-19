package servehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

const drainPoll = 20 * time.Millisecond

var admittingRoutes = []string{"/adapters/*/sessions", "/sessions/*/submit", "/adapters/*/work", "/work/sessions/*/send"}

type stopState struct {
	admission sync.RWMutex
	mu        sync.Mutex
	mode      protocol.ShutdownMode
	watching  bool
	finished  bool
	withdrawn map[protocol.RunID]bool
	done      chan struct{}
}

func newStopState() *stopState {
	return &stopState{done: make(chan struct{}), withdrawn: map[protocol.RunID]bool{}}
}

func (st *stopState) finishLocked() {
	if !st.finished {
		st.finished = true
		close(st.done)
	}
}

func (s *Server) Done() <-chan struct{} { return s.stopping.done }

func (s *Server) Forced() bool {
	s.stopping.mu.Lock()
	defer s.stopping.mu.Unlock()
	return s.stopping.mode == protocol.ShutdownForce
}

func admitting(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	for _, pattern := range admittingRoutes {
		if matched, _ := path.Match(pattern, r.URL.Path); matched {
			return true
		}
	}
	return false
}

func (s *Server) refuseWhileStopping(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !admitting(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.stopping.admission.RLock()
		defer s.stopping.admission.RUnlock()
		s.stopping.mu.Lock()
		accepted := s.stopping.mode != ""
		s.stopping.mu.Unlock()
		if accepted {
			s.writeError(w, http.StatusServiceUnavailable, "shutting_down", "the endpoint is shutting down and admits no new work", protocol.Envelope{})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "a request with a body declares application/json; the daemon reads no other media type", protocol.Envelope{})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the daemon limit", protocol.Envelope{})
		} else {
			s.writeError(w, http.StatusBadRequest, "request_read", "request body could not be read", protocol.Envelope{})
		}
		return
	}
	if len(bytes.TrimSpace(body)) > 0 && !json.Valid(body) {
		s.writeError(w, http.StatusBadRequest, "malformed_json", "the request body is not JSON", protocol.Envelope{})
		return
	}
	asked, ok := serve.AskedShutdown(body)
	if !ok {
		s.writeError(w, http.StatusBadRequest, "invalid_request", "request is an object whose only member is mode, graceful or force", protocol.Envelope{})
		return
	}
	writeJSON(w, http.StatusOK, s.acceptShutdown(context.WithoutCancel(r.Context()), asked))
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

func (s *Server) awaitDrained(ctx context.Context, st *stopState) {
	ticker := time.NewTicker(drainPoll)
	defer ticker.Stop()
	for {
		select {
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
