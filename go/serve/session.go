package serve

import (
	"context"
	"errors"
	"fmt"
	"github.com/lsm/open-agent-protocol/go/binding"
	"sync"
	"sync/atomic"
	"time"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

type sessionRegistry struct {
	mu       sync.RWMutex
	sessions map[protocol.SessionID]*Session
	runs     *runIndex
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[protocol.SessionID]*Session), runs: &runIndex{}}
}

type runIndex struct {
	mu     sync.RWMutex
	owners map[protocol.RunID]map[protocol.SessionID]bool
}

func (r *runIndex) claim(session protocol.SessionID, run protocol.RunID) {
	if run == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owners == nil {
		r.owners = make(map[protocol.RunID]map[protocol.SessionID]bool)
	}
	holders := r.owners[run]
	if holders == nil {
		holders = make(map[protocol.SessionID]bool)
		r.owners[run] = holders
	}
	holders[session] = true
}

func (r *runIndex) foreign(run protocol.RunID, self protocol.SessionID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	holders := r.owners[run]
	if len(holders) == 0 || holders[self] {
		return false
	}
	return true
}

func (r *runIndex) release(session protocol.SessionID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for run, holders := range r.owners {
		delete(holders, session)
		if len(holders) == 0 {
			delete(r.owners, run)
		}
	}
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

func (r *sessionRegistry) remove(id protocol.SessionID, entry *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[id] == entry {
		delete(r.sessions, id)
	}
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

	closed  bool
	release func(*Session)

	binding binding.Record

	work workLog

	readers      int
	reservations int
	finishDue    bool
	pendingEnd   *terminalState
	deferred     []*subscriber
	subs         map[*subscriber]struct{}
	nextSerial   uint64
	serials      map[protocol.RunID]uint64
	sequences    map[protocol.RunID]uint64

	finished map[protocol.RunID]bool

	runs *runIndex

	drainWake chan struct{}
	gateCond  *sync.Cond
	gate      *publicationGate
}

const gateDeadline = 30 * time.Second

const steerDrainDeadline = 5 * time.Second

const steerDrainBatch = 64

type publicationGate struct {
	runs     []protocol.RunID
	unnamed  bool
	request  protocol.EnvelopeID
	boundary uint64
	withheld []protocol.Envelope
	deadline *time.Timer

	requested atomic.Bool
	drained   atomic.Bool
	drainedCh chan struct{}
	doneOnce  sync.Once
}

func (g *publicationGate) cover(run protocol.RunID) {
	if run == "" {
		g.unnamed = true
		return
	}
	for _, covered := range g.runs {
		if covered == run {
			return
		}
	}
	g.runs = append(g.runs, run)
}

func (g *publicationGate) covers(run protocol.RunID) bool {
	if g.unnamed {
		return true
	}
	if run == "" {
		return false
	}
	for _, covered := range g.runs {
		if covered == run {
			return true
		}
	}
	return false
}

func (g *publicationGate) markDrainRequested() { g.requested.Store(true) }

func (s *Session) awaitDrain(gate *publicationGate, readers int) {
	if readers == 0 {
		return
	}
	s.mu.Lock()
	served := s.gate == gate && s.gateServedLocked(gate)
	s.mu.Unlock()
	if !served {
		return
	}
	select {
	case <-gate.drainedCh:
	case <-time.After(steerDrainDeadline):
	}
}

func (s *Session) gateServedLocked(gate *publicationGate) bool {
	if gate.unnamed {
		return s.readers > 0
	}
	for _, run := range gate.runs {
		if s.serials[run] != 0 && !s.finished[run] {
			return true
		}
	}
	return false
}

func (g *publicationGate) drainPending() bool { return g.requested.Load() && !g.drained.Load() }

func (g *publicationGate) withholds(envelope protocol.Envelope) bool {
	for _, covered := range g.runs {
		if covered == envelope.RunID {
			return true
		}
	}
	return g.request != "" && settlementRequest(envelope) == g.request
}

func settlementRequest(envelope protocol.Envelope) protocol.EnvelopeID {
	switch envelope.Type {
	case protocol.TypeRunSteerApplied:
		var payload protocol.RunSteerAppliedPayload
		if err := envelope.DecodePayload(&payload); err != nil {
			return ""
		}
		return payload.RequestID
	case protocol.TypeRunSteerDropped:
		var payload protocol.RunSteerDroppedPayload
		if err := envelope.DecodePayload(&payload); err != nil {
			return ""
		}
		return payload.RequestID
	}
	return ""
}

func (g *publicationGate) finishDrain() {
	g.doneOnce.Do(func() {
		g.drained.Store(true)
		close(g.drainedCh)
	})
}

func (g *publicationGate) split(boundary uint64) (prefix, rest []protocol.Envelope) {
	for _, envelope := range g.withheld {
		if envelope.Sequence != nil && *envelope.Sequence <= boundary {
			prefix = append(prefix, envelope)
			continue
		}
		rest = append(rest, envelope)
	}
	return prefix, rest
}

func newSession(id protocol.SessionID, adapterName string, session base.Session, release func(*Session)) *Session {
	entry := &Session{
		id: id, adapterName: adapterName, session: session, release: release,
		created: time.Now(), subs: make(map[*subscriber]struct{}), serials: make(map[protocol.RunID]uint64),
		sequences: make(map[protocol.RunID]uint64),
		finished:  make(map[protocol.RunID]bool),
	}
	entry.gateCond = sync.NewCond(&entry.mu)
	return entry
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

func (s *Session) SwitchModel(ctx context.Context, request protocol.SessionModelSwitchRequest) (protocol.SessionModelSwitchResponse, protocol.SessionState, error) {
	if request.SessionID != s.id {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}
	switcher, ok := s.session.(base.ModelSwitcher)
	if !ok {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, &base.UnsupportedControlError{
			Feature: protocol.FeatureSessionModelSwitch, Reason: base.ControlUnadvertised,
		}
	}
	response, state, err := switcher.SwitchModel(ctx, request)
	if errors.Is(err, base.ErrSessionClosed) {
		s.markClosed()
	}
	if err != nil {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, err
	}
	if response.SessionID != s.id || state.SessionID != s.id || response.ModelID != state.CurrentModelID {
		return protocol.SessionModelSwitchResponse{}, protocol.SessionState{}, fmt.Errorf("serve: adapter reported a model switch outside session %q", s.id)
	}
	return response, state, nil
}

func (s *Session) UpdateSettings(ctx context.Context, request protocol.SessionSettingsUpdateRequest) (protocol.SessionSettingsUpdateResponse, protocol.SessionState, error) {
	if request.SessionID != s.id {
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}
	keys := base.OpenSettingKeys(request.ReasoningLevel, request.CompactionPolicy)
	if len(keys) == 0 {
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, fmt.Errorf("%w: a settings update names no setting", base.ErrInvalidSubmission)
	}
	updater, ok := s.session.(base.SettingsUpdater)
	if !ok {
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, &base.UnsupportedControlError{
			Feature: keys[0], Reason: base.ControlUnadvertised, Field: base.OpenSettingField(keys[0]),
		}
	}
	response, state, err := updater.UpdateSettings(ctx, request)
	if errors.Is(err, base.ErrSessionClosed) {
		s.markClosed()
	}
	if err != nil {
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, err
	}
	if response.SessionID != s.id || state.SessionID != s.id {
		return protocol.SessionSettingsUpdateResponse{}, protocol.SessionState{}, fmt.Errorf("serve: adapter reported a settings update outside session %q", s.id)
	}
	return response, state, nil
}

func (s *Session) armGate(ctx context.Context, run protocol.RunID, request protocol.EnvelopeID) (*publicationGate, error) {
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			s.mu.Lock()
			s.gateCond.Broadcast()
			s.mu.Unlock()
		case <-done:
		}
	}()
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.gate != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.gateCond.Wait()
	}
	if s.closed {
		return nil, base.ErrSessionClosed
	}
	gate := &publicationGate{request: request, boundary: s.sequences[run], drainedCh: make(chan struct{})}
	gate.cover(run)
	s.gate = gate
	gate.deadline = time.AfterFunc(gateDeadline, func() { s.liftGate(gate) })
	return gate, nil
}

func (s *Session) releaseToBoundary(gate *publicationGate, boundary uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate != gate {
		return
	}
	prefix, rest := gate.split(boundary)
	gate.withheld, gate.boundary = rest, boundary
	for _, envelope := range prefix {
		s.deliverLocked(envelope)
	}
}

func (s *Session) liftGate(gate *publicationGate) {
	if gate == nil {
		return
	}
	s.mu.Lock()
	if s.gate != gate {
		s.mu.Unlock()
		return
	}
	s.gate = nil
	gate.finishDrain()
	withheld := gate.withheld
	gate.withheld = nil
	if gate.deadline != nil {
		gate.deadline.Stop()
	}
	s.gateCond.Broadcast()
	for _, envelope := range withheld {
		s.deliverLocked(envelope)
	}
	s.mu.Unlock()
}

func (s *Session) Published(request protocol.EnvelopeID) {
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	if gate == nil || gate.request != request {
		return
	}
	s.liftGate(gate)
}

func (s *Session) waitGateLifted(run protocol.RunID) {
	s.mu.Lock()
	for s.gate != nil && s.gate.covers(run) {
		if s.gate.drainPending() {
			s.gate.finishDrain()
		}
		s.gateCond.Wait()
	}
	s.mu.Unlock()
}

func (s *Session) Submit(ctx context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, error) {
	request := submit.Request
	if request.SessionID != s.id {
		return protocol.MessageSubmitResponse{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}
	if request.Delivery == protocol.DeliverySteer {
		return s.submitSteer(ctx, submit)
	}

	s.mu.Lock()
	s.reservations++
	s.mu.Unlock()
	admission, stream, err := s.session.Submit(ctx, submit)
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
	s.recordSubmitted(request, admission.RunID, time.Now().UnixMilli())
	if admission.Admission == protocol.AdmissionSteered {
		s.releaseReservation()
		return admission, nil
	}
	s.adoptRun(admission.RunID, stream, admission.Admission == protocol.AdmissionQueued)
	return admission, nil
}

func (s *Session) Compact(ctx context.Context, compact base.CompactRequest) (protocol.SessionCompactResponse, error) {
	request := compact.Request
	if request.SessionID != s.id {
		return protocol.SessionCompactResponse{}, &ScopeMismatchError{Payload: request.SessionID, Addressed: s.id}
	}
	compactor, ok := s.session.(base.Compactor)
	if !ok {
		return protocol.SessionCompactResponse{}, &base.UnsupportedControlError{Feature: protocol.FeatureSessionCompact, Reason: base.ControlUnadvertised}
	}
	s.mu.Lock()
	s.reservations++
	s.mu.Unlock()
	admission, stream, err := compactor.Compact(ctx, compact)
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

func (s *Session) retargetGate(gate *publicationGate, run protocol.RunID) {
	if gate == nil || run == "" {
		return
	}
	s.mu.Lock()
	if s.gate == gate {
		gate.cover(run)
	}
	s.mu.Unlock()
}

func (s *Session) foreignRun(run protocol.RunID) bool {
	if s.runs == nil {
		return false
	}
	return s.runs.foreign(run, s.id)
}

func (s *Session) submitSteer(ctx context.Context, submit base.SubmitRequest) (protocol.MessageSubmitResponse, error) {
	target := submit.Request.TargetRunID
	if target == "" {
		if current, ok := s.currentRun(); ok {
			target = current
		}
	}
	if s.foreignRun(target) {
		return protocol.MessageSubmitResponse{}, &base.InvalidSteerTargetError{RunID: target, Reason: base.SteerReasonCrossSession}
	}
	gate, err := s.armGate(ctx, target, submit.EnvelopeID)
	if err != nil {
		return protocol.MessageSubmitResponse{}, err
	}
	s.mu.Lock()
	s.reservations++
	s.mu.Unlock()
	admission, stream, err := s.session.Submit(ctx, submit)
	s.retargetGate(gate, admission.RunID)
	s.mu.Lock()
	readers := s.readers
	s.mu.Unlock()
	gate.markDrainRequested()
	s.broadcastDrain()
	s.awaitDrain(gate, readers)
	if err != nil {
		var refusal *base.InvalidSteerTargetError
		if errors.As(err, &refusal) {
			s.retargetGate(gate, refusal.RunID)
		}
		boundary := gate.boundary
		if refusal != nil && refusal.TargetSequence != nil {
			boundary = *refusal.TargetSequence
		}
		s.releaseToBoundary(gate, boundary)
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
	boundary := gate.boundary
	if admission.TargetSequence != nil {
		boundary = *admission.TargetSequence
	}
	s.releaseToBoundary(gate, boundary)
	s.recordSubmitted(submit.Request, admission.RunID, time.Now().UnixMilli())
	if stream != nil {
		s.adoptOrphan(stream)
	} else {
		s.releaseReservation()
	}
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

func (s *Session) CancelAndHold(ctx context.Context, runID protocol.RunID, request protocol.EnvelopeID) (protocol.RunCancelResponse, error) {
	gate, err := s.armGate(ctx, runID, request)
	if err != nil {
		return protocol.RunCancelResponse{}, err
	}
	ack, err := s.session.Cancel(ctx, runID)
	if err != nil {
		s.liftGate(gate)
	}
	return ack, err
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

func (s *Session) subscribe(queue int) (*subscriber, protocol.RunID, uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, "", 0, false
	}
	sub := newSubscriber(queue, s.runID, s.serials[s.runID])
	s.subs[sub] = struct{}{}
	return sub, s.runID, s.sequences[s.runID], true
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

func (s *Session) claimRun(runID protocol.RunID) {
	if s.runs == nil {
		return
	}
	s.runs.claim(s.id, runID)
}

func (s *Session) startRun(runID protocol.RunID, stream base.EventStream) {
	s.claimRun(runID)
	s.mu.Lock()
	s.readers++
	s.runID = runID
	s.nextSerial++
	s.serials[runID] = s.nextSerial
	delete(s.finished, runID)
	errored, failed := s.supersedeLocked()
	s.mu.Unlock()
	s.deliverDeferredError(errored, failed)
	go s.readRun(runID, stream, 0)
}

func (s *Session) adoptRun(runID protocol.RunID, stream base.EventStream, queued bool) {
	s.claimRun(runID)
	s.mu.Lock()
	s.reservations--
	s.readers++
	s.nextSerial++
	s.serials[runID] = s.nextSerial
	delete(s.finished, runID)
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

var closedSignal = func() chan struct{} {
	closed := make(chan struct{})
	close(closed)
	return closed
}()

func (s *Session) drainSignal(run protocol.RunID) chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate != nil && s.gate.drainPending() && s.gate.covers(run) {
		return closedSignal
	}
	if s.drainWake == nil {
		s.drainWake = make(chan struct{})
	}
	return s.drainWake
}

func (s *Session) broadcastDrain() {
	s.mu.Lock()
	if s.drainWake != nil {
		close(s.drainWake)
		s.drainWake = nil
	}
	s.gateCond.Broadcast()
	s.mu.Unlock()
}

func (s *Session) nextResult(run protocol.RunID, stream base.EventStream) (base.Result, bool) {
	for {
		if gate := s.drainingGate(run); gate != nil {
			result, ready, ok := s.drainGate(gate, stream)
			gate.finishDrain()
			if !ok {
				return base.Result{}, false
			}
			if ready {
				return result, true
			}
			continue
		}
		select {
		case result, ok := <-stream:
			return result, ok
		case <-s.drainSignal(run):
		}
	}
}

func (s *Session) drainingGate(run protocol.RunID) *publicationGate {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate == nil || !s.gate.drainPending() || !s.gate.covers(run) {
		return nil
	}
	return s.gate
}

func (s *Session) drainGate(gate *publicationGate, stream base.EventStream) (base.Result, bool, bool) {
	for drained := 0; drained < steerDrainBatch; drained++ {
		select {
		case result, ok := <-stream:
			if !ok {
				return base.Result{}, false, false
			}
			if result.Error != nil {
				return result, true, true
			}
			s.drainInto(result.Envelope)
		default:
			return base.Result{}, false, true
		}
	}
	return base.Result{}, false, true
}

func (s *Session) drainInto(envelope protocol.Envelope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gate != nil && s.gate.withholds(envelope) {
		s.gate.withheld = append(s.gate.withheld, envelope)
		return
	}
	s.deliverLocked(envelope)
}

func (s *Session) readRun(runID protocol.RunID, stream base.EventStream, reserved uint64) {
	var end *terminalState
	for {
		result, ok := s.nextResult(runID, stream)
		if !ok {
			break
		}

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
	s.waitGateLifted(runID)
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
	if s.gate != nil && s.gate.withholds(envelope) {
		s.gate.withheld = append(s.gate.withheld, envelope)
		s.mu.Unlock()
		return
	}
	s.deliverLocked(envelope)
	s.mu.Unlock()
}

func (s *Session) deliverLocked(envelope protocol.Envelope) {
	s.observeWorkLocked(envelope, time.Now().UnixMilli())
	if envelope.Sequence != nil && *envelope.Sequence > s.sequences[envelope.RunID] {
		s.sequences[envelope.RunID] = *envelope.Sequence
	}
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
	releasing := !s.closed && s.release != nil
	s.closed = true
	if s.runs != nil {
		s.runs.release(s.id)
	}
	var withheld []protocol.Envelope
	if s.gate != nil {
		withheld = s.gate.withheld
		s.gate.withheld = nil
		if s.gate.deadline != nil {
			s.gate.deadline.Stop()
		}
		s.gate.finishDrain()
		s.gate = nil
		s.gateCond.Broadcast()
	}
	for _, envelope := range withheld {
		s.deliverLocked(envelope)
	}
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
	if releasing {
		s.release(s)
	}
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
