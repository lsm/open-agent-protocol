package serve

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type SubscribeOption func(*subscribeOptions)

type subscribeOptions struct {
	runID protocol.RunID
	after *uint64
}

func After(runID protocol.RunID, sequence uint64) SubscribeOption {
	return func(options *subscribeOptions) {
		value := sequence
		options.after = &value
		options.runID = runID
	}
}

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
		sub, joinedRun, joinedAt, open := entry.subscribe(h.queue)
		if !open {
			return nil, &SessionClosedError{ID: id}
		}
		return &Subscription{session: entry, ctx: ctx, sub: sub, joinedRun: joinedRun, joinedAt: joinedAt, positions: make(map[protocol.RunID]uint64)}, nil
	}
	if entry.IsClosed() {
		return nil, &SessionClosedError{ID: id}
	}
	runID := config.runID
	if runID == "" {
		var hasRun bool

		if runID, hasRun = entry.currentRun(); !hasRun {
			return nil, ErrNoRunToResume
		}
	}
	sub, _, _, open := entry.subscribe(h.queue)
	if !open {
		return nil, &SessionClosedError{ID: id}
	}
	replays, err := resumeRuns(ctx, entry, runID, *config.after)
	if err != nil {
		entry.unsubscribe(sub)
		sub.stop(nil)
	}
	var gap *base.ReplayGap
	if errors.As(err, &gap) {
		return nil, gap
	}
	if err != nil {
		return nil, err
	}
	positions := map[protocol.RunID]uint64{runID: *config.after}
	return &Subscription{session: entry, ctx: ctx, sub: sub, replay: replays[0].stream, replaying: runID, later: replays[1:], run: runID, last: *config.after, positions: positions, firstRun: entry.serial(runID)}, nil
}

type resumedRun struct {
	run    protocol.RunID
	stream base.EventStream
}

func resumeRuns(ctx context.Context, entry *Session, runID protocol.RunID, after uint64) ([]resumedRun, error) {
	var replays []resumedRun
	for _, run := range append([]protocol.RunID{runID}, entry.laterRuns(runID)...) {
		_, stream, err := entry.session.Resume(ctx, base.ResumeRequest{RunID: run, AfterSequence: after})
		if err != nil {
			abandon(replays)
			return nil, err
		}
		replays = append(replays, resumedRun{run: run, stream: stream})
		after = 0
	}
	return replays, nil
}

func abandon(replays []resumedRun) {
	for _, replay := range replays {
		go func() {
			for range replay.stream {
			}
		}()
	}
}

type Subscription struct {
	session *Session
	ctx     context.Context

	sub       *subscriber
	replay    base.EventStream
	replaying protocol.RunID
	later     []resumedRun
	run       protocol.RunID

	last      uint64
	lastRun   protocol.RunID
	positions map[protocol.RunID]uint64
	firstRun  uint64

	joinedRun protocol.RunID
	joinedAt  uint64

	closeOnce sync.Once
	finished  bool
	err       error
}

func (s *Subscription) RunID() protocol.RunID { return s.run }

func (s *Subscription) JoinedAt() (protocol.RunID, uint64, bool) {
	return s.joinedRun, s.joinedAt, s.joinedRun != "" && s.joinedAt > 0
}

func (s *Subscription) Next() (protocol.Envelope, error) {
	if s.finished {
		if s.err != nil {
			return protocol.Envelope{}, s.err
		}
		return protocol.Envelope{}, io.EOF
	}
	for s.replay != nil {
		envelope, err := s.nextReplay()
		if !errors.Is(err, io.EOF) {
			return envelope, err
		}
		s.replay = nil
		if len(s.later) > 0 {
			s.replay, s.replaying, s.last = s.later[0].stream, s.later[0].run, 0
			s.later = s.later[1:]
		}
	}
	return s.nextLive()
}

func (s *Subscription) Close() {
	s.closeOnce.Do(func() {
		s.finished = true
		s.detach()
	})
}

func (s *Subscription) nextLive() (protocol.Envelope, error) {
	for {
		select {
		case envelope := <-s.sub.ch:
			if s.observe(envelope) {
				return envelope, nil
			}
		case <-s.sub.finish:

			for {
				select {
				case envelope := <-s.sub.ch:
					if s.observe(envelope) {
						return envelope, nil
					}
				default:
					s.finished = true
					if state := s.sub.terminal.Load(); state != nil {
						if state.overflow {

							cursorSeq := uint64(0)
							if sequence, observed := s.positions[state.run]; observed {
								cursorSeq = sequence
							}
							s.err = &OverflowError{RunID: state.run, LastSequence: cursorSeq}
						} else {

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

func (s *Subscription) nextReplay() (protocol.Envelope, error) {
	select {
	case result, ok := <-s.replay:
		return s.replayed(result, ok)
	case <-s.sub.finish:
		select {
		case result, ok := <-s.replay:
			return s.replayed(result, ok)
		default:
			abandon(append(s.later, resumedRun{stream: s.replay}))
			s.later = nil
			return protocol.Envelope{}, io.EOF
		}
	case <-s.ctx.Done():
		s.Close()
		s.finished, s.err = true, s.ctx.Err()
		return protocol.Envelope{}, s.err
	}
}

func (s *Subscription) replayed(result base.Result, ok bool) (protocol.Envelope, error) {
	if !ok {
		return protocol.Envelope{}, io.EOF
	}
	if result.Error != nil {

		s.Close()
		s.finished = true
		if errors.Is(result.Error, base.ErrEventStreamOverflow) {
			s.err = &OverflowError{RunID: s.replaying, LastSequence: s.last}
		} else {
			s.err = result.Error
		}
		return protocol.Envelope{}, s.err
	}
	s.last = observedSequence(result.Envelope, s.last)
	if result.Envelope.Sequence != nil {
		s.positions[result.Envelope.RunID] = *result.Envelope.Sequence
	}
	return result.Envelope, nil
}

func (s *Subscription) observe(envelope protocol.Envelope) bool {
	if s.firstRun > 0 && s.session.serial(envelope.RunID) < s.firstRun {
		s.sub.untrack(envelope.RunID)
		return false
	}
	s.sub.acknowledge(envelope.RunID)
	if envelope.Sequence != nil && *envelope.Sequence <= s.positions[envelope.RunID] {
		return false
	}
	if envelope.RunID != s.lastRun {
		s.lastRun = envelope.RunID
		s.last = 0
	}
	s.last = observedSequence(envelope, s.last)
	if envelope.Sequence == nil {
		return true
	}
	if s.positions == nil {
		s.positions = make(map[protocol.RunID]uint64)
	}
	s.positions[envelope.RunID] = *envelope.Sequence
	return true
}

func (s *Subscription) detach() {
	s.session.unsubscribe(s.sub)
	s.sub.stop(nil)
	if s.replay != nil {
		abandon([]resumedRun{{stream: s.replay}})
	}
	abandon(s.later)
}

type OverflowError struct {
	RunID        protocol.RunID
	LastSequence uint64
}

func (e *OverflowError) Error() string {
	return fmt.Sprintf("serve: event stream overflowed behind sequence %d (run %s); resume with a cursor after it", e.LastSequence, e.RunID)
}

func observedSequence(envelope protocol.Envelope, last uint64) uint64 {
	if envelope.Sequence != nil && *envelope.Sequence > last {
		return *envelope.Sequence
	}
	return last
}
