package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	base "github.com/lsm/open-agent-protocol/adapter"
	"github.com/lsm/open-agent-protocol/protocol"
)

// SubscribeOption tunes Hub.Subscribe; see After.
type SubscribeOption func(*subscribeOptions)

type subscribeOptions struct {
	runID protocol.RunID
	after *uint64
}

// After replays one run from just after the given sequence before live
// events: the subscription first delivers that run's envelopes after the
// cursor, then continues with its live events. Binding the cursor to the run
// it was observed on keeps recovery exact when a newer run has already been
// admitted — a cursor from an OverflowError names that error's RunID, and a
// cursor taken from a stream position names the run the stream was
// consuming. An empty runID binds to the session's current run, the same
// resolution the daemon applies to a bare Last-Event-ID. A cursor the
// adapter no longer retains surfaces as *adapter.ReplayGap from Subscribe
// itself.
func After(runID protocol.RunID, sequence uint64) SubscribeOption {
	return func(options *subscribeOptions) {
		value := sequence
		options.after = &value
		options.runID = runID
	}
}

// Subscribe returns one event-stream subscription for the session. Without
// options the subscription parks until the session's next run and delivers
// its envelopes from wherever it joins; with After it replays the current
// run's retained suffix first.
//
// Delivery surfaces exactly the signals the daemon defines, as typed values:
// Next returns *OverflowError when this subscription's bounded buffer fell
// behind (its RunID and LastSequence are the resume cursor) or when the
// adapter itself reported a stream overflow, a run stream that fails ends
// the subscription with the adapter's error, and a cursor that expired
// before the subscription started fails here with *adapter.ReplayGap.
// Subscribing to a closed session fails with *SessionClosedError, and to a
// session that never ran with After, with ErrNoRunToResume.
//
// The context ends the subscription: a Next blocked on it reports ctx.Err()
// and the subscription detaches from the hub, so a consumer can be torn down
// from another goroutine by cancelling the context.
func (h *Hub) Subscribe(ctx context.Context, id protocol.SessionID, options ...SubscribeOption) (*Subscription, error) {
	entry, err := h.Session(id)
	if err != nil {
		return nil, err
	}
	var config subscribeOptions
	for _, option := range options {
		option(&config)
	}
	if config.after == nil {
		sub, open := entry.subscribe(h.queue)
		if !open {
			return nil, &SessionClosedError{ID: id}
		}
		return &Subscription{session: entry, ctx: ctx, sub: sub, positions: make(map[protocol.RunID]uint64)}, nil
	}
	if entry.IsClosed() {
		return nil, &SessionClosedError{ID: id}
	}
	runID := config.runID
	if runID == "" {
		var hasRun bool
		// The cursor names no run: resolve it onto the session's current
		// run, exactly as the daemon resolves a bare Last-Event-ID.
		if runID, hasRun = entry.currentRun(); !hasRun {
			return nil, ErrNoRunToResume
		}
	}
	_, replay, err := entry.session.Resume(ctx, base.ResumeRequest{RunID: runID, AfterSequence: *config.after})
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		return nil, gap
	}
	if err != nil {
		// An adapter that reports the session closed from a resume has
		// confirmed the session can never publish again: close the
		// hub-side entry — a later live subscription would otherwise park
		// forever, since the live path never probes the adapter —
		// mirroring the State, Submit, and Close rejections, while the
		// sentinel still returns for the caller's already-closed refusal.
		if errors.Is(err, base.ErrSessionClosed) {
			entry.markClosed()
		}
		return nil, err
	}
	return &Subscription{session: entry, ctx: ctx, replay: replay, run: runID, last: *config.after}, nil
}

// Subscription is one ordered run-event consumer, the in-process counterpart
// of one daemon SSE connection. Envelopes are delivered in emission order
// through Next; io.EOF is the clean end at a run's terminal event or the
// session's close, *OverflowError is the terminal fell-behind signal, and any
// other error is terminal for the subscription — once Next reports an error,
// subsequent calls return the same error.
//
// A Subscription is not safe for concurrent use: exactly one goroutine
// consumes it through Next and Close. To stop a blocked Next from another
// goroutine, cancel the context passed to Subscribe.
type Subscription struct {
	session *Session
	ctx     context.Context
	// sub is set for a live subscription; replay for a cursor subscription,
	// with run naming the run the cursor was bound to.
	sub    *subscriber
	replay base.EventStream
	run    protocol.RunID
	// last tracks this subscription's last delivered position: sequences are
	// per-run, so a connection that spans runs must not mix their sequence
	// spaces into the overflow cursor. positions remembers the last
	// delivered sequence of every run consumed, so an overflow cursor can
	// resume the run whose events were dropped rather than whichever run
	// happens to own the mailbox tail.
	last      uint64
	lastRun   protocol.RunID
	positions map[protocol.RunID]uint64

	closeOnce sync.Once
	finished  bool
	err       error
}

// Next returns the next envelope. It returns io.EOF after the stream ends
// cleanly at a run's terminal event, at the session's close, or after Close;
// *OverflowError is the fell-behind signal carrying the resume cursor; the
// context's error ends a subscription whose context was cancelled.
func (s *Subscription) Next() (protocol.Envelope, error) {
	if s.finished {
		if s.err != nil {
			return protocol.Envelope{}, s.err
		}
		return protocol.Envelope{}, io.EOF
	}
	if s.sub != nil {
		return s.nextLive()
	}
	return s.nextReplay()
}

// Close detaches the subscription from the hub: envelopes already queued to
// it are dropped, and subsequent Next calls return io.EOF, or the error the
// stream already terminated with. Close is idempotent and must not race
// with Next — cancel the Subscribe context to stop a blocked consumer
// instead.
func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		s.finished = true
		s.detach()
	})
}

// nextLive serves the hub's live mailbox until the run reaches a terminal
// event, the subscription overflows, or the context ends. The terminal state
// was recorded at stop time — including the run that overflowed this
// subscriber — so the signal's cursor stays valid even when a newer run was
// admitted before the consumer read it.
func (s *Subscription) nextLive() (protocol.Envelope, error) {
	for {
		select {
		case envelope := <-s.sub.ch:
			s.observe(envelope)
			return envelope, nil
		case <-s.sub.finish:
			// The producer never closes the mailbox: drain the envelopes
			// already queued before reporting the terminal outcome.
			for {
				select {
				case envelope := <-s.sub.ch:
					s.observe(envelope)
					return envelope, nil
				default:
					s.finished = true
					if state := s.sub.terminal.Load(); state != nil {
						if state.overflow {
							// The cursor resumes where the loss began: the
							// run of the dropped or overflowed envelope,
							// from the consumer's last delivered position in
							// it — or sequence zero when that run was never
							// delivered, replaying the unseen run from its
							// start: a cursor naming an older observed run
							// could not recover it.
							cursorSeq := uint64(0)
							if sequence, observed := s.positions[state.run]; observed {
								cursorSeq = sequence
							}
							s.err = &OverflowError{RunID: state.run, LastSequence: cursorSeq}
						} else {
							// The run's adapter stream ended on this error;
							// a failed run must not read as a clean end.
							s.err = state.err
						}
						return protocol.Envelope{}, s.err
					}
					return protocol.Envelope{}, io.EOF
				}
			}
		case <-s.ctx.Done():
			s.Close()
			s.finished, s.err = true, s.ctx.Err()
			return protocol.Envelope{}, s.err
		}
	}
}

// nextReplay serves one adapter Resume stream: the replayed suffix first,
// then live events, ending when the adapter closes the stream at terminality.
// A stream error other than overflow surfaces verbatim — the documented
// recovery path is a fresh cursor subscription.
func (s *Subscription) nextReplay() (protocol.Envelope, error) {
	select {
	case result, ok := <-s.replay:
		if !ok {
			s.finished = true
			return protocol.Envelope{}, io.EOF
		}
		if result.Error != nil {
			// The adapter may keep the errored stream open; hand it to the
			// background drainer so its bounded replay channel cannot fill
			// and block later adapter emits once this consumer stops. The
			// error stays sticky for the subscription.
			s.Close()
			s.finished = true
			if errors.Is(result.Error, base.ErrEventStreamOverflow) {
				s.err = &OverflowError{RunID: s.run, LastSequence: s.last}
			} else {
				s.err = result.Error
			}
			return protocol.Envelope{}, s.err
		}
		s.last = observedSequence(result.Envelope, s.last)
		return result.Envelope, nil
	case <-s.ctx.Done():
		s.Close()
		s.finished, s.err = true, s.ctx.Err()
		return protocol.Envelope{}, s.err
	}
}

// observe records one delivered envelope's position for the overflow
// cursor — per run, so a dropped run's cursor survives later runs owning
// the tail — and acknowledges its run on the hub side, where
// adapter-overflow scoping reads the acknowledged position.
func (s *Subscription) observe(envelope protocol.Envelope) {
	s.sub.acknowledge(envelope.RunID)
	if envelope.RunID != s.lastRun {
		s.lastRun = envelope.RunID
		s.last = 0
	}
	s.last = observedSequence(envelope, s.last)
	if envelope.Sequence == nil {
		return
	}
	if s.positions == nil {
		s.positions = make(map[protocol.RunID]uint64)
	}
	if *envelope.Sequence > s.positions[envelope.RunID] {
		s.positions[envelope.RunID] = *envelope.Sequence
	}
}

// detach detaches from the hub: a live subscriber unregisters and is
// finished, while an adapter replay stream is handed to a background drainer
// — whatever ends this subscription, the adapter stream keeps being drained
// until the adapter closes it, so an adapter whose bounded replay buffer
// would otherwise fill cannot block its own later emits on this departed
// consumer. Draining an already-closed channel exits at once. detach runs
// exactly once, under Close's guard.
func (s *Subscription) detach() {
	if s.sub != nil {
		s.session.unsubscribe(s.sub)
		s.sub.stop(nil)
		return
	}
	if s.replay != nil {
		stream := s.replay
		go func() {
			for range stream {
			}
		}()
	}
}

// OverflowError reports that this subscription's event delivery fell
// behind: its bounded buffer overflowed, or the adapter itself reported an
// event-stream overflow. LastSequence is the last sequence this subscription
// delivered — for a replay subscription, at least the cursor it started
// after — and RunID is the run the delivery position belongs to; resume
// with Subscribe and After(RunID, LastSequence).
type OverflowError struct {
	RunID        protocol.RunID
	LastSequence uint64
}

func (e *OverflowError) Error() string {
	return fmt.Sprintf("serve: event stream overflowed behind sequence %d (run %s); resume with a cursor after it", e.LastSequence, e.RunID)
}

// observedSequence advances a cursor to an envelope's sequence when it lies
// ahead; replay may redeliver positions at or behind the cursor.
func observedSequence(envelope protocol.Envelope, last uint64) uint64 {
	if envelope.Sequence != nil && *envelope.Sequence > last {
		return *envelope.Sequence
	}
	return last
}
