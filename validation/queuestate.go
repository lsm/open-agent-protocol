package validation

import (
	"fmt"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The queue unit's session-state rules. A session with more than one
// nonterminal run cannot be described by active_run_id alone, so the snapshot
// grows active_runs, and with it the question of what an accurate snapshot is
// allowed to disagree with the trace about.
//
// The answer throughout is that the snapshot states the position it was taken
// at, rather than the validator guessing whether a disagreement is a stale
// read or a false report. State reads are not serialized with lifecycle
// publication and should not be: an interaction can resolve inside the
// endpoint before capture while the event carrying it drains after the state
// response, and a run can settle before capture while its terminal drains
// afterwards. Both are ordinary scheduling, and both would otherwise be
// diagnosed against an endpoint that answered correctly.

// checkSessionCapture judges one session.state payload: which runs it had to
// list, whether it listed them consistently, what it claimed to know, and
// which model it reports.
func (s *state) checkSessionCapture(i, line int, e protocol.Envelope, p protocol.SessionState, st *sessionTrack) {
	s.checkCaptureModel(i, line, e, p, st)
	anchored := map[protocol.EnvelopeID]bool{}
	settled := map[protocol.RunID]uint64{}
	if p.AsOf != nil {
		for _, id := range p.AsOf.AdmittedSubmitRequests {
			if !s.namesSubmitRequest(id, p.SessionID, "") {
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/admitted_submit_requests", "capture anchor names an envelope that is not a submit request the trace carries for this session", "a submit request on "+string(p.SessionID), string(id))
				continue
			}
			anchored[id] = true
		}
		for _, entry := range p.AsOf.Settled {
			settled[entry.RunID] = entry.Sequence
		}
	}
	required := s.requiredActiveRuns(i, e, p, st, anchored, settled)
	s.recordSettledClaims(i, line, e, p, settled)
	if p.ActiveRuns == nil {
		s.checkActiveRunsOmitted(i, line, e, required)
		return
	}
	s.checkActiveRunsListing(i, line, e, p, required)
}

// requiredActiveRuns is the set of nonterminal runs a snapshot had to list.
//
// Membership is judged against what the snapshot says it knew rather than
// against the trace's current state, because a snapshot can legitimately drop
// a queued run it settled before capture and legitimately omit one admitted
// after capture whose admission reached the trace first. The anchor makes the
// difference stateable: an admission whose response precedes the state request
// is settled fact and must be accounted for, and only admissions whose
// responses fall inside the request/response window may be left out on the
// strength of being absent from the set. Where as_of is absent the snapshot
// claims no knowledge the trace lacks and is judged as it stands.
func (s *state) requiredActiveRuns(i int, e protocol.Envelope, p protocol.SessionState, st *sessionTrack, anchored map[protocol.EnvelopeID]bool, settled map[protocol.RunID]uint64) []*runState {
	window := s.captureWindowStart(i, e)
	var required []*runState
	if st == nil {
		return nil
	}
	for _, id := range st.order {
		r := s.runs[id]
		if r == nil || r.terminal {
			continue
		}
		if _, claimed := settled[id]; claimed {
			continue
		}
		if p.AsOf != nil && r.admittedAt > window && !anchored[r.submitRequest] {
			continue
		}
		required = append(required, r)
	}
	return required
}

// captureWindowStart is the index the snapshot's window opens at: its own
// request, or the snapshot itself when nothing solicited it.
func (s *state) captureWindowStart(i int, e protocol.Envelope) int {
	if req := s.requests[e.InReplyTo]; req != nil {
		return req.index
	}
	return i
}

// checkActiveRunsOmitted judges a snapshot that carries no active_runs at all.
// The field is required exactly where active_run_id cannot carry the answer: a
// reservation, an overlap, or — on an endpoint advertising a unit that puts
// entries there — a run whose recovery path needs an id the entry holds. An
// absent field would read as an empty queue to a reconnecting client.
func (s *state) checkActiveRunsOmitted(i, line int, e protocol.Envelope, required []*runState) {
	reason := ""
	switch {
	case len(required) > 1:
		reason = "the session has more than one nonterminal run"
	default:
		for _, r := range required {
			if r.admittedQueued && !r.started {
				reason = "the session has a queued reservation"
			}
		}
	}
	if reason == "" && s.queueOffered() {
		for _, r := range required {
			for _, x := range r.interactions {
				if !x.resolved {
					reason = "a nonterminal run has an unresolved interaction whose id only an entry can carry"
				}
			}
		}
	}
	if reason == "" {
		return
	}
	s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs", "snapshot omits active_runs where active_run_id cannot carry the answer: "+reason, "an entry per nonterminal run", "absent")
}

// checkActiveRunsListing judges a present active_runs: that it lists every run
// it owed, in admission order, with consistent queue positions, and that each
// entry's pending set is accurate at the position the entry states.
func (s *state) checkActiveRunsListing(i, line int, e protocol.Envelope, p protocol.SessionState, required []*runState) {
	listed := map[protocol.RunID]bool{}
	lastOrder := -1
	queuePosition := 0
	for index, entry := range p.ActiveRuns {
		pointer := fmt.Sprintf("/payload/active_runs/%d", index)
		if listed[entry.RunID] {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs lists one run twice", "one entry per run", string(entry.RunID))
			continue
		}
		listed[entry.RunID] = true
		r := s.runs[entry.RunID]
		if r == nil || r.session != p.SessionID {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs names a run the trace does not carry for this session", "a run admitted on "+string(p.SessionID), string(entry.RunID))
			continue
		}
		if r.order <= lastOrder {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs is not in admission order", "admission order", string(entry.RunID))
		}
		lastOrder = r.order
		reservation := r.admittedQueued && !r.started
		switch {
		case reservation:
			queuePosition++
			if entry.QueuePosition == nil || *entry.QueuePosition != queuePosition {
				s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/queue_position", "a reservation's queue position must be its 1-based place in the queue", fmt.Sprintf("%d", queuePosition), describeQueuePosition(entry.QueuePosition), string(entry.RunID))
			}
		case entry.QueuePosition != nil:
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/queue_position", "a started run holds no queue position", "absent", fmt.Sprintf("%d", *entry.QueuePosition), string(entry.RunID))
		}
		s.checkEntryAnchor(i, line, e, pointer, p.SessionID, entry, r)
		s.checkEntryPending(i, line, e, pointer, entry, r)
	}
	for _, r := range required {
		if !listed[r.id] {
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs", "snapshot omits a nonterminal run it accounted for", string(r.id), "absent", string(r.id))
		}
	}
	// active_run_id keeps naming the started run, or is absent when only
	// reservations remain.
	started := protocol.RunID("")
	for _, r := range required {
		if r.started || !r.admittedQueued {
			started = r.id
		}
	}
	if started != "" && p.ActiveRunID != started {
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "active_run_id must name the started run of the session", string(started), string(p.ActiveRunID), string(started))
	}
}

func describeQueuePosition(position *int) string {
	if position == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *position)
}

// checkEntryAnchor holds an entry's own capture anchor to naming submit
// requests the trace carries for that run. A marker naming a request the trace
// does not carry is a mismatch at once: unlike a run position, the request the
// endpoint has seen is by construction already in the trace.
func (s *state) checkEntryAnchor(i, line int, e protocol.Envelope, pointer string, session protocol.SessionID, entry protocol.ActiveRun, r *runState) {
	for _, id := range entry.AdmittedSubmitRequests {
		if !s.namesSubmitRequest(id, session, r.id) {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/admitted_submit_requests", "entry anchor names an envelope that is not a submit request the trace carries for this run", "a submit request on "+string(r.id), string(id), string(r.id))
		}
	}
}

// checkEntryPending judges an entry's unresolved-interaction set at the
// position the entry states it was captured at. The position may name one the
// trace has not reached, which is held and reconciled when it arrives; a
// position the run never reaches is a mismatch, because a snapshot may
// describe a position the trace has not yet seen but not one that never
// exists.
func (s *state) checkEntryPending(i, line int, e protocol.Envelope, pointer string, entry protocol.ActiveRun, r *runState) {
	unresolved := map[protocol.InteractionID]bool{}
	for id, x := range r.interactions {
		if !x.resolved {
			unresolved[id] = true
		}
	}
	listed := map[protocol.InteractionID]bool{}
	for _, id := range entry.PendingInteractions {
		listed[id] = true
	}
	// An empty list and an absent one say the same thing on the wire, since
	// the JSON encodings a serializer can produce for "no pending
	// interactions" are not distinguishable in practice. What is
	// distinguishable, and what the rule needs, is whether the entry claims
	// any — those are the entries whose reconciliation depends on a position.
	if len(listed) > 0 && entry.AsOfSequence == nil {
		s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/as_of_sequence", "an entry carrying pending_interactions must state the position it was captured at", "a capture position", "absent", string(r.id))
		return
	}
	if entry.AsOfSequence == nil {
		// Without a stated position the entry claims no knowledge the trace
		// lacks and is judged as it stands. On an endpoint that puts entries
		// here at all, an entry for a run blocked on an interaction must
		// carry the id: the single-run recovery path reads it from exactly
		// this field.
		if s.queueOffered() && !sameIDSet(unresolved, listed) {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/pending_interactions", "entry does not report the unresolved interactions its run is blocked on", describeIDs(unresolved), describeIDs(listed), string(r.id))
		}
		return
	}
	seq := *entry.AsOfSequence
	if seq > r.next-1 {
		s.deferred = append(s.deferred, &deferredStateClaim{kind: claimCapture, session: r.session, run: r.id, sequence: seq, listed: listed, index: i, line: line, envelope: e})
		return
	}
	want := pendingAt(r, seq)
	if !sameIDSet(want, listed) {
		s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/pending_interactions", "active_runs entry does not report the run's unresolved interactions at the position it states", describeIDs(want), describeIDs(listed), string(r.id))
	}
}

// recordSettledClaims registers every run the snapshot says it has already
// removed. The endpoint asserts, the hub corroborates by draining the run to
// the claimed sequence before forwarding, and the validator checks the
// outcome — the same division that keeps the ahead-of-trace reconciliation
// honest without making conformance depend on drainer timing.
func (s *state) recordSettledClaims(i, line int, e protocol.Envelope, p protocol.SessionState, settled map[protocol.RunID]uint64) {
	if p.AsOf == nil {
		return
	}
	for _, entry := range p.AsOf.Settled {
		r := s.runs[entry.RunID]
		if r == nil || r.session != p.SessionID {
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/settled", "snapshot claims a settled run the trace does not carry for this session", "a run admitted on "+string(p.SessionID), string(entry.RunID))
			continue
		}
		if r.terminal {
			if r.next-1 != entry.Sequence {
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/settled", "snapshot claims a terminal sequence the run did not settle at", fmt.Sprintf("%d", r.next-1), fmt.Sprintf("%d", entry.Sequence), string(entry.RunID))
			}
			continue
		}
		s.deferred = append(s.deferred, &deferredStateClaim{kind: claimSettled, session: p.SessionID, run: entry.RunID, sequence: entry.Sequence, index: i, line: line, envelope: e})
	}
}

// namesSubmitRequest reports whether an envelope id names a submit request the
// trace carries for the session, and — when a run is named — for that run.
func (s *state) namesSubmitRequest(id protocol.EnvelopeID, session protocol.SessionID, run protocol.RunID) bool {
	req := s.requests[id]
	if req == nil || req.typ != protocol.TypeSessionMessageSubmitRequest || req.session != session {
		return false
	}
	if run == "" {
		return true
	}
	for _, candidate := range s.runs {
		if candidate.submitRequest == id {
			return candidate.id == run
		}
	}
	// The request is on the session but has not been answered yet, so it
	// cannot contradict the run it is claimed for.
	return true
}

// checkCaptureModel judges the session default a snapshot reports against the
// run in force at the position the snapshot states.
//
// A queued session_mutation promoting concurrently with a state read straddles
// this both ways: the snapshot can capture the old model while run.started
// reaches the trace first, or the new one while that event drains afterwards,
// and either accurate reading would be diagnosed against the trace as it
// stands. So the marker names the last model-affecting event the snapshot
// reflects, and its genesis form names the position before any — the session's
// opening model, judged against no run at all.
func (s *state) checkCaptureModel(i, line int, e protocol.Envelope, p protocol.SessionState, st *sessionTrack) {
	if st == nil {
		return
	}
	if p.AsOf == nil || p.AsOf.ModelRunSequence == nil {
		model, known := s.startedMutationModel(st)
		if known && p.CurrentModelID != model {
			s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot reports a model other than the started run's while that run is started", model, p.CurrentModelID)
		}
		return
	}
	position := *p.AsOf.ModelRunSequence
	if position.Genesis() {
		if st.openingKnown && p.CurrentModelID != st.openingModel {
			s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot marked before any model-affecting event reports a model other than the session's opening one", st.openingModel, p.CurrentModelID)
		}
		return
	}
	r := s.runs[position.RunID]
	if r == nil {
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a run the trace does not carry", "an admitted run", string(position.RunID))
		return
	}
	if !r.started || r.startSequence != position.Sequence {
		// The named promotion has not reached the trace; it is held and
		// reconciled when it arrives.
		s.deferred = append(s.deferred, &deferredStateClaim{kind: claimModel, session: p.SessionID, run: position.RunID, sequence: position.Sequence, model: p.CurrentModelID, index: i, line: line, envelope: e})
		return
	}
	model, known := mutationModel(r)
	if known && p.CurrentModelID != model {
		s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot reports a model other than the one in force at the position it states", model, p.CurrentModelID, string(r.id))
	}
}

// startedMutationModel is the model the session's started run installed, for a
// run admitted under the session_mutation mode with a selection.
func (s *state) startedMutationModel(st *sessionTrack) (string, bool) {
	for _, id := range st.order {
		r := s.runs[id]
		if r == nil || r.terminal || !r.started {
			continue
		}
		if model, ok := mutationModel(r); ok {
			return model, true
		}
	}
	return "", false
}
