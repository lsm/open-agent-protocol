package serve

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

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

func (r *sessionRegistry) remove(id protocol.SessionID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
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

type Session struct {
	mu          sync.Mutex
	id          protocol.SessionID
	adapterName string
	session     base.Session
	created     time.Time

	runID protocol.RunID

	closed bool

	readers      int
	reservations int
	finishDue    bool
	pendingEnd   *terminalState
	deferred     []*subscriber
	subs         map[*subscriber]struct{}
	nextSerial   uint64
	serials      map[protocol.RunID]uint64

	finished map[protocol.RunID]bool
}

func newSession(id protocol.SessionID, adapterName string, session base.Session) *Session {
	return &Session{
		id: id, adapterName: adapterName, session: session,
		created: time.Now(), subs: make(map[*subscriber]struct{}), serials: make(map[protocol.RunID]uint64),
		finished: make(map[protocol.RunID]bool),
	}
}

func (s *Session) ID() protocol.SessionID { return s.id }

func (s *Session) Adapter() string { return s.adapterName }

func (s *Session) CreatedAt() time.Time { return s.created }

func (s *Session) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Session) State(ctx context.Context) (protocol.SessionState, error) {
	state, err := s.session.State(ctx)
	if errors.Is(err, base.ErrSessionClosed) {
		s.markClosed()
	}
	if err != nil {
		return state, err
	}
	if state.SessionID != s.id {

		return protocol.SessionState{}, fmt.Errorf("serve: adapter reported state scoped to session %q, want %q", state.SessionID, s.id)
	}
	return state, nil
}

func (s *Session) Models(ctx context.Context, request protocol.ModelsRequest) (base.Catalog, error) {
	if request.SessionID != "" && request.SessionID != s.id {
		return base.Catalog{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}
	request.SessionID = s.id
	lister, ok := s.session.(base.ModelLister)
	if !ok {
		return base.Catalog{}, &base.UnsupportedControlError{
			Feature: protocol.FeatureModelsList, Reason: base.ControlUnadvertised,
		}
	}
	catalog, err := lister.Models(ctx, request)
	if errors.Is(err, base.ErrSessionClosed) {
		s.markClosed()
	}
	if err != nil {
		return base.Catalog{}, err
	}

	if catalog.Revision == "" {

		return base.Catalog{}, errors.New("serve: adapter served a model catalog with no capability revision")
	}
	if catalog.Models.SessionID != s.id {

		return base.Catalog{}, fmt.Errorf("serve: adapter served a model catalog scoped to session %q, want %q", catalog.Models.SessionID, s.id)
	}
	if catalog.Models.Models == nil {

		catalog.Models.Models = []protocol.ModelDescriptor{}
	}
	return catalog, nil
}

func (s *Session) Submit(ctx context.Context, request protocol.MessageSubmitRequest) (protocol.MessageSubmitResponse, error) {
	if request.SessionID != s.id {
		return protocol.MessageSubmitResponse{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}

	s.mu.Lock()
	s.reservations++
	s.mu.Unlock()
	admission, stream, err := s.session.Submit(ctx, request)
	if err != nil {
		if stream != nil {

			s.adoptOrphan(stream)
		} else {
			s.releaseReservation()
		}

		if errors.Is(err, base.ErrSessionClosed) {
			s.markClosed()
		}
		return admission, err
	}
	s.adoptRun(admission.RunID, stream, admission.Admission == protocol.AdmissionQueued)
	return admission, nil
}

func (s *Session) Tools(ctx context.Context, request protocol.ToolsListRequest) (base.ToolCatalog, error) {
	if request.SessionID != "" && request.SessionID != s.id {
		return base.ToolCatalog{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}
	lister, ok := s.session.(base.ToolLister)
	if !ok {
		return base.ToolCatalog{}, base.ErrToolCatalogUnavailable
	}
	catalog, err := lister.Tools(ctx, request)
	if errors.Is(err, base.ErrSessionClosed) {
		s.markClosed()
	}
	if err != nil {
		return base.ToolCatalog{}, err
	}
	if catalog.Revision == "" {

		return base.ToolCatalog{}, errors.New("serve: adapter served a tool catalog with no capability revision")
	}
	if catalog.Tools.SessionID != request.SessionID {

		return base.ToolCatalog{}, fmt.Errorf("serve: adapter served a tool catalog scoped to session %q, want %q", catalog.Tools.SessionID, request.SessionID)
	}
	if catalog.Tools.Tools == nil {

		catalog.Tools.Tools = []protocol.ToolDefinition{}
	}
	return catalog, nil
}

func (s *Session) Resolve(ctx context.Context, resolution base.InteractionResolution) error {
	if resolution.Permission != nil && resolution.Permission.SessionID != s.id {
		return &ScopeMismatchError{Payload: resolution.Permission.SessionID, Addressed: s.id}
	}
	if resolution.Input != nil && resolution.Input.SessionID != s.id {
		return &ScopeMismatchError{Payload: resolution.Input.SessionID, Addressed: s.id}
	}
	return s.session.Resolve(ctx, resolution)
}

func (s *Session) ResolveCall(ctx context.Context, resolution base.CallResolution) (protocol.ActionCallResolveResponse, error) {
	if resolution.Request.SessionID != s.id {
		return protocol.ActionCallResolveResponse{}, &ScopeMismatchError{Payload: resolution.Request.SessionID, Addressed: s.id}
	}
	resolver, ok := s.session.(base.CallResolver)
	if !ok {
		return protocol.ActionCallResolveResponse{}, &base.UnsupportedControlError{
			Feature: protocol.FeatureToolsProvide, Reason: base.ControlUnadvertised,
		}
	}
	return resolver.ResolveCall(ctx, resolution)
}

func (s *Session) Cancel(ctx context.Context, runID protocol.RunID) (protocol.RunCancelResponse, error) {
	return s.session.Cancel(ctx, runID)
}

func (s *Session) Close(ctx context.Context) error {
	if err := s.session.Close(ctx); err != nil {
		return err
	}
	s.markClosed()
	return nil
}

type subscriber struct {
	ch         chan protocol.Envelope
	finish     chan struct{}
	finishOnce sync.Once
	terminal   atomic.Pointer[terminalState]

	attached       protocol.RunID
	attachedSerial uint64
	lastRun        protocol.RunID
	ack            atomic.Pointer[protocol.RunID]

	pendMu     sync.Mutex
	pending    map[protocol.RunID]int
	runSerials map[protocol.RunID]uint64
	ackSerial  uint64
}

func (sub *subscriber) acknowledge(run protocol.RunID) {
	sub.pendMu.Lock()
	if sub.pending[run] > 0 {
		sub.pending[run]--
	}

	if serial := sub.runSerials[run]; serial > sub.ackSerial {
		sub.ackSerial = serial
		sub.ack.Store(&run)
	} else if current := sub.ack.Load(); current == nil {
		sub.ack.Store(&run)
	}
	sub.pendMu.Unlock()
}

func (sub *subscriber) track(run protocol.RunID, serial uint64) {
	sub.pendMu.Lock()
	sub.pending[run]++
	sub.runSerials[run] = serial
	sub.pendMu.Unlock()
}

func (sub *subscriber) untrack(run protocol.RunID) {
	sub.pendMu.Lock()
	if sub.pending[run] > 0 {
		sub.pending[run]--
	}
	sub.pendMu.Unlock()
}

func (sub *subscriber) lossRun(dropped protocol.RunID, droppedSerial uint64, current protocol.RunID, currentSerial uint64, finished map[protocol.RunID]bool) protocol.RunID {
	sub.pendMu.Lock()
	defer sub.pendMu.Unlock()
	spent := func(run protocol.RunID) bool { return run != current && finished[run] }
	newest, newestSerial := dropped, droppedSerial
	live := !spent(dropped)
	consider := func(run protocol.RunID, serial uint64) {
		switch {
		case live && spent(run):
		case !live && !spent(run):
			newest, newestSerial, live = run, serial, true
		case serial > newestSerial:
			newest, newestSerial = run, serial
		}
	}
	if ack := sub.ack.Load(); ack != nil {
		consider(*ack, sub.ackSerial)
	}
	if sub.ackSerial == 0 {
		consider(sub.attached, sub.attachedSerial)
	}
	for run, queued := range sub.pending {
		if queued == 0 {
			continue
		}
		consider(run, sub.runSerials[run])
	}
	consider(current, currentSerial)
	return newest
}

type terminalState struct {
	overflow bool
	run      protocol.RunID
	err      error
}

func newSubscriber(queue int, attached protocol.RunID, attachedSerial uint64) *subscriber {
	return &subscriber{
		ch: make(chan protocol.Envelope, queue), finish: make(chan struct{}),
		attached: attached, attachedSerial: attachedSerial,
		pending: make(map[protocol.RunID]int), runSerials: make(map[protocol.RunID]uint64),
	}
}

func (sub *subscriber) stop(state *terminalState) {
	if state != nil {
		sub.terminal.CompareAndSwap(nil, state)
	}
	sub.finishOnce.Do(func() { close(sub.finish) })
}

func (s *Session) subscribe(queue int) (*subscriber, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false
	}
	sub := newSubscriber(queue, s.runID, s.serials[s.runID])
	s.subs[sub] = struct{}{}
	return sub, true
}

func (s *Session) unsubscribe(sub *subscriber) {
	s.mu.Lock()
	delete(s.subs, sub)
	s.mu.Unlock()
}

func (s *Session) currentRun() (protocol.RunID, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runID, s.runID != ""
}

func (s *Session) bindRun(runID protocol.RunID, reserved uint64) protocol.RunID {
	s.mu.Lock()
	if s.serials[runID] == 0 {
		s.serials[runID] = reserved
	}
	if s.reservations > 0 {

		s.reservations--
	}
	promoted := reserved > s.serials[s.runID]
	if promoted {
		s.runID = runID
	}

	var errored []*subscriber
	var failed *terminalState
	if promoted {
		errored, failed = s.supersedeLocked()
	}
	s.mu.Unlock()
	s.deliverDeferredError(errored, failed)
	return runID
}

func (s *Session) startRun(runID protocol.RunID, stream base.EventStream) {
	s.mu.Lock()
	s.readers++
	s.runID = runID
	s.nextSerial++
	s.serials[runID] = s.nextSerial
	errored, failed := s.supersedeLocked()
	s.mu.Unlock()
	s.deliverDeferredError(errored, failed)
	go s.readRun(runID, stream, 0)
}

func (s *Session) adoptRun(runID protocol.RunID, stream base.EventStream, queued bool) {
	s.mu.Lock()
	s.reservations--
	s.readers++
	s.nextSerial++
	s.serials[runID] = s.nextSerial
	var errored []*subscriber
	var failed *terminalState
	if !queued {
		s.runID = runID
		errored, failed = s.supersedeLocked()
	}
	s.mu.Unlock()
	s.deliverDeferredError(errored, failed)
	go s.readRun(runID, stream, 0)
}

func (s *Session) promoteCurrent(runID protocol.RunID, envelope protocol.Envelope) {
	switch envelope.Type {
	case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
		return
	}
	s.mu.Lock()
	if s.runID == runID || s.serials[runID] <= s.serials[s.runID] {
		s.mu.Unlock()
		return
	}
	s.runID = runID
	errored, failed := s.supersedeLocked()
	s.mu.Unlock()
	s.deliverDeferredError(errored, failed)
}

func (s *Session) supersedeLocked() (cohort []*subscriber, failed *terminalState) {
	if s.pendingEnd != nil && s.pendingEnd.err != nil {
		cohort, failed = s.deferred, s.pendingEnd
		for _, sub := range cohort {
			delete(s.subs, sub)
		}
	}
	s.pendingEnd, s.finishDue, s.deferred = nil, false, nil
	return cohort, failed
}

func (s *Session) deliverDeferredError(cohort []*subscriber, failed *terminalState) {
	for _, sub := range cohort {
		sub.stop(failed)
	}
}

func (s *Session) adoptOrphan(stream base.EventStream) {
	s.mu.Lock()
	s.readers++

	s.nextSerial++
	reserved := s.nextSerial
	s.mu.Unlock()
	go s.readRun("", stream, reserved)
}

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

func (s *Session) readRun(runID protocol.RunID, stream base.EventStream, reserved uint64) {
	var end *terminalState
	for result := range stream {

		if runID == "" && result.Envelope.RunID != "" {
			runID = s.bindRun(result.Envelope.RunID, reserved)
		}
		if result.Error != nil {
			if errors.Is(result.Error, base.ErrEventStreamOverflow) {
				s.signalOverflow(runID)
				continue
			}

			end = &terminalState{err: result.Error}
			go func(stream base.EventStream) {
				for range stream {
				}
			}(stream)
			break
		}
		s.promoteCurrent(runID, result.Envelope)
		s.publish(result.Envelope)
	}
	if reserved > 0 && runID == "" {

		s.mu.Lock()
		s.readers--
		var cohort []*subscriber
		var state *terminalState
		if s.readers == 0 && s.reservations == 1 {
			if s.pendingEnd != nil || s.deferred != nil || s.finishDue || s.closed {
				state, cohort = s.pendingEnd, s.deferred
				s.pendingEnd, s.finishDue, s.deferred = nil, false, nil
				if cohort == nil {
					cohort = s.detachSubsLocked()
				} else {
					for _, sub := range cohort {
						delete(s.subs, sub)
					}
				}
			}
		}
		s.mu.Unlock()
		for _, sub := range cohort {
			sub.stop(state)
		}
		s.releaseReservation()
		return
	}
	s.exitReader(runID, end)
}

func (s *Session) stopExposed(runID protocol.RunID, state *terminalState) {
	s.mu.Lock()
	affected := s.detachExposedLocked(runID)
	s.mu.Unlock()
	for _, sub := range affected {
		sub.stop(state)
	}
}

func (s *Session) detachExposedLocked(runID protocol.RunID) []*subscriber {
	serial := s.serials[runID]
	affected := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		if sub.exposedTo(runID, serial) {
			affected = append(affected, sub)
			delete(s.subs, sub)
		}
	}
	return affected
}

func (s *Session) exitReader(runID protocol.RunID, end *terminalState) {

	var exposed []*subscriber
	s.mu.Lock()
	s.readers--
	s.finished[runID] = true
	current := s.runID

	if end != nil && end.err != nil && current != runID {
		exposed = s.detachExposedLocked(runID)
	}
	if current == runID {

		s.pendingEnd = end
		s.deferred = s.snapshotSubsLocked()
	}
	if s.readers > 0 {
		s.mu.Unlock()
		for _, sub := range exposed {
			sub.stop(end)
		}
		return
	}
	if s.reservations > 0 {
		s.finishDue = true
		if s.deferred == nil {
			s.deferred = s.snapshotSubsLocked()
		}
		s.mu.Unlock()
		for _, sub := range exposed {
			sub.stop(end)
		}
		return
	}
	if current != runID {

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
		for _, sub := range exposed {
			sub.stop(end)
		}
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

func (s *Session) publish(envelope protocol.Envelope) {
	s.mu.Lock()
	for sub := range s.subs {

		sub.track(envelope.RunID, s.serials[envelope.RunID])
		select {
		case sub.ch <- envelope:
			sub.lastRun = envelope.RunID
		default:
			sub.untrack(envelope.RunID)

			delete(s.subs, sub)
			sub.stop(&terminalState{overflow: true, run: sub.lossRun(envelope.RunID, s.serials[envelope.RunID], s.runID, s.serials[s.runID], s.finished)})
		}
	}
	s.mu.Unlock()
}

func (sub *subscriber) exposedTo(runID protocol.RunID, serial uint64) bool {
	sub.pendMu.Lock()
	defer sub.pendMu.Unlock()
	if sub.ackSerial == 0 {
		if sub.attached == runID {
			return true
		}
		if sub.attachedSerial == 0 {
			return sub.pending[runID] > 0
		}
		return sub.pending[runID] > 0 && serial >= sub.attachedSerial
	}
	if serial == 0 {
		return false
	}
	return serial == sub.ackSerial || (serial > sub.ackSerial && sub.pending[runID] > 0)
}

func (s *Session) signalOverflow(runID protocol.RunID) {
	s.stopExposed(runID, &terminalState{overflow: true, run: runID})
}

func (s *Session) finishSubs(state *terminalState) {
	for _, sub := range s.detachSubs() {
		sub.stop(state)
	}
}

func (s *Session) detachSubs() []*subscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detachSubsLocked()
}

func (s *Session) snapshotSubsLocked() []*subscriber {
	subs := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	return subs
}

func (s *Session) detachSubsLocked() []*subscriber {
	subs := make([]*subscriber, 0, len(s.subs))
	for sub := range s.subs {
		subs = append(subs, sub)
	}
	s.subs = make(map[*subscriber]struct{})
	return subs
}

func (s *Session) markClosed() {
	s.mu.Lock()
	s.closed = true
	var errored []*subscriber
	var failed *terminalState
	switch {
	case s.readers > 0:

		if s.pendingEnd != nil && s.pendingEnd.err != nil {
			errored, failed = s.deferred, s.pendingEnd
			for _, sub := range errored {
				delete(s.subs, sub)
			}
			s.pendingEnd = nil
		}
		s.deferred = s.snapshotSubsLocked()
		s.mu.Unlock()
	case s.reservations > 0:

		if s.pendingEnd != nil && s.pendingEnd.err != nil {
			errored, failed = s.deferred, s.pendingEnd
			for _, sub := range errored {
				delete(s.subs, sub)
			}
			s.pendingEnd = nil
		}
		s.finishDue = true
		s.deferred = s.snapshotSubsLocked()
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		s.finishSubs(nil)
	}
	s.deliverDeferredError(errored, failed)
}

func (s *Session) closeForShutdown(ctx context.Context) error {
	err := s.session.Close(ctx)
	for attempt := 0; errors.Is(err, base.ErrRunActive) && attempt < 3; attempt++ {
		if ctx.Err() != nil {
			break
		}
		state, stateErr := s.session.State(ctx)
		if stateErr == nil {
			for _, run := range liveRuns(state) {
				_, _ = s.session.Cancel(ctx, run)
			}
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

func liveRuns(state protocol.SessionState) []protocol.RunID {
	runs := make([]protocol.RunID, 0, len(state.ActiveRuns)+1)
	seen := map[protocol.RunID]bool{}
	for _, entry := range state.ActiveRuns {
		if entry.RunID == "" || seen[entry.RunID] {
			continue
		}
		seen[entry.RunID] = true
		runs = append(runs, entry.RunID)
	}
	if state.ActiveRunID != "" && !seen[state.ActiveRunID] {
		runs = append(runs, state.ActiveRunID)
	}
	return runs
}
