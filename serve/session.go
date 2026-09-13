package serve

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// sessionRegistry owns the hub-side session lifetime. Entries survive
// adapter close (listed with status closed) because a host that closed a
// session may still read its final state; they are dropped only when the Hub
// itself goes away.
type sessionRegistry struct {
	mu       sync.RWMutex
	sessions map[protocol.SessionID]*Session
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[protocol.SessionID]*Session)}
}

func (r *sessionRegistry) get(id protocol.SessionID) (*Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.sessions[id]
	return entry, ok
}

func (r *sessionRegistry) add(entry *Session) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[entry.id]; exists {
		return &SessionExistsError{ID: entry.id}
	}
	r.sessions[entry.id] = entry
	return nil
}

func (r *sessionRegistry) list() []*Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]*Session, 0, len(r.sessions))
	for _, id := range sortedKeys(r.sessions) {
		entries = append(entries, r.sessions[protocol.SessionID(id)])
	}
	return entries
}

// Session is one adapter session hosted by the hub. It forwards the adapter
// Session operations verbatim while draining every admitted run's event
// stream into the hub, so any number of subscriptions can follow a run —
// raw adapter.Session streams are single-consumer — and a subscription that
// falls behind overflows and is signalled instead of stalling the adapter.
// A Session is safe for concurrent use.
type Session struct {
	mu          sync.Mutex
	id          protocol.SessionID
	adapterName string
	session     base.Session
	created     time.Time
	// runID is the run whose stream most recently drove the hub; a replay
	// cursor always maps onto this run.
	runID protocol.RunID
	// closed records a successful adapter close: no further run can drive
	// the hub, so a live subscriber arriving afterwards must not park.
	closed bool
	// readers counts run streams still being drained. A close must not
	// finish subscribers while a reader may still be queueing final events,
	// so it either finishes immediately (no readers) or defers to the last
	// reader's exit.
	readers          int
	finishAfterDrain bool
	subs             map[*subscriber]struct{}
}

func newSession(id protocol.SessionID, adapterName string, session base.Session) *Session {
	return &Session{
		id: id, adapterName: adapterName, session: session,
		created: time.Now(), subs: make(map[*subscriber]struct{}),
	}
}

// ID is the adapter-confirmed session identifier.
func (s *Session) ID() protocol.SessionID { return s.id }

// Adapter is the registry name of the adapter the session runs on.
func (s *Session) Adapter() string { return s.adapterName }

// CreatedAt is when the session was opened through the hub.
func (s *Session) CreatedAt() time.Time { return s.created }

// IsClosed reports whether the session was closed through the hub. The entry
// survives close — State still reports the adapter's final session state —
// but subscriptions are refused.
func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// State reads the adapter's authoritative session state.
func (s *Session) State(ctx context.Context) (protocol.SessionState, error) {
	return s.session.State(ctx)
}

// Submit admits one message submission and returns the adapter's admission.
// The run's events are drained into the hub for delivery to every live
// subscription; a consumer that wants the run's first envelope subscribes
// before submitting. The request's SessionID must name this session.
func (s *Session) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, error) {
	if request.SessionID != s.id {
		return protocol.MessageSubmitResponse{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}
	admission, stream, err := s.session.Submit(ctx, request)
	if err != nil {
		return admission, err
	}
	s.startRun(admission.RunID, stream)
	return admission, nil
}

// Resolve resolves one pending interactive gate. The resolution's inner
// request must name this session; the adapter's own rejection (wrong
// responder, unknown or already-resolved interaction) surfaces verbatim.
func (s *Session) Resolve(ctx context.Context, resolution base.InteractionResolution) error {
	if resolution.Permission != nil && resolution.Permission.SessionID != s.id {
		return &ScopeMismatchError{Payload: resolution.Permission.SessionID, Addressed: s.id}
	}
	if resolution.Input != nil && resolution.Input.SessionID != s.id {
		return &ScopeMismatchError{Payload: resolution.Input.SessionID, Addressed: s.id}
	}
	return s.session.Resolve(ctx, resolution)
}

// Cancel requests cancellation of one run and returns the acknowledgement.
// The confirmed run.cancelled event on the event stream is authoritative.
func (s *Session) Cancel(ctx context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	return s.session.Cancel(ctx, runID)
}

// Close closes the adapter session. An active run refuses the close by
// contract; cancel it first. On success the entry stays listed with its
// final state and subscriptions are refused from then on.
func (s *Session) Close(ctx context.Context) error {
	if err := s.session.Close(ctx); err != nil {
		return err
	}
	s.markClosed()
	return nil
}

// subscriber is one subscription's bounded mailbox. The channel is never
// closed by the producer: a subscriber that must terminate is removed from
// the hub, its terminal state is recorded, and its finish channel is closed;
// the consumer drains whatever envelopes were already queued.
type subscriber struct {
	ch         chan protocol.Envelope
	finish     chan struct{}
	finishOnce sync.Once
	terminal   atomic.Pointer[terminalState]
}

// terminalState is a subscriber's end state, recorded at stop time: the
// overflow signal naming the run that overflowed, or the run stream's
// terminal error. A nil state ends the stream cleanly. Recording the run at
// stop time — not when the consumer gets around to reading the signal —
// keeps the recovery cursor valid even when a newer run is admitted before
// the consumer observes the overflow.
type terminalState struct {
	overflow bool
	run      protocol.RunID
	err      error
}

func newSubscriber(queue int) *subscriber {
	return &subscriber{ch: make(chan protocol.Envelope, queue), finish: make(chan struct{})}
}

// stop terminates the subscriber with its terminal state, if any.
func (sub *subscriber) stop(state *terminalState) {
	if state != nil {
		sub.terminal.Store(state)
	}
	sub.finishOnce.Do(func() { close(sub.finish) })
}

// subscribe registers a live-events mailbox. A subscriber that connects while
// no run is active simply parks until the next submit; one that arrives after
// the session closed is refused — nothing will ever finish it, so parking
// would hang the consumer. The check shares the hub lock with markClosed,
// closing the arrive/finish race.
func (s *Session) subscribe(queue int) (*subscriber, bool) {
	sub := newSubscriber(queue)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, false
	}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()
	return sub, true
}

func (s *Session) unsubscribe(sub *subscriber) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
}

// currentRun reports the run a replay cursor applies to: the run whose stream
// most recently drove the hub, active or already terminal.
func (s *Session) currentRun() (protocol.RunID, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runID, s.runID != ""
}

// startRun points the hub at a newly admitted run and starts draining its
// adapter stream. The drain is unconditional for the whole run lifetime, so
// the adapter's bounded subscriber buffers never fill on the hub side.
func (s *Session) startRun(runID protocol.RunID, stream base.EventStream) {
	s.mu.Lock()
	s.runID = runID
	s.readers++
	s.mu.Unlock()
	go s.readRun(runID, stream)
}

// readRun drains one run's adapter stream. When the stream ends it finishes
// subscribers only when it is the last reader and a newer run has not taken
// over the hub: a stale finishSubs from run N must not terminate subscribers
// already receiving run N+1, and a close that deferred its finish must not
// fire while run N+1's reader still has events queued. A stream that ends on
// an error other than overflow carries that error to every subscriber as its
// terminal error — a failed run must not read as a clean end.
func (s *Session) readRun(runID protocol.RunID, stream base.EventStream) {
	var end *terminalState
	for result := range stream {
		if result.Error != nil {
			if errors.Is(result.Error, base.ErrEventStreamOverflow) {
				s.signalOverflow(runID)
				continue
			}
			// Any other stream error ends delivery for this run; the
			// documented reconnect path is the replay cursor. The stream is
			// still drained so an adapter that reports an error but keeps
			// its channel open cannot block its own later emits.
			end = &terminalState{err: result.Error}
			go func(stream base.EventStream) {
				for range stream {
				}
			}(stream)
			break
		}
		s.publish(result.Envelope)
	}
	s.mu.Lock()
	s.readers--
	current := s.runID
	finish := s.readers == 0 && (current == runID || s.finishAfterDrain)
	s.mu.Unlock()
	if finish {
		s.finishSubs(end)
	}
}

// publish delivers one envelope to every subscriber, terminating (not
// blocking on) subscribers whose queue is full.
func (s *Session) publish(envelope protocol.Envelope) {
	s.mu.Lock()
	for sub := range s.subs {
		select {
		case sub.ch <- envelope:
		default:
			// The overflow is of THIS envelope's run — runs may be draining
			// concurrently, so the session's current run can already be a
			// newer one — and that is the run the recovery cursor must name,
			// whatever run is current by the time the consumer reads the
			// signal.
			delete(s.subs, sub)
			sub.stop(&terminalState{overflow: true, run: envelope.RunID})
		}
	}
	s.mu.Unlock()
}

// signalOverflow terminates every subscriber with the overflow signal after
// the adapter itself reported an event-stream overflow on runID.
func (s *Session) signalOverflow(runID protocol.RunID) {
	s.finishSubs(&terminalState{overflow: true, run: runID})
}

// finishSubs detaches every subscriber and terminates it with the given
// terminal state, or cleanly when the state is nil.
func (s *Session) finishSubs(state *terminalState) {
	s.mu.Lock()
	subs := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = make(map[*subscriber]struct{})
	s.mu.Unlock()
	for _, sub := range subs {
		sub.stop(state)
	}
}

// markClosed records a successful adapter close and ends every live stream.
// An adapter reports its run terminal once the terminal envelope is queued,
// not once a consumer drained it, so a reader may still hold final events:
// with no reader draining, subscribers finish immediately; otherwise the
// finish waits for the last reader so no terminal envelope is lost.
func (s *Session) markClosed() {
	s.mu.Lock()
	s.closed = true
	if s.readers > 0 {
		s.finishAfterDrain = true
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.finishSubs(nil)
}

// closeForShutdown cancels any active run and then closes the adapter
// session: active runs refuse Close by contract, so shutdown settles them
// through Cancel where the adapter supports it. Some adapters acknowledge a
// cancel before the run settles, so the refused Close is retried briefly — a
// shutdown that gave up here would leave the session's child process
// orphaned.
func (s *Session) closeForShutdown(ctx context.Context) error {
	err := s.session.Close(ctx)
	for attempt := 0; errors.Is(err, base.ErrRunActive) && attempt < 3; attempt++ {
		if ctx.Err() != nil {
			break
		}
		state, stateErr := s.session.State(ctx)
		if stateErr == nil && state.ActiveRunID != "" {
			_, _ = s.session.Cancel(ctx, state.ActiveRunID)
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		err = s.session.Close(ctx)
	}
	if err == nil {
		s.markClosed()
	}
	return err
}
