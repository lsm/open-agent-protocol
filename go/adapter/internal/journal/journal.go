package journal

import (
	"encoding/json"
	"sync"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const followerBuffer = 64

type Journal struct {
	mu        sync.Mutex
	capacity  int
	entries   []protocol.Envelope
	evicted   int
	latest    map[protocol.RunID]uint64
	ended     map[protocol.RunID]bool
	changed   chan struct{}
	stop      chan struct{}
	stopOnce  sync.Once
	stopped   bool
	followers map[*follower]struct{}
}

func New(capacity int) *Journal {
	return &Journal{capacity: capacity, latest: map[protocol.RunID]uint64{}, ended: map[protocol.RunID]bool{}, followers: map[*follower]struct{}{}, changed: make(chan struct{}), stop: make(chan struct{})}
}

func (j *Journal) Append(envelope protocol.Envelope, terminal bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = append(j.entries, Clone(envelope))
	if len(j.entries) > j.capacity {
		j.entries[0] = protocol.Envelope{}
		j.entries = j.entries[1:]
		j.evicted++
	}
	if envelope.Sequence != nil {
		j.latest[envelope.RunID] = *envelope.Sequence
		if terminal {
			j.ended[envelope.RunID] = true
		}
	}
	for f := range j.followers {
		for j.pumpLocked(f) {
		}
	}
	j.wakeLocked()
}

func (j *Journal) wakeLocked() {
	close(j.changed)
	j.changed = make(chan struct{})
}

func (j *Journal) Ended(run protocol.RunID) (uint64, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.ended[run] {
		return 0, false
	}
	return j.latest[run], true
}

func (j *Journal) Resume(state protocol.SessionState, run protocol.RunID, after, latest uint64) (base.Recovery, base.EventStream, error) {
	j.mu.Lock()
	var oldest, first, last uint64
	for _, envelope := range j.entries {
		if envelope.RunID != run || envelope.Sequence == nil {
			continue
		}
		if oldest == 0 {
			oldest = *envelope.Sequence
		}
		if *envelope.Sequence > after {
			if first == 0 {
				first = *envelope.Sequence
			}
			last = *envelope.Sequence
		}
	}
	j.mu.Unlock()
	recovery := base.Recovery{State: state, RunID: run, RequestedAfter: after, ReplayedFrom: after, ReplayedThrough: after}
	if after < latest && (oldest == 0 || after+1 < oldest) {
		recovery.ReplayGap = &base.ReplayGap{RequestedAfter: after, OldestAvailable: oldest, LatestAvailable: latest}
		recovery.ReplayedFrom, recovery.ReplayedThrough = 0, 0
		return recovery, Closed(), recovery.ReplayGap
	}
	if first != 0 {
		recovery.ReplayedFrom, recovery.ReplayedThrough = first, last
	}
	return recovery, j.Follow(run, after), nil
}

type follower struct {
	run      protocol.RunID
	cursor   uint64
	position int
	out      chan base.Result
	inflight bool
	done     bool
	unbound  bool
}

type Reservation struct {
	journal  *Journal
	follower *follower
}

func (j *Journal) Reserve() Reservation {
	f := &follower{out: make(chan base.Result, followerBuffer), unbound: true}
	j.mu.Lock()
	j.followers[f] = struct{}{}
	j.mu.Unlock()
	go j.follow(f)
	return Reservation{journal: j, follower: f}
}

func (r Reservation) Stream() base.EventStream { return r.follower.out }

func (r Reservation) Bind(run protocol.RunID) {
	r.journal.mu.Lock()
	defer r.journal.mu.Unlock()
	if r.follower.done || !r.follower.unbound {
		return
	}
	r.follower.run, r.follower.unbound = run, false
	for r.journal.pumpLocked(r.follower) {
	}
	r.journal.wakeLocked()
}

func (r Reservation) Release() {
	r.journal.mu.Lock()
	defer r.journal.mu.Unlock()
	if r.follower.unbound {
		r.journal.finishLocked(r.follower)
		r.journal.wakeLocked()
	}
}

func (j *Journal) Follow(run protocol.RunID, after uint64) base.EventStream {
	f := &follower{run: run, cursor: after, out: make(chan base.Result, followerBuffer)}
	j.mu.Lock()
	j.followers[f] = struct{}{}
	for j.pumpLocked(f) {
	}
	j.mu.Unlock()
	go j.follow(f)
	return f.out
}

func (j *Journal) Close() {
	j.mu.Lock()
	j.stopped = true
	j.mu.Unlock()
	j.stopOnce.Do(func() { close(j.stop) })
}

func (j *Journal) pumpLocked(f *follower) bool {
	if f.done || f.inflight || f.unbound {
		return false
	}
	envelope, found, next := j.findLocked(f.run, f.cursor+1, f.position)
	if !found {
		return false
	}
	select {
	case f.out <- base.Result{Envelope: envelope}:
		j.advanceLocked(f, next)
		return !f.done
	default:
		return false
	}
}

func (j *Journal) advanceLocked(f *follower, next int) {
	f.cursor++
	f.position = next
	if j.ended[f.run] && j.latest[f.run] <= f.cursor {
		j.finishLocked(f)
	}
}

func (j *Journal) finishLocked(f *follower) {
	if f.done {
		return
	}
	f.done = true
	delete(j.followers, f)
	close(f.out)
}

func (j *Journal) follow(f *follower) {
	for {
		j.mu.Lock()
		for j.pumpLocked(f) {
		}
		if f.done {
			j.mu.Unlock()
			return
		}
		if f.unbound {
			stopped, changed := j.stopped, j.changed
			if stopped {
				j.finishLocked(f)
			}
			j.mu.Unlock()
			if stopped {
				return
			}
			select {
			case <-changed:
			case <-j.stop:
			}
			continue
		}
		envelope, found, next := j.findLocked(f.run, f.cursor+1, f.position)
		behind := !found && j.latest[f.run] > f.cursor
		finished := !found && j.ended[f.run] && j.latest[f.run] <= f.cursor
		stopped := j.stopped
		changed := j.changed
		switch {
		case behind:
			select {
			case f.out <- base.Result{Error: base.ErrEventStreamOverflow}:
			default:
				if !stopped {
					f.inflight = true
					j.mu.Unlock()
					select {
					case f.out <- base.Result{Error: base.ErrEventStreamOverflow}:
					case <-j.stop:
					}
					j.mu.Lock()
					f.inflight = false
				}
			}
			j.finishLocked(f)
			j.mu.Unlock()
			return
		case finished, stopped:
			j.finishLocked(f)
			j.mu.Unlock()
			return
		case found:
			f.inflight = true
			j.mu.Unlock()
			select {
			case f.out <- base.Result{Envelope: envelope}:
				j.mu.Lock()
				f.inflight = false
				j.advanceLocked(f, next)
				j.mu.Unlock()
			case <-j.stop:
				j.mu.Lock()
				f.inflight = false
				j.mu.Unlock()
			}
		default:
			j.mu.Unlock()
			select {
			case <-changed:
			case <-j.stop:
			}
		}
	}
}

func (j *Journal) findLocked(run protocol.RunID, sequence uint64, position int) (protocol.Envelope, bool, int) {
	start := max(position-j.evicted, 0)
	for index := start; index < len(j.entries); index++ {
		envelope := j.entries[index]
		if envelope.RunID == run && envelope.Sequence != nil && *envelope.Sequence == sequence {
			return Clone(envelope), true, j.evicted + index + 1
		}
	}
	return protocol.Envelope{}, false, position
}

func Closed() base.EventStream {
	out := make(chan base.Result)
	close(out)
	return out
}

func Clone(e protocol.Envelope) protocol.Envelope {
	cloned := e
	if e.Payload != nil {
		cloned.Payload = append(json.RawMessage(nil), e.Payload...)
	}
	if e.Sequence != nil {
		sequence := *e.Sequence
		cloned.Sequence = &sequence
	}
	if e.TimestampMS != nil {
		timestamp := *e.TimestampMS
		cloned.TimestampMS = &timestamp
	}
	if e.Extensions != nil {
		cloned.Extensions = make(map[string]json.RawMessage, len(e.Extensions))
		for key, value := range e.Extensions {
			cloned.Extensions[key] = append(json.RawMessage(nil), value...)
		}
	}
	if e.Unknown != nil {
		cloned.Unknown = make(map[string]json.RawMessage, len(e.Unknown))
		for key, value := range e.Unknown {
			cloned.Unknown[key] = append(json.RawMessage(nil), value...)
		}
	}
	return cloned
}
