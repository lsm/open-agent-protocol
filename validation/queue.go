package validation

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The queue-delivery unit (unit `queue`). Decision 0002 fixed the queued
// admission shape and pre-start settlement for a single reservation; this unit
// adds the overlap, the ordering, and the state that come with admitting a
// second nonterminal run per session — one started run beside an advertised
// number of queued reservations.
//
// Every gate here is judged on the correlated response rather than on the
// request, for the reason the run controls are: the wire makes refusal the
// required behaviour, so diagnosing the request would fail the conduct the
// protocol mandates.

// The state rung of the refusal ladder is the transient failures, which a
// caller may simply retry. It sits below capability, degradation, and
// unsatisfiability, so a busy session never hides a control the caller must
// stop sending — which is why it is not a retained expectation like the other
// three but a judgement made at the response, after the ranked expectations
// have had their say. It has to be: which state condition applies is a fact
// about the whole request/response window, and the window is still moving when
// the request arrives. An auto submission made on an idle session whose queue
// fills before its response is owed exactly the answer an identical submission
// made a moment later is owed.
//
// The two conditions are mutually exclusive by construction: the busy
// condition applies where the endpoint advertises no queue, the limit
// condition where it does.
const (
	stateNone = iota
	stateBusySession
	stateQueueLimit
)

// errorRunActive is the wire code for a submission that cannot be admitted
// because the session is busy and no advertised busy outcome applies, or
// because the queue bound is reached. It is the daemon's existing code,
// adopted as the protocol's.
const errorRunActive = "run_active"

// resolutionSessionBusy is what an auto submission resolved to a queued
// reservation reports in delivery_resolution.
const resolutionSessionBusy = "session_busy"

// queueWindow is what one submit request retained for the queue rules: the
// bounds that applied to it, what the session held when it was made, and what
// happened to both while it was in flight.
//
// Submit decides atomically at an instant the trace cannot name, and both
// edges of that ignorance produce false verdicts — a queue full at the
// decision whose reservation terminates before the response makes a correct
// run_active look stale, and a queue with room at the request that fills
// before the response makes a correct refusal look unfounded. So the window is
// bracketed by the two envelopes the trace already has, and a bound counts as
// reached if it was reached anywhere inside it.
type queueWindow struct {
	request  protocol.EnvelopeID
	session  protocol.SessionID
	delivery protocol.RequestedDeliveryMode
	// maxActive and maxQueued are the bounds the descriptor disclosed when
	// the request was made.
	maxActive, maxQueued *int
	// reachedStrict counts admitted runs alone; reachedLoose also counts
	// every other unanswered submit request on the session, since a
	// concurrent admission may have taken the last slot without its response
	// having reached the trace yet. The loose reckoning is deliberately
	// pessimistic: a missed diagnosis leaves one stale refusal unflagged,
	// while a false one convicts an endpoint that did exactly the right thing
	// under contention it could see and the validator could not.
	reachedStrict, reachedLoose bool
	// busyAtRequest records whether the session held a nonterminal run when
	// the request was made, which is what the resolution a response reports
	// is judged against. busyEver and startedEver widen that to any point
	// inside the window, which is what a refusal is judged against.
	busyAtRequest         bool
	busyEver, startedEver bool
	// mutation marks a submission carrying a model selection under the
	// session_mutation mode: a busy endpoint that cannot defer one may refuse
	// it with run_active even after the bound has cleared, so the stale-limit
	// check stands down for it.
	mutation bool
	// offered records whether the descriptor the submission was made under
	// advertised the queue. It belongs to that descriptor, like the bounds
	// beside it, rather than to whatever a refresh installed in the meantime.
	offered bool
}

// stateCondition is the state-rung condition this window carries, as of now.
// It is read at the response rather than fixed at the request, because
// busyEver and the bound counters keep moving while the submit is in flight.
func (w *queueWindow) stateCondition() int {
	if w.delivery != "" && w.delivery != protocol.DeliveryAuto && w.delivery != protocol.DeliveryQueue {
		return stateNone
	}
	switch {
	case w.offered && (w.delivery == protocol.DeliveryQueue || w.busyEver):
		// A bound belongs to a queue the endpoint offers, so the limit
		// condition covers an explicit queue on an advertising endpoint and an
		// auto on a session that was busy at any point in the window.
		return stateQueueLimit
	case !w.offered && w.delivery != protocol.DeliveryQueue && w.busyEver:
		// The endpoint advertises no busy outcome, so a busy session owes the
		// wire's run_active and nothing else.
		return stateBusySession
	}
	return stateNone
}

// advertisedLevel reports a capability key's level without diagnosing
// anything. The gate itself is settled on the response, so reading the
// descriptor here must not put a diagnostic on a request the protocol requires
// to be answered. The second result is false when no descriptor is current at
// all, which is a different answer from unavailable.
func (s *state) advertisedLevel(key string) (protocol.SupportLevel, bool) {
	if s.currentCapability == "" || s.capabilitiesStale {
		return "", false
	}
	if level, ok := s.features[key]; ok {
		return level, true
	}
	return protocol.SupportUnavailable, true
}

// queueOffered reports whether the active descriptor advertises the queue
// above unavailable.
func (s *state) queueOffered() bool {
	level, known := s.advertisedLevel(protocol.FeatureDeliveryQueue)
	return known && affirmative(level)
}

// queueCounts is the session's nonterminal set and its queued subset. A
// reservation leaves the subset when it promotes, which is why the subset is
// read from the admission shape rather than from whether a start has been
// seen: a run admitted started is never a reservation, even in the envelope or
// two before its run.started reaches the trace.
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

// exceeds reports whether admitting one more run, beside the counts given and
// any number of in-flight reservations, would put the session over a disclosed
// bound. Absent bounds enforce nothing, since absence advertises none.
func (w *queueWindow) exceeds(active, queued, outstanding int) bool {
	if w.maxActive != nil && active+outstanding+1 > *w.maxActive {
		return true
	}
	if w.maxQueued != nil && queued+outstanding+1 > *w.maxQueued {
		return true
	}
	return false
}

// refreshQueueWindows re-reckons every open submit window on a session after
// anything that moves its counts: a new request, an admission, a terminal.
func (s *state) refreshQueueWindows(session protocol.SessionID) {
	open := s.openSubmits[session]
	if len(open) == 0 {
		return
	}
	active, queued, started := s.queueCounts(session)
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
		if w.exceeds(active, queued, len(open)-1) {
			w.reachedLoose = true
		}
	}
}

// closeSubmitWindow drops one request's window once its correlated response
// has been judged.
func (s *state) closeSubmitWindow(request protocol.EnvelopeID) {
	pending := s.pendingControls[request]
	if pending == nil || pending.queue == nil {
		return
	}
	session := pending.queue.session
	open := s.openSubmits[session]
	for i, candidate := range open {
		if candidate == pending {
			s.openSubmits[session] = append(open[:i:i], open[i+1:]...)
			break
		}
	}
	s.refreshQueueWindows(session)
}

// deliveryExpectations collects the refusals a submission's delivery owes,
// whether or not it carries a control: the capability gate on an explicit
// queue, the degraded opt-in on any explicit delivery, and the state rung the
// queue unit adds beneath both.
func (s *state) deliveryExpectations(i, line int, e protocol.Envelope, p protocol.MessageSubmitRequest, pending *pendingSubmit) []*controlExpectation {
	active, queued, started := s.queueCounts(p.SessionID)
	window := &queueWindow{
		request: e.ID, session: p.SessionID, delivery: p.Delivery,
		busyAtRequest: active > 0, busyEver: active > 0, startedEver: started > 0,
		mutation: p.ModelID != nil && s.featureDetail(protocol.FeatureModelSelection).Mode == protocol.ModeSessionMutation,
	}
	if s.limits != nil {
		window.maxActive, window.maxQueued = s.limits.MaxActiveRunsPerSession, s.limits.MaxQueuedRunsPerSession
	}
	window.offered = s.queueOffered()
	window.reachedStrict = window.exceeds(active, queued, 0)
	window.reachedLoose = window.exceeds(active, queued, len(s.openSubmits[p.SessionID]))
	pending.queue = window

	var expectations []*controlExpectation
	// The delivery an explicit non-auto request elects is gated like a
	// control. The mandatory auto delivery is exempt: its degraded level is
	// disclosure a caller reads from the descriptor, not a consent gate, and
	// a caller refused auto could not submit at all.
	if p.Delivery != "" && p.Delivery != protocol.DeliveryAuto && !(s.tolerant && foreignRequestedDelivery(p.Delivery)) {
		// A requested delivery outside this revision's vocabulary is opaque
		// in tolerant mode: which capability key it needs is a later
		// revision's rule, not one this validator can apply.
		key := protocol.DeliveryKey(p.Delivery)
		level, judged := s.controlDescriptor(i, line, e, key)
		switch {
		case !judged:
		case p.Delivery == protocol.DeliveryQueue && !affirmative(level):
			// An explicit queue to an endpoint that does not offer one is
			// refused before admission, never admitted or silently started.
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

// settleQueueRefusal judges an error.response against the state rung, across
// the window rather than at either edge of it. It runs only where no ranked
// expectation owned the response: the state rung is the lowest, so whatever a
// capability, a degradation, or an unsatisfiability owed the caller is the
// answer, and this one is discharged.
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
			// The bound was unreached throughout the window, on the
			// pessimistic reckoning that counts in-flight reservations, and
			// no other condition that independently owes run_active
			// survives. The caller is being told to wait for capacity it
			// never lacked.
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

// queueOverlap judges a second admission on a session that already has a
// nonterminal run. It reports whether it diagnosed, so the admission's own
// queue rules are not piled onto a transition the session could not make.
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

// queueAdmission judges an accepted admission against the queue unit: the
// resolutions the combination table now states, the capability gate on a
// reservation, and the disclosed bounds.
func (s *state) queueAdmission(i, line int, e protocol.Envelope, p protocol.MessageSubmitResponse, st *sessionTrack) {
	pending := s.pendingControls[e.InReplyTo]
	requested := p.RequestedDelivery
	if pending != nil && pending.queue != nil {
		requested = pending.queue.delivery
	}
	queued := p.Admission == protocol.AdmissionQueued
	switch {
	case requested == protocol.DeliveryQueue && !queued:
		// An explicit queue never resolves to start: the queued run promotes
		// immediately on an idle session instead, so "run after current work
		// reaches a safe boundary" is satisfied without a second shape.
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/admission", "an explicit queue request may only be admitted as a reservation", string(protocol.AdmissionQueued), string(p.Admission), string(e.InReplyTo))
	case requested == protocol.DeliveryAuto && queued && busyAtRequest(pending) && p.DeliveryResolution != resolutionSessionBusy:
		// auto resolves to queue on a busy session, and reports why, or the
		// resolution is described rather than enforced. The rule is scoped to
		// the busy session because that is the resolution it names: decision
		// 0002's lone reservation on an idle session is an endpoint reporting
		// what it observed at admission, not a resolution of contention, and
		// it predates this unit.
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/delivery_resolution", "an auto submission a busy session turned into a reservation must report the resolution that produced it", resolutionSessionBusy, p.DeliveryResolution, string(e.InReplyTo))
	}
	if !queued {
		return
	}
	// The capability gate on a reservation, whatever the session held. An
	// explicit queue retained the same judgement at the request and is
	// settled through the ladder, so it is not diagnosed twice here.
	gated := pending != nil && pending.expectation != nil && pending.expectation.key == protocol.FeatureDeliveryQueue
	if !gated {
		level, known := s.advertisedLevel(protocol.FeatureDeliveryQueue)
		switch {
		case !known:
			// No descriptor is current, so there is none that omits the key.
			// Decision 0002 made the lone reservation canonical without any
			// capability disclosure, and this unit judges what a descriptor
			// says rather than demanding one exist.
		case !affirmative(level):
			s.addExpected(CodeUnavailableCapability, i, line, e, "/payload/admission", "a queued reservation requires the endpoint to advertise "+protocol.FeatureDeliveryQueue, "native, emulated, or degraded", string(level), string(e.InReplyTo))
		case level == protocol.SupportDegraded && !s.optedIntoQueue(e.InReplyTo):
			s.addExpected(CodeDegradedWithoutOptin, i, line, e, "/payload/admission", "a degraded queue was used without the caller's opt-in", protocol.FeatureDeliveryQueue+" in allow_degraded_features", "absent", string(e.InReplyTo))
		}
	}
	// The bounds are checked at the reservation, since promotion adds no run.
	// The set the admission is judged against is the one the trace shows
	// outstanding, which is why a reservation's pre-start terminal is
	// published when it happens: a slot the trace has seen freed is free.
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

// busyAtRequest reports whether the session held a nonterminal run when the
// submission was made.
func busyAtRequest(pending *pendingSubmit) bool {
	return pending != nil && pending.queue != nil && pending.queue.busyAtRequest
}

// optedIntoQueue reports whether the request behind an admission named the
// queue key in allow_degraded_features.
func (s *state) optedIntoQueue(request protocol.EnvelopeID) bool {
	req := s.requests[request]
	if req == nil {
		return false
	}
	var p protocol.MessageSubmitRequest
	_ = req.envelope.DecodePayload(&p)
	return p.AllowsDegraded(protocol.FeatureDeliveryQueue)
}

// checkQueueOrder enforces one run domain at a time, in admission order: a
// later-admitted run's sequenced events may not appear while an
// earlier-admitted run of the session is nonterminal.
//
// Two things are exempt. Requests and responses addressed to a queued run are
// not run events at all — cancelling a reservation before promotion
// necessarily happens while the earlier run is nonterminal, and the rule
// governs the endpoint's timeline, not the control layer's commands. And the
// pre-start terminal of a run that never started is published when it happens:
// that run has no execution to interleave, and its release of a queue slot is
// capacity the trace has to show at the moment it occurs, or a legitimate
// reuse of a freed slot is indistinguishable from an over-admission.
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

// promote records a run's start. A reservation's held model control is applied
// here rather than at admission: a session_mutation must not move the session
// default while an earlier run is still started, and a per_run snapshot of
// that default has to follow any earlier-admitted mutation.
func (s *state) promote(i int, e protocol.Envelope, r *runState) {
	if e.Sequence != nil {
		r.startSequence = *e.Sequence
	}
	if st := s.sessions[r.session]; st != nil {
		// The run has begun, so this is the run a snapshot must name. A
		// reservation was never it, and the run it displaces is terminal by
		// the ordering rule — one run executes at a time.
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

// checkQueueLimits judges a descriptor's disclosure. Advertising a queue is a
// claim that some submission will be queued, and the bound is what makes the
// claim checkable: with none present the limit validation never engages, and
// an endpoint could advertise queueing and refuse every queued submission with
// run_active while remaining conforming.
func (s *state) checkQueueLimits(i, line int, e protocol.Envelope, p protocol.CapabilitiesResponse) {
	support, ok := p.EffectiveSupport(protocol.FeatureDeliveryQueue)
	if !ok || !affirmative(support.Level) {
		// Only an unavailable or absent capability is exempt, because
		// neither claims anything. degraded is held to the same disclosure:
		// the caller opts in and is entitled to the same promise.
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
	// The two bounds have to agree, or the disclosure is satisfied by numbers
	// that still forbid what the capability claims: one started run fills an
	// active set of 1, so every reservation beside it exceeds that set.
	// An absent active bound promises nothing and is not held to this.
	if p.Limits.MaxActiveRunsPerSession != nil && *p.Limits.MaxActiveRunsPerSession < queued+1 {
		s.addExpected(CodeUndisclosedQueueLimit, i, line, e, "/payload/limits/max_active_runs_per_session", "the disclosed active bound leaves no room for the disclosed queue beside a started run", fmt.Sprintf("at least %d", queued+1), describeLimit(p.Limits, true))
	}
}

// checkQueueAdvertisement holds a capabilities.response that repeats the
// active revision to repeating what that revision said about the queue.
//
// It is the catalog rule applied to this unit's own descriptor fields, and for
// the same reason: a revision identifies one descriptor, every envelope
// citing it is bound to that snapshot, and a bound that moves underneath it
// leaves admissions judged against a capacity no consumer holding the
// revision was ever told. The bounds are the sharp case — max_active or
// max_queued rising under one revision would excuse an overflow that the
// disclosed numbers forbid, and falling would convict an admission the caller
// was invited to make — but the advertised level belongs to the descriptor
// just as much: a queue that goes unavailable under one revision turns off
// every rule in this unit with nothing announcing it.
//
// The diagnosis is that the revision did not move where the descriptor did,
// which is what stale_capability_revision already says of a capabilities
// update that reuses its predecessor's revision. It is raised on the field
// that changed, because the fix is to introduce a new revision for it.
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

// deferredStateClaim is one thing a state snapshot asserted that the trace had
// not reached. A snapshot may describe a position the trace has not yet seen —
// state is captured inside the endpoint, and the event carrying that position
// may drain afterwards — but not one that never exists.
type deferredStateClaim struct {
	kind    int
	session protocol.SessionID
	run     protocol.RunID
	request protocol.EnvelopeID
	pointer string
	// accounted is the set of runs a session-level admission claim's snapshot
	// listed or said it had settled. The admission the claim is waiting on has
	// to land in it.
	accounted map[protocol.RunID]bool
	sequence  uint64
	// held is what an entry said about its own queue place at the position it
	// states: kept, so its run had not begun there, or given up, so it had.
	held bool
	// stated records that the entry named a position at all. One that did not
	// still claims its run had begun, and the start is the whole of what that
	// claim waits for.
	stated      bool
	listed      map[protocol.InteractionID]bool
	model       string
	index, line int
	envelope    protocol.Envelope
	// entry is the active_runs entry this admission claim was made for, where
	// the snapshot led the admission. The identity the claim settles is what
	// unlocks the entry's own rules, so they are one claim answered by one
	// event rather than two deferrals of the same fact.
	entry *entryClaim
	done  bool
}

// entryClaim is one active_runs entry that named a run before the trace
// carried it, kept with the little of its listing the deferred rules need: the
// run the listing already described as executing, the queue places established
// ahead of it, and how many other entries were leading beside it.
type entryClaim struct {
	entry      protocol.ActiveRun
	state      protocol.SessionState
	pointer    string
	executing  protocol.RunID
	queueAhead int
	leadsAhead int
	// group is every entry that led in the same listing, in listing order.
	// What one of them turns out to be narrows what the others can be, so
	// they are settled as a set rather than one at a time.
	group *ledGroup
	// classify marks an entry whose status could not say what it was without
	// the admission. The rest were classified where they were listed.
	classify bool
	// resolved and reservation are this entry's outcome, once something has
	// said what it is; judged records that its queue place has been reported
	// on, so tightening the range for its siblings cannot report it twice.
	resolved    bool
	reservation bool
	judged      bool
	run         protocol.RunID
	// order is the place in admission order the run turned out to hold, kept
	// so the set can be checked against the sequence it was listed in.
	order int
	// index, line and envelope are the snapshot this entry came from, because
	// every verdict about it points at that snapshot however late it lands.
	index, line int
	envelope    protocol.Envelope
}

// ledGroup is one listing's leading entries. Their queue places are a single
// arrangement: each entry that turns out to be a reservation takes a place the
// others cannot have, so an admission that lands narrows what is left for the
// ones still outstanding, and the last one to land determines them all.
type ledGroup struct {
	claims []*entryClaim
	// settled records that the listing's own fields have been judged, which
	// happens once — when the last of the group resolves, or when the trace
	// ends without it.
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

// reconcileDeferred settles every claim this run envelope answers.
func (s *state) reconcileDeferred(i, line int, e protocol.Envelope, r *runState) {
	for _, claim := range s.deferred {
		if claim.done || claim.run != r.id {
			continue
		}
		switch claim.kind {
		case claimSettled:
			// The claimed terminal must be the next envelope published for
			// that run: a snapshot could otherwise drop a live run, name the
			// sequence its terminal will eventually carry, and be vindicated
			// whenever the run happens to end there.
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
			// The run's start is what decides this, whatever sequence it
			// lands on: from there onwards the run is not queued at any
			// position, and before it the entry was accurate.
			if !r.started {
				continue
			}
			claim.done = true
			if claim.sequence < r.startSequence {
				continue
			}
			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs reports a run as queued at a position it had already started at", "a started status", string(protocol.RunQueued), string(claim.run))
		case claimReservation:
			// The same start decides this one, from the other side: an entry
			// that kept its queue place claims a position before the start,
			// and one that gave the place up — by dropping the position, or
			// by reporting the run as executing — claims a position from the
			// start onwards. Either is a claim, and the start is the only
			// thing that answers it.
			if !r.started {
				continue
			}
			claim.done = true
			if !claim.stated {
				// The entry named no position, so it claimed only that the
				// run had begun, and it has.
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

// judgePendingInteractions compares an entry's pending set with the run's own
// at the position the entry states it was captured at.
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
		// The promotion arrived and applied no session_mutation, so the
		// position the snapshot anchored on was never a model-affecting event.
		s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/as_of/model_run_sequence", "capture position names a run that applied no session_mutation, so it is not a model-affecting event", "a run admitted with a session_mutation model selection", string(claim.run), string(claim.run))
		return
	}
	if model == claim.model {
		return
	}
	s.addExpected(CodePrematureSessionMutation, claim.index, claim.line, claim.envelope, "/payload/current_model_id", "snapshot reports a model other than the one in force at the position it states", model, claim.model, string(claim.run))
}

// mutationModel is the session default a run's promotion installs, for a run
// admitted under the session_mutation mode with a model selection.
func mutationModel(r *runState) (string, bool) {
	if r == nil || !r.controls.present || !r.controls.modelPresent || r.controls.mode != protocol.ModeSessionMutation {
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

// admitLedEntries settles every entry that led this admission. The identity
// the claim rests on and the entry's own rules are answered by the same event:
// the response says which run the submission became, and with the run in hand
// the questions the listing stood down on have something to be judged against.
// Leaving them for the trace's end would make standing down mean forgiving,
// which is not what the lead was allowed for.
func (s *state) admitLedEntries(run *runState) {
	for _, claim := range s.deferred {
		if claim.done || claim.kind != claimAdmitted || claim.entry == nil || claim.request != run.submitRequest {
			continue
		}
		claim.done = true
		if claim.run != run.id || run.session != claim.session {
			// The anchor resolved to another run, or to another session.
			// That is the identity claim's own verdict, and an entry that was
			// not for this run describes nothing more.
			s.judgeAdmissionClaim(claim)
			continue
		}
		s.judgeLedEntry(claim, run)
	}
}

// judgeLedEntry applies to a led entry the rules its listing could not.
func (s *state) judgeLedEntry(claim *deferredStateClaim, r *runState) {
	c := claim.entry
	i, line, e := claim.index, claim.line, claim.envelope
	// The capture position and pending set wait on the run either way: the
	// entry states a position, and only the run says whether it reaches it.
	s.checkEntryPending(i, line, e, c.pointer, c.entry, r)
	c.run, c.order = r.id, r.order
	if !c.classify {
		// Already classified where it was listed, and reported on there.
		return
	}
	reservation := false
	switch {
	case !r.admittedQueued:
		// The response started the run, so it was never in a queue and no
		// capture position puts it in one: a snapshot may lead an admission
		// it made, but not invent the queue that admission did not use.
		if c.entry.Status == protocol.RunQueued {
			s.addExpected(CodeSessionStateMismatch, i, line, e, c.pointer+"/status", "active_runs reports a run as queued that its admission started", "a started status", string(c.entry.Status), string(r.id))
		}
	case c.entry.Status == protocol.RunQueued:
		reservation = true
	default:
		// Cancelling, asked now that there is a run to ask it of. A run
		// cannot have started before its own admission, so this reduces to
		// what the entry says about its own queue place, and the start it
		// waits for settles that exactly as it would for a run the trace
		// already carried.
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

// settleLedGroup reports everything the group now determines. An entry that
// has turned out to be a reservation takes a place its siblings cannot have,
// so each admission that lands tightens the arrangement left for the ones
// still outstanding: a place is judged as soon as every entry ahead of it is
// known, which for the last of them is a single number. The listing's own two
// fields wait for the whole group, because which run active_run_id owed is a
// question about all of them at once.
//
// final is the trace's end, where nothing more will resolve: the places still
// outstanding are judged against what their unresolved siblings leave them,
// which is the widest reading, and the listing's fields are judged if the
// group resolved without the last event that would have settled them.
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

// judgeLedListing holds the listing's own two fields to what its leading
// entries turned out to be. Every entry that is executing is one the session
// may not have twice, the one that is executing is the one active_run_id owed,
// and a listing whose leads are all reservations holds nothing else.
func (s *state) judgeLedListing(group *ledGroup) {
	// A listing is in admission order, and for entries that led their own
	// admissions that is a fact only their responses supply. Consistent queue
	// places in the wrong sequence is the shape this catches: two leads listed
	// one way and admitted the other take each other's numbers, so every place
	// checks out and the listing was still never in admission order.
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
			// An entry whose status classified it where it was listed was
			// judged against the listing there, by the same two rules. Only
			// what the admission decided is decided here.
			continue
		}
		if executing != "" {
			s.addExpected(CodeSessionStateMismatch, c.index, c.line, c.envelope, c.pointer+"/status", "active_runs claims a second started run, and a session has one", "a queued status behind "+string(executing), string(c.entry.Status), string(c.run))
			continue
		}
		executing, started = c.run, c
	}
	if executing != group.claims[0].executing && started != nil {
		// A lead turned out to be the listing's executing run, so it is the
		// one active_run_id owed and the one the session status describes.
		if state.ActiveRunID != started.run {
			s.addExpected(CodeSessionStateMismatch, started.index, started.line, started.envelope, "/payload/active_run_id", "active_run_id must name the started run of the session", string(started.run), string(state.ActiveRunID), string(started.run))
		}
		s.checkListedStatus(started.index, started.line, started.envelope, state, started.run, started.entry, false)
		return
	}
	if group.claims[0].executing != "" {
		// The listing already named an executing run where it was read, so
		// active_run_id and the status were judged against it there.
		return
	}
	// Nothing executes, so whatever the leads are, they are reservations or
	// runs that never resolved. A listing holding only those names no run.
	first := group.claims[0]
	if state.ActiveRunID != "" {
		s.addExpected(CodeSessionStateMismatch, first.index, first.line, first.envelope, "/payload/active_run_id", "active_run_id must be absent where the session holds only reservations", "absent", string(state.ActiveRunID))
	}
	s.checkListedStatus(first.index, first.line, first.envelope, state, "", first.entry, true)
}

// closeLedGroups settles what the trace ended without settling. A lead whose
// admission never arrived is diagnosed as that claim; what its siblings were
// left with is still theirs to answer for.
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

// judgeAdmissionClaim settles one claim that a submit request had already been
// admitted. The trace has ended, so its response either arrived and said which
// run it created, arrived and refused, or never arrived at all — and a claim
// resting on the last of those rests on nothing, the same evasion as anchoring
// on a promotion that never comes. A claim naming a run is answered by that
// run's admission; a session-level one by any admission, because that is all
// it asserted.
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

// closeQueue settles every claim the trace ended without answering. A position
// a run never reaches, and a terminal that never arrives, are both the case
// the deferral exists to allow being abused.
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
			// A model anchor names a promotion. One that never happens leaves
			// the model the snapshot reported judged against nothing, which
			// is how a snapshot would evade the authority check by pointing
			// at a start that never comes.
			s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/as_of/model_run_sequence", "capture position names a promotion that never arrived", fmt.Sprintf("run.started at sequence %d", claim.sequence), "the run never started", string(claim.run))
		case claimQueued:
			// The run never started, so queued is what it stayed. A stated
			// position the run never reaches is the capture claim's business,
			// and it is raised there rather than twice.
		case claimReservation:
			// The run never started, so it never left the queue. An entry
			// that kept its place was right all along; one that gave the
			// place up — or said outright that the run was executing — rests
			// on a promotion that never came, which is the evasion every
			// deferral here is closed against.
			if !claim.held {
				s.addExpected(CodeSessionStateMismatch, claim.index, claim.line, claim.envelope, "/payload/active_runs", "active_runs reports a run as executing that never started", "a run the trace saw start", "no run.started", string(claim.run))
			}
		case claimAdmitted:
			s.judgeAdmissionClaim(claim)
		}
	}
	s.closeLedGroups()
}
