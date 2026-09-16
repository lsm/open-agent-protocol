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
		for index, entry := range p.AsOf.Settled {
			if _, seen := settled[entry.RunID]; seen {
				// One run settles once, so two claims about it are two
				// terminals. They are also two claims into one map, where the
				// later would silently replace the earlier and take its
				// reconciliation with it.
				s.addExpected(CodeSessionStateMismatch, i, line, e, fmt.Sprintf("/payload/as_of/settled/%d/run_id", index), "as_of.settled claims one run settled twice", "one claim per run", string(entry.RunID), string(entry.RunID))
				continue
			}
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
	s.checkActiveRunsListing(i, line, e, p, required, settled, s.captureWindowStart(i, e))
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
func (s *state) checkActiveRunsListing(i, line int, e protocol.Envelope, p protocol.SessionState, required []*runState, settled map[protocol.RunID]uint64, window int) {
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
	// unresolved marks a listing carrying an entry for a run the trace has
	// not carried yet. Such an entry cannot be classified — the admission that
	// says whether it is a reservation has not arrived — so the fields read
	// off the classification are not judged against this listing at all.
	unresolved := false
	lastOrder := -1
	queuePosition := 0
	// leads counts the entries leading their own admission that the listing
	// has already passed. Everything after them is judged knowing that, and
	// each of them is carried forward to be judged when its admission lands.
	leads := 0
	var ledEntries []*entryClaim
	// markExecuting records an entry whose status says its run is executing.
	// Both paths into it read that off the status: the known entry's
	// classification, and, for an entry still leading its own admission, the
	// part of that classification the admission cannot change. One listing is
	// one moment either way, so both are held to the same two rules about what
	// a moment may contain.
	markExecuting := func(pointer string, entry protocol.ActiveRun) {
		if listedReservation {
			// Promotion is in admission order, so a reservation admitted
			// first cannot still be queued behind a run admitted after it.
			// The listing is one moment and this one describes an ordering
			// the queue does not permit — which no capture position excuses,
			// because there is no moment it describes.
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs describes a run executing ahead of a reservation admitted before it", "a queued status behind the earlier reservation", string(entry.Status), string(entry.RunID))
		}
		if listedStarted != "" {
			// One started run at a time is the whole of decision 0001 that
			// this unit kept. A listing is one moment, so two entries
			// describing runs as executing describe a moment that never
			// existed, however the promotion fell inside the window — and
			// letting the second replace the first would leave active_run_id
			// owing nothing but the last one named.
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
			// active_runs is what the session still holds and as_of.settled is
			// what it has already let go, so one run cannot be in both. The
			// two are read by different rules that each believe their own
			// input: the entry would take a place in the listing while the
			// settlement claim waits for a terminal that arrives on schedule
			// and vindicates it, and the snapshot would be accepted for
			// saying one run is two things at once. Neither field is judged
			// for this entry afterwards — a run the snapshot says it dropped
			// describes no position, no status and no queue place.
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs lists a run the same snapshot says it had already settled", "a run the snapshot still holds", string(entry.RunID), string(entry.RunID))
			continue
		}
		r := s.runs[entry.RunID]
		if r == nil || r.session != p.SessionID {
			if anchors := pendingAnchors(s, entry, p.SessionID); len(anchors) > 0 {
				// A snapshot may lead an admission it made: the endpoint knows
				// the run, the response that will tell the trace about it is
				// still in flight, and the entry says which submission it came
				// from. That anchor is what licenses the claim, so the entry
				// is held and reconciled against the response like every other
				// claim made ahead of the trace, rather than rejected before
				// its own anchor can be read.
				//
				// Only the entry's identity is reconciled — that the request
				// was admitted, to this run, on this session. Its status,
				// position and pending set describe a moment before the run's
				// admission reached the trace, and the per-entry rules have
				// nothing to judge them against there.
				made := len(s.deferred)
				for _, id := range anchors {
					s.deferred = append(s.deferred, &deferredStateClaim{kind: claimAdmitted, session: p.SessionID, run: entry.RunID, request: id, pointer: pointer + "/run_id", index: i, line: line, envelope: e})
				}
				// What the lead buys is time for the one thing the response
				// says: which run this submission became. It does not buy the
				// entry an exemption from what it says about itself. The
				// entry's own status is not something an admission can change,
				// so every rule that reads the status alone still reaches it,
				// and only the statuses that settle nothing without the run's
				// own history stand the classification down.
				// Standing a rule down is waiting for the answer, not
				// forgiving the question: what the admission decides is
				// carried forward on the same claim and judged when it
				// decides it.
				claim := &entryClaim{entry: entry, state: p, pointer: pointer, queueAhead: queuePosition, leadsAhead: leads, index: i, line: line, envelope: e}
				switch {
				case terminalStatus(entry.Status):
					// active_runs is the nonterminal set whatever admitted the
					// run, so this entry is already wrong and no response
					// could right it. It is classified too — a settled run is
					// neither a reservation nor a started one — so the fields
					// read off the listing are not left waiting on it either,
					// and there is nothing left for the admission to settle.
					s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs lists a run with a terminal status; a settled run is dropped and named in as_of.settled", "a nonterminal status", string(entry.Status), string(entry.RunID))
					claim = nil
				case entry.Status == protocol.RunQueued || entry.Status == protocol.RunCancelling:
					// The two the trace has to supply. Queued is what a
					// reservation says and the admission decides whether this
					// run is one; cancelling says nothing about whether the
					// run began, and without the run there is no start to
					// settle it against.
					unresolved, claim.classify = true, true
				default:
					// Everything else says the run is executing, and a
					// reservation's entry does not say that however it was
					// admitted — which also settles its queue place, since a
					// run the listing describes as executing is in no queue
					// whatever admitted it.
					markExecuting(pointer, entry)
					// Its status settles it without the admission, so it is
					// resolved from here and its siblings can count it as
					// taking no queue place.
					claim.resolved = true
					if entry.QueuePosition != nil {
						s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/queue_position", "a started run holds no queue position", "absent", fmt.Sprintf("%d", *entry.QueuePosition), string(entry.RunID))
					}
				}
				if claim != nil {
					ledEntries = append(ledEntries, claim)
					// Every anchor the entry named carries the entry, not just
					// the last of them. An entry may cite several submissions
					// and any one of them may be the one whose response
					// creates its run — including the first, whose claim would
					// otherwise reach that response carrying nothing and let
					// the entry's own rules go unasked. At most one of them
					// can name the run, because a run has one admission, so
					// binding them all judges the entry exactly once.
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
			// active_runs is the nonterminal set. A run that settled before
			// the read was even requested cannot be in it under any capture
			// position, so this is a stale listing rather than a race. One
			// that settled inside the window may still be listed: the
			// snapshot may have been captured before that terminal, which is
			// the race the capture positions exist to allow.
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/run_id", "active_runs lists a run that had already settled before the read was requested", "a nonterminal run", string(entry.RunID), string(entry.RunID))
			continue
		}
		if r.terminal && entry.AsOfSequence != nil && *entry.AsOfSequence >= r.next-1 {
			// That race allowance is for a snapshot that could not have seen
			// the terminal. An entry stating the position the run settled at,
			// or one past it, has said it could: it claims to reflect the run
			// as far as the envelope that ended it and lists it as
			// outstanding anyway.
			// The entry still describes the run the snapshot meant to list, so
			// it goes on being one: what is wrong is the position it states,
			// and setting the whole entry aside would change what
			// active_run_id is judged against.
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/as_of_sequence", "active_runs lists a run at a position it had already settled at", fmt.Sprintf("a position before %d", r.next-1), fmt.Sprintf("%d", *entry.AsOfSequence), string(entry.RunID))
		}
		if r.order <= lastOrder || leads > 0 {
			// A lead is an entry whose submit request the trace carries
			// unanswered, so the response that admits its run comes after
			// every admission the trace already has: whatever its place in
			// the queue turns out to be, its place in admission order is
			// last. A run the trace does carry, listed after it, is out of
			// order on the listing's own terms — which is why the entry's own
			// order is not what has to be waited for here.
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
		// A reservation cancelled before promotion goes queued -> cancelling
		// without passing through running: that transition is legal on its
		// own (legalRunStatusTransition), and decision 0001 has a run settle
		// failed or cancelled before run.started but never completed. So an
		// accurate snapshot reports it cancelling and still in the queue, at
		// the place it still holds. Cancelling says nothing about whether the
		// run began, so unlike queued it cannot classify on its own: the
		// trace decides, at the position the entry states.
		// Where the trace has not reached the stated position, there is
		// nothing yet to decide it against, and reading it either way alone is
		// a false verdict at one edge of the window: taking the entry for a
		// reservation lets a snapshot hold a queue place at a position past a
		// promotion that had not drained, and refusing to would reject the
		// accurate entry of a run cancelled after one. So the entry answers
		// for itself, with the field the classification governs, and is held
		// to that answer when the start arrives.
		cancelling, pending := cancellingReservation(entry, r)
		if pending && r.admittedQueued {
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimReservation, session: r.session, run: r.id, sequence: *entry.AsOfSequence, stated: true, held: cancelling, index: i, line: line, envelope: e})
		}
		reservation, settled := r.admittedQueued && (entry.Status == protocol.RunQueued || cancelling), terminalStatus(entry.Status)
		if r.admittedQueued && !r.started && !reservation && !settled && !pending {
			// The entry says a run admitted into the queue has begun. A
			// promotion happens inside the endpoint and its run.started can
			// drain after the snapshot, which is why the claim is allowed to
			// lead the trace — but leading is waiting for the answer, not
			// being excused from it. The run's start settles it, and a run
			// that settles without ever starting settles it the other way.
			// Cancelling entries make the same claim through their queue
			// place and are already held to it, so they are not held twice.
			//
			// The claim is that the run begins, not that it had begun at the
			// position the entry states. A promotion inside the endpoint is
			// exactly what the entry is allowed to be ahead of the trace
			// about, so the position it names is not evidence against it —
			// only a run that settles without ever starting is.
			s.deferred = append(s.deferred, &deferredStateClaim{kind: claimReservation, session: r.session, run: r.id, index: i, line: line, envelope: e})
		}
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
			markExecuting(pointer, entry)
		}
		s.checkEntryStatus(i, line, e, pointer, entry, r)
		s.checkEntryAnchor(i, line, e, pointer, p.SessionID, entry, r)
		s.checkEntryPending(i, line, e, pointer, entry, r)
	}
	if len(ledEntries) > 0 {
		// The leads of one listing are settled as a set. Which run the listing
		// described as executing is a fact about the whole listing rather than
		// about what had been read when an entry was reached: an entry's queue
		// place counts what precedes it, but the run active_run_id owed is
		// named wherever it appears.
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
	// active_run_id keeps naming the started run, or is absent when only
	// reservations remain — both read off the entries this snapshot carries,
	// at the positions those entries state, so the two fields cannot disagree
	// about the same run.
	// An entry leading its own admission costs this listing only what that
	// entry alone decides. What the entries the trace does carry already
	// establish is established: a listing saying a run is executing has named
	// that run, whatever a pending entry turns out to be, and a session with
	// an executing run in it is not idle. Standing all of it down let a
	// snapshot list a started run, name none, call itself idle, and be
	// answered forever by a deferred claim that only checks the other entry's
	// identity.
	started := listedStarted
	switch {
	case started != "" && p.ActiveRunID != started:
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "active_run_id must name the started run of the session", string(started), string(p.ActiveRunID), string(started))
	case started == "" && !unresolved && len(p.ActiveRuns) > 0 && p.ActiveRunID != "":
		// Only reservations remain, and a reservation is not a started run:
		// the field names one or it names none. A client reading it as the
		// run to follow would follow a run that has published nothing. This
		// one does stand down under a lead: the pending entry may be the
		// started run, and naming it would then be right.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "active_run_id must be absent where the session holds only reservations", "absent", string(p.ActiveRunID))
	}
	// The reservations-only status rule stands down for the same reason, and
	// the started-run one does not: an executing run the listing names is an
	// executing run whatever else the listing is waiting to learn.
	s.checkListedStatus(i, line, e, p, started, startedEntry, listedReservation && !unresolved)
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

// pendingAnchors is an entry's admitted_submit_requests where every one of
// them is a submit request on this session whose response has not arrived.
// Every one, because an anchor the trace has already answered resolves to some
// run, and an entry naming a different one contradicts it rather than leading
// it.
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

// cancellingReservation answers whether a cancelling entry still holds the
// queue place its run was admitted into, and whether that answer is one the
// trace cannot check yet.
//
// A run the trace has seen start settles it: the entry is a reservation where
// it states a position before that start, and not where it states one from the
// start onwards. Before the start arrives there is nothing to compare, so the
// entry answers for itself — a queue position beside a stated capture position
// claims the run had not begun there, its absence claims it had — and the
// answer is reconciled when the start lands, or when the trace ends without
// one. An entry stating no position at all claims no knowledge the trace
// lacks and is read as the trace stands, which is a run still in its queue.
func cancellingReservation(entry protocol.ActiveRun, r *runState) (reservation, pending bool) {
	if entry.Status != protocol.RunCancelling {
		return false, false
	}
	if r.started {
		return entry.AsOfSequence != nil && *entry.AsOfSequence < r.startSequence, false
	}
	return cancellingHoldsItsPlace(entry), entry.AsOfSequence != nil
}

// cancellingHoldsItsPlace is a cancelling entry's own answer to the one thing
// its status does not settle, for wherever the trace cannot settle it: a queue
// position beside a stated capture position claims the run had not begun
// there, its absence claims it had, and an entry stating no position at all
// claims no knowledge the trace lacks and is read as a run still in its queue.
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
		// The entry states a position before the run began, where queued is
		// what it was.
		return
	}
	s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/status", "active_runs reports a run as queued at a position it had already started at", "a started status", string(entry.Status), string(r.id))
}

// knownTo drops from a listed pending set the ids the run's own history cannot
// speak to. Only a run a recovery introduced without saying what it was
// blocked on has such a history: everything before the cursor is outside this
// trace, so an id it has never carried is not evidence of an interaction and
// its absence is not evidence of none. Judging either way would convict a
// snapshot for describing a moment that predates everything the validator can
// see. Every id the trace does carry is judged exactly as it always was.
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
		if s.queueOffered() && !sameIDSet(unresolved, knownTo(r, listed)) {
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
	if !sameIDSet(want, knownTo(r, listed)) {
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
	handled := map[protocol.RunID]bool{}
	for _, entry := range p.AsOf.Settled {
		if handled[entry.RunID] {
			// A second claim about one run is diagnosed where the settled set
			// is read. It is not a second thing to reconcile: one run settles
			// once, so there is one terminal for a claim to be judged against.
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
		// Without a marker the snapshot claims no knowledge the trace lacks,
		// so it answers to the last model-affecting event the trace carries at
		// or before the window — whether or not that run is still going. A
		// session_mutation moves the session default, and the default outlives
		// the run that moved it: terminality ends the run, not its effect.
		if r := s.latestMutationBefore(st, nil, s.captureWindowStart(i, e)); r != nil {
			if model, known := mutationModel(r); known && p.CurrentModelID != model {
				s.addExpected(CodePrematureSessionMutation, i, line, e, "/payload/current_model_id", "snapshot reports a model other than the one the session's last model-affecting run installed", model, p.CurrentModelID, string(r.id))
			}
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
