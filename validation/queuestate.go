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
	var claimedAdmissions []protocol.EnvelopeID
	if p.AsOf != nil {
		for _, entry := range p.AsOf.Settled {
			settled[entry.RunID] = entry.Sequence
		}
		for _, id := range p.AsOf.AdmittedSubmitRequests {
			switch s.namesSubmitRequest(id, p.SessionID, "") {
			case admissionWrong:
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/admitted_submit_requests", "capture anchor names an envelope that is not a submit request the trace carries for this session", "a submit request on "+string(p.SessionID), string(id))
				continue
			case admissionPending:
				// A claim that the endpoint had already admitted a submission,
				// made before its response exists. A refusal answers it and
				// the claim was false; an admission answers it and the run it
				// created is one this snapshot then had to account for.
				claimedAdmissions = append(claimedAdmissions, id)
			}
			anchored[id] = true
		}
	}
	if len(claimedAdmissions) > 0 {
		// What the snapshot accounted for, kept with the claim: a capture that
		// says it already reflects an admission has to show where. The run the
		// response creates must be one this listing names, or one it says it
		// had already settled — otherwise the anchor buys the snapshot an
		// exemption from listing the very run it claims to know about.
		accounted := map[protocol.RunID]bool{}
		for _, entry := range p.ActiveRuns {
			accounted[entry.RunID] = true
		}
		for id := range settled {
			accounted[id] = true
		}
		if p.ActiveRunID != "" {
			accounted[p.ActiveRunID] = true
		}
		for _, id := range claimedAdmissions {
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimAdmitted, session: p.SessionID, request: id, pointer: "/payload/as_of/admitted_submit_requests", accounted: accounted, index: i, line: line, envelope: e})
		}
	}
	required := s.requiredActiveRuns(i, e, p, st, anchored, settled)
	s.recordSettledClaims(i, line, e, p, settled)
	if p.ActiveRuns == nil {
		s.checkActiveRunsOmitted(i, line, e, required)
		return
	}
	s.checkActiveRunsListing(i, line, e, p, required, s.captureWindowStart(i, e))
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

// claimedSettled reports whether a snapshot says it already removed one run,
// naming the terminal it is about to publish for it.
func claimedSettled(p protocol.SessionState, run protocol.RunID) bool {
	if p.AsOf == nil {
		return false
	}
	for _, entry := range p.AsOf.Settled {
		if entry.RunID == run {
			return true
		}
	}
	return false
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
func (s *state) checkActiveRunsListing(i, line int, e protocol.Envelope, p protocol.SessionState, required []*runState, window int) {
	listed := map[protocol.RunID]bool{}
	// listedStarted is the run the snapshot itself describes as started: the
	// entry that is not a reservation at the position it states. It is what
	// active_run_id has to name, because the alternative — deriving it from
	// the trace's own idea of which run has started — judges one snapshot
	// against two different moments at once, and lets a listing disagree with
	// itself about the same run.
	listedStarted := protocol.RunID("")
	// startedEntry is that run's entry, kept because the session status is
	// judged against what the entry itself claims rather than against what the
	// trace knows now: an interaction can be raised or resolved inside the
	// window, so the two fields are compared to each other.
	var startedEntry protocol.ActiveRun
	// listedReservation records that the snapshot describes at least one run
	// as queued, which is what makes "only reservations remain" a thing the
	// listing says rather than a thing inferred from its emptiness.
	listedReservation := false
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
		if r.terminal && r.terminalAt < window {
			// active_runs is the nonterminal set. A run that settled before
			// the read was even requested cannot be in it under any capture
			// position, so this is a stale listing rather than a race. One
			// that settled inside the window may still be listed: the
			// snapshot may have been captured before that terminal, which is
			// the race the capture positions exist to allow.
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs lists a run that had already settled before the read was requested", "a nonterminal run", string(entry.RunID), string(entry.RunID))
			continue
		}
		if r.order <= lastOrder {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs is not in admission order", "admission order", string(entry.RunID))
		}
		lastOrder = r.order
		// A queue position and a queued status are one description of one
		// moment, so the entry's own status decides which this is — checked
		// against the position it states, a few lines below. Judging the
		// position against r.started instead rejects the position of an entry
		// whose status the same snapshot is allowed to keep, and then demands
		// that run in active_run_id as well; and the reverse, where a
		// promotion inside the endpoint lets an accurate entry lead the trace,
		// would have a running entry demand a queue position. Both readings
		// judge one snapshot against two different moments at once. What the
		// entry cannot do is invent a queue: a run admitted started was never
		// in one.
		reservation, settled := r.admittedQueued && entry.Status == protocol.RunQueued, terminalStatus(entry.Status)
		switch {
		case settled:
			// checkEntryStatus says what is wrong with a terminal entry. It
			// describes neither a reservation nor a started run, so it takes
			// no queue position and claims none, and a second complaint here
			// would only restate the first.
		case reservation:
			queuePosition++
			if entry.QueuePosition == nil || *entry.QueuePosition != queuePosition {
				s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/queue_position", "a reservation's queue position must be its 1-based place in the queue", fmt.Sprintf("%d", queuePosition), describeQueuePosition(entry.QueuePosition), string(entry.RunID))
			}
		case entry.QueuePosition != nil:
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/queue_position", "a started run holds no queue position", "absent", fmt.Sprintf("%d", *entry.QueuePosition), string(entry.RunID))
		}
		if reservation {
			listedReservation = true
		}
		if !reservation && !settled {
			if listedStarted != "" {
				// One started run at a time is the whole of decision 0001 that
				// this unit kept. A listing is one moment, so two entries
				// describing runs as executing describe a moment that never
				// existed, however the promotion fell inside the window — and
				// letting the second replace the first would leave
				// active_run_id owing nothing but the last one named.
				s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs claims a second started run, and a session has one", "a queued status behind "+string(listedStarted), string(entry.Status), string(entry.RunID))
			} else {
				listedStarted, startedEntry = r.id, entry
			}
		}
		s.checkEntryStatus(i, line, e, pointer, entry, r)
		s.checkEntryAnchor(i, line, e, pointer, p.SessionID, entry, r)
		s.checkEntryPending(i, line, e, pointer, entry, r)
	}
	for _, r := range required {
		if !listed[r.id] {
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs", "snapshot omits a nonterminal run it accounted for", string(r.id), "absent", string(r.id))
		}
	}
	// active_run_id keeps naming the started run, or is absent when only
	// reservations remain — both read off the entries this snapshot carries,
	// at the positions those entries state, so the two fields cannot disagree
	// about the same run.
	started := listedStarted
	switch {
	case started != "" && p.ActiveRunID != started:
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "active_run_id must name the started run of the session", string(started), string(p.ActiveRunID), string(started))
	case started == "" && len(p.ActiveRuns) > 0 && p.ActiveRunID != "":
		// Only reservations remain, and a reservation is not a started run:
		// the field names one or it names none. A client reading it as the
		// run to follow would follow a run that has published nothing.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "active_run_id must be absent where the session holds only reservations", "absent", string(p.ActiveRunID))
	}
	s.checkListedStatus(i, line, e, p, started, startedEntry, listedReservation)
}

// checkListedStatus holds the session status to the same listing the other two
// fields are read from. A reconnecting client reads all three at once, and a
// snapshot that says a run is executing while calling the session queued, or
// holds a reservation while calling itself idle, hands that client a session
// that never existed — the field it happens to trust decides what it does.
//
// Only the two directions the listing settles are judged. A session listing
// nothing keeps whatever status it reports: an empty listing is what a closed
// or errored session carries too, and those say something the runs cannot. An
// entry with a terminal status is diagnosed as an entry, and says nothing about
// the session either way.
func (s *state) checkListedStatus(i, line int, e protocol.Envelope, p protocol.SessionState, started protocol.RunID, entry protocol.ActiveRun, reservation bool) {
	switch {
	case started != "":
		// A started run is executing or blocked on an interaction it raised,
		// and which of those the session reports is the run's own business:
		// session status describes the started run, so the two say the same
		// thing or one of them is wrong. Every other status denies the run the
		// snapshot lists outright.
		//
		// What counts as waiting is read from the entry, not from the trace,
		// because an interaction can be raised or resolved inside the window
		// and the snapshot is entitled to have caught either edge. The entry
		// says so with its status or by naming what it is blocked on — and
		// naming it is not free, since the pending set is judged against the
		// run's own at the position the entry states.
		waiting := entry.Status == protocol.RunWaitingForInput || len(entry.PendingInteractions) > 0
		switch {
		case p.Status == protocol.SessionWaitingForInput && !waiting:
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "session waits for input the run it lists reports nothing waiting on", string(protocol.SessionRunning), string(p.Status), string(started))
		case p.Status == protocol.SessionRunning && waiting:
			// The same derivation, the other way round. An entry naming what
			// its run is blocked on is evidence of the wait wherever it is
			// read, so a session calling itself running beside it contradicts
			// the entry exactly as much as one calling itself waiting beside a
			// run that reports nothing.
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "session status denies the wait the entry beside it reports", string(protocol.SessionWaitingForInput), string(p.Status), string(started))
		case p.Status == protocol.SessionRunning || p.Status == protocol.SessionWaitingForInput:
		default:
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "session status denies the started run the snapshot lists", "running or waiting_for_input", string(p.Status), string(started))
		}
	case reservation:
		// Work is admitted and none of it has begun, which is the one thing
		// queued says. idle would tell a reconnecting client there is nothing
		// to wait for; running would tell it something is already executing.
		if p.Status == protocol.SessionQueued {
			return
		}
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "a session holding only reservations is queued", string(protocol.SessionQueued), string(p.Status))
	}
}

// checkEntryStatus holds an entry's status to what active_runs is a list of.
//
// A terminal status is always wrong there: the field lists the session's
// nonterminal runs, and a snapshot that knows a run settled drops it and names
// it in as_of.settled rather than listing it as completed. A run the trace has
// seen start is no longer queued at any position from its start onwards; a
// capture before that position may still call it queued, and is judged there.
//
// A queued entry whose run has not started yet is the ahead-of-trace case, and
// it is deferred rather than accepted: the position it states may be one the
// run turns out to be running at, and only the run's start decides that. A
// snapshot could otherwise name a position past a promotion that has not
// drained, call the run queued there, and never be judged.
//
// The converse — a reservation listed as running — is deliberately not
// diagnosed. A promotion happens inside the endpoint and its run.started may
// drain after the snapshot, so an accurate entry can lead the trace, which is
// the race the capture positions exist to allow.
// terminalStatus reports a run status that says the run is over.
func terminalStatus(status protocol.RunStatus) bool {
	switch status {
	case protocol.RunCompleted, protocol.RunFailed, protocol.RunCancelled:
		return true
	}
	return false
}

func (s *state) checkEntryStatus(i, line int, e protocol.Envelope, pointer string, entry protocol.ActiveRun, r *runState) {
	if terminalStatus(entry.Status) {
		s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs lists a run with a terminal status; a settled run is dropped and named in as_of.settled", "a nonterminal status", string(entry.Status), string(r.id))
		return
	}
	if entry.Status != protocol.RunQueued {
		return
	}
	if !r.started {
		if entry.AsOfSequence != nil {
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimQueued, session: r.session, run: r.id, sequence: *entry.AsOfSequence, index: i, line: line, envelope: e})
		}
		return
	}
	if entry.AsOfSequence != nil && *entry.AsOfSequence < r.startSequence {
		// The entry states a position before the run began, where queued is
		// what it was.
		return
	}
	s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs reports a run as queued at a position it had already started at", "a started status", string(entry.Status), string(r.id))
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
		switch s.namesSubmitRequest(id, session, r.id) {
		case admissionWrong:
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/admitted_submit_requests", "entry anchor names an envelope that is not a submit request the trace carries for this run", "a submit request on "+string(r.id), string(id), string(r.id))
		case admissionPending:
			// The request is on the session but unanswered, so it cannot
			// contradict the run it is claimed for yet. It can later: the
			// response may refuse it, or admit it to a different run.
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimAdmitted, session: session, run: r.id, request: id, pointer: pointer + "/admitted_submit_requests", index: i, line: line, envelope: e})
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

// The three answers namesSubmitRequest can give about an anchor.
const (
	// admissionGood: the trace carries the request and the admission claimed
	// for it.
	admissionGood = iota
	// admissionWrong: the trace does not carry it as a submit request for this
	// session, or carries it admitted somewhere else, or refused.
	admissionWrong
	// admissionPending: the request is on the session but its response has not
	// arrived, so the claim is about something the trace has not reached.
	admissionPending
)

// namesSubmitRequest judges whether an envelope id names a submit request the
// trace carries for the session, and — when a run is named — for that run.
func (s *state) namesSubmitRequest(id protocol.EnvelopeID, session protocol.SessionID, run protocol.RunID) int {
	req := s.requests[id]
	if req == nil || req.typ != protocol.TypeSessionMessageSubmitRequest || req.session != session {
		return admissionWrong
	}
	for _, candidate := range s.runs {
		if candidate.submitRequest == id {
			if run == "" || candidate.id == run {
				return admissionGood
			}
			return admissionWrong
		}
	}
	if req.responded {
		// The request was answered and produced no run, so the endpoint
		// refused it and there was never an admission to claim.
		return admissionWrong
	}
	return admissionPending
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
		// Genesis names the position before the session's first
		// model-affecting event, so any such event already behind the capture
		// window supersedes it exactly as a later promotion supersedes an
		// earlier one. Without this the marker is a way back to the opening
		// model from any point in the session.
		if later := s.latestMutationBefore(st, nil, s.captureWindowStart(i, e)); later != nil {
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names the moment before any model-affecting event, and one had already happened", string(later.id), "genesis", string(later.id))
			return
		}
		if st.openingKnown && p.CurrentModelID != st.openingModel {
			s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot marked before any model-affecting event reports a model other than the session's opening one", st.openingModel, p.CurrentModelID)
		}
		return
	}
	r := s.runs[position.RunID]
	if r == nil || r.session != p.SessionID {
		// A position is a position in this session's history. An anchor on
		// another session's run would let a snapshot borrow a model authority
		// that says nothing about the session it describes.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a run the trace does not carry for this session", "a run admitted on "+string(p.SessionID), string(position.RunID))
		return
	}
	if r.started && r.startSequence != position.Sequence {
		// The run began somewhere else, and a run begins once: no later
		// envelope can make this position the promotion it claims to be, so
		// there is nothing to wait for.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a sequence its run did not start at", fmt.Sprintf("%d", r.startSequence), fmt.Sprintf("%d", position.Sequence), string(r.id))
		return
	}
	if !r.started {
		// The named promotion has not reached the trace; it is held and
		// reconciled when it arrives.
		s.deferred = append(s.deferred, &deferredStateClaim{kind: claimModel, session: p.SessionID, run: position.RunID, sequence: position.Sequence, model: p.CurrentModelID, index: i, line: line, envelope: e})
		return
	}
	model, known := mutationModel(r)
	if !known {
		// model_run_sequence names the last model-affecting event, and a run
		// that applied no session_mutation is not one. Accepting the anchor
		// anyway would let a snapshot point at a control-free start and report
		// any model at all, since supplying an anchor is what sets the
		// unanchored check aside.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a run that applied no session_mutation, so it is not a model-affecting event", "a run admitted with a session_mutation model selection", string(r.id), string(r.id))
		return
	}
	if later := s.latestMutationBefore(st, r, s.captureWindowStart(i, e)); later != nil {
		// The field names the *last* model-affecting event the snapshot
		// reflects, so an anchor with a later one already behind it describes
		// a moment that had passed and reports the model of that moment. The
		// window is what makes "later" answerable: a promotion that happened
		// after the read was requested is not one the snapshot had to reflect.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a model-affecting event a later one had already superseded", string(later.id), string(r.id), string(r.id))
		return
	}
	if p.CurrentModelID != model {
		s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot reports a model other than the one in force at the position it states", model, p.CurrentModelID, string(r.id))
	}
}

// latestMutationBefore is the session's last model-affecting promotion at or
// before a capture window that is later than the one anchored, or nil when the
// anchor is already that event. A nil anchor is the genesis marker, which every
// model-affecting event is later than. A model-affecting promotion is a started
// run that applied a session_mutation selection, and later means later in the
// trace: which promotion moved the default last is a question about the order
// they reached it, not about admission order.
func (s *state) latestMutationBefore(st *sessionTrack, anchor *runState, window int) *runState {
	var latest *runState
	for _, id := range st.order {
		r := s.runs[id]
		if r == nil || r == anchor || !r.started || r.startedAt > window {
			continue
		}
		if _, known := mutationModel(r); !known {
			continue
		}
		if anchor != nil && r.startedAt <= anchor.startedAt {
			continue
		}
		if latest == nil || r.startedAt > latest.startedAt {
			latest = r
		}
	}
	return latest
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
