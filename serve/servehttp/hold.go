package servehttp

import (
	"context"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
	"github.com/lsm/open-agent-protocol/serve"
)

const DefaultSubscriptionHold = 30 * time.Second

type heldSubscription struct {
	subscription *serve.Subscription
	cancel       context.CancelFunc
	expiry       *time.Timer
}

func (h *heldSubscription) discard() {
	if h.expiry != nil {
		h.expiry.Stop()
	}
	h.subscription.Close()
	h.cancel()
}

func (s *Server) holdSubscription(id protocol.SessionID, subscription *serve.Subscription, cancel context.CancelFunc) {
	held := &heldSubscription{subscription: subscription, cancel: cancel}
	held.expiry = time.AfterFunc(s.holdFor, func() { s.expireHeld(id, held) })
	s.mu.Lock()
	previous := s.held[id]
	s.held[id] = held
	s.mu.Unlock()
	if previous != nil {
		previous.discard()
	}
}

func (s *Server) takeHeld(id protocol.SessionID) *heldSubscription {
	s.mu.Lock()
	held := s.held[id]
	delete(s.held, id)
	s.mu.Unlock()
	if held != nil && held.expiry != nil {
		held.expiry.Stop()
	}
	return held
}

func (s *Server) expireHeld(id protocol.SessionID, held *heldSubscription) {
	s.mu.Lock()
	current, ok := s.held[id]
	if ok && current == held {
		delete(s.held, id)
	} else {
		held = nil
	}
	s.mu.Unlock()
	if held != nil {
		held.subscription.Close()
		held.cancel()
	}
}

func (s *Server) releaseHeld() {
	s.mu.Lock()
	held := s.held
	s.held = map[protocol.SessionID]*heldSubscription{}
	s.mu.Unlock()
	for _, entry := range held {
		entry.discard()
	}
}
