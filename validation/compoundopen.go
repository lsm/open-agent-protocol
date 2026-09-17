package validation

import (
	"github.com/lsm/open-agent-protocol/protocol"
)

// The compound open: a session.open.request that carries a subscription, a
// first message, or both (Decision 0009).
//
// The rule this file exists for is narrow and load-bearing. A message carried
// by an open is admitted by the open, so the open request is the admitting
// request for the run it produced and the open response is the admission. Both
// facts already have shapes elsewhere in the protocol — admitted_submit_requests
// names the admitting request, and Decision 0002 requires admission before
// start — and neither shape knew a request that was not a submit could fill
// them.

// compoundOpenRequest judges an open's optional members and records what the
// open owes when its response arrives.
//
// Both members are optional features and take the gate every optional feature
// takes: the envelope must cite the active descriptor revision, and the key
// must be affirmatively advertised. A message is gated under the delivery key
// it requests, because a message carried by an open is admitted under exactly
// the rules a separate submit is — the carrier changed, not the admission.
func (s *state) compoundOpenRequest(i, line int, e protocol.Envelope, p protocol.SessionOpenRequest) {
	if p.Subscribe {
		s.featureKeys(i, line, e, []string{protocol.FeatureOpenSubscribe})
		// Held for the honour aspect: an endpoint that advertised the key owes
		// this open a subscription, and a refusal of a request the validator
		// finds defect-free is the endpoint honouring nothing it advertised.
		// Only an affirmative advertisement creates the debt — where the key
		// is missing or unavailable the gate above already answered, and a
		// refusal there is the required behaviour rather than a failure.
		if affirmative(s.features[protocol.FeatureOpenSubscribe]) {
			s.pendingSubscribes[e.ID] = true
		}
	}
	if p.Message == nil {
		return
	}
	// Recorded before the gate, not after: the request carried a message
	// whatever the endpoint then did with it, and a later entry naming this
	// open as its anchor is judged against what the open asked for. A gate
	// failure is its own diagnostic and must not also make the anchor
	// unrecognizable.
	if req := s.requests[e.ID]; req != nil {
		req.carriesMessage = true
	}
	s.featureKeys(i, line, e, []string{protocol.DeliveryKey(p.Message.Delivery)})
}

// compoundOpenResponse admits the run a compound open produced.
//
// The open response plays the submit response's part here: it is the response
// to the request that carried the message, and it names the run in
// active_runs. Decision 0002's admission-before-start is satisfied by the same
// event that satisfies it for a separate submit — a response, arriving before
// any run-scoped envelope — so nothing in the run lifecycle below this needs
// to know which carrier admitted it.
//
// An open that carried no message admits nothing, whatever its state says: a
// session opened onto an adapter that already had a run in flight reports that
// run without having admitted it here, and treating a reported run as an
// admission would invent one the trace never made.
func (s *state) compoundOpenResponse(i, line int, e protocol.Envelope, p protocol.SessionOpenResponse) {
	req := s.requests[e.InReplyTo]
	if req == nil || !req.carriesMessage {
		return
	}
	entry, ok := soleAdmittedRun(p)
	if !ok {
		// A compound open that admitted a message owes exactly one run. None
		// is a refusal the endpoint reported as a success; more than one is a
		// claim about an admission this open cannot have made, since it
		// carried one message.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs",
			"a compound open's response names the one run its message admitted",
			"one active run", activeRunCount(p), string(p.SessionID))
		return
	}
	if _, seen := s.runs[entry.RunID]; seen {
		// The run already exists, so something else admitted it and this open
		// is claiming an admission that is not its own.
		s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_runs",
			"a compound open's run is admitted by that open and by nothing before it",
			"a run this open admitted", string(entry.RunID), string(p.SessionID))
		return
	}
	st := s.sessions[p.SessionID]
	if st == nil {
		st = &sessionTrack{}
		s.sessions[p.SessionID] = st
	}
	queued := entry.Status == protocol.RunQueued
	run := &runState{
		id: entry.RunID, session: p.SessionID, admitted: true, next: 1,
		lastIndex: i, lastLine: line, status: entry.Status,
		admittedModel: p.CurrentModelID,
		tools:         map[protocol.ToolCallID]toolTrack{},
		interactions:  map[protocol.InteractionID]*interactionState{},
	}
	run.order = len(st.order)
	run.admittedQueued = queued
	run.admittedAt = i
	run.submitRequest = e.InReplyTo
	s.runs[entry.RunID] = run
	st.order = append(st.order, entry.RunID)
	if !queued {
		if st.active == "" {
			st.active = entry.RunID
		} else if prev := s.runs[st.active]; prev == nil || prev.terminal {
			st.active = entry.RunID
		}
	}
	s.refreshQueueWindows(p.SessionID)
	s.admitLedEntries(run)
}

// soleAdmittedRun reports the single nonterminal run a compound open's response
// names, and false when it names anything other than one.
func soleAdmittedRun(p protocol.SessionOpenResponse) (protocol.ActiveRun, bool) {
	if len(p.ActiveRuns) == 1 {
		return p.ActiveRuns[0], true
	}
	return protocol.ActiveRun{}, false
}

func activeRunCount(p protocol.SessionOpenResponse) string {
	switch len(p.ActiveRuns) {
	case 0:
		return "none"
	case 1:
		return "one"
	}
	return "several"
}

// settleSubscribeRefusal judges a refusal of an open that elected subscribe
// against an endpoint advertising it.
func (s *state) settleSubscribeRefusal(i, line int, e protocol.Envelope) {
	if !s.pendingSubscribes[e.InReplyTo] {
		return
	}
	delete(s.pendingSubscribes, e.InReplyTo)
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error",
		"an open electing subscribe was refused by an endpoint advertising session.open.subscribe",
		"a session opened with its subscription", describeRefusal(payload.Error), string(e.InReplyTo))
}
