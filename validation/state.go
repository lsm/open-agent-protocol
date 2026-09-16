package validation

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
)

type runState struct {
	id                          protocol.RunID
	session                     protocol.SessionID
	admitted, started, terminal bool
	terminalType                protocol.EnvelopeType
	next                        uint64
	lastIndex, lastLine         int
	cancelAccepted              bool
	status                      protocol.RunStatus
	admittedModel               string
	// opaqueAdmission marks a run admitted under a foreign admission in
	// tolerant mode. Which lifecycle it follows is a later revision's rule,
	// so the admission-dependent check — the pre-start rule — is suspended,
	// while sequence, terminality, and scope bookkeeping stay.
	opaqueAdmission bool
	tools           map[protocol.ToolCallID]toolTrack
	interactions    map[protocol.InteractionID]*interactionState
}
type interactionState struct {
	kind                     string
	requestedBy, respondedBy protocol.ParticipantID
	allowCancel              bool
	choices                  map[string]bool
	questions                []protocol.InputQuestion
	toolCallID               protocol.ToolCallID
	resolved                 bool
}
type recoveryExpectation struct {
	session         protocol.SessionID
	run             protocol.RunID
	gap             bool
	openIndex       int
	stateSeen       bool
	stateChecked    bool
	cursor          uint64
	cursorSet       bool
	firstReplaySeen bool
}
type requestState struct {
	typ                protocol.EnvelopeType
	responded          bool
	index, line        int
	envelope           protocol.Envelope
	capabilityRevision string
	session            protocol.SessionID
	run                protocol.RunID
	interaction        protocol.InteractionID
}
type sessionTrack struct {
	status protocol.SessionStatus
	active protocol.RunID
}

// toolTrack retains a tool call's lifecycle status and the execution owner that
// opened it; the owner must not change mid-lifecycle.
type toolTrack struct {
	status string
	owner  protocol.ParticipantID
}
type state struct {
	fixture           string
	diagnostics       []Diagnostic
	ids               map[protocol.EnvelopeID]int
	requests          map[protocol.EnvelopeID]*requestState
	participants      map[protocol.ParticipantID]bool
	sessions          map[protocol.SessionID]*sessionTrack
	runs              map[protocol.RunID]*runState
	currentCapability string
	capabilitiesStale bool
	initialized       bool
	features          map[string]protocol.SupportLevel
	recoveries        map[protocol.SessionID]*recoveryExpectation
	// tolerant lets an envelope of unknown type take part in the
	// type-independent bookkeeping its wire scope implies, instead of being
	// skipped. Without it a tolerated unknown run event at sequence N would be
	// ignored and the next known event at N+1 diagnosed as sequence_gap.
	tolerant bool
	// packs is the loaded extension vocabulary. A packed type resolves its
	// role and its feature key through the pack that declared it; the rules
	// themselves are the core ones, in packstate.go.
	packs *PackSet
}

func newState(f string) *state {
	return &state{fixture: f, ids: map[protocol.EnvelopeID]int{}, requests: map[protocol.EnvelopeID]*requestState{}, participants: map[protocol.ParticipantID]bool{}, sessions: map[protocol.SessionID]*sessionTrack{}, runs: map[protocol.RunID]*runState{}, recoveries: map[protocol.SessionID]*recoveryExpectation{}, features: map[string]protocol.SupportLevel{}}
}
func (s *state) add(code string, i, line int, e protocol.Envelope, ptr, msg string) {
	s.diagnostics = append(s.diagnostics, baseDiagnostic(s.fixture, PhaseSemantic, code, i, line, e, ptr, msg))
}
func (s *state) addExpected(code string, i, line int, e protocol.Envelope, ptr, msg, expected, actual string, related ...string) {
	d := baseDiagnostic(s.fixture, PhaseSemantic, code, i, line, e, ptr, msg)
	d.Expected = expected
	d.Actual = actual
	d.RelatedIDs = related
	s.diagnostics = append(s.diagnostics, d)
}

func (s *state) apply(i, line int, e protocol.Envelope) {
	if first, ok := s.ids[e.ID]; ok {
		s.addExpected(CodeDuplicateEnvelopeID, i, line, e, "/id", "envelope id must be unique", "unique id", string(e.ID), fmt.Sprintf("index:%d", first))
		return
	}
	s.ids[e.ID] = i
	if s.isRequestType(e.Type) {
		session, run := requestScope(e)
		if s.envelopeScoped(e.Type) {
			// An unknown request has no payload decoder to read scope from,
			// but its envelope scope is the wire's own and is retained so the
			// generic correlation checks still bind its response: a request
			// on run A answered on run B is a scope_mismatch whatever the
			// operation is called.
			session, run = e.SessionID, e.RunID
		}
		// A request that declares scope in both its envelope and payload must
		// agree; otherwise its stored correlation scope is self-contradictory.
		s.checkScope(i, line, e, session, run)
		s.requests[e.ID] = &requestState{typ: e.Type, index: i, line: line, envelope: e, capabilityRevision: string(e.CapabilityRevision), session: session, run: run, interaction: envelopeInteraction(e)}
	}
	duplicateResponse := false
	if s.isResponseType(e.Type) {
		duplicateResponse = !s.response(i, line, e)
	}
	// capabilities.updated is the one operation that legitimately carries a
	// revision different from the active one: it introduces the next revision.
	// Its own case below validates continuity and novelty, so the generic
	// stale check must not reject it here (a transition could otherwise never
	// be accepted).
	if e.CapabilityRevision != "" && s.currentCapability != "" && string(e.CapabilityRevision) != s.currentCapability && e.Type != protocol.TypeProtocolInitializeRequest && e.Type != protocol.TypeCapabilitiesRequest && e.Type != protocol.TypeCapabilitiesUpdated && e.Type != protocol.TypeErrorResponse {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "operation uses a stale capability revision", s.currentCapability, e.CapabilityRevision)
	}
	if duplicateResponse {
		// The envelope is already rejected as a duplicate; interpreting its
		// payload again would only pile cascading diagnostics onto it.
		return
	}
	s.packEnvelope(i, line, e)
	switch e.Type {
	case protocol.TypeProtocolInitializeRequest:
		var p protocol.InitializeRequest
		_ = e.DecodePayload(&p)
		s.initialized = true
		if p.Participant != nil {
			s.participants[p.Participant.ID] = true
		}
	case protocol.TypeProtocolInitializeResponse:
		var p protocol.InitializeResponse
		_ = e.DecodePayload(&p)
		s.initialized = true
		s.participants[protocol.ParticipantID(p.Endpoint.ID)] = true
		if req := s.requests[e.InReplyTo]; req != nil {
			var request protocol.InitializeRequest
			_ = req.envelope.DecodePayload(&request)
			if request.Participant != nil {
				s.participants[request.Participant.ID] = true
			}
			// A negotiation must select from what the request offered; a response
			// naming an unoffered version or profile proves no mutual capability.
			if len(request.ProtocolVersions) > 0 && !slices.Contains(request.ProtocolVersions, p.ProtocolVersion) {
				s.addExpected(CodeScopeMismatch, i, line, e, "/payload/protocol_version", "initialize response selects a protocol version the request did not offer", strings.Join(request.ProtocolVersions, ","), p.ProtocolVersion, string(e.InReplyTo))
			}
			if len(request.Profiles) > 0 && !slices.Contains(request.Profiles, p.Profile) {
				s.addExpected(CodeScopeMismatch, i, line, e, "/payload/profile", "initialize response selects a profile the request did not offer", strings.Join(request.Profiles, ","), p.Profile, string(e.InReplyTo))
			}
		}
	case protocol.TypeCapabilitiesResponse:
		var p protocol.CapabilitiesResponse
		_ = e.DecodePayload(&p)
		s.currentCapability = e.CapabilityRevision
		s.capabilitiesStale = false
		s.features = map[string]protocol.SupportLevel{}
		collectFeatures(s.features, p.Features)
		for _, layer := range p.Layers {
			collectFeatures(s.features, layer.Features)
		}
	case protocol.TypeCapabilitiesUpdated:
		var p protocol.CapabilitiesUpdated
		_ = e.DecodePayload(&p)
		if s.currentCapability != "" && p.PreviousRevision != s.currentCapability {
			s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/payload/previous_revision", "capability update does not continue the active revision", s.currentCapability, p.PreviousRevision)
		}
		if string(e.CapabilityRevision) == p.PreviousRevision {
			s.add(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "capability update must introduce a new revision")
		}
		s.currentCapability = e.CapabilityRevision
		s.capabilitiesStale = true
	case protocol.TypeSessionOpenResponse:
		var p protocol.SessionOpenResponse
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, "")
		if p.Recovery != nil && p.Recovery.Reason == "replay_gap" && p.Recovery.ResumeCursor == "" {
			s.add(CodeUndeclaredReplayGap, i, line, e, "/payload/recovery/resume_cursor", "replay gap lacks an explicit retained boundary cursor")
		}
		if p.Recovery != nil && p.Recovery.Recovered {
			recovery := &recoveryExpectation{session: p.SessionID, run: p.Recovery.PreviousRunID, gap: p.Recovery.Reason == "replay_gap", openIndex: i}
			if p.Recovery.ResumeCursor != "" {
				cursor, err := strconv.ParseUint(p.Recovery.ResumeCursor, 10, 64)
				if err != nil {
					s.add(CodeUndeclaredReplayGap, i, line, e, "/payload/recovery/resume_cursor", "recovery cursor must be an unsigned sequence")
				} else {
					recovery.cursor = cursor
					recovery.cursorSet = true
				}
			}
			s.recoveries[p.SessionID] = recovery
		}
		// Reopening a known session must not clear a tracked nonterminal run:
		// that would let a later admission overlap it unseen.
		st := s.sessions[p.SessionID]
		if st == nil {
			st = &sessionTrack{}
			s.sessions[p.SessionID] = st
		}
		st.status = p.Status
	case protocol.TypeSessionStateResponse, protocol.TypeSessionStateUpdated:
		var p protocol.SessionState
		_ = e.DecodePayload(&p)
		if rec := s.recoveries[p.SessionID]; rec != nil && !rec.stateChecked {
			rec.stateChecked = true
			rec.stateSeen = true
			if rec.gap && p.Recovery == nil {
				s.add(CodeUndeclaredReplayGap, i, line, e, "/payload/recovery", "authoritative state after a replay gap must retain recovery metadata")
			}
			if !rec.gap && p.ActiveRunID != "" {
				if rec.run != "" && p.ActiveRunID != rec.run {
					s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "recovered state names a different active run", string(rec.run), string(p.ActiveRunID))
				}
				run := s.runs[p.ActiveRunID]
				if run == nil {
					next := uint64(1)
					if rec.cursorSet {
						next = rec.cursor + 1
					}
					s.runs[p.ActiveRunID] = &runState{id: p.ActiveRunID, session: p.SessionID, admitted: true, started: true, next: next, lastIndex: i, lastLine: line, tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: protocol.RunRunning}
				}
			}
		}
		s.checkScope(i, line, e, p.SessionID, p.ActiveRunID)
		st := s.sessions[p.SessionID]
		if st == nil {
			st = &sessionTrack{}
			s.sessions[p.SessionID] = st
		}
		if p.ActiveRunID != "" && p.Status == protocol.SessionIdle {
			s.add(CodeSessionStateMismatch, i, line, e, "/payload/status", "idle session cannot have an active run")
		}
		// A snapshot may not erase a run the trace has admitted and not terminated:
		// that would allow an overlapping second admission on one session. Keep the
		// tracked run when the snapshot contradicts it.
		contradiction := false
		if prev := st.active; prev != "" {
			if r := s.runs[prev]; r != nil && !r.terminal && p.ActiveRunID != prev {
				contradiction = true
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "snapshot contradicts a nonterminal active run", string(prev), string(p.ActiveRunID), string(prev))
			}
		}
		st.status = p.Status
		if !contradiction {
			st.active = p.ActiveRunID
		}
	case protocol.TypeSessionMessageSubmitRequest:
		var p protocol.MessageSubmitRequest
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, "")
		if s.capabilitiesStale {
			s.add(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "submission occurred before refreshed capabilities")
		}
		if p.Delivery != protocol.DeliveryAuto && !(s.tolerant && foreignRequestedDelivery(p.Delivery)) {
			// A requested delivery outside this revision's vocabulary is
			// opaque in tolerant mode: which capability key it needs is a
			// later revision's rule, not one this validator can apply.
			s.feature(i, line, e, "delivery."+string(p.Delivery))
		}
	case protocol.TypeSessionMessageSubmitResponse:
		s.submitResponse(i, line, e)
	case protocol.TypeRunCancelRequest:
		var p protocol.RunCancelRequest
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		if r := s.runs[p.RunID]; r == nil {
			s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "cannot cancel an unknown run")
		} else if r.terminal && r.terminalType != protocol.TypeRunCancelled {
			s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "cannot cancel a completed or failed run")
		}
	case protocol.TypeActionToolsListRequest:
		s.feature(i, line, e, "tools")
	case protocol.TypeActionToolsListResponse:
		s.feature(i, line, e, "tools")
	case protocol.TypeActionPermissionResolveRequest:
		var p protocol.PermissionResolveRequest
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		s.feature(i, line, e, "permissions")
		s.interactionResolutionRequest(i, line, e, "permission")
	case protocol.TypeActionPermissionResolveResponse:
		var p protocol.PermissionResolveResponse
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		s.feature(i, line, e, "permissions")
	case protocol.TypeUserInputResolveRequest:
		var p protocol.UserInputResolveRequest
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		s.feature(i, line, e, "user_input")
		s.interactionResolutionRequest(i, line, e, "input")
	case protocol.TypeUserInputCancelRequest:
		var p protocol.UserInputCancelRequest
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		s.feature(i, line, e, "user_input")
		s.interactionResolutionRequest(i, line, e, "input")
	case protocol.TypeUserInputResolveResponse, protocol.TypeUserInputCancelResponse:
		var p protocol.UserInputResolveResponse
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		s.feature(i, line, e, "user_input")
	case protocol.TypeRunCancelResponse:
		var p protocol.RunCancelResponse
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		if p.Accepted {
			r := s.runs[p.RunID]
			if r == nil || (r.terminal && r.terminalType != protocol.TypeRunCancelled) {
				s.add(CodeIllegalRunTransition, i, line, e, "/payload/accepted", "cancellation cannot be accepted for an unknown, completed, or failed run")
			} else if !r.terminal {
				r.cancelAccepted = true
				r.status = protocol.RunCancelling
				if s.tolerant && foreignRunStatus(p.Status) {
					// The response declared a status this revision does not
					// know. It is recorded as opaque, as a foreign status from
					// run.status.updated is, so the first known step out of it
					// is not judged as a step out of cancelling. The accepted
					// exchange still stands as evidence for run.cancelled.
					r.status = p.Status
				}
			}
		}
	default:
		if isRunEvent(e.Type) {
			s.runEvent(i, line, e)
		} else if s.tolerant && !isKnownType(e.Type) && e.RunID != "" && e.Sequence != nil {
			// Tolerant mode classifies an unknown type by its wire scope: an
			// envelope carrying both run_id and sequence is a run-scoped event
			// and enters the type-independent bookkeeping (scope agreement,
			// an accepted admission, sequence contiguity, terminality). One
			// carrying session_id alone is session-scoped and advances no
			// cursor; one carrying neither touches no bookkeeping at all.
			s.runEvent(i, line, e)
		}
	}
}

// isKnownType reports whether the type is one this revision defines. The
// payload table is the authority: every known type has a decode target.
func isKnownType(t protocol.EnvelopeType) bool {
	return payloadTarget(t) != nil
}
func collectFeatures(dst map[string]protocol.SupportLevel, src map[string]protocol.FeatureSupport) {
	for name, support := range src {
		dst[name] = support.Level
	}
}

// response correlates a response with its request. It reports false only when
// the envelope is rejected as a duplicate response, so the caller can skip
// payload interpretation for an envelope that is already diagnosed.
func (s *state) response(i, line int, e protocol.Envelope) bool {
	req := s.requests[e.InReplyTo]
	if req == nil {
		s.add(CodeUnmatchedResponse, i, line, e, "/in_reply_to", "response does not match an earlier request")
		return true
	}
	if expected := s.expectedResponse(req.typ); e.Type != expected && e.Type != protocol.TypeErrorResponse {
		s.addExpected(CodeUnmatchedResponse, i, line, e, "/type", "response type does not match request", string(expected), string(e.Type), string(e.InReplyTo))
		return true
	}
	if req.responded {
		s.add(CodeDuplicateResponse, i, line, e, "/in_reply_to", "request already has a response")
		return false
	}
	req.responded = true
	// Discovery requests may legitimately carry an obsolete revision (the field
	// is ignored so recovery cannot deadlock), so their successful response is
	// not required to repeat it; only the authoritative current revision counts.
	if e.Type != protocol.TypeErrorResponse && req.typ != protocol.TypeProtocolInitializeRequest && req.typ != protocol.TypeCapabilitiesRequest && req.capabilityRevision != "" && string(e.CapabilityRevision) != req.capabilityRevision {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "successful response must repeat the request capability revision", req.capabilityRevision, string(e.CapabilityRevision), string(e.InReplyTo))
	}
	if e.Type == protocol.TypeErrorResponse {
		// An error still answers a scoped request: any session or run it names
		// must belong to that request's scope.
		if req.session != "" && e.SessionID != "" && e.SessionID != req.session {
			s.addExpected(CodeScopeMismatch, i, line, e, "/session_id", "error response session does not match the request scope", string(req.session), string(e.SessionID), string(e.InReplyTo))
		}
		if req.run != "" && e.RunID != "" && e.RunID != req.run {
			s.addExpected(CodeScopeMismatch, i, line, e, "/run_id", "error response run does not match the request scope", string(req.run), string(e.RunID), string(e.InReplyTo))
		}
		return true
	}
	// A scoped response must answer within the request's scope: an internally
	// consistent response for another session or run cannot answer this request.
	session, run := responseScope(e)
	if s.envelopeScoped(e.Type) {
		// An unknown response, like an unknown request, is scoped by its
		// envelope: the correlation check binds it to the request it answers.
		session, run = e.SessionID, e.RunID
	}
	if req.session != "" && session != "" && session != req.session {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/session_id", "response session does not match the request scope", string(req.session), string(session), string(e.InReplyTo))
	}
	if req.run != "" && run != "" && run != req.run {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/run_id", "response run does not match the request scope", string(req.run), string(run), string(e.InReplyTo))
	}
	// A resolution response must name the same interaction the request resolved;
	// session and run alone cannot distinguish two gates on one run.
	if req.interaction != "" {
		if got := envelopeInteraction(e); got != "" && got != req.interaction {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/interaction_id", "response interaction does not match the request scope", string(req.interaction), string(got), string(e.InReplyTo))
		}
	}
	return true
}

// envelopeInteraction reports the interaction a resolution request or response
// names in its payload. Non-resolution envelopes yield the zero value.
func envelopeInteraction(e protocol.Envelope) protocol.InteractionID {
	switch e.Type {
	case protocol.TypeActionPermissionResolveRequest, protocol.TypeActionPermissionResolveResponse:
		if e.Type == protocol.TypeActionPermissionResolveResponse {
			var p protocol.PermissionResolveResponse
			_ = e.DecodePayload(&p)
			return p.InteractionID
		}
		var p protocol.PermissionResolveRequest
		_ = e.DecodePayload(&p)
		return p.InteractionID
	case protocol.TypeUserInputResolveRequest, protocol.TypeUserInputResolveResponse, protocol.TypeUserInputCancelRequest, protocol.TypeUserInputCancelResponse:
		if e.Type == protocol.TypeUserInputResolveRequest {
			var p protocol.UserInputResolveRequest
			_ = e.DecodePayload(&p)
			return p.InteractionID
		}
		if e.Type == protocol.TypeUserInputCancelRequest {
			var p protocol.UserInputCancelRequest
			_ = e.DecodePayload(&p)
			return p.InteractionID
		}
		var p protocol.UserInputResolveResponse
		_ = e.DecodePayload(&p)
		return p.InteractionID
	}
	return ""
}

// requestScope reports the session/run scope a request declares in its payload.
// Requests that carry no payload scope yield zero values.
func requestScope(e protocol.Envelope) (protocol.SessionID, protocol.RunID) {
	switch e.Type {
	case protocol.TypeSessionMessageSubmitRequest:
		var p protocol.MessageSubmitRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, ""
	case protocol.TypeSessionStateRequest:
		var p protocol.SessionStateRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, ""
	case protocol.TypeSessionOpenRequest:
		var p protocol.SessionOpenRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, ""
	case protocol.TypeRunCancelRequest:
		var p protocol.RunCancelRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeActionPermissionResolveRequest:
		var p protocol.PermissionResolveRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeUserInputResolveRequest:
		var p protocol.UserInputResolveRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeUserInputCancelRequest:
		var p protocol.UserInputCancelRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	}
	return "", ""
}

// responseScope reports the session/run scope a response declares in its
// payload, mirroring requestScope so the two can be correlated.
func responseScope(e protocol.Envelope) (protocol.SessionID, protocol.RunID) {
	switch e.Type {
	case protocol.TypeSessionMessageSubmitResponse:
		var p protocol.MessageSubmitResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeSessionStateResponse, protocol.TypeSessionStateUpdated:
		var p protocol.SessionState
		_ = e.DecodePayload(&p)
		return p.SessionID, ""
	case protocol.TypeSessionOpenResponse:
		var p protocol.SessionOpenResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, ""
	case protocol.TypeRunCancelResponse:
		var p protocol.RunCancelResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeActionPermissionResolveResponse:
		var p protocol.PermissionResolveResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeUserInputResolveResponse, protocol.TypeUserInputCancelResponse:
		var p protocol.UserInputResolveResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	}
	return "", ""
}
func (s *state) submitResponse(i, line int, e protocol.Envelope) {
	var p protocol.MessageSubmitResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	// requested_delivery must repeat the submission's request value; a response
	// that silently changes it would misreport what the client asked for.
	if req := s.requests[e.InReplyTo]; req != nil {
		var request protocol.MessageSubmitRequest
		_ = req.envelope.DecodePayload(&request)
		if p.RequestedDelivery != request.Delivery {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/requested_delivery", "submit response must repeat the requested delivery", string(request.Delivery), string(p.RequestedDelivery), string(e.InReplyTo))
		}
	}
	if !p.Accepted {
		// conformance.md:78-83: a rejected request receives exactly one
		// correlated error.response, so a submit.response carrying
		// accepted:false is non-canonical and must not silently pass.
		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/accepted", "rejected submission must be reported as a correlated error.response, not a submit.response", string(protocol.TypeErrorResponse), string(e.Type), string(e.InReplyTo))
		return
	}
	// Decision 0002: an accepted submission resolves to exactly one of the
	// two canonical admission shapes — started (run.started emitted
	// atomically with the response) or queued (run identity reserved, nothing
	// emitted yet, settled pre-start on failure/cancellation).
	switch {
	case p.RunID == "":
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "accepted submission must reserve a run identity")
		return
	case !admissionShape(s.tolerant, p):
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/admission", "v0.1 admission must resolve auto to one started (status running) or queued (status queued) run (decision 0002)")
		return
	}
	if old := s.runs[p.RunID]; old != nil {
		if s.tolerant && foreignAdmission(p.Admission) {
			// A foreign admission naming a run already tracked may describe
			// an operation on that run rather than a second admission — a
			// later revision's steer, say. What it does the validator cannot
			// judge, so nothing is reserved and the run is left as it is;
			// that the run belongs to the session it can judge.
			if old.session != p.SessionID {
				s.addExpected(CodeScopeMismatch, i, line, e, "/payload/run_id", "submit response names a run owned by another session", string(old.session), string(p.SessionID), string(old.id))
			}
			return
		}
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "run was admitted more than once")
		return
	}
	if sess := s.sessions[p.SessionID]; sess != nil && sess.active != "" {
		if r := s.runs[sess.active]; r != nil && !r.terminal {
			s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "session already has a nonterminal run")
		}
	}
	st := s.sessions[p.SessionID]
	if st == nil {
		st = &sessionTrack{}
		s.sessions[p.SessionID] = st
	}
	st.active = p.RunID
	status, opaque := protocol.RunQueued, s.tolerant && foreignAdmission(p.Admission)
	if opaque {
		// The run's status is what the response declared, not the queued
		// shape's: a known status is judged from there, a foreign one is
		// opaque until a known status is reached (see run.status.updated).
		status = p.Status
	}
	s.runs[p.RunID] = &runState{id: p.RunID, session: p.SessionID, admitted: true, next: 1, lastIndex: i, lastLine: line, admittedModel: p.ModelID, opaqueAdmission: opaque, tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: status}
}
func (s *state) runEvent(i, line int, e protocol.Envelope) {
	var scope struct {
		SessionID protocol.SessionID `json:"session_id"`
		RunID     protocol.RunID     `json:"run_id"`
	}
	_ = e.DecodePayload(&scope)
	s.checkScope(i, line, e, scope.SessionID, scope.RunID)
	r := s.runs[e.RunID]
	if r == nil {
		s.add(CodeIllegalRunTransition, i, line, e, "/run_id", "run event has no accepted admission")
		r = &runState{id: e.RunID, session: e.SessionID, next: 1, tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}}
		s.runs[e.RunID] = r
	}
	r.lastIndex = i
	r.lastLine = line
	if rec := s.recoveries[r.session]; rec != nil && !rec.gap && !rec.firstReplaySeen && e.RunID == rec.run {
		rec.firstReplaySeen = true
		if rec.cursorSet && (e.Sequence == nil || *e.Sequence != rec.cursor+1) {
			actual := "missing"
			if e.Sequence != nil {
				actual = uintString(*e.Sequence)
			}
			s.addExpected(CodeSequenceGap, i, line, e, "/sequence", "retained replay must begin immediately after the declared cursor", uintString(rec.cursor+1), actual)
		}
	}
	if e.SessionID != r.session {
		s.addExpected(CodeScopeMismatch, i, line, e, "/session_id", "run event session differs from run owner", string(r.session), string(e.SessionID), string(e.RunID))
	}
	if e.Sequence != nil {
		if *e.Sequence < r.next {
			s.addExpected(CodeSequenceRegression, i, line, e, "/sequence", "run sequence regressed or repeated", uintString(r.next), uintString(*e.Sequence))
		} else if *e.Sequence > r.next {
			s.addExpected(CodeSequenceGap, i, line, e, "/sequence", "run sequence is not contiguous", uintString(r.next), uintString(*e.Sequence))
		}
		if *e.Sequence >= r.next {
			r.next = *e.Sequence + 1
		}
	}
	if r.terminal {
		if isTerminal(e.Type) {
			if e.Type == protocol.TypeRunCancelled && !r.cancelAccepted {
				s.add(CodeIllegalRunTransition, i, line, e, "/type", "run.cancelled requires accepted cancellation")
			}
			// A pre-start-settled run is still settled: its terminal is its
			// one absorbing event whether or not run.started ever appeared.
			s.add(CodeDuplicateRunTerminal, i, line, e, "/type", "run emitted more than one terminal event")
		} else {
			s.add(CodeEventAfterTerminal, i, line, e, "/type", "run event occurred after terminality")
		}
		return
	}
	if e.Type == protocol.TypeRunStarted {
		if r.started {
			s.add(CodeIllegalRunTransition, i, line, e, "/type", "run.started occurred more than once")
		} else {
			r.started = true
			r.status = protocol.RunRunning
		}
		// The admitted model is authoritative for the run: a started event naming
		// a different model would misattribute the same execution.
		if r.admittedModel != "" {
			var p protocol.RunStartedPayload
			_ = e.DecodePayload(&p)
			if p.ModelID != "" && p.ModelID != r.admittedModel {
				s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/model_id", "run.started model disagrees with the admitted model", string(r.admittedModel), string(p.ModelID), string(r.id))
			}
		}
		return
	}
	// The pre-start rule is a known-type, known-admission rule: only a known
	// terminal may settle a run before run.started, and only a known type can
	// be judged against that list. An unknown type (tolerant mode) says
	// nothing about whether it is a valid pre-start event, and a run admitted
	// under a foreign admission says nothing about whether run.started is
	// owed at all; both take only the type-independent bookkeeping.
	if !r.started && !preStartSettlement(e.Type) && isKnownType(e.Type) && !r.opaqueAdmission {
		s.add(CodeMissingRunStarted, i, line, e, "/type", "run-scoped event occurred before run.started")
	}
	if e.Type == protocol.TypeRunStatusUpdated {
		var p protocol.RunStatusUpdatedPayload
		_ = e.DecodePayload(&p)
		switch {
		case s.tolerant && (foreignRunStatus(p.Status) || foreignRunStatus(r.status)):
			// A foreign status is opaque in tolerant mode: the transition
			// table can judge neither the step into it nor the first step
			// out of it, so both pass and the run records it as its status.
			// The table takes over again once a known status is reached.
			r.status = p.Status
		case !legalRunStatusTransition(r.status, p.Status):
			s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/status", "illegal run status transition", legalRunStatusTargets(r.status), string(p.Status))
		default:
			r.status = p.Status
		}
	}
	switch e.Type {
	case protocol.TypeActionCallRequested:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "requested")
	case protocol.TypeActionCallStarted:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "started")
	case protocol.TypeActionCallProgress:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "progress")
	case protocol.TypeActionCallCompleted:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "completed")
	case protocol.TypeActionCallFailed:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "failed")
	case protocol.TypeActionCallCancelled:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "cancelled")
	case protocol.TypeActionPermissionRequested:
		s.feature(i, line, e, "permissions")
		s.interactionRequested(i, line, e, "permission")
	case protocol.TypeActionPermissionResolved:
		s.feature(i, line, e, "permissions")
		s.interactionResolved(i, line, e, "permission")
	case protocol.TypeUserInputRequested:
		s.feature(i, line, e, "user_input")
		s.interactionRequested(i, line, e, "input")
	case protocol.TypeUserInputResolved:
		s.feature(i, line, e, "user_input")
		s.interactionResolved(i, line, e, "input")
	}
	if isTerminal(e.Type) {
		if e.Type == protocol.TypeRunCancelled && !r.cancelAccepted {
			s.add(CodeIllegalRunTransition, i, line, e, "/type", "run.cancelled requires accepted cancellation")
		}
		for id, st := range r.tools {
			if !toolTerminal(st.status) {
				s.addExpected(CodePendingToolAtTerminal, i, line, e, "/type", "run terminated with a pending tool call", "terminal tool", st.status, string(id))
			}
		}
		for id, x := range r.interactions {
			if !x.resolved {
				s.add(CodePendingInteractionAtTerminal, i, line, e, "/type", "run terminated with a pending interaction")
				s.diagnostics[len(s.diagnostics)-1].RelatedIDs = []string{string(id)}
			}
		}
		r.terminal = true
		r.terminalType = e.Type
		switch e.Type {
		case protocol.TypeRunCompleted:
			r.status = protocol.RunCompleted
		case protocol.TypeRunFailed:
			r.status = protocol.RunFailed
		case protocol.TypeRunCancelled:
			r.status = protocol.RunCancelled
		}
		if st := s.sessions[r.session]; st != nil && st.active == r.id {
			st.active = ""
		}
	}
}
func (s *state) checkScope(i, line int, e protocol.Envelope, session protocol.SessionID, run protocol.RunID) {
	if e.SessionID != "" && session != "" && e.SessionID != session {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/session_id", "envelope and payload session_id differ", string(e.SessionID), string(session))
	}
	if e.RunID != "" && run != "" && e.RunID != run {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/run_id", "envelope and payload run_id differ", string(e.RunID), string(run))
	}
}
func (s *state) tool(i, line int, e protocol.Envelope, next string) {
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	if e.ToolCallID != p.ToolCallID {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "envelope and payload tool_call_id differ", string(e.ToolCallID), string(p.ToolCallID))
	}
	r := s.runs[e.RunID]
	track, ok := r.tools[p.ToolCallID]
	current := track.status
	valid := false
	switch next {
	case "requested":
		valid = !ok
	case "started":
		valid = ok && current == "requested"
	case "progress":
		valid = ok && (current == "started" || current == "progress")
	case "cancelled":
		// conformance.md:143-146 permits a call cancelled or denied before
		// execution starts, so cancellation from requested stays valid; a
		// terminal call cannot terminate twice.
		valid = ok && !toolTerminal(current)
	default:
		// completed / failed require execution to have begun: conformance.md:145
		// emits exactly one terminal event for each *started* tool call.
		valid = ok && (current == "started" || current == "progress")
	}
	if !valid {
		code := CodeIllegalToolTransition
		if !ok && next != "requested" {
			code = CodeUnmatchedTool
		}
		s.add(code, i, line, e, "/tool_call_id", "illegal tool-call lifecycle transition")
	}
	// execution_owner is required on every action-call payload and must not be
	// reassigned mid-lifecycle.
	if ok && track.owner != "" && p.ExecutionOwner != track.owner {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/execution_owner", "tool execution owner changed mid-lifecycle", string(track.owner), string(p.ExecutionOwner))
	}
	if !ok {
		track.owner = p.ExecutionOwner
	}
	track.status = next
	r.tools[p.ToolCallID] = track
}
func (s *state) participant(i, line int, e protocol.Envelope, id protocol.ParticipantID, pointer string) {
	if s.initialized && !s.participants[id] {
		s.addExpected(CodeUnknownParticipant, i, line, e, pointer, "participant was not declared during initialization", "initialized participant", string(id))
	}
}
func (s *state) interactionRequested(i, line int, e protocol.Envelope, kind string) {
	var id protocol.InteractionID
	var requested, responded protocol.ParticipantID
	var allowCancel bool
	var choices map[string]bool
	var questions []protocol.InputQuestion
	var toolCallID protocol.ToolCallID
	if kind == "permission" {
		var p protocol.PermissionRequestedPayload
		_ = e.DecodePayload(&p)
		id = p.InteractionID
		requested = p.RequestedBy
		responded = p.RespondedBy
		toolCallID = p.ToolCallID
		choices = make(map[string]bool, len(p.Choices))
		for _, choice := range p.Choices {
			if choice.ID != "" {
				choices[choice.ID] = true
			}
		}
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		// The portable tool binding must not contradict itself between the
		// envelope and the payload, as is already enforced for action-call events.
		if e.ToolCallID != p.ToolCallID {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "envelope and payload tool_call_id differ", string(e.ToolCallID), string(p.ToolCallID))
		}
	} else {
		var p protocol.UserInputRequestedPayload
		_ = e.DecodePayload(&p)
		id = p.InteractionID
		requested = p.RequestedBy
		responded = p.RespondedBy
		allowCancel = p.AllowCancel
		questions = p.Questions
		toolCallID = p.ToolCallID
		s.checkScope(i, line, e, p.SessionID, p.RunID)
		// A tool-bound prompt must not contradict itself between the envelope and
		// the payload, as is already enforced for permission requests.
		if e.ToolCallID != p.ToolCallID {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "envelope and payload tool_call_id differ", string(e.ToolCallID), string(p.ToolCallID))
		}
	}
	s.participant(i, line, e, requested, "/payload/requested_by")
	s.participant(i, line, e, responded, "/payload/responded_by")
	r := s.runs[e.RunID]
	if _, ok := r.interactions[id]; ok {
		s.add(CodeDuplicateInteraction, i, line, e, "/payload", "interaction id was requested more than once")
	}
	r.interactions[id] = &interactionState{kind: kind, requestedBy: requested, respondedBy: responded, allowCancel: allowCancel, choices: choices, questions: questions, toolCallID: toolCallID}
}
func (s *state) interactionResolutionRequest(i, line int, e protocol.Envelope, kind string) {
	id, requested, responded := interactionFields(e, kind)
	x := s.lookupInteraction(e.RunID, id)
	if x == nil {
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution has no pending interaction")
		return
	}
	if x.kind != kind {
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution has the wrong interaction kind")
	}
	// A prompt that declared allow_cancel:false may not be withdrawn.
	if e.Type == protocol.TypeUserInputCancelRequest && !x.allowCancel {
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "interaction was not opened for cancellation")
	}
	// A permission resolution must select a choice the gate offered.
	if e.Type == protocol.TypeActionPermissionResolveRequest {
		var p protocol.PermissionResolveRequest
		_ = e.DecodePayload(&p)
		if p.ChoiceID != "" && !x.choices[p.ChoiceID] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/choice_id", "resolution selects a choice the permission did not offer", "offered choice", p.ChoiceID, string(id))
		}
	}
	// A resolution must answer the prompt's questions as offered.
	if e.Type == protocol.TypeUserInputResolveRequest {
		var p protocol.UserInputResolveRequest
		_ = e.DecodePayload(&p)
		s.validateInputAnswers(i, line, e, id, x.questions, p.Answers)
	}
	if responded != x.respondedBy || (requested != "" && requested != x.requestedBy) {
		s.addExpected(CodeWrongInteractionResponder, i, line, e, "/payload/responded_by", "only the declared responder may resolve an interaction", string(x.respondedBy), string(responded), string(id))
	}
}

// validateInputAnswers checks a resolution against the questions the prompt
// offered: every answer must name an offered question, match its kind, select
// only offered options, and every required question must be answered.
func (s *state) validateInputAnswers(i, line int, e protocol.Envelope, id protocol.InteractionID, questions []protocol.InputQuestion, answers []protocol.InputAnswer) {
	offered := make(map[string]protocol.InputQuestion, len(questions))
	for _, question := range questions {
		offered[question.ID] = question
	}
	answered := make(map[string]bool, len(answers))
	for _, answer := range answers {
		question, ok := offered[answer.QuestionID]
		if !ok {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/answers", "answer names a question the prompt did not offer", "offered question", answer.QuestionID, string(id))
			continue
		}
		if answered[answer.QuestionID] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/answers", "question was answered more than once", "one answer per question", answer.QuestionID, string(id))
			continue
		}
		answered[answer.QuestionID] = true
		options := make(map[string]bool, len(question.Options))
		for _, option := range question.Options {
			options[option.ID] = true
		}
		switch question.Kind {
		case protocol.InputText:
			if answer.Text == "" {
				s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/answers", "text answer carries no text", "non-empty text", "", string(id))
			}
		case protocol.InputSingleChoice, protocol.InputMultiChoice:
			if len(answer.SelectedOptionIDs) == 0 {
				s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/answers", "choice answer selects no option", "offered option", "", string(id))
			}
			if question.Kind == protocol.InputSingleChoice && len(answer.SelectedOptionIDs) > 1 {
				s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/answers", "single-choice answer selects more than one option", "one option", fmt.Sprintf("%d", len(answer.SelectedOptionIDs)), string(id))
			}
			for _, option := range answer.SelectedOptionIDs {
				if !options[option] {
					s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/answers", "answer selects an option the question did not offer", "offered option", option, string(id))
				}
			}
		}
	}
	for _, question := range questions {
		if question.Required && !answered[question.ID] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/answers", "required question was not answered", question.ID, "missing", string(id))
		}
	}
}

func (s *state) interactionResolved(i, line int, e protocol.Envelope, kind string) {
	id, requested, responded := interactionFields(e, kind)
	x := s.lookupInteraction(e.RunID, id)
	if x == nil {
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution event has no pending interaction")
		return
	}
	if x.resolved {
		s.add(CodeDuplicateInteraction, i, line, e, "/payload", "interaction was resolved more than once")
	}
	if responded != x.respondedBy || requested != x.requestedBy {
		s.addExpected(CodeWrongInteractionResponder, i, line, e, "/payload/responded_by", "resolution ownership differs from request", string(x.respondedBy), string(responded), string(id))
	}
	// A resolution event must match the pending interaction's kind; a
	// permission event cannot resolve a pending input (or the converse).
	if x.kind != kind {
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution event has the wrong interaction kind", x.kind, kind, string(id))
	}
	// The authoritative resolution payload must answer the offered questions.
	if kind == "input" {
		var p protocol.UserInputResolvedPayload
		_ = e.DecodePayload(&p)
		if p.Status == protocol.InputSubmitted {
			s.validateInputAnswers(i, line, e, id, x.questions, p.Answers)
		}
	}
	// A resolved permission event must likewise select a choice the gate
	// offered; the resolve-request check alone cannot constrain the event that
	// actually certifies the resolution.
	if kind == "permission" {
		var p protocol.PermissionResolvedPayload
		_ = e.DecodePayload(&p)
		if p.Outcome == protocol.InteractionResolved && p.ChoiceID != "" && !x.choices[p.ChoiceID] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/choice_id", "resolution event selects a choice the permission did not offer", "offered choice", p.ChoiceID, string(id))
		}
		// The authoritative resolution must keep the request's tool binding; a
		// present field that names a different tool has reassigned it. An omitted
		// field carries no binding and is judged only through the request's own
		// envelope/payload agreement.
		if e.ToolCallID != "" && e.ToolCallID != x.toolCallID {
			s.addExpected(CodeScopeMismatch, i, line, e, "/tool_call_id", "resolution envelope reassigns the request's tool binding", string(x.toolCallID), string(e.ToolCallID))
		}
		if p.ToolCallID != "" && p.ToolCallID != x.toolCallID {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "resolution payload reassigns the request's tool binding", string(x.toolCallID), string(p.ToolCallID))
		}
	}
	x.resolved = true
}
func interactionFields(e protocol.Envelope, kind string) (protocol.InteractionID, protocol.ParticipantID, protocol.ParticipantID) {
	if kind == "permission" {
		if e.Type == protocol.TypeActionPermissionResolveRequest {
			var p protocol.PermissionResolveRequest
			_ = e.DecodePayload(&p)
			return p.InteractionID, p.RequestedBy, p.RespondedBy
		}
		var p protocol.PermissionResolvedPayload
		_ = e.DecodePayload(&p)
		return p.InteractionID, p.RequestedBy, p.RespondedBy
	}
	if e.Type == protocol.TypeUserInputResolveRequest {
		var p protocol.UserInputResolveRequest
		_ = e.DecodePayload(&p)
		return p.InteractionID, p.RequestedBy, p.RespondedBy
	}
	if e.Type == protocol.TypeUserInputCancelRequest {
		var p protocol.UserInputCancelRequest
		_ = e.DecodePayload(&p)
		return p.InteractionID, p.RequestedBy, p.RespondedBy
	}
	var p protocol.UserInputResolvedPayload
	_ = e.DecodePayload(&p)
	return p.InteractionID, p.RequestedBy, p.RespondedBy
}
func (s *state) lookupInteraction(run protocol.RunID, id protocol.InteractionID) *interactionState {
	if r := s.runs[run]; r != nil {
		return r.interactions[id]
	}
	return nil
}
func (s *state) feature(i, line int, e protocol.Envelope, name string) {
	if s.currentCapability == "" || s.capabilitiesStale {
		s.add(CodeUnavailableCapability, i, line, e, "/type", "optional feature requires a current capability descriptor")
		return
	}
	if string(e.CapabilityRevision) != s.currentCapability {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "optional feature must cite the active capability descriptor", s.currentCapability, string(e.CapabilityRevision))
		return
	}
	for _, key := range []string{name, "session.message." + name, "agent_control." + name, "action." + name} {
		if level, ok := s.features[key]; ok {
			switch level {
			case protocol.SupportNative, protocol.SupportEmulated, protocol.SupportDegraded:
				return
			case protocol.SupportUnavailable:
				s.add(CodeUnavailableCapability, i, line, e, "/type", "event uses capability declared unavailable")
			default:
				// agent-control-profile.md: an unknown support level is
				// treated as unavailable. Tolerant mode keeps the value
				// opaque on the descriptor, but only a known affirmative
				// level satisfies the gate.
				s.addExpected(CodeUnavailableCapability, i, line, e, "/type", "event uses capability whose support level is not one this revision recognises; an unknown level is unavailable", "native, emulated, or degraded", string(level))
			}
			return
		}
	}
	s.add(CodeUnavailableCapability, i, line, e, "/type", "optional feature was not affirmatively advertised")
}
func (s *state) close(index int) {
	for _, rec := range s.recoveries {
		if rec.gap && !rec.stateSeen {
			e := protocol.Envelope{Type: protocol.TypeSessionStateResponse, SessionID: rec.session}
			s.add(CodeUndeclaredReplayGap, rec.openIndex, 0, e, "", "replay gap lacks authoritative session state")
		}
	}
	for id, req := range s.requests {
		if !req.responded {
			s.add(CodeMissingResponse, req.index, req.line, req.envelope, "", "request has no correlated response")
			s.diagnostics[len(s.diagnostics)-1].RelatedIDs = []string{string(id)}
		}
	}
	for _, r := range s.runs {
		if r.admitted && !r.started && !r.terminal {
			e := protocol.Envelope{ID: protocol.EnvelopeID(r.id), Type: protocol.TypeRunStarted, RunID: r.id, SessionID: r.session}
			s.add(CodeMissingRunStarted, r.lastIndex, r.lastLine, e, "", "admitted run never emitted run.started")
		}
		if r.admitted && !r.terminal {
			e := protocol.Envelope{ID: protocol.EnvelopeID(r.id), RunID: r.id, SessionID: r.session}
			code := CodeMissingRunTerminal
			msg := "admitted run has no terminal event"
			if r.cancelAccepted {
				code = CodeCancelNotSettled
				msg = "accepted cancellation did not settle with a terminal event"
			}
			s.add(code, r.lastIndex, r.lastLine, e, "", msg)
		}
	}
}
func expectedResponse(t protocol.EnvelopeType) protocol.EnvelopeType {
	if t == protocol.TypeUserInputCancelRequest {
		return protocol.TypeUserInputCancelResponse
	}
	return protocol.EnvelopeType(strings.TrimSuffix(string(t), ".request") + ".response")
}
func isRequest(t protocol.EnvelopeType) bool  { return strings.HasSuffix(string(t), ".request") }
func isResponse(t protocol.EnvelopeType) bool { return strings.HasSuffix(string(t), ".response") }
func isTerminal(t protocol.EnvelopeType) bool {
	return t == protocol.TypeRunCompleted || t == protocol.TypeRunFailed || t == protocol.TypeRunCancelled
}

// preStartSettlement reports whether the event type may settle an accepted
// run before run.started (decision 0002): a run can fail or be cancelled
// before any start was observable, but it cannot complete — completion
// without an observed start has no faithful projection.
func preStartSettlement(t protocol.EnvelopeType) bool {
	return t == protocol.TypeRunFailed || t == protocol.TypeRunCancelled
}
func isRunEvent(t protocol.EnvelopeType) bool {
	switch t {
	case protocol.TypeRunStarted, protocol.TypeRunStatusUpdated, protocol.TypeContentDelta, protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled, protocol.TypeActionPermissionRequested, protocol.TypeActionPermissionResolved, protocol.TypeUserInputRequested, protocol.TypeUserInputResolved:
		return true
	}
	return false
}

// A foreign value is one present on the wire but outside the vocabulary this
// revision defines for the field. Tolerant mode treats it as opaque: the
// value-specific rule is suspended, since it cannot judge what it does not
// know, while the type-independent bookkeeping around it still applies. An
// absent value is not foreign — a missing member is a shape defect in any
// revision, and the strict rule keeps judging it.
func foreignRunStatus(s protocol.RunStatus) bool {
	switch s {
	case "", protocol.RunQueued, protocol.RunRunning, protocol.RunWaitingForInput, protocol.RunCancelling, protocol.RunCompleted, protocol.RunFailed, protocol.RunCancelled:
		return false
	}
	return true
}
func foreignAdmission(a protocol.Admission) bool {
	switch a {
	case "", protocol.AdmissionStarted, protocol.AdmissionQueued, protocol.AdmissionSteered, protocol.AdmissionSideRun, protocol.AdmissionRejected:
		return false
	}
	return true
}
func foreignEffectiveDelivery(d protocol.EffectiveDeliveryMode) bool {
	switch d {
	case "", protocol.DeliveryStart, protocol.EffectiveDeliveryQueue, protocol.EffectiveDeliverySteer, protocol.EffectiveDeliveryBTW:
		return false
	}
	return true
}
func foreignRequestedDelivery(d protocol.RequestedDeliveryMode) bool {
	switch d {
	case "", protocol.DeliveryAuto, protocol.DeliveryQueue, protocol.DeliverySteer, protocol.DeliveryBTW:
		return false
	}
	return true
}

// admissionShape judges Decision 0002's canonical-shape rule for an accepted
// submission member by member. Admission, effective delivery, and status each
// name one of the two shapes — started (started/start/running) or queued
// (queued/queue/queued) — and every member that names a shape must name the
// same one. A known value outside both shapes (a non-auto admission, a
// missing status) names none and fails. In tolerant mode a foreign value is
// opaque: it names no shape and constrains nothing, but the known members are
// still held to each other, so admission started with status queued is
// contradictory whatever the delivery is called.
func admissionShape(tolerant bool, p protocol.MessageSubmitResponse) bool {
	votes := []struct {
		shape   string
		foreign bool
	}{
		{namesShape(string(p.Admission), string(protocol.AdmissionStarted), string(protocol.AdmissionQueued)), foreignAdmission(p.Admission)},
		{namesShape(string(p.EffectiveDelivery), string(protocol.DeliveryStart), string(protocol.EffectiveDeliveryQueue)), foreignEffectiveDelivery(p.EffectiveDelivery)},
		{namesShape(string(p.Status), string(protocol.RunRunning), string(protocol.RunQueued)), foreignRunStatus(p.Status)},
	}
	named := ""
	for _, v := range votes {
		if tolerant && v.foreign {
			continue
		}
		if v.shape == "" || (named != "" && named != v.shape) {
			return false
		}
		named = v.shape
	}
	return true
}

// namesShape maps one member's value to the shape it names, or "" for none.
func namesShape(value, started, queued string) string {
	switch value {
	case started:
		return "started"
	case queued:
		return "queued"
	}
	return ""
}
func legalRunStatusTransition(from, to protocol.RunStatus) bool {
	switch from {
	case protocol.RunQueued:
		return to == protocol.RunRunning || to == protocol.RunCancelling
	case protocol.RunRunning:
		return to == protocol.RunRunning || to == protocol.RunWaitingForInput || to == protocol.RunCancelling
	case protocol.RunWaitingForInput:
		return to == protocol.RunWaitingForInput || to == protocol.RunRunning || to == protocol.RunCancelling
	case protocol.RunCancelling:
		return to == protocol.RunCancelling
	default:
		return false
	}
}
func legalRunStatusTargets(from protocol.RunStatus) string {
	switch from {
	case protocol.RunQueued:
		return "running or cancelling"
	case protocol.RunRunning, protocol.RunWaitingForInput:
		return "running, waiting_for_input, or cancelling"
	case protocol.RunCancelling:
		return "cancelling or a terminal event"
	default:
		return "no transition from terminal status"
	}
}
func toolTerminal(s string) bool { return s == "completed" || s == "failed" || s == "cancelled" }
