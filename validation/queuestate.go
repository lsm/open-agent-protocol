package validation

import (
	"fmt"

	"github.com/lsm/open-agent-protocol/protocol"
)

func (s *state) checkSessionCapture(i, line int, e protocol.Envelope, p protocol.SessionState, st *sessionTrack) {
	s.checkCaptureModel(i, line, e, p, st)
	anchored := map[protocol.EnvelopeID]bool{}
	settled := map[protocol.RunID]uint64{}
	var claimedAdmissions []protocol.EnvelopeID
	if p.AsOf != nil {
		for index, entry := range p.AsOf.Settled {
			if _, seen := settled[entry.RunID]; seen {

				s.addExpected(CodeSessionStateMismatch, i, line, e, fmt.Sprintf("/payload/as_of/settled/%d/run_id", index), "as_of.settled claims one run settled twice", "one claim per run", string(entry.RunID), string(entry.RunID))
				continue
			}
			settled[entry.RunID] = entry.Sequence
		}
		for _, id := range p.AsOf.AdmittedSubmitRequests {
			switch s.namesSubmitRequest(id, p.SessionID, "") {
			case admissionWrong:
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/admitted_submit_requests", "capture anchor names an envelope that is not a request the trace carries as admitting a message on this session", "a submit request, or an open carrying a message, on "+string(p.SessionID), string(id))
				continue
			case admissionPending:

				claimedAdmissions = append(claimedAdmissions, id)
			}
			anchored[id] = true
		}
	}
	if len(claimedAdmissions) > 0 {

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
	s.checkActiveRunsListing(i, line, e, p, required, settled, s.captureWindowStart(i, e))
}

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

func (s *state) captureWindowStart(i int, e protocol.Envelope) int {
	if req := s.requests[e.InReplyTo]; req != nil {
		return req.index
	}
	return i
}

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

func (s *state) checkActiveRunsListing(i, line int, e protocol.Envelope, p protocol.SessionState, required []*runState, settled map[protocol.RunID]uint64, window int) {
	listed := map[protocol.RunID]bool{}

	listedStarted := protocol.RunID("")

	var startedEntry protocol.ActiveRun

	listedReservation := false

	unresolved := false
	lastOrder := -1
	queuePosition := 0

	leads := 0
	var ledEntries []*entryClaim

	markExecuting := func(pointer string, entry protocol.ActiveRun) {
		if listedReservation {

			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs describes a run executing ahead of a reservation admitted before it", "a queued status behind the earlier reservation", string(entry.Status), string(entry.RunID))
		}
		if listedStarted != "" {

			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs claims a second started run, and a session has one", "a queued status behind "+string(listedStarted), string(entry.Status), string(entry.RunID))
			return
		}
		listedStarted, startedEntry = entry.RunID, entry
	}
	for index, entry := range p.ActiveRuns {
		pointer := fmt.Sprintf("/payload/active_runs/%d", index)
		if listed[entry.RunID] {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs lists one run twice", "one entry per run", string(entry.RunID))
			continue
		}
		listed[entry.RunID] = true
		if _, gone := settled[entry.RunID]; gone {

			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs lists a run the same snapshot says it had already settled", "a run the snapshot still holds", string(entry.RunID), string(entry.RunID))
			continue
		}
		r := s.runs[entry.RunID]
		if r == nil || r.session != p.SessionID {
			if anchors := pendingAnchors(s, entry, p.SessionID); len(anchors) > 0 {

				made := len(s.deferred)
				for _, id := range anchors {
					s.deferred = append(s.deferred, &deferredStateClaim{kind: claimAdmitted, session: p.SessionID, run: entry.RunID, request: id, pointer: pointer + "/run_id", index: i, line: line, envelope: e})
				}

				claim := &entryClaim{entry: entry, state: p, pointer: pointer, queueAhead: queuePosition, leadsAhead: leads, index: i, line: line, envelope: e}
				switch {
				case terminalStatus(entry.Status):

					s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs lists a run with a terminal status; a settled run is dropped and named in as_of.settled", "a nonterminal status", string(entry.Status), string(entry.RunID))
					claim = nil
				case entry.Status == protocol.RunQueued || entry.Status == protocol.RunCancelling:

					unresolved, claim.classify = true, true
				default:

					markExecuting(pointer, entry)

					claim.resolved = true
					if entry.QueuePosition != nil {
						s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/queue_position", "a started run holds no queue position", "absent", fmt.Sprintf("%d", *entry.QueuePosition), string(entry.RunID))
					}
				}
				if claim != nil {
					ledEntries = append(ledEntries, claim)

					for _, held := range s.deferred[made:] {
						held.entry = claim
					}
				}
				leads++
				continue
			}
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs names a run the trace does not carry for this session", "a run admitted on "+string(p.SessionID), string(entry.RunID))
			continue
		}
		if r.terminal && r.terminalAt < window {

			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs lists a run that had already settled before the read was requested", "a nonterminal run", string(entry.RunID), string(entry.RunID))
			continue
		}
		if r.terminal && entry.AsOfSequence != nil && *entry.AsOfSequence >= r.next-1 {

			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/as_of_sequence", "active_runs lists a run at a position it had already settled at", fmt.Sprintf("a position before %d", r.next-1), fmt.Sprintf("%d", *entry.AsOfSequence), string(entry.RunID))
		}
		if r.order <= lastOrder || leads > 0 {

			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs is not in admission order", "admission order", string(entry.RunID))
		}
		lastOrder = r.order

		cancelling, pending := cancellingReservation(entry, r)
		if pending && r.admittedQueued {
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimReservation, session: r.session, run: r.id, sequence: *entry.AsOfSequence, stated: true, held: cancelling, index: i, line: line, envelope: e})
		}
		reservation, settled := r.admittedQueued && (entry.Status == protocol.RunQueued || cancelling), terminalStatus(entry.Status)
		if r.admittedQueued && !r.started && !reservation && !settled && !pending {

			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimReservation, session: r.session, run: r.id, index: i, line: line, envelope: e})
		}
		switch {
		case settled:

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
			markExecuting(pointer, entry)
		}
		s.checkEntryStatus(i, line, e, pointer, entry, r)
		s.checkEntryAnchor(i, line, e, pointer, p.SessionID, entry, r)
		s.checkEntryPending(i, line, e, pointer, entry, r)
	}
	if len(ledEntries) > 0 {

		group := &ledGroup{claims: ledEntries}
		for _, led := range ledEntries {
			led.executing, led.group = listedStarted, group
		}
		s.ledGroups = append(s.ledGroups, group)
	}
	for _, r := range required {
		if !listed[r.id] {
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs", "snapshot omits a nonterminal run it accounted for", string(r.id), "absent", string(r.id))
		}
	}

	started := listedStarted
	switch {
	case started != "" && p.ActiveRunID != started:
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "active_run_id must name the started run of the session", string(started), string(p.ActiveRunID), string(started))
	case started == "" && !unresolved && len(p.ActiveRuns) > 0 && p.ActiveRunID != "":

		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "active_run_id must be absent where the session holds only reservations", "absent", string(p.ActiveRunID))
	}

	s.checkListedStatus(i, line, e, p, started, startedEntry, listedReservation && !unresolved)
}

func (s *state) checkListedStatus(i, line int, e protocol.Envelope, p protocol.SessionState, started protocol.RunID, entry protocol.ActiveRun, reservation bool) {
	switch {
	case started != "":

		waiting := entry.Status == protocol.RunWaitingForInput || len(entry.PendingInteractions) > 0
		switch {
		case p.Status == protocol.SessionWaitingForInput && !waiting:
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "session waits for input the run it lists reports nothing waiting on", string(protocol.SessionRunning), string(p.Status), string(started))
		case p.Status == protocol.SessionRunning && waiting:

			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "session status denies the wait the entry beside it reports", string(protocol.SessionWaitingForInput), string(p.Status), string(started))
		case p.Status == protocol.SessionRunning || p.Status == protocol.SessionWaitingForInput:
		default:
			s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "session status denies the started run the snapshot lists", "running or waiting_for_input", string(p.Status), string(started))
		}
	case reservation:

		if p.Status == protocol.SessionQueued {
			return
		}
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/status", "a session holding only reservations is queued", string(protocol.SessionQueued), string(p.Status))
	}
}

func pendingAnchors(s *state, entry protocol.ActiveRun, session protocol.SessionID) []protocol.EnvelopeID {
	if len(entry.AdmittedSubmitRequests) == 0 {
		return nil
	}
	for _, id := range entry.AdmittedSubmitRequests {
		if s.namesSubmitRequest(id, session, "") != admissionPending {
			return nil
		}
	}
	return entry.AdmittedSubmitRequests
}

func terminalStatus(status protocol.RunStatus) bool {
	switch status {
	case protocol.RunCompleted, protocol.RunFailed, protocol.RunCancelled:
		return true
	}
	return false
}

func cancellingReservation(entry protocol.ActiveRun, r *runState) (reservation, pending bool) {
	if entry.Status != protocol.RunCancelling {
		return false, false
	}
	if r.started {
		return entry.AsOfSequence != nil && *entry.AsOfSequence < r.startSequence, false
	}
	return cancellingHoldsItsPlace(entry), entry.AsOfSequence != nil
}

func cancellingHoldsItsPlace(entry protocol.ActiveRun) bool {
	return entry.AsOfSequence == nil || entry.QueuePosition != nil
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

		return
	}
	s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs reports a run as queued at a position it had already started at", "a started status", string(entry.Status), string(r.id))
}

func knownTo(r *runState, listed map[protocol.InteractionID]bool) map[protocol.InteractionID]bool {
	if !r.priorUnknown {
		return listed
	}
	known := map[protocol.InteractionID]bool{}
	for id := range listed {
		if r.interactions[id] != nil {
			known[id] = true
		}
	}
	return known
}

func describeQueuePosition(position *int) string {
	if position == nil {
		return "absent"
	}
	return fmt.Sprintf("%d", *position)
}

func (s *state) checkEntryAnchor(i, line int, e protocol.Envelope, pointer string, session protocol.SessionID, entry protocol.ActiveRun, r *runState) {
	for _, id := range entry.AdmittedSubmitRequests {
		switch s.namesSubmitRequest(id, session, r.id) {
		case admissionWrong:
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/admitted_submit_requests", "entry anchor names an envelope that is not a request the trace carries as admitting a message on this run", "a submit request, or an open carrying a message, on "+string(r.id), string(id), string(r.id))
		case admissionPending:

			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimAdmitted, session: session, run: r.id, request: id, pointer: pointer + "/admitted_submit_requests", index: i, line: line, envelope: e})
		}
	}
}

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

	if len(listed) > 0 && entry.AsOfSequence == nil {
		s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/as_of_sequence", "an entry carrying pending_interactions must state the position it was captured at", "a capture position", "absent", string(r.id))
		s.checkEntryAcknowledged(i, line, e, pointer, entry, r, nil)
		return
	}
	if entry.AsOfSequence == nil {

		if s.queueOffered() && !sameIDSet(unresolved, knownTo(r, listed)) {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/pending_interactions", "entry does not report the unresolved interactions its run is blocked on", describeIDs(unresolved), describeIDs(listed), string(r.id))
		}
		s.checkEntryAcknowledged(i, line, e, pointer, entry, r, unresolved)
		return
	}
	seq := *entry.AsOfSequence
	if seq > r.next-1 {
		s.deferred = append(s.deferred, &deferredStateClaim{kind: claimCapture, session: r.session, run: r.id, sequence: seq, listed: listed, index: i, line: line, envelope: e})

		s.checkEntryAcknowledged(i, line, e, pointer, entry, r, nil)
		return
	}
	want := pendingAt(r, seq)
	if !sameIDSet(want, knownTo(r, listed)) {
		s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/pending_interactions", "active_runs entry does not report the run's unresolved interactions at the position it states", describeIDs(want), describeIDs(listed), string(r.id))
	}
	s.checkEntryAcknowledged(i, line, e, pointer, entry, r, want)
}

func (s *state) recordSettledClaims(i, line int, e protocol.Envelope, p protocol.SessionState, settled map[protocol.RunID]uint64) {
	if p.AsOf == nil {
		return
	}
	handled := map[protocol.RunID]bool{}
	for _, entry := range p.AsOf.Settled {
		if handled[entry.RunID] {

			continue
		}
		handled[entry.RunID] = true
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

const (
	admissionGood = iota

	admissionWrong

	admissionPending
)

func (s *state) namesSubmitRequest(id protocol.EnvelopeID, session protocol.SessionID, run protocol.RunID) int {
	req := s.requests[id]
	if req == nil || req.session != session || !admitsMessages(req) {
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

		return admissionWrong
	}
	return admissionPending
}

func admitsMessages(req *requestState) bool {
	switch req.typ {
	case protocol.TypeSessionMessageSubmitRequest:
		return true
	case protocol.TypeSessionOpenRequest:
		return req.carriesMessage
	}
	return false
}

func (s *state) checkCaptureModel(i, line int, e protocol.Envelope, p protocol.SessionState, st *sessionTrack) {
	if st == nil {
		return
	}
	if p.AsOf == nil || p.AsOf.ModelRunSequence == nil {

		if r := s.latestMutationBefore(st, nil, s.captureWindowStart(i, e)); r != nil {
			if model, known := mutationModel(r); known && p.CurrentModelID != model {
				s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot reports a model other than the one the session's last model-affecting run installed", model, p.CurrentModelID, string(r.id))
			}
		}
		return
	}
	position := *p.AsOf.ModelRunSequence
	if position.Genesis() {

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

		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a run the trace does not carry for this session", "a run admitted on "+string(p.SessionID), string(position.RunID))
		return
	}
	if r.started && r.startSequence != position.Sequence {

		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a sequence its run did not start at", fmt.Sprintf("%d", r.startSequence), fmt.Sprintf("%d", position.Sequence), string(r.id))
		return
	}
	if !r.started {

		s.deferred = append(s.deferred, &deferredStateClaim{kind: claimModel, session: p.SessionID, run: position.RunID, sequence: position.Sequence, model: p.CurrentModelID, index: i, line: line, envelope: e})
		return
	}
	model, known := mutationModel(r)
	if !known {

		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a run that applied no session_mutation, so it is not a model-affecting event", "a run admitted with a session_mutation model selection", string(r.id), string(r.id))
		return
	}
	if later := s.latestMutationBefore(st, r, s.captureWindowStart(i, e)); later != nil {

		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/as_of/model_run_sequence", "capture position names a model-affecting event a later one had already superseded", string(later.id), string(r.id), string(r.id))
		return
	}
	if p.CurrentModelID != model {
		s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot reports a model other than the one in force at the position it states", model, p.CurrentModelID, string(r.id))
	}
}

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
