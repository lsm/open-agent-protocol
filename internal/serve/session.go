package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// sessionRegistry owns the daemon-side session lifetime. Entries survive
// adapter close (listed with status closed) because a client that closed a
// session may still read its final state; they are dropped only when the
// daemon exits.
type sessionRegistry struct {
	mu       sync.RWMutex
	sessions map[protocol.SessionID]*serverSession
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[protocol.SessionID]*serverSession)}
}

func (r *sessionRegistry) get(id protocol.SessionID) (*serverSession, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.sessions[id]
	return entry, ok
}

func (r *sessionRegistry) add(entry *serverSession) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[entry.id]; exists {
		return fmt.Errorf("session %q already exists", entry.id)
	}
	r.sessions[entry.id] = entry
	return nil
}

func (r *sessionRegistry) list() []*serverSession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]*serverSession, 0, len(r.sessions))
	for _, id := range sortedKeys(r.sessions) {
		entries = append(entries, r.sessions[protocol.SessionID(id)])
	}
	return entries
}

// serverSession fans one adapter session's run events out to SSE subscribers.
// The daemon consumes each run's adapter stream with a dedicated reader and
// forwards envelopes through bounded per-connection queues, so a slow SSE
// client overflows and is signalled instead of stalling the adapter.
type serverSession struct {
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

func newServerSession(id protocol.SessionID, adapterName string, session base.Session) *serverSession {
	return &serverSession{
		id: id, adapterName: adapterName, session: session,
		created: time.Now(), subs: make(map[*subscriber]struct{}),
	}
}

// subscriber is one SSE connection's bounded mailbox. The channel is never
// closed by the producer: a subscriber that must terminate is removed from the
// hub and its finish channel is closed, and the connection drains whatever
// envelopes were already queued.
type subscriber struct {
	ch         chan protocol.Envelope
	finish     chan struct{}
	finishOnce sync.Once
	overflow   atomic.Bool
}

func newSubscriber(queue int) *subscriber {
	return &subscriber{ch: make(chan protocol.Envelope, queue), finish: make(chan struct{})}
}

func (sub *subscriber) stop(overflowed bool) {
	if overflowed {
		sub.overflow.Store(true)
	}
	sub.finishOnce.Do(func() { close(sub.finish) })
}

// subscribe registers a live-events mailbox. A subscriber that connects while
// no run is active simply parks until the next submit; one that arrives after
// the session closed is refused — nothing will ever finish it, so parking
// would hang the connection. The check shares the hub lock with markClosed,
// closing the arrive/finish race.
func (ss *serverSession) subscribe(queue int) (*subscriber, bool) {
	sub := newSubscriber(queue)
	ss.mu.Lock()
	if ss.closed {
		ss.mu.Unlock()
		return nil, false
	}
	ss.subs[sub] = struct{}{}
	ss.mu.Unlock()
	return sub, true
}

// isClosed reports whether the adapter session was closed through the daemon.
func (ss *serverSession) isClosed() bool {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.closed
}

func (ss *serverSession) unsubscribe(sub *subscriber) {
	ss.mu.Lock()
	delete(ss.subs, sub)
	ss.mu.Unlock()
}

// currentRun reports the run a replay cursor applies to: the run whose stream
// most recently drove the hub, active or already terminal.
func (ss *serverSession) currentRun() (protocol.RunID, bool) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	return ss.runID, ss.runID != ""
}

// startRun points the hub at a newly admitted run and starts draining its
// adapter stream. The drain is unconditional for the whole run lifetime, so
// the adapter's bounded subscriber buffers never fill on the daemon side.
func (ss *serverSession) startRun(runID protocol.RunID, stream base.EventStream) {
	ss.mu.Lock()
	ss.runID = runID
	ss.readers++
	ss.mu.Unlock()
	go ss.readRun(runID, stream)
}

// readRun drains one run's adapter stream. When the stream ends it finishes
// subscribers only when it is the last reader and a newer run has not taken
// over the hub: a stale finishSubs from run N must not terminate subscribers
// already receiving run N+1, and a close that deferred its finish must not
// fire while run N+1's reader still has events queued.
func (ss *serverSession) readRun(runID protocol.RunID, stream base.EventStream) {
	for result := range stream {
		if result.Error != nil {
			if errors.Is(result.Error, base.ErrEventStreamOverflow) {
				ss.signalOverflow()
				continue
			}
			// Any other stream error ends delivery for this run; the
			// documented reconnect path is the replay cursor. The stream is
			// still drained so an adapter that reports an error but keeps
			// its channel open cannot block its own later emits.
			go func(stream base.EventStream) {
				for range stream {
				}
			}(stream)
			break
		}
		ss.publish(result.Envelope)
	}
	ss.mu.Lock()
	ss.readers--
	current := ss.runID
	finish := ss.readers == 0 && (current == runID || ss.finishAfterDrain)
	ss.mu.Unlock()
	if finish {
		ss.finishSubs(false)
	}
}

// publish delivers one envelope to every subscriber, terminating (not
// blocking on) subscribers whose queue is full.
func (ss *serverSession) publish(envelope protocol.Envelope) {
	ss.mu.Lock()
	for sub := range ss.subs {
		select {
		case sub.ch <- envelope:
		default:
			delete(ss.subs, sub)
			sub.stop(true)
		}
	}
	ss.mu.Unlock()
}

// signalOverflow terminates every subscriber with the overflow signal after
// the adapter itself reported an event-stream overflow.
func (ss *serverSession) signalOverflow() {
	ss.finishSubs(true)
}

func (ss *serverSession) finishSubs(overflow bool) {
	ss.mu.Lock()
	subs := make([]*subscriber, 0, len(ss.subs))
	for sub := range ss.subs {
		subs = append(subs, sub)
	}
	ss.subs = make(map[*subscriber]struct{})
	ss.mu.Unlock()
	for _, sub := range subs {
		sub.stop(overflow)
	}
}

// markClosed records a successful adapter close and ends every live stream.
// An adapter reports its run terminal once the terminal envelope is queued,
// not once a consumer drained it, so a reader may still hold final events:
// with no reader draining, subscribers finish immediately; otherwise the
// finish waits for the last reader so no terminal envelope is lost.
func (ss *serverSession) markClosed() {
	ss.mu.Lock()
	ss.closed = true
	if ss.readers > 0 {
		ss.finishAfterDrain = true
		ss.mu.Unlock()
		return
	}
	ss.mu.Unlock()
	ss.finishSubs(false)
}

// close cancels any active run and then closes the adapter session: active
// runs refuse Close by contract, so shutdown settles them through Cancel
// where the adapter supports it. Some adapters acknowledge a cancel before
// the run settles, so the refused Close is retried briefly — a shutdown that
// gave up here would leave the session's child process orphaned.
func (ss *serverSession) close(ctx context.Context) error {
	err := ss.session.Close(ctx)
	for attempt := 0; errors.Is(err, base.ErrRunActive) && attempt < 3; attempt++ {
		if ctx.Err() != nil {
			break
		}
		state, stateErr := ss.session.State(ctx)
		if stateErr == nil && state.ActiveRunID != "" {
			_, _ = ss.session.Cancel(ctx, state.ActiveRunID)
		}
		select {
		case <-ctx.Done():
		case <-time.After(100 * time.Millisecond):
		}
		err = ss.session.Close(ctx)
	}
	if err == nil {
		ss.markClosed()
	}
	return err
}

// SSE stream names for daemon-side terminal signals. They are transport
// framing, not OAP envelopes: data carries a small JSON object documenting
// the condition and the reconnect cursor.
const (
	sseEventOverflow  = "oap-overflow"
	sseEventReplayGap = "oap-replay-gap"
)

// streamLive serves one SSE connection from the hub until the run reaches a
// terminal event, the connection overflows, or the request context ends. The
// session is consulted at signal time so a subscriber parked before the run
// started still reports the run that overflowed it.
func streamLive(w io.Writer, flusher http.Flusher, sub *subscriber, ctx context.Context, entry *serverSession) {
	last := uint64(0)
	var lastRun protocol.RunID
	for {
		select {
		case envelope := <-sub.ch:
			if err := writeSSE(w, envelope); err != nil {
				return
			}
			// Sequences are per-run: a connection that spans runs (a
			// resubmit landing in the settle window of the previous run)
			// must not mix their independent sequence spaces into the
			// overflow cursor.
			if envelope.RunID != lastRun {
				lastRun = envelope.RunID
				last = 0
			}
			last = observedSequence(envelope, last)
			flusher.Flush()
		case <-sub.finish:
			for {
				select {
				case envelope := <-sub.ch:
					if err := writeSSE(w, envelope); err != nil {
						return
					}
					if envelope.RunID != lastRun {
						lastRun = envelope.RunID
						last = 0
					}
					last = observedSequence(envelope, last)
					flusher.Flush()
				default:
					if sub.overflow.Load() {
						runID, _ := entry.currentRun()
						writeSSESignal(w, flusher, sseEventOverflow, map[string]any{
							"run_id": string(runID), "last_sequence": last,
							"message": "event stream consumer fell behind; reconnect with a cursor after this sequence",
						})
					}
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// streamReplay serves one SSE connection from an adapter Resume stream: the
// replayed suffix first, then live events, ending when the adapter closes the
// stream at terminality.
func streamReplay(w io.Writer, flusher http.Flusher, replay base.EventStream, ctx context.Context, runID protocol.RunID) {
	// Whatever ends this connection — completion, a client disconnect, a
	// stream error, or context cancellation — the adapter stream keeps being
	// drained until the adapter closes it, so an adapter whose bounded
	// replay buffer would otherwise fill cannot block its own later emits on
	// this dead consumer. Draining an already-closed channel exits at once;
	// against a run that never settles the drainers accumulate only until
	// the session closes.
	defer func() {
		go func(stream base.EventStream) {
			for range stream {
			}
		}(replay)
	}()
	last := uint64(0)
	for {
		select {
		case result, ok := <-replay:
			if !ok {
				return
			}
			if result.Error != nil {
				if errors.Is(result.Error, base.ErrEventStreamOverflow) {
					writeSSESignal(w, flusher, sseEventOverflow, map[string]any{
						"run_id": string(runID), "last_sequence": last,
						"message": "event stream consumer fell behind; reconnect with a cursor after this sequence",
					})
				}
				return
			}
			if err := writeSSE(w, result.Envelope); err != nil {
				return
			}
			last = observedSequence(result.Envelope, last)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}

// writeSSEGap terminates an SSE response with the documented replay-gap
// signal after the adapter reported the requested cursor as expired.
func writeSSEGap(w io.Writer, flusher http.Flusher, gap *base.ReplayGap) {
	writeSSESignal(w, flusher, sseEventReplayGap, map[string]any{
		"requested_after":  gap.RequestedAfter,
		"oldest_available": gap.OldestAvailable,
		"latest_available": gap.LatestAvailable,
		"message":          "requested replay cursor is no longer retained; reconnect with a cursor at or after oldest_available - 1",
	})
}

func writeSSESignal(w io.Writer, flusher http.Flusher, event string, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
	flusher.Flush()
}

func writeSSE(w io.Writer, envelope protocol.Envelope) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if envelope.Sequence != nil {
		if _, err := fmt.Fprintf(w, "id: %s\n", strconv.FormatUint(*envelope.Sequence, 10)); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func observedSequence(envelope protocol.Envelope, last uint64) uint64 {
	if envelope.Sequence != nil && *envelope.Sequence > last {
		return *envelope.Sequence
	}
	return last
}
