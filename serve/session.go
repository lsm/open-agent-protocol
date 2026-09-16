package serve

import (
	"context"
	"errors"
	"fmt"
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
	// nextSerial numbers runs in admission order, recorded in serials:
	// overflow exposure compares admission order, not the order envelopes
	// happen to reach a mailbox — a lagging older drainer may publish
	// after a newer run's first envelope.
	readers      int
	reservations int
	finishDue    bool
	pendingEnd   *terminalState
	deferred     []*subscriber
	subs         map[*subscriber]struct{}
	nextSerial   uint64
	serials      map[protocol.RunID]uint64
}

func newSession(id protocol.SessionID, adapterName string, session base.Session) *Session {
	return &Session{
		id: id, adapterName: adapterName, session: session,
		created: time.Now(), subs: make(map[*subscriber]struct{}), serials: make(map[protocol.RunID]uint64),
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

// State reads the adapter's authoritative session state. An adapter
// reporting the session closed (a process-backed adapter whose child exited
// while idle) closes the hub-side entry too, mirroring the terminal Submit
// rejection: live subscribers end and new subscriptions are refused rather
// than parking on a session that can never run again.
func (s *Session) State(ctx context.Context) (protocol.SessionState, error) {
	state, err := s.session.State(ctx)
	if errors.Is(err, base.ErrSessionClosed) {
		s.markClosed()
	}
	if err != nil {
		return state, err
	}
	if state.SessionID != s.id {
		// The same rule the catalog takes below, and this unit is why it
		// applies here: a snapshot now carries the sources a session attached,
		// so a snapshot answered in the wrong scope publishes one session's
		// attachments under another's id — or under none, which no later rule
		// can catch, because every one of them keys off the session the
		// snapshot names. The codec labels the envelope from the session it
		// addressed, so an unchecked value here survives into a response the
		// clients reject.
		return protocol.SessionState{}, fmt.Errorf("serve: adapter reported state scoped to session %q, want %q", state.SessionID, s.id)
	}
	return state, nil
}

// Models reads the session's effective model catalog. The request is
// forwarded unchanged — the caller's own allow_degraded_features included, so
// the adapter applies the opt-in rule the wire states — and a session whose
// adapter does not serve a catalog is refused rather than answered with an
// empty one: an endpoint that lists nothing and an endpoint that cannot list
// are different answers to the same question.
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
	// What an adapter hands back is checked against what the hub knows before
	// any of it reaches the wire. The hub asked one session for its catalog,
	// so it owes its caller that session's catalog under the revision that
	// governs it, or nothing at all.
	if catalog.Revision == "" {
		// A listing nothing can bind to a descriptor is worse than none: a
		// consumer would cache it under no revision and never know when to
		// discard it, and the validator's own gate rejects the envelope. The
		// adapter is at fault, so the hub refuses rather than inventing a
		// revision from a descriptor it read at another moment.
		return base.Catalog{}, errors.New("serve: adapter served a model catalog with no capability revision")
	}
	if catalog.Models.SessionID != s.id {
		// The same rule for the listing's scope. Publishing the adapter's
		// value would emit a models.response scoped to another session, or to
		// none — schema-invalid, and rejected by the very clients this unit
		// taught to check a catalog's scope, so the daemon would be producing
		// envelopes its own clients refuse. Rewriting it would be worse than
		// refusing: the adapter computed this listing for the session it
		// named, so relabelling it would show one session's models under
		// another's id and launder the fault into something that looks
		// correct.
		return base.Catalog{}, fmt.Errorf("serve: adapter served a model catalog scoped to session %q, want %q", catalog.Models.SessionID, s.id)
	}
	if catalog.Models.Models == nil {
		// An adapter that builds its listing by appending hands back an absent
		// list where it meant an empty one, and Go marshals that as null. The
		// schema requires an array, so the envelope a codec mints from it
		// would be invalid — and this is the one fault of the three that is
		// repaired rather than refused. A missing revision and a foreign scope
		// have no correct substitute: inventing either would publish something
		// the adapter never said. An absent list and an empty one say the same
		// thing, that this session lists no models, and only one of the two
		// spellings is legal on the wire, so normalising states the adapter's
		// own answer rather than replacing it.
		catalog.Models.Models = []protocol.ModelDescriptor{}
	}
	return catalog, nil
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
		if stream != nil {
			// Some adapters return a live stream alongside the error —
			// cancellation after native admission, with the run still
			// alive adapter-side. The hub still drains and publishes it;
			// the caller still sees the error.
			s.adoptOrphan(stream)
		} else {
			s.releaseReservation()
		}
		// An adapter reporting the session closed (a dead transport made a
		// process-backed adapter unusable) closes the hub-side entry too:
		// no future run can ever reach live subscribers, and a
		// subscribe-before-submit consumer parked in Next would otherwise
		// block forever.
		if errors.Is(err, base.ErrSessionClosed) {
			s.markClosed()
		}
		return admission, err
	}
	s.adoptRun(admission.RunID, stream)
	return admission, nil
}

// Tools serves this session's effective tool catalog. The request is
// forwarded unchanged — the hub adds no protocol semantics, so the degraded
// opt-in the caller sent is applied by the adapter to exactly what the wire
// said — and its SessionID, when present, must name this session. An adapter
// that serves no portable catalog answers ErrToolCatalogUnavailable, which
// every binding maps to the typed refusal naming action.tools.list.
//
// What the adapter hands back is checked against what the hub asked before any
// of it reaches a binding, exactly as Models does. Forwarding it unchecked is
// how one session's catalog reaches another session's envelope: a codec labels
// the envelope with the session it addressed, so an adapter's conflicting
// payload scope survives into a response this project's own clients reject.
// Fixing the adapters that get it wrong is necessary and not sufficient — the
// boundary is where every adapter, including ones this repository never sees,
// stops being taken at its word.
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
		// A listing nothing can bind to a descriptor is worse than none: a
		// consumer would cache it under no revision and never know when to
		// discard it. This unit's catalogs need that more than the model ones,
		// not less — an endpoint that republishes its tools per turn changes
		// what it lists without anyone asking, and the revision is what tells a
		// caller which descriptor it is holding.
		return base.ToolCatalog{}, errors.New("serve: adapter served a tool catalog with no capability revision")
	}
	if catalog.Tools.SessionID != request.SessionID {
		// The scope the adapter answered in must be the scope the caller asked
		// in, in both directions. An unscoped request asks for the endpoint's
		// own catalog and a scoped one for a session's; answering either with
		// the other substitutes a different question's answer, and the one that
		// leaks — a session's attachments published with no scope — escapes
		// every later rule, because the lifetime checks that would catch it key
		// off the session the response names.
		//
		// Refused rather than relabelled: the adapter computed this listing for
		// the scope it named, so rewriting the label would show one session's
		// tools under another's id and launder the fault into something that
		// looks correct.
		return base.ToolCatalog{}, fmt.Errorf("serve: adapter served a tool catalog scoped to session %q, want %q", catalog.Tools.SessionID, request.SessionID)
	}
	if catalog.Tools.Tools == nil {
		// An adapter that builds its listing by appending hands back an absent
		// list where it meant an empty one, and Go marshals that as null while
		// the schema requires an array. This is the one fault of the three that
		// is repaired rather than refused, for the reason Models gives: an
		// absent list and an empty one say the same thing, and only one of the
		// two spellings is legal on the wire.
		catalog.Tools.Tools = []protocol.ToolDefinition{}
	}
	return catalog, nil
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
	// lastRun tracks the tail of its mailbox; ack the run of the last
	// envelope the consumer received through Next; pending the runs with
	// envelopes still in the mailbox; and runSerials the admission-order
	// token of each run the mailbox saw, with ackSerial the acknowledged
	// run's token. An adapter-reported overflow terminates the subscriber
	// when the overflowing run is its acknowledged position or still holds
	// undelivered envelopes admitted after that position — the consumer's
	// view of such a run is incomplete — but not for a run it already
	// moved past, whose stragglers are delivery lag, not loss. Admission
	// order, not mailbox-delivery order, decides "after": a lagging older
	// drainer may publish behind a newer run's first envelope.
	attached       protocol.RunID
	attachedSerial uint64
	lastRun        protocol.RunID
	ack            atomic.Pointer[protocol.RunID]

	pendMu     sync.Mutex
	pending    map[protocol.RunID]int
	runSerials map[protocol.RunID]uint64
	ackSerial  uint64
}

// acknowledge records the run of an envelope the consumer received and
// retires one of its pending mailbox entries; the steps share the pending
// lock so the hub's exposure decision never observes the half-done state.
func (sub *subscriber) acknowledge(run protocol.RunID) {
	sub.pendMu.Lock()
	if sub.pending[run] > 0 {
		sub.pending[run]--
	}
	// The acknowledged admission order only moves forward: a late envelope
	// from an older, still-draining run must not drag the position back,
	// and the acknowledged run identifier must stay paired with the
	// newest serial — a regressed pointer would name the wrong run for
	// loss cursors that resolve by acknowledged position.
	if serial := sub.runSerials[run]; serial > sub.ackSerial {
		sub.ackSerial = serial
		sub.ack.Store(&run)
	} else if current := sub.ack.Load(); current == nil {
		sub.ack.Store(&run)
	}
	sub.pendMu.Unlock()
}

// track records an envelope of run — admitted at serial — as queued to the
// mailbox.
func (sub *subscriber) track(run protocol.RunID, serial uint64) {
	sub.pendMu.Lock()
	sub.pending[run]++
	sub.runSerials[run] = serial
	sub.pendMu.Unlock()
}

// untrack retires one queued entry of run — the send could not proceed.
func (sub *subscriber) untrack(run protocol.RunID) {
	sub.pendMu.Lock()
	if sub.pending[run] > 0 {
		sub.pending[run]--
	}
	sub.pendMu.Unlock()
}

// lossRun reports the run a queue-full drop's cursor should name: the
// newest of the dropped envelope's run, the subscriber's acknowledged
// position (or its attachment run before anything is acknowledged), any
// run with envelopes still queued, and the session's current admitted run
// — detaching the subscriber discards every one of those runs' remaining
// delivery, and a cursor on any older run cannot recover the newer ones'
// tails. The drain before the terminal records the queued envelopes'
// positions, so the cursor resumes a queued run exactly where delivery
// stopped; a run never delivered replays from its start.
func (sub *subscriber) lossRun(dropped protocol.RunID, droppedSerial uint64, current protocol.RunID, currentSerial uint64) protocol.RunID {
	sub.pendMu.Lock()
	defer sub.pendMu.Unlock()
	newest, newestSerial := dropped, droppedSerial
	consider := func(run protocol.RunID, serial uint64) {
		if serial > newestSerial {
			newest, newestSerial = run, serial
		}
	}
	if sub.ackSerial > newestSerial {
		if ack := sub.ack.Load(); ack != nil {
			newest, newestSerial = *ack, sub.ackSerial
		}
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

func newSubscriber(queue int, attached protocol.RunID, attachedSerial uint64) *subscriber {
	return &subscriber{
		ch: make(chan protocol.Envelope, queue), finish: make(chan struct{}),
		attached: attached, attachedSerial: attachedSerial,
		pending: make(map[protocol.RunID]int), runSerials: make(map[protocol.RunID]uint64),
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
	sub := newSubscriber(queue, s.runID, s.serials[s.runID])
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

// bindRun registers a run discovered from an orphan stream's envelopes
// against the admission serial reserved when the orphan was adopted: the
// bound run becomes current when that serial outranks the current run's —
// superseding the run that predated the adoption, deferring to any
// genuinely newer admission.
func (s *Session) bindRun(runID protocol.RunID, reserved uint64) protocol.RunID {
	s.mu.Lock()
	if s.serials[runID] == 0 {
		s.serials[runID] = reserved
	}
	if s.reservations > 0 {
		// The envelope proves the admission occurred: the reservation this
		// drainer holds becomes the reader.
		s.reservations--
	}
	promoted := reserved > s.serials[s.runID]
	if promoted {
		s.runID = runID
	}
	// Only a promoted binding supersedes: an older orphan's binding leaves
	// the current run and its deferred state untouched — clearing them
	// would let a newer run's deferred error die with the older reader's
	// eventual stale exit.
	var errored []*subscriber
	var failed *terminalState
	if promoted {
		errored, failed = s.supersedeLocked()
	}
	s.mu.Unlock()
	s.deliverDeferredError(errored, failed)
	return runID
}

// startRun points the hub at a newly admitted run and starts draining its
// adapter stream. The drain is unconditional for the whole run lifetime, so
// the adapter's bounded subscriber buffers never fill on the hub side.
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

// adoptRun converts a Submit reservation into the new run's draining
// reader. A deferred clean end is superseded — the new run governs the
// subscribers' outcome — but a deferred stream error is first delivered to
// the cohort that observed the failed run: such errors are terminal for
// the subscriptions that saw them, and a newer run must not bury one.
func (s *Session) adoptRun(runID protocol.RunID, stream base.EventStream) {
	s.mu.Lock()
	s.reservations--
	s.readers++
	s.runID = runID
	s.nextSerial++
	s.serials[runID] = s.nextSerial
	errored, failed := s.supersedeLocked()
	s.mu.Unlock()
	s.deliverDeferredError(errored, failed)
	go s.readRun(runID, stream, 0)
}

// supersedeLocked clears the deferred finish state a new run supersedes,
// detaching the cohort first when the deferred outcome was a stream error
// that must still be delivered — a stopped subscriber left in the live set
// would keep receiving the new run's envelopes ahead of its terminal. s.mu
// must be held.
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

// adoptOrphan spawns a drainer for a stream an adapter returned alongside
// a submit error — cancellation after native admission with the run still
// alive (Pi does this). The hub must still drain and publish the stream or
// the adapter's bounded emission blocks and the run's events never reach
// subscriptions. The reservation stays held until the stream proves which
// it is: an envelope binds the run and converts the reservation into the
// reader, while a stream that ends empty (ACP's pre-admission write
// failure) releases it as a rejected admission.
func (s *Session) adoptOrphan(stream base.EventStream) {
	s.mu.Lock()
	s.readers++
	// Reserve the admission serial now, in admission order; the run the
	// envelopes later name binds to it.
	s.nextSerial++
	reserved := s.nextSerial
	s.mu.Unlock()
	go s.readRun("", stream, reserved)
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
func (s *Session) readRun(runID protocol.RunID, stream base.EventStream, reserved uint64) {
	var end *terminalState
	for result := range stream {
		// An orphan stream — returned alongside a submit error — carries no
		// run id of its own: bind it to the run its envelopes name before
		// any terminal signal, so overflow cursors and deferral bookkeeping
		// resolve to the authoritative run rather than an empty one. The
		// admission serial was reserved when the orphan was adopted, so the
		// bound run supersedes the run that predated the adoption while any
		// genuinely newer admission keeps precedence.
		if runID == "" && result.Envelope.RunID != "" {
			runID = s.bindRun(result.Envelope.RunID, reserved)
		}
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
	if reserved > 0 && runID == "" {
		// An error stream that never delivered an envelope is a rejected
		// admission after all (ACP's pre-admission write failure closes an
		// empty stream): undo the drainer and release the reservation as a
		// rejection, so parked subscribers stay parked for a corrected
		// retry instead of reading a phantom run's end. Leaving last still
		// applies whatever finish is pending — a real run's deferred end
		// stashed while this drainer held the reader slot, or a close's
		// cohort — exactly as a last reader's exit would.
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

// stopExposed detaches the subscribers exposed to runID and terminates
// them with the given state.
func (s *Session) stopExposed(runID protocol.RunID, state *terminalState) {
	s.mu.Lock()
	affected := s.detachExposedLocked(runID)
	s.mu.Unlock()
	for _, sub := range affected {
		sub.stop(state)
	}
}

// detachExposedLocked removes and returns the subscribers exposed to runID
// from the live set; s.mu must be held, so the scan cannot miss members a
// concurrent bookkeeping path is about to detach.
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
	// terminated after the bookkeeping below. Staleness is decided inside
	// that same critical section — deciding it earlier would let a
	// concurrent admission flip the run between the classification and the
	// exit bookkeeping, dropping the error through the gap. The current
	// run's error keeps the deferred path — co-drainers may still be
	// delivering its tail.
	var exposed []*subscriber
	s.mu.Lock()
	s.readers--
	current := s.runID
	// A stale drainer's error must not vanish because a newer run keeps
	// the hub busy: the subscribers still exposed to the failed run are
	// collected in this same critical section — collecting them after the
	// bookkeeping below would miss members detached from the live set on
	// the way out. The current run's error keeps the deferred path —
	// co-drainers may still be delivering its tail.
	if end != nil && end.err != nil && current != runID {
		exposed = s.detachExposedLocked(runID)
	}
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

// publish delivers one envelope to every subscriber, terminating (not
// blocking on) subscribers whose queue is full. Delivery advances the
// subscriber's position to the envelope's run.
func (s *Session) publish(envelope protocol.Envelope) {
	s.mu.Lock()
	for sub := range s.subs {
		// Record the run and its admission serial before the send can wake
		// a blocked consumer: acknowledge pairs with this bookkeeping, and
		// a consumer woken ahead of it would leave a phantom pending
		// envelope that later detaches it from an unrelated overflow.
		sub.track(envelope.RunID, s.serials[envelope.RunID])
		select {
		case sub.ch <- envelope:
			sub.lastRun = envelope.RunID
		default:
			sub.untrack(envelope.RunID)
			// The subscriber fell behind and this envelope was dropped. The
			// terminal names the run whose future delivery is discarded:
			// the dropped envelope's run, unless the subscriber's
			// acknowledged position is newer — a cursor on an older run
			// cannot recover the newer run's remaining events.
			delete(s.subs, sub)
			sub.stop(&terminalState{overflow: true, run: sub.lossRun(envelope.RunID, s.serials[envelope.RunID], s.runID, s.serials[s.runID])})
		}
	}
	s.mu.Unlock()
}

// exposedTo reports, as one consistent snapshot, whether the subscriber is
// exposed to runID — admitted at serial: the acknowledged run always
// exposes, and so does a run with undelivered envelopes admitted after the
// acknowledged position — the consumer's view of it is incomplete, and a
// clean end would hide the loss. A run the consumer already moved past
// does not expose: its late envelopes are delivery lag, not loss. Before
// anything is acknowledged, the attachment run and runs admitted after it
// expose; a pending envelope from a run admitted before the attachment
// does not — the subscription's position was the newer run all along.
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
	var errored []*subscriber
	var failed *terminalState
	switch {
	case s.readers > 0:
		// Readers still drain, and the last one's exit ends the
		// subscribers. A close owes every live subscriber — no future run
		// can reach the ones that registered after the current run's
		// reader deferred its end — so the deferred cohort is refreshed to
		// everyone subscribed at close time. A deferred stream error is
		// delivered first to its own cohort and detached: the close's
		// clean end is what subscribers outside that cohort are owed, and
		// first-writer-wins keeps the error for those inside it.
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
		// No reader remains; the in-flight admission's resolution ends the
		// subscribers the close found (later subscribes are refused). A
		// deferred stream error reaches its own cohort first and detaches —
		// subscribers that registered after that run's exit are owed the
		// clean close, not an error from a run they never observed — and
		// the close cohort is then everyone still live.
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
