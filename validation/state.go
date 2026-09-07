package validation

import (
	"fmt"
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
	tools                       map[protocol.ToolCallID]string
	interactions                map[protocol.InteractionID]*interactionState
}
type interactionState struct {
	kind                     string
	requestedBy, respondedBy protocol.ParticipantID
	resolved                 bool
}
type recoveryExpectation struct {
	session         protocol.SessionID
	run             protocol.RunID
	gap             bool
	openIndex       int
	stateSeen       bool
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
}
type sessionTrack struct {
	status protocol.SessionStatus
	active protocol.RunID
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
	recovery          *recoveryExpectation
}

func newState(f string) *state {
	return &state{fixture: f, ids: map[protocol.EnvelopeID]int{}, requests: map[protocol.EnvelopeID]*requestState{}, participants: map[protocol.ParticipantID]bool{}, sessions: map[protocol.SessionID]*sessionTrack{}, runs: map[protocol.RunID]*runState{}, features: map[string]protocol.SupportLevel{}}
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
	if isRequest(e.Type) {
		s.requests[e.ID] = &requestState{typ: e.Type, index: i, line: line, envelope: e, capabilityRevision: string(e.CapabilityRevision)}
	}
	if isResponse(e.Type) {
		s.response(i, line, e)
	}
	if e.CapabilityRevision != "" && s.currentCapability != "" && string(e.CapabilityRevision) != s.currentCapability && e.Type != protocol.TypeProtocolInitializeRequest && e.Type != protocol.TypeCapabilitiesRequest && e.Type != protocol.TypeErrorResponse {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "operation uses a stale capability revision", s.currentCapability, e.CapabilityRevision)
	}
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
			s.recovery = recovery
		}
		s.sessions[p.SessionID] = &sessionTrack{status: p.Status}
	case protocol.TypeSessionStateResponse, protocol.TypeSessionStateUpdated:
		var p protocol.SessionState
		_ = e.DecodePayload(&p)
		if s.recovery != nil && p.SessionID == s.recovery.session {
			s.recovery.stateSeen = true
			if s.recovery.gap && p.Recovery == nil {
				s.add(CodeUndeclaredReplayGap, i, line, e, "/payload/recovery", "authoritative state after a replay gap must retain recovery metadata")
			}
			if !s.recovery.gap && p.ActiveRunID != "" {
				if s.recovery.run != "" && p.ActiveRunID != s.recovery.run {
					s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "recovered state names a different active run", string(s.recovery.run), string(p.ActiveRunID))
				}
				run := s.runs[p.ActiveRunID]
				if run == nil {
					next := uint64(1)
					if s.recovery.cursorSet {
						next = s.recovery.cursor + 1
					}
					s.runs[p.ActiveRunID] = &runState{id: p.ActiveRunID, session: p.SessionID, admitted: true, started: true, next: next, lastIndex: i, lastLine: line, tools: map[protocol.ToolCallID]string{}, interactions: map[protocol.InteractionID]*interactionState{}, status: protocol.RunRunning}
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
		st.status = p.Status
		st.active = p.ActiveRunID
	case protocol.TypeSessionMessageSubmitRequest:
		var p protocol.MessageSubmitRequest
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, "")
		if s.capabilitiesStale {
			s.add(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "submission occurred before refreshed capabilities")
		}
		if p.Delivery != protocol.DeliveryAuto {
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
			}
		}
	default:
		if isRunEvent(e.Type) {
			s.runEvent(i, line, e)
		}
	}
}
func collectFeatures(dst map[string]protocol.SupportLevel, src map[string]protocol.FeatureSupport) {
	for name, support := range src {
		dst[name] = support.Level
	}
}
func (s *state) response(i, line int, e protocol.Envelope) {
	req := s.requests[e.InReplyTo]
	if req == nil {
		s.add(CodeUnmatchedResponse, i, line, e, "/in_reply_to", "response does not match an earlier request")
		return
	}
	if expected := expectedResponse(req.typ); e.Type != expected && e.Type != protocol.TypeErrorResponse {
		s.addExpected(CodeUnmatchedResponse, i, line, e, "/type", "response type does not match request", string(expected), string(e.Type), string(e.InReplyTo))
		return
	}
	if req.responded {
		s.add(CodeDuplicateResponse, i, line, e, "/in_reply_to", "request already has a response")
		return
	}
	req.responded = true
	if e.Type != protocol.TypeErrorResponse && req.capabilityRevision != "" && string(e.CapabilityRevision) != req.capabilityRevision {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "successful response must repeat the request capability revision", req.capabilityRevision, string(e.CapabilityRevision), string(e.InReplyTo))
	}
}
func (s *state) submitResponse(i, line int, e protocol.Envelope) {
	var p protocol.MessageSubmitResponse
	_ = e.DecodePayload(&p)
	s.checkScope(i, line, e, p.SessionID, p.RunID)
	if !p.Accepted {
		return
	}
	if p.Admission != protocol.AdmissionStarted || p.EffectiveDelivery != protocol.DeliveryStart || p.RunID == "" {
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/admission", "v0.1 admission must resolve auto to one started run")
		return
	}
	if old := s.runs[p.RunID]; old != nil {
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
	s.runs[p.RunID] = &runState{id: p.RunID, session: p.SessionID, admitted: true, next: 1, lastIndex: i, lastLine: line, tools: map[protocol.ToolCallID]string{}, interactions: map[protocol.InteractionID]*interactionState{}, status: protocol.RunQueued}
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
		r = &runState{id: e.RunID, session: e.SessionID, next: 1, tools: map[protocol.ToolCallID]string{}, interactions: map[protocol.InteractionID]*interactionState{}}
		s.runs[e.RunID] = r
	}
	r.lastIndex = i
	r.lastLine = line
	if s.recovery != nil && !s.recovery.gap && !s.recovery.firstReplaySeen && e.RunID == s.recovery.run {
		s.recovery.firstReplaySeen = true
		if s.recovery.cursorSet && (e.Sequence == nil || *e.Sequence != s.recovery.cursor+1) {
			actual := "missing"
			if e.Sequence != nil {
				actual = uintString(*e.Sequence)
			}
			s.addExpected(CodeSequenceGap, i, line, e, "/sequence", "retained replay must begin immediately after the declared cursor", uintString(s.recovery.cursor+1), actual)
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
			if r.started {
				s.add(CodeDuplicateRunTerminal, i, line, e, "/type", "run emitted more than one terminal event")
			}
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
		return
	}
	if !r.started {
		s.add(CodeMissingRunStarted, i, line, e, "/type", "run-scoped event occurred before run.started")
	}
	if e.Type == protocol.TypeRunStatusUpdated {
		var p protocol.RunStatusUpdatedPayload
		_ = e.DecodePayload(&p)
		if !legalRunStatusTransition(r.status, p.Status) {
			s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/status", "illegal run status transition", legalRunStatusTargets(r.status), string(p.Status))
		} else {
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
			if !toolTerminal(st) {
				s.addExpected(CodePendingToolAtTerminal, i, line, e, "/type", "run terminated with a pending tool call", "terminal tool", st, string(id))
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
	current, ok := r.tools[p.ToolCallID]
	valid := false
	switch next {
	case "requested":
		valid = !ok
	case "started":
		valid = ok && current == "requested"
	case "progress":
		valid = ok && (current == "started" || current == "progress")
	default:
		valid = ok && !toolTerminal(current)
	}
	if !valid {
		code := CodeIllegalToolTransition
		if !ok && next != "requested" {
			code = CodeUnmatchedTool
		}
		s.add(code, i, line, e, "/tool_call_id", "illegal tool-call lifecycle transition")
	}
	r.tools[p.ToolCallID] = next
}
func (s *state) participant(i, line int, e protocol.Envelope, id protocol.ParticipantID, pointer string) {
	if s.initialized && !s.participants[id] {
		s.addExpected(CodeUnknownParticipant, i, line, e, pointer, "participant was not declared during initialization", "initialized participant", string(id))
	}
}
func (s *state) interactionRequested(i, line int, e protocol.Envelope, kind string) {
	var id protocol.InteractionID
	var requested, responded protocol.ParticipantID
	if kind == "permission" {
		var p protocol.PermissionRequestedPayload
		_ = e.DecodePayload(&p)
		id = p.InteractionID
		requested = p.RequestedBy
		responded = p.RespondedBy
		s.checkScope(i, line, e, p.SessionID, p.RunID)
	} else {
		var p protocol.UserInputRequestedPayload
		_ = e.DecodePayload(&p)
		id = p.InteractionID
		requested = p.RequestedBy
		responded = p.RespondedBy
		s.checkScope(i, line, e, p.SessionID, p.RunID)
	}
	s.participant(i, line, e, requested, "/payload/requested_by")
	s.participant(i, line, e, responded, "/payload/responded_by")
	r := s.runs[e.RunID]
	if _, ok := r.interactions[id]; ok {
		s.add(CodeDuplicateInteraction, i, line, e, "/payload", "interaction id was requested more than once")
	}
	r.interactions[id] = &interactionState{kind: kind, requestedBy: requested, respondedBy: responded}
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
	if responded != x.respondedBy || (requested != "" && requested != x.requestedBy) {
		s.addExpected(CodeWrongInteractionResponder, i, line, e, "/payload/responded_by", "only the declared responder may resolve an interaction", string(x.respondedBy), string(responded), string(id))
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
	for _, key := range []string{name, "agent_control." + name, "action." + name} {
		if level, ok := s.features[key]; ok {
			if level != protocol.SupportUnavailable {
				return
			}
			s.add(CodeUnavailableCapability, i, line, e, "/type", "event uses capability declared unavailable")
			return
		}
	}
	s.add(CodeUnavailableCapability, i, line, e, "/type", "optional feature was not affirmatively advertised")
}
func (s *state) close(index int) {
	if s.recovery != nil && s.recovery.gap && !s.recovery.stateSeen {
		e := protocol.Envelope{Type: protocol.TypeSessionStateResponse, SessionID: s.recovery.session}
		s.add(CodeUndeclaredReplayGap, s.recovery.openIndex, 0, e, "", "replay gap lacks authoritative session state")
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
func isRunEvent(t protocol.EnvelopeType) bool {
	switch t {
	case protocol.TypeRunStarted, protocol.TypeRunStatusUpdated, protocol.TypeContentDelta, protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled, protocol.TypeActionCallRequested, protocol.TypeActionCallStarted, protocol.TypeActionCallProgress, protocol.TypeActionCallCompleted, protocol.TypeActionCallFailed, protocol.TypeActionCallCancelled, protocol.TypeActionPermissionRequested, protocol.TypeActionPermissionResolved, protocol.TypeUserInputRequested, protocol.TypeUserInputResolved:
		return true
	}
	return false
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
