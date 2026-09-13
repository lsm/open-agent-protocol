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
	// readers counts run streams still being drained; reservations count
	// Submit admissions in flight (a reservation bridges an old reader's
	// exit to the new run's start, so a zero-reader detach cannot land
	// between the adapter admitting a run and the hub recording it).
	// pendingEnd is the current run's terminal state once its own drainer
	// exited into company, and finishDue marks a finish a real reader's
	// exit (or a close) would have fired but a reservation holds, owed to
	// exactly the deferred cohort — the subscribers subscribed when the
	// finish was deferred. Subscribers registering inside the reservation
	// window are owed nothing (no reader existed to publish to them) and
	// stay parked for the resolved admission or the next submit.
	readers      int
	reservations int
	finishDue    bool
	pendingEnd   *terminalState
	deferred     []*subscriber
	subs         map[*subscriber]struct{}
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
	// Reserve against the zero-reader detach while the adapter decides: it
	// may admit this run while an old one's reader is still exiting, and
	// that detach must not run between the admission and the hub recording
	// the new run — subscribers attached before the resubmit would take the
	// old run's end and never see the new one.
	s.mu.Lock()
	s.reservations++
	s.mu.Unlock()
	admission, stream, err := s.session.Submit(ctx, request)
	if err != nil {
		s.releaseReservation()
		return admission, err
	}
	s.adoptRun(admission.RunID, stream)
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
	// attached names the run current when the subscriber registered;
	// lastRun tracks the tail of its mailbox (the run of the last envelope
	// enqueued, which a queue-full recovery cursor names); ack the run of
	// the last envelope the consumer actually received through Next; and
	// pending the runs with envelopes still sitting in the mailbox. An
	// adapter-reported overflow terminates the subscriber when the
	// overflowing run is its acknowledged or attached position — or still
	// has undelivered envelopes ahead of the consumer: mailbox insertion
	// is not observation, but an undelivered envelope of the run means the
	// consumer's view of it is not yet complete either.
	attached protocol.RunID
	lastRun  protocol.RunID
	ack      atomic.Pointer[protocol.RunID]

	pendMu  sync.Mutex
	pending map[protocol.RunID]int
}

// acknowledge records the run of an envelope the consumer received and
// retires one of its pending mailbox entries; the two steps share the
// pending lock so the hub's exposure decision never observes the half-done
// state.
func (sub *subscriber) acknowledge(run protocol.RunID) {
	sub.pendMu.Lock()
	if sub.pending[run] > 0 {
		sub.pending[run]--
	}
	if current := sub.ack.Load(); current == nil || *current != run {
		sub.ack.Store(&run)
	}
	sub.pendMu.Unlock()
}

// track records an envelope of run enqueued to the mailbox.
func (sub *subscriber) track(run protocol.RunID) {
	sub.pendMu.Lock()
	sub.pending[run]++
	sub.pendMu.Unlock()
}

// position reports the run the subscriber has acknowledged consuming, or —
// while it has received nothing — the run it attached under. A consumer
// whose acknowledged position has moved past a run has seen that run
// complete (its terminal envelope precedes any newer run's events), so an
// overflow from the older run must not cut it off from the newer one it is
// actually consuming.
func (sub *subscriber) position() protocol.RunID {
	if ack := sub.ack.Load(); ack != nil {
		return *ack
	}
	return sub.attached
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

func newSubscriber(queue int, attached protocol.RunID) *subscriber {
	return &subscriber{
		ch: make(chan protocol.Envelope, queue), finish: make(chan struct{}),
		attached: attached, pending: make(map[protocol.RunID]int),
	}
}

// stop terminates the subscriber with its terminal state, if any. The
// first terminal wins: a subscriber may sit in a deferred cohort that is
// stopped again after an overflow already signalled it, and the later stop
// must not replace the recovery cursor the consumer is entitled to.
func (sub *subscriber) stop(state *terminalState) {
	if state != nil {
		sub.terminal.CompareAndSwap(nil, state)
	}
	sub.finishOnce.Do(func() { close(sub.finish) })
}

// subscribe registers a live-events mailbox. A subscriber that connects while
// no run is active simply parks until the next submit; one that arrives after
// the session closed is refused — nothing will ever finish it, so parking
// would hang the consumer. The check shares the hub lock with markClosed,
// closing the arrive/finish race.
func (s *Session) subscribe(queue int) (*subscriber, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	sub := newSubscriber(queue, s.runID)
	s.subs[sub] = struct{}{}
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
	s.readers++
	s.runID = runID
	s.pendingEnd, s.finishDue, s.deferred = nil, false, nil
	s.mu.Unlock()
	go s.readRun(runID, stream)
}

// adoptRun converts a Submit reservation into the new run's draining
// reader. Any finish an old reader deferred is superseded: the new run now
// governs the subscribers' outcome.
func (s *Session) adoptRun(runID protocol.RunID, stream base.EventStream) {
	s.mu.Lock()
	s.reservations--
	s.readers++
	s.runID = runID
	s.pendingEnd, s.finishDue, s.deferred = nil, false, nil
	s.mu.Unlock()
	go s.readRun(runID, stream)
}

// releaseReservation unwinds a Submit admission that never became a run.
// Parked subscribers stay parked — the subscribe-before-submit flow must
// survive a rejected submit — unless a real reader's exit (or a close)
// deferred a finish into the reservation and this release is the last one
// holding it, in which case that finish fires for exactly the deferred
// cohort: subscribers that registered inside the reservation window are
// owed nothing and keep waiting for the next submit. A finish deferred
// behind further reservations is retained for whichever release resolves
// last.
func (s *Session) releaseReservation() {
	s.mu.Lock()
	s.reservations--
	due := s.finishDue && s.readers == 0 && s.reservations == 0
	var (
		state  *terminalState
		cohort []*subscriber
	)
	if due {
		state, cohort = s.pendingEnd, s.deferred
		s.pendingEnd, s.finishDue, s.deferred = nil, false, nil
		for _, sub := range cohort {
			delete(s.subs, sub)
		}
	}
	s.mu.Unlock()
	for _, sub := range cohort {
		sub.stop(state)
	}
}

// readRun drains one run's adapter stream. A stream that ends on an error
// other than overflow carries that error to every subscriber as its terminal
// error — a failed run must not read as a clean end. When the stream ends,
// a reader with company finishes nobody: a stale finishSubs from run N must
// not terminate subscribers already receiving run N+1. The last reader to
// leave always finishes them, with the current run's terminal outcome — its
// own when it drained the current run, otherwise the outcome the current
// run's reader stashed on exit (see pendingEnd).
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
	s.exitReader(runID, end)
}

// stopExposed detaches the subscribers exposed to runID and terminates
// them with the given state.
func (s *Session) stopExposed(runID protocol.RunID, state *terminalState) {
	s.mu.Lock()
	affected := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		if sub.exposedTo(runID) {
			affected = append(affected, sub)
			delete(s.subs, sub)
		}
	}
	s.mu.Unlock()
	for _, sub := range affected {
		sub.stop(state)
	}
}

// exitReader is the path a run reader takes when its stream ends: with
// readers remaining, the current run's terminal state and the cohort it is
// owed to are stashed for the last one to apply; the last one finishes
// subscribers — with its own outcome for every subscriber when it drained
// the current run, otherwise the deferred outcome for the deferred cohort
// only — detaching under the lock so a concurrent startRun or subscribe
// cannot interleave. When only a Submit reservation remains, the finish is
// deferred to whichever resolves it.
func (s *Session) exitReader(runID protocol.RunID, end *terminalState) {
	// A stale drainer's error must not vanish because a newer run keeps
	// the hub busy: the subscribers still exposed to the failed run are
	// terminated now. The current run's error keeps the deferred path —
	// co-drainers may still be delivering its tail.
	if end != nil && end.err != nil {
		s.mu.Lock()
		stale := s.runID != runID
		s.mu.Unlock()
		if stale {
			s.stopExposed(runID, end)
		}
	}
	s.mu.Lock()
	s.readers--
	current := s.runID
	if current == runID {
		// The current run's outcome is deferred behind company — older
		// readers still draining, or a reservation in flight — and is owed
		// to exactly the subscribers present now: ones registering inside
		// the deferral window never observed the run and keep waiting for
		// the next one.
		s.pendingEnd = end
		s.deferred = s.snapshotSubsLocked()
	}
	if s.readers > 0 {
		s.mu.Unlock()
		return
	}
	if s.reservations > 0 {
		s.finishDue = true
		if s.deferred == nil {
			s.deferred = s.snapshotSubsLocked()
		}
		s.mu.Unlock()
		return
	}
	if current != runID {
		// This stale drainer is the last to leave: apply the current
		// run's deferred outcome to the cohort it was owed to. (The
		// cohort is never empty here — the current run's reader exited
		// before this one and snapshotted it — but falling back to every
		// subscriber keeps the invariant failure-safe rather than parking
		// everyone.)
		state, cohort := s.pendingEnd, s.deferred
		s.pendingEnd, s.deferred = nil, nil
		if cohort == nil {
			cohort = s.detachSubsLocked()
		} else {
			for _, sub := range cohort {
				delete(s.subs, sub)
			}
		}
		s.mu.Unlock()
		for _, sub := range cohort {
			sub.stop(state)
		}
		return
	}
	s.pendingEnd, s.deferred = nil, nil
	subs := s.detachSubsLocked()
	s.mu.Unlock()
	for _, sub := range subs {
		sub.stop(end)
	}
}

// publish delivers one envelope to every subscriber, terminating (not
// blocking on) subscribers whose queue is full. Delivery advances the
// subscriber's position to the envelope's run.
func (s *Session) publish(envelope protocol.Envelope) {
	s.mu.Lock()
	for sub := range s.subs {
		select {
		case sub.ch <- envelope:
			sub.lastRun = envelope.RunID
			sub.track(envelope.RunID)
		default:
			// The subscriber fell behind. Its recovery cursor is its own
			// position — the tail of its mailbox, whichever run that
			// belongs to — not the run of the envelope that found the
			// queue full: a late envelope from an older, still-draining run
			// must not strand the newer run's observed tail behind a
			// cursor that cannot replay it.
			delete(s.subs, sub)
			sub.stop(&terminalState{overflow: true, run: sub.lastRun})
		}
	}
	s.mu.Unlock()
}

// exposedTo reports, as one consistent snapshot, whether the subscriber is
// exposed to runID: once a run is acknowledged, that position governs — a
// late envelope from an older run still queued behind it does not re-expose
// the consumer to the older run's overflow. Before anything is
// acknowledged, undelivered envelopes of the run or attachment to it do.
func (sub *subscriber) exposedTo(runID protocol.RunID) bool {
	sub.pendMu.Lock()
	defer sub.pendMu.Unlock()
	if ack := sub.ack.Load(); ack != nil {
		return *ack == runID
	}
	return sub.pending[runID] > 0 || sub.attached == runID
}

// signalOverflow terminates the subscribers exposed to runID with the
// overflow signal after the adapter itself reported an event-stream
// overflow on that run's stream: every subscriber whose acknowledged or
// attached position is the run, or whose mailbox still holds undelivered
// envelopes of it — their view of the run is broken or incomplete.
// Subscribers that already moved past the run with nothing of it pending
// have seen it complete; an overflow from it recovers nothing for them and
// must not cut them off from the newer run they are consuming.
func (s *Session) signalOverflow(runID protocol.RunID) {
	s.stopExposed(runID, &terminalState{overflow: true, run: runID})
}

// finishSubs detaches every subscriber and terminates it with the given
// terminal state, or cleanly when the state is nil.
func (s *Session) finishSubs(state *terminalState) {
	for _, sub := range s.detachSubs() {
		sub.stop(state)
	}
}

// detachSubs empties the subscriber set under the session lock; callers
// that already hold the lock (the last run reader's exit) detach in their
// own critical section via detachSubsLocked.
func (s *Session) detachSubs() []*subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detachSubsLocked()
}

// snapshotSubsLocked copies the subscriber set without detaching it; s.mu
// must be held, so the snapshot is atomic with the decision deferring a
// finish onto it.
func (s *Session) snapshotSubsLocked() []*subscriber {
	subs := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	return subs
}

// detachSubsLocked snapshots and empties the subscriber set; s.mu must be
// held, so the detach is atomic with the caller's decision to finish.
func (s *Session) detachSubsLocked() []*subscriber {
	subs := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = make(map[*subscriber]struct{})
	return subs
}

// markClosed records a successful adapter close and ends every live stream.
// An adapter reports its run terminal once the terminal envelope is queued,
// not once a consumer drained it, so a reader may still hold final events:
// with a reader draining, its exit ends subscribers with the run's terminal
// outcome so no terminal envelope is lost; with only a Submit admission in
// flight, that admission's resolution ends them; with neither, they finish
// immediately.
func (s *Session) markClosed() {
	s.mu.Lock()
	s.closed = true
	switch {
	case s.readers > 0:
		// Readers still drain, and the last one's exit ends the
		// subscribers. A close owes every live subscriber — no future run
		// can reach the ones that registered after the current run's
		// reader deferred its end — so the deferred cohort is refreshed to
		// everyone subscribed at close time.
		s.deferred = s.snapshotSubsLocked()
		s.mu.Unlock()
	case s.reservations > 0:
		// No reader remains; the in-flight admission's resolution ends the
		// subscribers the close found (later subscribes are refused). The
		// close re-snapshots the cohort: a reader-exit deferral may predate
		// subscribers that registered since and are owed the close too.
		s.finishDue = true
		s.deferred = s.snapshotSubsLocked()
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		s.finishSubs(nil)
	}
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
