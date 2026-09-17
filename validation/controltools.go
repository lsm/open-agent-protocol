package validation

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The control-tools unit (T3c, Decision 0011): tools the control layer
// supplies at session open and executes itself.
//
// Two things are new here and everything else is the core contract applied to
// them. A call to such a tool is an interaction, so Decision 0001's rules —
// one responder, one resolution, nothing pending at a terminal — govern it
// unchanged. And the resolution travels a request/response pair, so the
// refusal is the required behaviour for anything the endpoint cannot accept,
// and the gate is judged on the response exactly as the tool-sources gates
// are.
//
// What the unit adds is that a refusal must say which of five conditions it
// observed, and that the endpoint's terminal must carry what the participant
// actually said.

// interactionKindToolCall is the interaction kind a control-owned call opens.
// It is a kind of its own beside "permission" and "input" because the kind is
// what decides which resolution vocabulary may answer an interaction: a
// permission resolution cannot settle a call, and a call resolution cannot
// settle a permission.
const interactionKindToolCall = "tool_call"

// pendingResolve is what one action.call.resolve.request left for its
// correlated response to settle: which arm it used, and the highest ladder
// reason the request satisfies — empty when the request is valid and must
// therefore be accepted.
type pendingResolve struct {
	index, line int
	interaction protocol.InteractionID
	run         protocol.RunID
	arm         string
	reason      protocol.ResolveReason
	// opaque marks a request the validator cannot judge the state of: a
	// recovered interaction whose opening is behind the cursor, or one whose
	// kind already failed. Nothing is required of the response's reason.
	opaque bool
}

// resolveReasonDiagnostic maps one ladder condition to the diagnostic an
// endpoint earns for ignoring it — by accepting a request the condition
// forbids, or by refusing under a reason that names a different condition.
//
// The mapping is the existing interaction vocabulary, not a new one: refusing
// or accepting past a settlement or a prior acknowledgement is the
// one-resolution rule (duplicate_interaction), a foreign sender is the
// responder rule (wrong_interaction_responder), and everything else is a
// resolution that answers no pending interaction.
func resolveReasonDiagnostic(reason protocol.ResolveReason) string {
	switch reason {
	case protocol.ReasonWrongResponder:
		return CodeWrongInteractionResponder
	case protocol.ReasonAlreadyResolved, protocol.ReasonRepeatedAcknowledgement:
		return CodeDuplicateInteraction
	default:
		return CodeUnmatchedInteraction
	}
}

// controlCallRequested opens the interaction a control-owned call is.
//
// A call whose execution_owner is not the declared control participant is not
// this unit's: the harness owns it, the observational lifecycle governs it,
// and nothing below applies. A trace with no protocol.initialize.request
// declares no control participant at all, and the unit stands down rather
// than guessing which id is the control layer's — envelopes carry no sender
// field, so there is nothing else to compare against.
func (s *state) controlCallRequested(i, line int, e protocol.Envelope, r *runState) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	if !s.controlOwned(p.ExecutionOwner) {
		return
	}
	// A control-owned call is an interaction, and an interaction the endpoint
	// opens without saying which one it is, or who may answer it, is one no
	// control layer can resolve.
	if p.InteractionID == "" || p.RespondedBy == "" {
		s.addExpected(CodeIllegalToolTransition, i, line, e, "/payload/interaction_id", "a call the control participant executes must open an interaction and name its responder", "interaction_id and responded_by", describeCallBinding(p), string(p.ToolCallID))
		return
	}
	s.participant(i, line, e, p.RespondedBy, "/payload/responded_by")
	if _, ok := r.interactions[p.InteractionID]; ok {
		s.add(CodeDuplicateInteraction, i, line, e, "/payload/interaction_id", "interaction id was requested more than once")
		return
	}
	opened := uint64(0)
	if e.Sequence != nil {
		opened = *e.Sequence
	}
	r.interactions[p.InteractionID] = &interactionState{
		kind: interactionKindToolCall, requestedBy: p.RequestedBy, respondedBy: p.RespondedBy,
		toolCallID: p.ToolCallID, openedAt: opened,
		resolveRequests: map[protocol.EnvelopeID]bool{}, settlements: map[protocol.EnvelopeID]bool{},
	}
	track := r.tools[p.ToolCallID]
	track.interaction = p.InteractionID
	r.tools[p.ToolCallID] = track
}

func describeCallBinding(p protocol.ActionCallPayload) string {
	return fmt.Sprintf("interaction_id=%s responded_by=%s", p.InteractionID, p.RespondedBy)
}

// controlOwned reports whether an execution owner is the declared control
// participant.
func (s *state) controlOwned(owner protocol.ParticipantID) bool {
	return s.controlParticipant != "" && owner == s.controlParticipant
}

// controlCallEvent judges the events a control-owned call's lifecycle is made
// of, each against the resolution that authorized it.
//
// Execution is never recorded on the endpoint's own initiative: action.call.started
// says the control participant has begun, and only an accepted resolution is
// evidence of that. A terminal is held to more — it must derive from an
// accepted result or error, and carry exactly what that resolution stated —
// because the authorization and the payload are separate facts and an adapter
// that forwarded something else to the harness would otherwise pass with an
// authorized terminal saying what nobody said.
//
// Cancellation is the one settlement that needs no resolution: a run cancel
// closes an unacknowledged call from `requested`, which the transition table
// already permits, and a harness-side timeout settles it without the control
// participant having spoken at all.
func (s *state) controlCallEvent(i, line int, e protocol.Envelope, r *runState, next string) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	x := s.callInteraction(r, p)
	if x == nil || !x.controlCall() || x.opaque {
		return
	}
	switch next {
	case "started":
		if !x.acked && x.acceptedArm == "" {
			s.addExpected(CodeIllegalToolTransition, i, line, e, "/type", "a control-owned call cannot start before an accepted resolution evidences it", "an accepted action.call.resolve.response", "no accepted resolution", string(p.ToolCallID))
			return
		}
		s.checkDerivedRequestID(i, line, e, p, x)
	case "completed", "failed":
		arm := protocol.ResolveArmResult
		if next == "failed" {
			arm = protocol.ResolveArmError
		}
		// The call terminated on the wire whatever authorized it, so it
		// leaves the pending set either way: settling it here is what keeps
		// one fault to one diagnosis instead of adding a
		// pending_interaction_at_terminal to every unauthorized terminal.
		defer s.settleControlCall(x, e.ID, e.Sequence)
		if x.acceptedArm != arm {
			s.addExpected(CodeIllegalToolTransition, i, line, e, "/type", "a control-owned call's terminal must derive from an accepted resolution of the matching arm", "an accepted "+arm+" resolution", describeAcceptedArm(x), string(p.ToolCallID))
			return
		}
		s.checkDerivedRequestID(i, line, e, p, x)
		s.checkResolutionPayload(i, line, e, p, x, arm)
	case "cancelled":
		s.settleControlCall(x, e.ID, e.Sequence)
	}
}

func describeAcceptedArm(x *interactionState) string {
	if x.acceptedArm == "" {
		return "no accepted resolution"
	}
	return "an accepted " + x.acceptedArm + " resolution"
}

// settleControlCall records that the call has left the pending set, and which
// envelope did it. The envelope joins the interaction's settlements, which is
// the set an already_resolved refusal's details.settlement_id must name.
func (s *state) settleControlCall(x *interactionState, id protocol.EnvelopeID, sequence *uint64) {
	x.settled = true
	x.resolved = true
	if x.settlements == nil {
		x.settlements = map[protocol.EnvelopeID]bool{}
	}
	x.settlements[id] = true
	if sequence != nil && x.resolvedAt == 0 {
		x.resolvedAt = *sequence
	}
}

// checkDerivedRequestID holds a resolve-derived event to naming the request it
// came from.
//
// Over HTTP the resolve response is a POST body and the event it releases
// travels the event stream, so a client sees them on two sockets and must
// hold the event until the call that authorized it returns. Holding on
// tool_call_id cannot work: two resolutions of one call can be outstanding at
// once — a result and its retry — so a client would either release on the
// wrong one's refusal or hold forever. The id therefore has to be on the
// event, and it has to be one the trace carries for this interaction, or the
// field could be omitted or invented and the client would be back where it
// started.
func (s *state) checkDerivedRequestID(i, line int, e protocol.Envelope, p protocol.ActionCallPayload, x *interactionState) {
	if p.RequestID == "" {
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/request_id", "a resolve-derived event must name the resolve request it came from", "a resolve request for the interaction", "absent", string(p.ToolCallID))
		return
	}
	if !x.resolveRequests[p.RequestID] {
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/request_id", "a resolve-derived event names no resolve request the trace carries for its interaction", "a resolve request for the interaction", string(p.RequestID), string(p.ToolCallID))
	}
}

// checkResolutionPayload compares a terminal with the resolution it was
// authorized by, as canonical JSON.
func (s *state) checkResolutionPayload(i, line int, e protocol.Envelope, p protocol.ActionCallPayload, x *interactionState, arm string) {
	if arm == protocol.ResolveArmResult {
		want, got := canonicalJSON(x.acceptedResult), canonicalJSON(p.Result)
		if want != got {
			s.addExpected(CodeResolutionPayloadMismatch, i, line, e, "/payload/result", "a control-owned call's completion carries a result the accepted resolution did not state", want, got, string(p.ToolCallID))
		}
		return
	}
	want, got := canonicalError(x.acceptedError), canonicalError(p.Error)
	if want != got {
		s.addExpected(CodeResolutionPayloadMismatch, i, line, e, "/payload/error", "a control-owned call's failure carries an error the accepted resolution did not state", want, got, string(p.ToolCallID))
	}
}

// canonicalJSON renders a raw payload in a form two encodings of one value
// agree on: Go marshals object members in sorted key order, so re-marshalling
// a decoded value normalizes member order and insignificant whitespace, which
// are the two differences a wire encoding may legitimately introduce.
func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "absent"
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return string(raw)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return string(raw)
	}
	return string(encoded)
}

func canonicalError(err *protocol.ProtocolError) string {
	if err == nil {
		return "absent"
	}
	encoded, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		return "unencodable error"
	}
	return canonicalJSON(encoded)
}

// callInteraction resolves the interaction one action-call event belongs to.
// The binding is the one the requested event established, so a later event
// that omits interaction_id is still judged, and one that names another
// interaction cannot move the call to it.
func (s *state) callInteraction(r *runState, p protocol.ActionCallPayload) *interactionState {
	if r == nil {
		return nil
	}
	if track, ok := r.tools[p.ToolCallID]; ok && track.interaction != "" {
		return r.interactions[track.interaction]
	}
	if p.InteractionID == "" {
		return nil
	}
	return r.interactions[p.InteractionID]
}

// controlResolveRequest retains what one resolution owes its response: which
// arm it used, and the highest condition on the ladder it satisfies.
func (s *state) controlResolveRequest(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallResolveRequest
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	if e.ToolCallID != "" && e.ToolCallID != p.ToolCallID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "envelope and payload tool_call_id differ", string(e.ToolCallID), string(p.ToolCallID))
	}
	s.feature(i, line, e, "tools")
	run := p.RunID
	if run == "" {
		run = e.RunID
	}
	pending := &pendingResolve{index: i, line: line, interaction: p.InteractionID, run: run, arm: p.Arm()}
	defer func() { s.pendingResolves[e.ID] = pending }()
	x := s.lookupInteraction(run, p.InteractionID)
	if x == nil {
		if r := s.runs[run]; r != nil && r.priorUnknown {
			// The run entered this trace through a recovery that said nothing
			// about what it was blocked on, so the interaction may have been
			// opened before the cursor. Unmatched here is what the validator
			// does not know.
			r.interactions[p.InteractionID] = &interactionState{opaque: true}
			pending.opaque = true
			return
		}
		pending.reason = protocol.ReasonUnknownInteraction
		return
	}
	if x.opaque {
		pending.opaque = true
		return
	}
	if !x.controlCall() {
		// A harness-owned call, a permission gate, or an input prompt. The
		// resolution vocabulary does not reach them, and the interaction's
		// own kind says so.
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/interaction_id", "a call resolution answers an interaction of another kind", interactionKindToolCall, x.kind, string(p.InteractionID))
		pending.opaque = true
		return
	}
	if x.resolveRequests == nil {
		x.resolveRequests = map[protocol.EnvelopeID]bool{}
	}
	x.resolveRequests[e.ID] = true
	if p.ToolCallID != x.toolCallID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "a call resolution names a tool call the interaction is not bound to", string(x.toolCallID), string(p.ToolCallID), string(p.InteractionID))
	}
	pending.reason = s.resolveLadder(p, x)
}

// resolveLadder is the ranked refusal the unit owns, stated once.
//
// A request can satisfy several conditions at once — a foreign responder
// sending a second acknowledgement is both wrong_responder and
// repeated_acknowledgement — and one response carries one reason, so the
// endpoint reports, and this requires, the highest the request satisfies.
// Each of the five names a condition the validator can observe, so a refusal
// is checkable rather than a free-form excuse:
//
//   - unknown_interaction: no such pending interaction on the run.
//   - wrong_responder: the sender is not the interaction's declared responder.
//   - already_resolved: the call is resolved — a terminal the trace carries,
//     from the endpoint's own derivation or from a harness-side timeout, or,
//     for a result or error arm, a resolution the endpoint has already
//     accepted. This is the one reason with a settlement envelope to point
//     at, which is why it is the one that carries settlement_id, and both
//     halves of the condition supply one: the terminal in the first case, the
//     accepted resolve response in the second.
//   - repeated_acknowledgement: an acknowledgement, and one was already
//     accepted. A repeat is the more specific diagnosis than the one below,
//     so it outranks it where both hold.
//   - late_acknowledgement: an acknowledgement arriving after the
//     participant's own result or error was accepted, but before the terminal
//     derived from it has been published. The call is not settled on the wire
//     yet, so the acknowledgement is simply too late to mean anything — the
//     resolution it would have preceded has already landed.
//
// An empty reason means the request is valid, and a valid request must be
// accepted: without that an endpoint could refuse the one correct resolution
// with any reason at all, emit nothing, and let a later cancellation settle
// the call and the run so the trace passed.
//
// Two properties are load-bearing and neither is self-evident, so both are
// stated where the ladder is. Each reason must be reachable *as the highest*,
// or the vocabulary carries a name nothing can produce. And each must have a
// conforming refusal in every window it can fire, or an endpoint is required
// to report a reason it cannot legally report — which is what happened in the
// window between an accepted resolution and its terminal, where the reason
// demanded an id that only the terminal could have supplied.
//
// The windows, exhaustively, for an interaction the sender owns:
//
//	acceptedArm  settled  arm          highest reason            names
//	—            false    any          (valid: must be accepted)  —
//	—            false    started+ack  repeated_acknowledgement   nothing
//	set          false    started      late_acknowledgement       nothing
//	set          false    result/err   already_resolved           the acceptance
//	any          true     any          already_resolved           the terminal
//
// A foreign sender outranks all of them with wrong_responder, which names
// nothing, and an absent interaction outranks that with unknown_interaction,
// which names nothing either.
func (s *state) resolveLadder(p protocol.ActionCallResolveRequest, x *interactionState) protocol.ResolveReason {
	var conditions []protocol.ResolveReason
	if p.RespondedBy != x.respondedBy || (p.RequestedBy != "" && p.RequestedBy != x.requestedBy) {
		conditions = append(conditions, protocol.ReasonWrongResponder)
	}
	acknowledgement := p.Arm() == protocol.ResolveArmAcknowledge
	switch {
	case x.settled:
		conditions = append(conditions, protocol.ReasonAlreadyResolved)
	case x.acceptedArm == "":
	case acknowledgement:
		conditions = append(conditions, protocol.ReasonLateAcknowledgement)
	default:
		// A second result or error while the first is accepted and its
		// terminal has not yet been published. The call is resolved, and
		// already_resolved is what says so; late_acknowledgement is about
		// acknowledgements alone.
		conditions = append(conditions, protocol.ReasonAlreadyResolved)
	}
	if acknowledgement && x.acked {
		conditions = append(conditions, protocol.ReasonRepeatedAcknowledgement)
	}
	return protocol.HighestResolveReason(conditions...)
}

// controlResolveResponse settles one resolution: whether it could be accepted
// at all, and, when it could not, whether the refusal names the condition the
// validator observes.
func (s *state) controlResolveResponse(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallResolveResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	if e.ToolCallID != "" && e.ToolCallID != p.ToolCallID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "envelope and payload tool_call_id differ", string(e.ToolCallID), string(p.ToolCallID))
	}
	s.feature(i, line, e, "tools")
	pending := s.pendingResolves[e.InReplyTo]
	if pending == nil || pending.opaque {
		return
	}
	x := s.lookupInteraction(pending.run, pending.interaction)
	if x == nil {
		// The request named no pending interaction, so unknown_interaction is
		// the only answer it can have. There is no interaction state to
		// record against, and the rules below all read one, so the two ways
		// of getting this wrong are judged here: accepting it, and refusing
		// it as though the interaction existed and was in some other state.
		switch {
		case p.Accepted:
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/accepted", "a resolution of no pending interaction was accepted", "a refusal reporting "+string(protocol.ReasonUnknownInteraction), "accepted", string(e.InReplyTo))
		case p.Reason != protocol.ReasonUnknownInteraction:
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/reason", "a refusal names a condition other than the highest one the request satisfies", string(protocol.ReasonUnknownInteraction), string(p.Reason), string(e.InReplyTo))
		}
		return
	}
	if x.opaque {
		return
	}
	if p.Accepted {
		if pending.reason != "" {
			s.addExpected(resolveReasonDiagnostic(pending.reason), i, line, e, "/payload/accepted", "a resolution the interaction's state forbids was accepted", "a refusal reporting "+string(pending.reason), "accepted", string(e.InReplyTo))
		}
		// Recorded even when the acceptance was wrong, because the endpoint
		// did accept it and the events that follow are consistent with that
		// acceptance. Standing the record down would convict the endpoint a
		// second time, on the terminal, for the one fault already named here.
		s.acceptResolution(e, pending, x)
		return
	}
	if pending.reason == "" {
		// The request was correctly scoped, from the bound responder, and the
		// first of its arm for a pending interaction. Nothing about the
		// interaction's state justifies refusing it.
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/accepted", "a valid resolution was refused", "accepted", "refused with "+string(p.Reason), string(e.InReplyTo))
		return
	}
	if p.Reason != pending.reason {
		s.addExpected(resolveReasonDiagnostic(pending.reason), i, line, e, "/payload/reason", "a refusal names a condition other than the highest one the request satisfies", string(pending.reason), string(p.Reason), string(e.InReplyTo))
		return
	}
	if p.Reason == protocol.ReasonAlreadyResolved {
		settlement := protocol.EnvelopeID("")
		if p.Details != nil {
			settlement = p.Details.SettlementID
		}
		if !x.settlements[settlement] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/details/settlement_id", "an already_resolved refusal names an envelope that settled nothing for its interaction", describeIDStrings(settlementIDs(x)), string(settlement), string(e.InReplyTo))
		}
	}
}

// acceptResolution records what an accepted resolution binds. An
// acknowledgement leaves the call pending and only evidences execution; a
// result or error settles it, and what it stated is what the terminal must
// carry.
func (s *state) acceptResolution(e protocol.Envelope, pending *pendingResolve, x *interactionState) {
	var p protocol.ActionCallResolveRequest
	if req := s.requests[e.InReplyTo]; req != nil {
		_ = req.envelope.DecodePayload(&p)
	}
	if x.settlements == nil {
		x.settlements = map[protocol.EnvelopeID]bool{}
	}
	switch pending.arm {
	case protocol.ResolveArmAcknowledge:
		x.acked = true
	case protocol.ResolveArmResult, protocol.ResolveArmError:
		if pending.arm == protocol.ResolveArmResult {
			x.acceptedArm, x.acceptedResult = protocol.ResolveArmResult, p.Result
		} else {
			x.acceptedArm, x.acceptedError = protocol.ResolveArmError, p.Error
		}
		// The acceptance settles the call, and is therefore an envelope a
		// later already_resolved refusal may name — which it has to be, or
		// the window between an accepted resolution and the terminal derived
		// from it has no conforming refusal at all: the reason is required,
		// the reason requires an id, and the terminal that would supply one
		// has not been published yet. That window is the result-and-retry
		// interleaving request_id exists for, so it is the last one that may
		// be left without an answer.
		//
		// It does not settle the call *on the wire*, which is a separate
		// fact: x.settled stays false until a terminal event is published,
		// because that is what decides whether a later acknowledgement is
		// already_resolved or late_acknowledgement.
		x.settlements[e.ID] = true
	}
}

func settlementIDs(x *interactionState) []string {
	ids := make([]string, 0, len(x.settlements))
	for id := range x.settlements {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	return ids
}

func describeIDStrings(ids []string) string {
	if len(ids) == 0 {
		return "no settlement"
	}
	joined := ids[0]
	for _, id := range ids[1:] {
		joined += "," + id
	}
	return joined
}

// checkCallOwner holds a call's execution_owner to the owner the catalog in
// force records for its name.
//
// The lifecycle rule in tool() already refuses an owner that changes
// mid-call; this is the other half, and the one that matters once tools have
// two possible owners: an adapter must not route a harness-owned tool to the
// control participant, which would make the run wait forever for a resolution
// nobody owes, nor a provided tool to the harness, which would execute
// something the control layer was supposed to.
func (s *state) checkCallOwner(i, line int, e protocol.Envelope) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	if p.Name == "" {
		return
	}
	owner, known := s.ownerInForce(s.sessions[p.SessionID], p.Name)
	if !known || owner == p.ExecutionOwner {
		return
	}
	s.addExpected(CodeWrongToolOwner, i, line, e, "/payload/execution_owner", "a call names an execution owner other than the one the catalog in force records for the tool", string(owner), string(p.ExecutionOwner), p.Name)
}

// ownerInForce resolves which participant owns one tool's execution.
//
// The open's provided tools come first and are not superseded by a list: they
// are session-lifetime facts, provisioning is all-or-nothing, and a catalog
// that listed one under another owner is already catalog_mismatch. Otherwise
// it is the session's served catalog under the active revision, and failing
// that the descriptor's own — the same precedence attributionInForce states
// for a tool's source, for the same reasons.
func (s *state) ownerInForce(track *sessionTrack, name string) (protocol.ParticipantID, bool) {
	if track != nil {
		if tool, ok := track.provided[name]; ok {
			return tool.ExecutionOwner, true
		}
		if track.toolCatalog != nil && track.toolCatalog.revision == s.currentCapability {
			owner, ok := track.toolCatalog.owners[name]
			return owner, ok
		}
	}
	owner, ok := s.descriptorOwners[name]
	return owner, ok
}

// provideExpectations judges one open's `tools` array and returns what the
// open owes: the refusals its defects require, the refusal a disclosed-limit
// violation permits, and whether the array is one the endpoint must honour.
//
// Every defect here is one the validator can see in the request, which is
// what makes the corresponding refusal checkable. A dangling source cannot be
// attributed or routed; a foreign owner names a participant that never
// provided the tool; a colliding name leaves the catalog unable to resolve
// either entry. Provisioning is whole or not at all, so any of them refuses
// the open rather than dropping an entry.
func (s *state) provideExpectations(p protocol.SessionOpenRequest) (defects []*controlExpectation, limit *controlExpectation, honour bool) {
	key := protocol.FeatureToolsProvide
	support := s.featureDetail(key)
	level := s.features[key]
	unsatisfiable := func(pointer, detailName, detailValue, diagnostic, message string) *controlExpectation {
		return &controlExpectation{
			rung: rungUnsatisfiable, key: key, pointer: pointer,
			code: errorUnsupportedFeature, reason: reasonUnsatisfiable,
			detailName: detailName, detailValue: detailValue,
			diagnostic: diagnostic, message: message,
		}
	}
	if !affirmative(level) {
		return []*controlExpectation{{
			rung: rungCapability, key: key, pointer: "/payload/tools",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: key,
			diagnostic: CodeUnavailableCapability,
			message:    "an open supplies control-layer tools to an endpoint that has not affirmatively advertised provisioning",
		}}, nil, false
	}
	if level == protocol.SupportDegraded && !p.AllowsDegraded(key) {
		return []*controlExpectation{{
			rung: rungDegradation, key: key, pointer: "/payload/tools",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: key,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "an open supplies control-layer tools under a degraded capability without the caller's opt-in",
		}}, nil, false
	}
	// The sources a supplied tool may name: the ones this open attaches and
	// the ones the active descriptor declares. Nothing else exists yet — the
	// session has no catalog before it is open.
	attached := map[string]bool{}
	for _, attachment := range p.ToolSources {
		attached[attachment.ID] = true
	}
	native := map[string]bool{}
	if s.catalogKnown {
		for _, name := range s.catalog {
			native[name] = true
		}
	}
	seen := map[string]bool{}
	for index, tool := range p.Tools {
		pointer := fmt.Sprintf("/payload/tools/%d", index)
		if !s.ownedByOpener(tool.ExecutionOwner) {
			defects = append(defects, unsatisfiable(pointer+"/execution_owner", "tool", tool.Name, CodeWrongToolOwner,
				"an open supplies a tool whose execution owner is not the opening participant"))
		}
		if seen[tool.Name] || native[tool.Name] {
			defects = append(defects, unsatisfiable(pointer+"/name", "tool", tool.Name, CodeDuplicateToolName,
				"an open supplies a tool under a name the session's catalog already resolves"))
		}
		seen[tool.Name] = true
		if tool.Source == "" {
			continue
		}
		if _, declared := s.declaredSources[tool.Source]; !declared && !attached[tool.Source] {
			defects = append(defects, unsatisfiable(pointer+"/source", "source", tool.Source, CodeUnmatchedToolSource,
				"an open supplies a tool naming a source neither the descriptor nor the same open declares"))
		}
	}
	if len(defects) > 0 {
		return defects, nil, false
	}
	if violation := provideLimitViolation(support, p.Tools); violation != nil {
		return nil, violation, false
	}
	return nil, nil, true
}

// ownedByOpener reports whether a supplied tool's execution owner is the
// participant that opened the session.
//
// A trace that never declared a control participant cannot be held to this:
// there is no identity to compare against, and convicting every such open
// would diagnose the absence of an initialize exchange rather than the tool.
func (s *state) ownedByOpener(owner protocol.ParticipantID) bool {
	return s.controlParticipant == "" || owner == s.controlParticipant
}

// provideLimitViolation names the first limit a `tools` array puts outside
// what the endpoint disclosed, and nil when it violates none.
//
// The bound has to exist for the capability to promise anything. A native name
// shape, a schema dialect, a cardinality ceiling — any of these can make a
// well-formed array unprovisionable, and an adapter permitted to say so about
// any array could advertise the key, refuse everything, and pass conformance
// while honouring nothing. So a constraint is exercisable only where it is
// advertised, and refusing an array that satisfies every advertised limit and
// carries no defect any rule names is undisclosed_provide_limit.
func provideLimitViolation(support protocol.FeatureSupport, tools []protocol.ToolDefinition) *controlExpectation {
	refusal := func(pointer, tool, message string) *controlExpectation {
		return &controlExpectation{
			rung: rungUnsatisfiable, key: protocol.FeatureToolsProvide, pointer: pointer,
			code: errorUnsupportedFeature, reason: reasonUnsatisfiable,
			detailName: "tool", detailValue: tool,
			diagnostic: CodeUnavailableCapability, message: message,
		}
	}
	var violations []*controlExpectation
	if max, ok := support.MaxTools(); ok && len(tools) > max {
		// The entry that carries the array past the ceiling is the one a
		// caller drops to get under it.
		violations = append(violations, refusal(
			fmt.Sprintf("/payload/tools/%d", max), tools[max].Name,
			"an open supplies more tools than the endpoint disclosed it accepts",
		))
	}
	if pattern, ok := support.NamePattern(); ok {
		if expression, err := regexp.Compile(pattern); err == nil {
			for index, tool := range tools {
				if !expression.MatchString(tool.Name) {
					violations = append(violations, refusal(
						fmt.Sprintf("/payload/tools/%d/name", index), tool.Name,
						"an open supplies a tool whose name is outside the shape the endpoint disclosed",
					))
				}
			}
		}
	}
	if dialect, ok := support.SchemaDialect(); ok {
		for index, tool := range tools {
			declared, stated := schemaDialect(tool.InputSchema)
			if stated && declared != dialect {
				violations = append(violations, refusal(
					fmt.Sprintf("/payload/tools/%d/input_schema", index), tool.Name,
					"an open supplies a tool whose input schema declares a dialect the endpoint did not disclose",
				))
			}
		}
	}
	if len(violations) == 0 {
		return nil
	}
	sort.SliceStable(violations, func(a, b int) bool { return violations[a].less(violations[b]) })
	return violations[0]
}

// schemaDialect reports the `$schema` an input schema declares, and whether it
// declared one at all. A schema that names none elects the endpoint's, so only
// one that names a different dialect is outside a disclosed limit: an endpoint
// cannot disclose a dialect and then refuse every array that did not repeat it.
func schemaDialect(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var document struct {
		Schema string `json:"$schema"`
	}
	if json.Unmarshal(raw, &document) != nil || document.Schema == "" {
		return "", false
	}
	return document.Schema, true
}

// recordProvidedTools keeps an admitted open's supplied definitions for the
// session's lifetime, so a later catalog, a later call, and every capability
// refresh are all judged against what was actually provisioned.
func (s *state) recordProvidedTools(track *sessionTrack, tools []protocol.ToolDefinition) {
	if len(tools) == 0 {
		return
	}
	if track.provided == nil {
		track.provided = map[string]protocol.ToolDefinition{}
	}
	for _, tool := range tools {
		if _, ok := track.provided[tool.Name]; !ok {
			track.providedOrder = append(track.providedOrder, tool.Name)
		}
		track.provided[tool.Name] = tool
	}
}

// checkCatalogProvided holds a session-scoped catalog to listing every tool
// the open provided, exactly as it was supplied.
//
// The comparison is of the whole entry rather than the name, for the reason
// the attached-source comparison is: a redirected schema or a tool moved to
// another source changes how it is routed and attributed just as an omission
// does, and provisioning is for the session's lifetime. `features` is the
// adapter's to fill, so it is not compared.
func (s *state) checkCatalogProvided(i, line int, e protocol.Envelope, session protocol.SessionID, track *sessionTrack, listed []protocol.ToolDefinition) {
	if track == nil || len(track.providedOrder) == 0 {
		return
	}
	published := make(map[string]protocol.ToolDefinition, len(listed))
	for _, tool := range listed {
		if _, seen := published[tool.Name]; !seen {
			published[tool.Name] = tool
		}
	}
	for _, name := range track.providedOrder {
		supplied := track.provided[name]
		got, ok := published[name]
		switch {
		case !ok:
			s.addExpected(CodeCatalogMismatch, i, line, e, "/payload/tools", "a session catalog omits a tool the open provided", name, "absent", string(session))
		case !describesProvidedTool(supplied, got):
			s.addExpected(CodeCatalogMismatch, i, line, e, "/payload/tools", "a session catalog describes a provided tool differently", describeTool(supplied), describeTool(got), string(session))
		}
	}
}

// describesProvidedTool reports whether a listed entry is the supplied one:
// the same description, schema, owner, and source, present or absent exactly
// as supplied.
func describesProvidedTool(supplied, listed protocol.ToolDefinition) bool {
	return supplied.Description == listed.Description &&
		supplied.ExecutionOwner == listed.ExecutionOwner &&
		supplied.Source == listed.Source &&
		canonicalJSON(supplied.InputSchema) == canonicalJSON(listed.InputSchema)
}

func describeTool(tool protocol.ToolDefinition) string {
	return fmt.Sprintf("%s owner=%s source=%s input_schema=%s", tool.Name, tool.ExecutionOwner, tool.Source, canonicalJSON(tool.InputSchema))
}

// checkRefreshAgainstProvided reruns the provisioning checks on every
// revision-changing descriptor.
//
// A post-refresh list is not mandatory, so a refreshed descriptor whose native
// tools collide with a provided one, or which stops declaring a source a
// provided tool references, would otherwise leave the session with an
// ambiguous catalog nobody ever looks at. An adapter whose refresh would
// introduce a colliding native tool namespaces it or keeps it out of the
// session's catalog; it never shadows a provided tool.
func (s *state) checkRefreshAgainstProvided(i, line int, e protocol.Envelope, declared map[string]protocol.ToolSourceDescriptor, native map[string]bool) {
	for _, id := range s.sessionIDsInOrder() {
		track := s.sessions[id]
		for _, name := range track.providedOrder {
			if native[name] {
				s.addExpected(CodeDuplicateToolName, i, line, e, "/payload/tools", "a refreshed descriptor declares a native tool under a name an open session provided", "one tool per name", name, string(id))
			}
			source := track.provided[name].Source
			if source == "" {
				continue
			}
			if _, ok := declared[source]; ok {
				continue
			}
			if _, attached := track.attached[source]; attached {
				continue
			}
			s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/sources", "a refreshed descriptor no longer declares a source a provided tool references", source, "absent", string(id), name)
		}
	}
}

// checkEntryAcknowledged judges an active_runs entry's acknowledged subset.
//
// It is held to the same standard as the list it subsets: every id in it must
// be one the entry itself lists as pending, and must have an accepted
// acknowledgement the trace carries. Acknowledgement is ordered by the trace
// rather than by the run's sequence domain, because a resolve response
// consumes no sequence and so has no position in it; what the entry's
// as_of_sequence anchors is the pending set, and the acknowledged subset is
// read at the snapshot itself.
func (s *state) checkEntryAcknowledged(i, line int, e protocol.Envelope, pointer string, entry protocol.ActiveRun, r *runState, want map[protocol.InteractionID]bool) {
	listed := map[protocol.InteractionID]bool{}
	for _, id := range entry.PendingInteractions {
		listed[id] = true
	}
	acknowledged := map[protocol.InteractionID]bool{}
	for _, id := range entry.AcknowledgedInteractions {
		acknowledged[id] = true
		if !listed[id] {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/acknowledged_interactions", "an acknowledged interaction is not among the entry's pending interactions", describeIDs(listed), string(id), string(r.id))
			continue
		}
		if x := r.interactions[id]; x == nil || (!x.opaque && !x.acked) {
			s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/acknowledged_interactions", "an entry reports an acknowledgement the trace never saw accepted", "an accepted started acknowledgement", string(id), string(r.id))
		}
	}
	if want == nil {
		return
	}
	for id := range want {
		x := r.interactions[id]
		if x == nil || x.opaque || !x.acked || acknowledged[id] {
			continue
		}
		s.addExpected(CodeSessionStateMismatch, i, line, e, pointer+"/acknowledged_interactions", "an entry omits an interaction whose acknowledgement the endpoint accepted", string(id), describeIDs(acknowledged), string(r.id))
	}
}
