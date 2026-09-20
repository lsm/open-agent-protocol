package validation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	stateNone = iota
	stateBusySession
	stateQueueLimit
)

const errorRunActive = "run_active"

const resolutionSessionBusy = "session_busy"

type queueWindow struct {
	request  protocol.EnvelopeID
	session  protocol.SessionID
	delivery protocol.RequestedDeliveryMode

	maxActive, maxQueued *int

	reachedStrict, reachedLoose bool

	busyAtRequest         bool
	busyEver, startedEver bool

	mutation bool

	offered bool
}

func (w *queueWindow) stateCondition() int {
	if w.delivery != "" && w.delivery != protocol.DeliveryAuto && w.delivery != protocol.DeliveryQueue {
		return stateNone
	}
	switch {
	case w.offered && (w.delivery == protocol.DeliveryQueue || w.busyEver):

		return stateQueueLimit
	case !w.offered && w.delivery != protocol.DeliveryQueue && w.busyEver:

		return stateBusySession
	}
	return stateNone
}

func (s *state) advertisedLevel(key string) (protocol.SupportLevel, bool) {
	if s.currentCapability == "" || s.capabilitiesStale {
		return "", false
	}
	if level, ok := s.features[key]; ok {
		return level, true
	}
	return protocol.SupportUnavailable, true
}

func (s *state) queueOffered() bool {
	level, known := s.advertisedLevel(protocol.FeatureDeliveryQueue)
	return known && affirmative(level)
}

func (s *state) queueCounts(session protocol.SessionID) (active, queued, started int) {
	st := s.sessions[session]
	if st == nil {
		return 0, 0, 0
	}
	for _, id := range st.order {
		r := s.runs[id]
		if r == nil || r.terminal {
			continue
		}
		active++
		switch {
		case r.admittedQueued && !r.started:
			queued++
		default:
			started++
		}
	}
	return active, queued, started
}

func (w *queueWindow) exceeds(active, queued, outstanding int) bool {
	if w.maxActive != nil && active+outstanding+1 > *w.maxActive {
		return true
	}
	if w.maxQueued != nil && queued+outstanding+1 > *w.maxQueued {
		return true
	}
	return false
}

func (s *state) refreshQueueWindows(session protocol.SessionID) {
	open := s.openSubmits[session]
	if len(open) == 0 {
		return
	}
	active, queued, started := s.queueCounts(session)

	outstanding := 0
	for _, pending := range open {
		if pending.queue != nil && !s.answeredRequest(pending.queue.request) {
			outstanding++
		}
	}
	for _, pending := range open {
		w := pending.queue
		if w == nil {
			continue
		}
		if active > 0 {
			w.busyEver = true
		}
		if started > 0 {
			w.startedEver = true
		}
		if w.exceeds(active, queued, 0) {
			w.reachedStrict = true
		}
		others := outstanding
		if !s.answeredRequest(w.request) {
			others--
		}
		if w.exceeds(active, queued, others) {
			w.reachedLoose = true
		}
	}
}

func (s *state) answeredRequest(id protocol.EnvelopeID) bool {
	req := s.requests[id]
	return req != nil && req.responded
}

func (s *state) closeSubmitWindow(request protocol.EnvelopeID) {
	pending := s.pendingControls[request]
	if pending == nil || pending.queue == nil {
		return
	}
	session := pending.session
	open := s.openSubmits[session]
	for i, candidate := range open {
		if candidate == pending {
			s.openSubmits[session] = append(open[:i:i], open[i+1:]...)
			break
		}
	}
	s.refreshQueueWindows(session)
}

func (s *state) deliveryExpectations(i, line int, e protocol.Envelope, p protocol.MessageSubmitRequest, pending *pendingSubmit) []*controlExpectation {
	active, queued, started := s.queueCounts(p.SessionID)
	window := &queueWindow{
		request: e.ID, session: p.SessionID, delivery: p.Delivery,
		busyAtRequest: active > 0, busyEver: active > 0, startedEver: started > 0,
		mutation: p.ModelID != nil && s.featureDetail(protocol.FeatureModelSelection).Scope == protocol.ScopeSession,
	}
	if s.limits != nil {
		window.maxActive, window.maxQueued = s.limits.MaxActiveRunsPerSession, s.limits.MaxQueuedRunsPerSession
	}
	window.offered = s.queueOffered()
	window.reachedStrict = window.exceeds(active, queued, 0)
	window.reachedLoose = window.exceeds(active, queued, len(s.openSubmits[p.SessionID]))
	pending.queue = window

	var expectations []*controlExpectation

	if p.Delivery != "" && p.Delivery != protocol.DeliveryAuto && !(s.tolerant && foreignRequestedDelivery(p.Delivery)) {

		key := protocol.DeliveryKey(p.Delivery)
		level, judged := s.controlDescriptor(i, line, e, key)
		switch {
		case !judged:
		case p.Delivery == protocol.DeliveryQueue && !affirmative(level):

			expectations = append(expectations, &controlExpectation{
				rung: rungCapability, key: key, pointer: "/payload/delivery",
				code: errorUnsupportedFeature, reason: reasonUnadvertised,
				detailName: "feature", detailValue: key,
				diagnostic: CodeUnavailableCapability,
				message:    "submission elects a delivery the endpoint has not affirmatively advertised",
			})
		case level == protocol.SupportDegraded && !p.AllowsDegraded(key):
			expectations = append(expectations, &controlExpectation{
				rung: rungDegradation, key: key, pointer: "/payload/delivery",
				code: errorCapabilityDegraded, detailName: "feature", detailValue: key,
				diagnostic: CodeDegradedWithoutOptin,
				message:    "submission elects a degraded delivery without the caller's opt-in",
			})
		}
	}
	return expectations
}

func (s *state) settleQueueRefusal(i, line int, e protocol.Envelope, pending *pendingSubmit, payload protocol.ErrorResponse) {
	window := pending.queue
	if window == nil {
		return
	}
	switch window.stateCondition() {
	case stateBusySession:
		if payload.Error.Code != errorRunActive {
			s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/error", "a busy session's refusal must be the wire's run_active", errorRunActive, describeRefusal(payload.Error), string(e.InReplyTo))
		}
	case stateQueueLimit:
		switch {
		case window.reachedStrict:
			if payload.Error.Code != errorRunActive {
				s.addExpected(CodeQueueLimitExceeded, i, line, e, "/payload/error", "a refusal at a reached queue bound must be the wire's run_active", errorRunActive, describeRefusal(payload.Error), string(e.InReplyTo))
			}
		case payload.Error.Code == errorRunActive && !window.reachedLoose && !(window.mutation && window.startedEver):

			s.addExpected(CodeQueueLimitExceeded, i, line, e, "/payload/error", "refusal reports a queue bound that was never reached", "admission", describeRefusal(payload.Error)+" "+window.describeBounds(), string(e.InReplyTo))
		}
	}
}

func (w *queueWindow) describeBounds() string {
	parts := []string{}
	if w.maxActive != nil {
		parts = append(parts, fmt.Sprintf("max_active_runs_per_session=%d", *w.maxActive))
	}
	if w.maxQueued != nil {
		parts = append(parts, fmt.Sprintf("max_queued_runs_per_session=%d", *w.maxQueued))
	}
	if len(parts) == 0 {
		return "(no bound disclosed)"
	}
	return "(" + strings.Join(parts, " ") + ")"
}

func (s *state) queueOverlap(i, line int, e protocol.Envelope, p protocol.MessageSubmitResponse, st *sessionTrack) bool {
	active, _, _ := s.queueCounts(p.SessionID)
	if active == 0 {
		return false
	}
	if p.Admission == protocol.AdmissionQueued && s.queueOffered() {
		return false
	}
	s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "session already has a nonterminal run", "a queued reservation on an endpoint advertising "+protocol.FeatureDeliveryQueue, string(p.Admission))
	return true
}

func (s *state) queueAdmission(i, line int, e protocol.Envelope, p protocol.MessageSubmitResponse, st *sessionTrack) {
	pending := s.pendingControls[e.InReplyTo]
	requested := p.RequestedDelivery
	if pending != nil && pending.queue != nil {
		requested = pending.queue.delivery
	}
	queued := p.Admission == protocol.AdmissionQueued
	switch {
	case requested == protocol.DeliveryQueue && !queued:

		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/admission", "an explicit queue request may only be admitted as a reservation", string(protocol.AdmissionQueued), string(p.Admission), string(e.InReplyTo))
	case requested == protocol.DeliveryAuto && queued && busyAtRequest(pending) && p.DeliveryResolution != resolutionSessionBusy:

		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/delivery_resolution", "an auto submission a busy session turned into a reservation must report the resolution that produced it", resolutionSessionBusy, p.DeliveryResolution, string(e.InReplyTo))
	}
	if !queued {
		return
	}

	gated := pending != nil && pending.expectation != nil && pending.expectation.key == protocol.FeatureDeliveryQueue
	if !gated {
		level, known := s.advertisedLevel(protocol.FeatureDeliveryQueue)
		switch {
		case !known:

		case !affirmative(level):
			s.addExpected(CodeUnavailableCapability, i, line, e, "/payload/admission", "a queued reservation requires the endpoint to advertise "+protocol.FeatureDeliveryQueue, "native, emulated, or degraded", string(level), string(e.InReplyTo))
		case level == protocol.SupportDegraded && !s.optedIntoQueue(e.InReplyTo):
			s.addExpected(CodeDegradedWithoutOptin, i, line, e, "/payload/admission", "a degraded queue was used without the caller's opt-in", protocol.FeatureDeliveryQueue+" in allow_degraded_features", "absent", string(e.InReplyTo))
		}
	}

	active, queuedCount, _ := s.queueCounts(p.SessionID)
	if s.limits == nil {
		return
	}
	if max := s.limits.MaxActiveRunsPerSession; max != nil && active+1 > *max {
		s.addExpected(CodeQueueLimitExceeded, i, line, e, "/payload/admission", "reservation puts the session's nonterminal set above the disclosed bound", fmt.Sprintf("at most %d", *max), fmt.Sprintf("%d", active+1), string(e.InReplyTo))
		return
	}
	if max := s.limits.MaxQueuedRunsPerSession; max != nil && queuedCount+1 > *max {
		s.addExpected(CodeQueueLimitExceeded, i, line, e, "/payload/admission", "reservation puts the session's queued subset above the disclosed bound", fmt.Sprintf("at most %d", *max), fmt.Sprintf("%d", queuedCount+1), string(e.InReplyTo))
	}
}

func busyAtRequest(pending *pendingSubmit) bool {
	return pending != nil && pending.queue != nil && pending.queue.busyAtRequest
}

func (s *state) optedIntoQueue(request protocol.EnvelopeID) bool {
	req := s.requests[request]
	if req == nil {
		return false
	}
	var p protocol.MessageSubmitRequest
	_ = req.envelope.DecodePayload(&p)
	return p.AllowsDegraded(protocol.FeatureDeliveryQueue)
}

func (s *state) checkQueueOrder(i, line int, e protocol.Envelope, r *runState) {
	if !r.admitted || e.Sequence == nil {
		return
	}
	if isTerminal(e.Type) && !r.started {
		return
	}
	st := s.sessions[r.session]
	if st == nil {
		return
	}
	for _, id := range st.order {
		earlier := s.runs[id]
		if earlier == nil || earlier.id == r.id || earlier.order >= r.order {
			continue
		}
		if !earlier.terminal {
			s.addExpected(CodeQueueOrderViolation, i, line, e, "/sequence", "a later-admitted run published an event while an earlier-admitted run was nonterminal", "every earlier admission terminal", string(earlier.id), string(earlier.id))
			return
		}
	}
}

func (s *state) promote(i int, e protocol.Envelope, r *runState) {
	if e.Sequence != nil {
		r.startSequence = *e.Sequence
	}
	if st := s.sessions[r.session]; st != nil {

		st.active = r.id
	}
	if !r.deferredControls {
		return
	}
	r.deferredControls = false
	if st := s.sessions[r.session]; st != nil {
		s.applyModelControl(st, r.controls)
	}
}

func (s *state) checkQueueLimits(i, line int, e protocol.Envelope, p protocol.CapabilitiesResponse) {
	support, ok := p.EffectiveSupport(protocol.FeatureDeliveryQueue)
	if !ok || !affirmative(support.Level) {

		return
	}
	queued := 0
	if p.Limits != nil && p.Limits.MaxQueuedRunsPerSession != nil {
		queued = *p.Limits.MaxQueuedRunsPerSession
	}
	if queued < 1 {
		s.addExpected(CodeUndisclosedQueueLimit, i, line, e, "/payload/limits/max_queued_runs_per_session", protocol.FeatureDeliveryQueue+" is advertised without a queue bound a submission could ever reach", "at least 1", describeLimit(p.Limits, false))
		return
	}

	if p.Limits.MaxActiveRunsPerSession != nil && *p.Limits.MaxActiveRunsPerSession < queued+1 {
		s.addExpected(CodeUndisclosedQueueLimit, i, line, e, "/payload/limits/max_active_runs_per_session", "the disclosed active bound leaves no room for the disclosed queue beside a started run", fmt.Sprintf("at least %d", queued+1), describeLimit(p.Limits, true))
	}
}

func (s *state) checkQueueAdvertisement(i, line int, e protocol.Envelope, outgoing descriptorSnapshot) {
	if !outgoing.repeats(s.currentCapability) {
		return
	}
	if current := s.features[protocol.FeatureDeliveryQueue]; current != outgoing.queue {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/payload/features/"+protocol.FeatureDeliveryQueue, protocol.FeatureDeliveryQueue+" changed under one capability revision without a capabilities.updated", describeSupport(outgoing.queue), describeSupport(current))
	}
	for _, bound := range []struct {
		name  string
		field func(*protocol.CapabilityLimits) *int
	}{
		{"max_active_runs_per_session", func(l *protocol.CapabilityLimits) *int { return l.MaxActiveRunsPerSession }},
		{"max_queued_runs_per_session", func(l *protocol.CapabilityLimits) *int { return l.MaxQueuedRunsPerSession }},
	} {
		before, after := boundOf(outgoing.limits, bound.field), boundOf(s.limits, bound.field)
		if sameBound(before, after) {
			continue
		}
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/payload/limits/"+bound.name, bound.name+" changed under one capability revision without a capabilities.updated", describeBound(before), describeBound(after))
	}
}

func boundOf(limits *protocol.CapabilityLimits, field func(*protocol.CapabilityLimits) *int) *int {
	if limits == nil {
		return nil
	}
	return field(limits)
}

func sameBound(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func describeBound(value *int) string {
	if value == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *value)
}

func describeLimit(limits *protocol.CapabilityLimits, active bool) string {
	if limits == nil {
		return "absent"
	}
	value := limits.MaxQueuedRunsPerSession
	if active {
		value = limits.MaxActiveRunsPerSession
	}
	if value == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *value)
}

type deferredStateClaim struct {
	kind    int
	session protocol.SessionID
	run     protocol.RunID
	request protocol.EnvelopeID
	pointer string

	accounted map[protocol.RunID]bool
	sequence  uint64

	held bool

	stated      bool
	listed      map[protocol.InteractionID]bool
	model       string
	index, line int
	envelope    protocol.Envelope

	entry *entryClaim
	done  bool
}

type entryClaim struct {
	entry      protocol.ActiveRun
	state      protocol.SessionState
	pointer    string
	executing  protocol.RunID
	queueAhead int
	leadsAhead int

	group *ledGroup

	classify bool

	resolved    bool
	reservation bool
	judged      bool
	run         protocol.RunID

	order int

	index, line int
	envelope    protocol.Envelope
}

type ledGroup struct {
	claims []*entryClaim

	settled bool
}

const (
	claimSettled = iota + 1
	claimCapture
	claimModel
	claimQueued
	claimAdmitted
	claimReservation
)

func (s *state) reconcileDeferred(i, line int, e protocol.Envelope, r *runState) {
	for _, claim := range s.deferred {
		if claim.done || claim.run != r.id {
			continue
		}
		switch claim.kind {
		case claimSettled:

			claim.done = true
			if isTerminal(e.Type) && e.Sequence != nil && *e.Sequence == claim.sequence {
				continue
			}
			actual := string(e.Type)
			if e.Sequence != nil {
				actual = fmt.Sprintf("%s at sequence %d", e.Type, *e.Sequence)
			}
			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/as_of/settled", "snapshot dropped a run whose claimed terminal is not what its domain published next", fmt.Sprintf("a terminal at sequence %d", claim.sequence), actual, string(claim.run))
		case claimCapture:
			if e.Sequence == nil || *e.Sequence < claim.sequence {
				continue
			}
			claim.done = true
			s.judgePendingInteractions(claim, r)
		case claimModel:
			if !r.started || r.startSequence != claim.sequence {
				continue
			}
			claim.done = true
			s.judgeCaptureModel(claim, r)
		case claimQueued:

			if !r.started {
				continue
			}
			claim.done = true
			if claim.sequence < r.startSequence {
				continue
			}
			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs reports a run as queued at a position it had already started at", "a started status", string(protocol.RunQueued), string(claim.run))
		case claimReservation:

			if !r.started {
				continue
			}
			claim.done = true
			if !claim.stated {

				continue
			}
			if claim.held == (claim.sequence < r.startSequence) {
				continue
			}
			if claim.held {
				s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs keeps a run in the queue at a position it had already started at", "no queue position", fmt.Sprintf("a queue place stated at sequence %d", claim.sequence), string(claim.run))
				continue
			}
			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs reports a run as out of the queue at a position it had not started at", "the queue place it still held", fmt.Sprintf("out of the queue at sequence %d", claim.sequence), string(claim.run))
		}
	}
}

func (s *state) judgePendingInteractions(claim *deferredStateClaim, r *runState) {
	want := pendingAt(r, claim.sequence)
	if sameIDSet(want, knownTo(r, claim.listed)) {
		return
	}
	s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs entry does not report the run's unresolved interactions at the position it states", describeIDs(want), describeIDs(claim.listed), string(claim.run))
}

func (s *state) judgeCaptureModel(claim *deferredStateClaim, r *runState) {
	model, known := mutationModel(r)
	if !known {

		s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/as_of/model_run_sequence", "capture position names a run that applied no session_mutation, so it is not a model-affecting event", "a run admitted with a session_mutation model selection", string(claim.run), string(claim.run))
		return
	}
	if model == claim.model {
		return
	}
	s.addExpected(CodePrematureSessionMutation, claim.index, claim.line, claim.envelope, "/payload/current_model_id", "snapshot reports a model other than the one in force at the position it states", model, claim.model, string(claim.run))
}

func mutationModel(r *runState) (string, bool) {
	if r == nil || !r.controls.present || !r.controls.modelPresent || r.controls.mode != protocol.ScopeSession {
		return "", false
	}
	return r.controls.model, true
}

func pendingAt(r *runState, seq uint64) map[protocol.InteractionID]bool {
	out := map[protocol.InteractionID]bool{}
	for id, x := range r.interactions {
		if x.openedAt > seq {
			continue
		}
		if x.resolved && x.resolvedAt != 0 && x.resolvedAt <= seq {
			continue
		}
		if x.resolved && x.resolvedAt == 0 {
			continue
		}
		out[id] = true
	}
	return out
}

func sameIDSet(a, b map[protocol.InteractionID]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

func describeIDs(set map[protocol.InteractionID]bool) string {
	if len(set) == 0 {
		return "none"
	}
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func (s *state) admitLedEntries(run *runState) {
	for _, claim := range s.deferred {
		if claim.done || claim.kind != claimAdmitted || claim.entry == nil || claim.request != run.submitRequest {
			continue
		}
		claim.done = true
		if claim.run != run.id || run.session != claim.session {

			s.judgeAdmissionClaim(claim)
			continue
		}
		s.judgeLedEntry(claim, run)
	}
}

func (s *state) judgeLedEntry(claim *deferredStateClaim, r *runState) {
	c := claim.entry
	i, line, e := claim.index, claim.line, claim.envelope

	s.checkEntryPending(i, line, e, c.pointer, c.entry, r)
	c.run, c.order = r.id, r.order
	if !c.classify {

		if r.admittedQueued && !r.started {
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimReservation, session: r.session, run: r.id, index: i, line: line, envelope: e})
		}
		return
	}
	reservation := false
	switch {
	case !r.admittedQueued:

		if c.entry.Status == protocol.RunQueued {
			s.addExpected(CodeSessionStateMismatch, i, line, e, c.pointer+"/status", "active_runs reports a run as queued that its admission started", "a started status", string(c.entry.Status), string(r.id))
		}
	case c.entry.Status == protocol.RunQueued:
		reservation = true
	default:

		var pending bool
		reservation, pending = cancellingReservation(c.entry, r)
		if pending {
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimReservation, session: r.session, run: r.id, sequence: *c.entry.AsOfSequence, stated: true, held: reservation, index: i, line: line, envelope: e})
		}
	}
	if !reservation && c.entry.QueuePosition != nil {
		s.addExpected(CodeSessionStateMismatch, i, line, e, c.pointer+"/queue_position", "a started run holds no queue position", "absent", fmt.Sprintf("%d", *c.entry.QueuePosition), string(r.id))
	}
	c.resolved, c.reservation = true, reservation
	s.settleLedGroup(c.group, false)
}

func (s *state) settleLedGroup(group *ledGroup, final bool) {
	if group == nil {
		return
	}
	reservations := 0
	for index, c := range group.claims {
		if !c.resolved || c.judged {
			if c.resolved && c.reservation {
				reservations++
			}
			continue
		}
		unknown := 0
		for _, ahead := range group.claims[:index] {
			if !ahead.resolved {
				unknown++
			}
		}
		if unknown > 0 && !final {
			continue
		}
		c.judged = true
		if c.reservation {
			low := c.queueAhead + reservations + 1
			if position := c.entry.QueuePosition; position == nil || *position < low || *position > low+unknown {
				s.addExpected(CodeSessionStateMismatch, c.index, c.line, c.envelope, c.pointer+"/queue_position", "a reservation's queue position must be its 1-based place in the queue", describeQueueRange(low, low+unknown), describeQueuePosition(c.entry.QueuePosition), string(c.run))
			}
		}
		if c.reservation {
			reservations++
		}
	}
	if group.settled {
		return
	}
	for _, c := range group.claims {
		if !c.resolved && !final {
			return
		}
	}
	group.settled = true
	s.judgeLedListing(group)
}

func (s *state) judgeLedListing(group *ledGroup) {

	previous := -1
	for _, c := range group.claims {
		if !c.resolved || c.run == "" {
			continue
		}
		if c.order <= previous {
			s.addExpected(CodeSessionStateMismatch, c.index, c.line, c.envelope, c.pointer+"/run_id", "active_runs is not in admission order", "admission order", string(c.run))
			continue
		}
		previous = c.order
	}
	executing := group.claims[0].executing
	state := group.claims[0].state
	var started *entryClaim
	for _, c := range group.claims {
		if !c.classify || !c.resolved || c.reservation {

			continue
		}
		if executing != "" {
			s.addExpected(CodeSessionStateMismatch, c.index, c.line, c.envelope, c.pointer+"/status", "active_runs claims a second started run, and a session has one", "a queued status behind "+string(executing), string(c.entry.Status), string(c.run))
			continue
		}
		executing, started = c.run, c
	}
	if executing != group.claims[0].executing && started != nil {

		if state.ActiveRunID != started.run {
			s.addExpected(CodeSessionStateMismatch, started.index, started.line, started.envelope, "/payload/active_run_id", "active_run_id must name the started run of the session", string(started.run), string(state.ActiveRunID), string(started.run))
		}
		s.checkListedStatus(started.index, started.line, started.envelope, state, started.run, started.entry, false)
		return
	}
	if group.claims[0].executing != "" {

		return
	}

	first := group.claims[0]
	if state.ActiveRunID != "" {
		s.addExpected(CodeSessionStateMismatch, first.index, first.line, first.envelope, "/payload/active_run_id", "active_run_id must be absent where the session holds only reservations", "absent", string(state.ActiveRunID))
	}
	s.checkListedStatus(first.index, first.line, first.envelope, state, "", first.entry, true)
}

func (s *state) closeLedGroups() {
	for _, group := range s.ledGroups {
		s.settleLedGroup(group, true)
	}
}

func describeQueueRange(low, high int) string {
	if low == high {
		return fmt.Sprintf("%d", low)
	}
	return fmt.Sprintf("%d to %d", low, high)
}

func (s *state) judgeAdmissionClaim(claim *deferredStateClaim) {
	want := "an admission on " + string(claim.session)
	if claim.run != "" {
		want = "an admission on " + string(claim.run)
	}
	var admitted *runState
	for _, candidate := range s.runs {
		if candidate.submitRequest == claim.request {
			admitted = candidate
			break
		}
	}
	switch {
	case admitted != nil && admitted.session != claim.session:
		s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, claim.pointer, "snapshot claims a submit request its response admitted on another session", want, "admitted on "+string(admitted.session), string(claim.request))
	case admitted != nil && claim.run == "" && !claim.accounted[admitted.id]:
		s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, claim.pointer, "snapshot claims a submit request it already reflected as admitted, and accounts for its run nowhere", "the run it was admitted to, listed or settled", string(admitted.id), string(claim.request))
	case admitted != nil && (claim.run == "" || admitted.id == claim.run):
		return
	case admitted != nil:
		s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, claim.pointer, "snapshot claims a submit request was admitted to a run its response admitted elsewhere", want, "admitted to "+string(admitted.id), string(claim.request))
	case s.requests[claim.request] != nil && s.requests[claim.request].responded:
		s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, claim.pointer, "snapshot claims a submit request was admitted that its response refused", want, "the request was refused", string(claim.request))
	default:
		s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, claim.pointer, "snapshot claims a submit request was admitted whose response never arrived", want, "no response", string(claim.request))
	}
}

func (s *state) closeQueue() {
	for _, claim := range s.deferred {
		if claim.done {
			continue
		}
		claim.done = true
		switch claim.kind {
		case claimSettled:
			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/as_of/settled", "snapshot dropped a run whose claimed terminal never arrived", fmt.Sprintf("a terminal at sequence %d", claim.sequence), "no terminal", string(claim.run))
		case claimCapture:
			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs entry states a capture position its run never reaches", fmt.Sprintf("sequence %d", claim.sequence), "the run never reached it", string(claim.run))
		case claimModel:

			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/as_of/model_run_sequence", "capture position names a promotion that never arrived", fmt.Sprintf("run.started at sequence %d", claim.sequence), "the run never started", string(claim.run))
		case claimQueued:

		case claimReservation:

			if !claim.held {
				s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs reports a run as executing that never started", "a run the trace saw start", "no run.started", string(claim.run))
			}
		case claimAdmitted:
			s.judgeAdmissionClaim(claim)
		}
	}
	s.closeLedGroups()
}
