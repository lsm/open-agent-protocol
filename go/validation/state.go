package validation

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/lsm/open-agent-protocol/go/protocol"
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

	opaqueAdmission bool

	controls     admittedControls
	tools        map[protocol.ToolCallID]toolTrack
	interactions map[protocol.InteractionID]*interactionState

	order          int
	admittedQueued bool

	startedAt int

	startSequence uint64
	terminalAt    int

	admittedAt    int
	submitRequest protocol.EnvelopeID

	deferredControls bool

	recovered bool

	priorUnknown bool
}
type interactionState struct {
	kind                     string
	requestedBy, respondedBy protocol.ParticipantID
	allowCancel              bool
	choices                  map[string]bool
	questions                []protocol.InputQuestion
	toolCallID               protocol.ToolCallID
	resolved                 bool

	opaque bool

	openedAt, resolvedAt uint64

	acked           bool
	settled         bool
	acceptedArm     string
	acceptedResult  json.RawMessage
	acceptedError   *protocol.ProtocolError
	resolveRequests map[protocol.EnvelopeID]bool
	settlements     map[protocol.EnvelopeID]bool
}

func (x *interactionState) controlCall() bool { return x.kind == interactionKindToolCall }

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
	carriesMessage     bool

	gates []packGate
}
type sessionTrack struct {
	status protocol.SessionStatus
	active protocol.RunID

	currentModel    string
	currentKnown    bool
	expectedDefault string
	guardDefault    bool

	order []protocol.RunID

	openingModel string
	openingKnown bool

	mutated bool

	attached      map[string]protocol.ToolSourceDescriptor
	attachedOrder []string

	toolCatalog *sessionCatalog

	provided      map[string]protocol.ToolDefinition
	providedOrder []string

	catalog        *modelCatalog
	modelMarks     []modelMark
	unjudgedModels []unjudgedModel
	heldCatalogs   []heldCatalog
}

type toolTrack struct {
	status      string
	owner       protocol.ParticipantID
	source      string
	interaction protocol.InteractionID
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

	featureSupports map[string]protocol.FeatureSupport
	catalog         []string
	catalogKnown    bool

	catalogAmbiguous bool

	pendingControls map[protocol.EnvelopeID]*pendingSubmit

	openSubmits map[protocol.SessionID][]*pendingSubmit

	limits *protocol.CapabilityLimits

	deferred []*deferredStateClaim

	ledGroups  []*ledGroup
	recoveries map[protocol.SessionID]*recoveryExpectation

	pendingLists      map[protocol.EnvelopeID]*pendingList
	pendingOpens      map[protocol.EnvelopeID]*pendingOpen
	pendingSubscribes map[protocol.EnvelopeID]*pendingSubscribe

	descriptorAttribution map[string]string

	declaredSources map[string]protocol.ToolSourceDescriptor

	descriptorOwners map[string]protocol.ParticipantID

	controlParticipant protocol.ParticipantID

	pendingResolves map[protocol.EnvelopeID]*pendingResolve

	pendingModels map[protocol.EnvelopeID]*pendingModelsQuery

	tolerant bool

	packs *PackSet
}

func newState(f string) *state {
	return &state{fixture: f, ids: map[protocol.EnvelopeID]int{}, requests: map[protocol.EnvelopeID]*requestState{}, participants: map[protocol.ParticipantID]bool{}, sessions: map[protocol.SessionID]*sessionTrack{}, runs: map[protocol.RunID]*runState{}, recoveries: map[protocol.SessionID]*recoveryExpectation{}, features: map[string]protocol.SupportLevel{}, featureSupports: map[string]protocol.FeatureSupport{}, pendingControls: map[protocol.EnvelopeID]*pendingSubmit{}, pendingLists: map[protocol.EnvelopeID]*pendingList{}, pendingOpens: map[protocol.EnvelopeID]*pendingOpen{}, pendingSubscribes: map[protocol.EnvelopeID]*pendingSubscribe{}, pendingResolves: map[protocol.EnvelopeID]*pendingResolve{}, declaredSources: map[string]protocol.ToolSourceDescriptor{}, pendingModels: map[protocol.EnvelopeID]*pendingModelsQuery{}, openSubmits: map[protocol.SessionID][]*pendingSubmit{}}
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

			session, run = unknownScope(e)
		}

		if !s.tolerantRunEvent(e) {
			s.checkScope(i, line, e, session, run)
		}
		s.requests[e.ID] = &requestState{typ: e.Type, index: i, line: line, envelope: e, capabilityRevision: string(e.CapabilityRevision), session: session, run: run, interaction: envelopeInteraction(e)}
	}
	duplicateResponse := false
	if s.isResponseType(e.Type) {
		duplicateResponse = !s.response(i, line, e)
	}

	if e.CapabilityRevision != "" && s.currentCapability != "" && string(e.CapabilityRevision) != s.currentCapability && e.Type != protocol.TypeProtocolInitializeRequest && e.Type != protocol.TypeCapabilitiesRequest && e.Type != protocol.TypeCapabilitiesUpdated && e.Type != protocol.TypeErrorResponse {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "operation uses a stale capability revision", s.currentCapability, e.CapabilityRevision)
	}
	if duplicateResponse {

		return
	}
	switch e.Type {
	case protocol.TypeProtocolInitializeRequest:
		var p protocol.InitializeRequest
		_ = e.DecodePayload(&p)
		s.initialized = true
		if p.Participant != nil {
			s.participants[p.Participant.ID] = true

			s.controlParticipant = p.Participant.ID
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

		outgoing := descriptorSnapshot{
			revision: s.currentCapability,
			stale:    s.capabilitiesStale,
			models:   s.features[protocol.FeatureModelsList],
			queue:    s.features[protocol.FeatureDeliveryQueue],
			limits:   s.limits,
		}
		s.currentCapability = e.CapabilityRevision
		s.capabilitiesStale = false
		s.features = map[string]protocol.SupportLevel{}
		s.featureSupports = map[string]protocol.FeatureSupport{}
		collectFeatures(s.features, s.featureSupports, p)
		s.catalog, s.catalogKnown = collectCatalog(p), true
		s.limits = p.Limits
		s.checkSelectionModes(i, line, e, p)
		s.checkQueueLimits(i, line, e, p)
		s.checkAttachModes(i, line, e, p)
		s.checkDescriptorSources(i, line, e, p)
		s.checkCatalogAdvertisement(i, line, e, outgoing)
		s.checkQueueAdvertisement(i, line, e, outgoing)
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

		s.catalog, s.catalogKnown = nil, false

		s.limits = nil
		s.declaredSources = map[string]protocol.ToolSourceDescriptor{}
		s.descriptorAttribution = nil
	case protocol.TypeModelsRequest:
		var p protocol.ModelsRequest
		_ = e.DecodePayload(&p)

		s.modelsRequest(i, line, e, p)
	case protocol.TypeModelsResponse:
		var p protocol.ModelsResponse
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, "")
		s.modelsResponse(i, line, e, p)
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

		st := s.track(p.SessionID)
		if p.Recovery != nil && p.Recovery.Recovered {
			s.bootstrapRecoveredRuns(i, line, p, st)
		}
		s.compoundOpenResponse(i, line, e, p)
		s.applyStateDocument(i, line, e, p, st)
		s.checkSessionCapture(i, line, e, p, st)

		if p.CurrentModelID != "" {
			st.currentModel, st.currentKnown = p.CurrentModelID, true
			if !st.mutated && !st.openingKnown {

				st.openingModel, st.openingKnown = p.CurrentModelID, true
			}
		}
		s.observeModel(p.SessionID, p.CurrentModelID, "", 0)
		s.sessionOpenResponse(i, line, e, p)
	case protocol.TypeSessionOpenRequest:
		var p protocol.SessionOpenRequest
		_ = e.DecodePayload(&p)
		s.sessionOpenRequest(i, line, e)
		s.compoundOpenRequest(i, line, e, p)
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
				if s.runs[p.ActiveRunID] == nil {
					s.introduceRecoveredRun(i, line, s.track(p.SessionID), &runState{id: p.ActiveRunID, session: p.SessionID, admitted: true, started: true, next: resumeSequence(rec, p.ActiveRunID, nil), tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: protocol.RunRunning}, nil)
				}
			}
		}
		s.checkScope(i, line, e, p.SessionID, p.ActiveRunID)
		st := s.sessions[p.SessionID]
		if st == nil {
			st = &sessionTrack{}
			s.sessions[p.SessionID] = st
		}
		s.applyStateDocument(i, line, e, p, st)

		if st.guardDefault && p.CurrentModelID != st.expectedDefault {
			s.addExpected(CodeUnappliedControl, i, line, e, "/payload/current_model_id", "a per_run model selection moved the session default", st.expectedDefault, p.CurrentModelID)
		}
		s.checkSessionCapture(i, line, e, p, st)
		st.currentModel, st.currentKnown = p.CurrentModelID, true
		if !st.mutated && !st.openingKnown {

			st.openingModel, st.openingKnown = p.CurrentModelID, true
		}
		s.observeModel(p.SessionID, p.CurrentModelID, "", 0)
		s.checkPublishedSources(i, line, e)
		s.checkPublishedUnion(i, line, e, p.SessionID, p.Sources, false)
	case protocol.TypeSessionMessageSubmitRequest:
		var p protocol.MessageSubmitRequest
		_ = e.DecodePayload(&p)
		s.checkScope(i, line, e, p.SessionID, "")
		if s.capabilitiesStale {
			s.add(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "submission occurred before refreshed capabilities")
		}
		s.submitControls(i, line, e, p)
		if p.Delivery != protocol.DeliveryAuto && p.Delivery != protocol.DeliveryQueue && !(s.tolerant && foreignRequestedDelivery(p.Delivery)) {

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

		s.toolsListRequest(i, line, e)
	case protocol.TypeActionToolsListResponse:
		s.toolsListResponse(i, line, e)
	case protocol.TypeActionCallResolveRequest:
		s.controlResolveRequest(i, line, e)
	case protocol.TypeActionCallResolveResponse:
		s.controlResolveResponse(i, line, e)
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
	case protocol.TypeErrorResponse:

		s.settleControlRefusal(i, line, e)
		s.settleToolSourceRefusal(i, line, e)
		s.settleModelsRefusal(i, line, e)
		s.settleSubscribeRefusal(i, line, e)
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

					r.status = p.Status
				}
			}
		}
	default:
		if isRunEvent(e.Type) {
			s.runEvent(i, line, e)
		} else if s.envelopeScoped(e.Type) {

			switch {
			case s.tolerantRunEvent(e):

				s.runEvent(i, line, e)
			case !s.isRequestType(e.Type):

				session, run := unknownScope(e)
				s.checkScope(i, line, e, session, run)
			}
		}
	}

	if e.Type == protocol.TypeSessionMessageSubmitResponse || e.Type == protocol.TypeErrorResponse || e.Type == protocol.TypeSessionOpenResponse {
		s.closeSubmitWindow(e.InReplyTo)
	}

	s.packEnvelope(i, line, e)
}

func isKnownType(t protocol.EnvelopeType) bool {
	return payloadTarget(t) != nil
}

func (s *state) tolerantRunEvent(e protocol.Envelope) bool {
	return s.envelopeScoped(e.Type) && e.RunID != "" && e.Sequence != nil
}

func unknownScope(e protocol.Envelope) (protocol.SessionID, protocol.RunID) {
	var p struct {
		SessionID protocol.SessionID `json:"session_id"`
		RunID     protocol.RunID     `json:"run_id"`
	}
	_ = e.DecodePayload(&p)
	if p.SessionID == "" {
		p.SessionID = e.SessionID
	}
	if p.RunID == "" {
		p.RunID = e.RunID
	}
	return p.SessionID, p.RunID
}

func collectFeatures(dst map[string]protocol.SupportLevel, detail map[string]protocol.FeatureSupport, p protocol.CapabilitiesResponse) {
	keys := make(map[string]bool, len(p.Features))
	for name := range p.Features {
		keys[name] = true
	}
	for _, layer := range p.Layers {
		for name := range layer.Features {
			keys[name] = true
		}
	}
	for name := range keys {
		support, ok := p.EffectiveSupport(name)
		if !ok {
			continue
		}
		dst[name] = support.Level
		detail[name] = support
	}
}

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

	if e.Type != protocol.TypeErrorResponse && req.typ != protocol.TypeProtocolInitializeRequest && req.typ != protocol.TypeCapabilitiesRequest && req.capabilityRevision != "" && string(e.CapabilityRevision) != req.capabilityRevision {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "successful response must repeat the request capability revision", req.capabilityRevision, string(e.CapabilityRevision), string(e.InReplyTo))
	}
	if e.Type == protocol.TypeErrorResponse {

		if req.session != "" && e.SessionID != "" && e.SessionID != req.session {
			s.addExpected(CodeScopeMismatch, i, line, e, "/session_id", "error response session does not match the request scope", string(req.session), string(e.SessionID), string(e.InReplyTo))
		}
		if req.run != "" && e.RunID != "" && e.RunID != req.run {
			s.addExpected(CodeScopeMismatch, i, line, e, "/run_id", "error response run does not match the request scope", string(req.run), string(e.RunID), string(e.InReplyTo))
		}
		return true
	}

	session, run := responseScope(e)
	if s.envelopeScoped(e.Type) {

		session, run = unknownScope(e)
	}

	if req.session != "" && session != req.session {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/session_id", "response session does not match the request scope", string(req.session), string(session), string(e.InReplyTo))
	}
	if req.run != "" && run != req.run {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/run_id", "response run does not match the request scope", string(req.run), string(run), string(e.InReplyTo))
	}

	if req.interaction != "" {
		if got := envelopeInteraction(e); got != "" && got != req.interaction {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/interaction_id", "response interaction does not match the request scope", string(req.interaction), string(got), string(e.InReplyTo))
		}
	}
	return true
}

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
	case protocol.TypeActionCallResolveRequest:
		var p protocol.ActionCallResolveRequest
		_ = e.DecodePayload(&p)
		return p.InteractionID
	case protocol.TypeActionCallResolveResponse:
		var p protocol.ActionCallResolveResponse
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
	case protocol.TypeModelsRequest:
		var p protocol.ModelsRequest
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
	case protocol.TypeActionCallResolveRequest:
		var p protocol.ActionCallResolveRequest
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
	case protocol.TypeActionToolsListRequest:

		var p protocol.ToolsListRequest
		_ = e.DecodePayload(&p)
		if p.SessionID == "" {
			return e.SessionID, ""
		}
		return p.SessionID, ""
	}
	return "", ""
}

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
	case protocol.TypeModelsResponse:
		var p protocol.ModelsResponse
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
	case protocol.TypeActionCallResolveResponse:
		var p protocol.ActionCallResolveResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeUserInputResolveResponse, protocol.TypeUserInputCancelResponse:
		var p protocol.UserInputResolveResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeActionToolsListResponse:
		var p protocol.ToolsListResponse
		_ = e.DecodePayload(&p)
		return p.SessionID, ""
	}
	return "", ""
}
func (s *state) submitResponse(i, line int, e protocol.Envelope) {
	var p protocol.MessageSubmitResponse
	_ = e.DecodePayload(&p)
	s.admitSubmission(i, line, e, p)
}

func (s *state) admitSubmission(i, line int, e protocol.Envelope, p protocol.MessageSubmitResponse) {
	s.checkScope(i, line, e, p.SessionID, p.RunID)

	if req := s.requests[e.InReplyTo]; req != nil {
		request := requestedSubmission(req)
		if p.RequestedDelivery != request.Delivery {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/requested_delivery", "submit response must repeat the requested delivery", string(request.Delivery), string(p.RequestedDelivery), string(e.InReplyTo))
		}
	}
	if !p.Accepted {

		s.addExpected(CodeIllegalRunTransition, i, line, e, "/payload/accepted", "rejected submission must be reported as a correlated error.response, not a submit.response", string(protocol.TypeErrorResponse), string(e.Type), string(e.InReplyTo))
		return
	}

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

			if old.session != p.SessionID {
				s.addExpected(CodeScopeMismatch, i, line, e, "/payload/run_id", "submit response names a run owned by another session", string(old.session), string(p.SessionID), string(old.id))
			}
			return
		}
		s.add(CodeIllegalRunTransition, i, line, e, "/payload/run_id", "run was admitted more than once")
		return
	}
	st := s.sessions[p.SessionID]
	if st == nil {
		st = &sessionTrack{}
		s.sessions[p.SessionID] = st
	}

	overlap := s.queueOverlap(i, line, e, p, st)
	queued := p.Admission == protocol.AdmissionQueued
	if !overlap {
		s.queueAdmission(i, line, e, p, st)
	}
	controls := s.settleSubmitAdmission(i, line, e, p)
	if controls.present && controls.modelPresent && !queued {
		s.applyModelControl(st, controls)
	}
	status, opaque := protocol.RunQueued, s.tolerant && foreignAdmission(p.Admission)
	if opaque {

		status = p.Status
	}
	run := &runState{id: p.RunID, session: p.SessionID, admitted: true, next: 1, lastIndex: i, lastLine: line, admittedModel: p.ModelID, opaqueAdmission: opaque, controls: controls, tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: status}
	run.order = len(st.order)
	run.admittedQueued = queued
	run.admittedAt = i
	run.submitRequest = e.InReplyTo

	run.deferredControls = queued && controls.present && controls.modelPresent
	s.runs[p.RunID] = run
	st.order = append(st.order, p.RunID)

	if !queued {
		if st.active == "" {
			st.active = p.RunID
		} else if prev := s.runs[st.active]; prev == nil || prev.terminal {
			st.active = p.RunID
		}
	}
	s.refreshQueueWindows(p.SessionID)

	s.admitLedEntries(run)
}

func requestedSubmission(req *requestState) protocol.MessageSubmitRequest {
	switch req.typ {
	case protocol.TypeSessionMessageSubmitRequest:
		var request protocol.MessageSubmitRequest
		_ = req.envelope.DecodePayload(&request)
		return request
	case protocol.TypeSessionOpenRequest:
		var open protocol.SessionOpenRequest
		_ = req.envelope.DecodePayload(&open)
		if open.Message == nil {
			return protocol.MessageSubmitRequest{}
		}
		return open.Message.Submit(req.session)
	}
	return protocol.MessageSubmitRequest{}
}

func (s *state) applyModelControl(st *sessionTrack, controls admittedControls) {
	switch controls.mode {
	case protocol.ModePerRun:

		st.expectedDefault, st.guardDefault = st.currentModel, st.currentKnown
	case protocol.ModeSessionMutation:

		st.currentModel, st.currentKnown = controls.model, true
		st.mutated = true
	}
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

	defer s.reconcileDeferred(i, line, e, r)

	defer s.reconcileEveryHeldCatalog()
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
			if e.Type == protocol.TypeRunCancelled && !r.cancelAccepted && !r.recovered {
				s.add(CodeIllegalRunTransition, i, line, e, "/type", "run.cancelled requires accepted cancellation")
			}

			s.add(CodeDuplicateRunTerminal, i, line, e, "/type", "run emitted more than one terminal event")
		} else {
			s.add(CodeEventAfterTerminal, i, line, e, "/type", "run event occurred after terminality")
		}
		return
	}
	s.checkQueueOrder(i, line, e, r)
	if e.Type == protocol.TypeRunStarted {
		if r.started {
			s.add(CodeIllegalRunTransition, i, line, e, "/type", "run.started occurred more than once")
		} else {
			r.started = true
			r.startedAt = i
			r.status = protocol.RunRunning
			s.promote(i, e, r)
		}
		if r.controls.modelPresent && r.controls.mode == protocol.ModeSessionMutation && e.Sequence != nil {

			s.observeModel(r.session, r.controls.model, r.id, *e.Sequence)
		}

		if r.admittedModel != "" {
			var p protocol.RunStartedPayload
			_ = e.DecodePayload(&p)
			switch {
			case p.ModelID == "" && r.controls.modelPresent:

				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "run.started omits the model the run was admitted under", string(r.admittedModel), "absent", string(r.id))
			case p.ModelID != "" && p.ModelID != r.admittedModel:

				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "run.started model disagrees with the admitted model", string(r.admittedModel), string(p.ModelID), string(r.id))
			}
		}
		return
	}

	if !r.started && !preStartSettlement(e.Type) && isKnownType(e.Type) && !r.opaqueAdmission {
		s.add(CodeMissingRunStarted, i, line, e, "/type", "run-scoped event occurred before run.started")
	}
	if e.Type == protocol.TypeRunStatusUpdated {
		var p protocol.RunStatusUpdatedPayload
		_ = e.DecodePayload(&p)
		switch {
		case s.tolerant && (foreignRunStatus(p.Status) || foreignRunStatus(r.status)):

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
		s.checkCallAgainstChoice(i, line, e, r)
		s.checkCallSource(i, line, e)
		s.checkCallOwner(i, line, e)
		s.controlCallRequested(i, line, e, r)

	case protocol.TypeActionCallStarted:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "started")
		s.controlCallEvent(i, line, e, r, "started")
	case protocol.TypeActionCallProgress:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "progress")
	case protocol.TypeActionCallCompleted:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "completed")
		s.controlCallEvent(i, line, e, r, "completed")
	case protocol.TypeActionCallFailed:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "failed")
		s.controlCallEvent(i, line, e, r, "failed")
	case protocol.TypeActionCallCancelled:
		s.feature(i, line, e, "tools")
		s.tool(i, line, e, "cancelled")
		s.controlCallEvent(i, line, e, r, "cancelled")
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

		if e.Type == protocol.TypeRunCancelled && !r.cancelAccepted && !r.recovered {
			s.add(CodeIllegalRunTransition, i, line, e, "/type", "run.cancelled requires accepted cancellation")
		}
		if e.Type == protocol.TypeRunCompleted {

			s.checkCompletedControls(i, line, e, r)
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
		r.terminalAt = i
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

		s.refreshQueueWindows(r.session)
	}
}

func (s *state) applyStateDocument(i, line int, e protocol.Envelope, p protocol.SessionState, st *sessionTrack) {
	if p.ActiveRunID != "" && p.Status == protocol.SessionIdle {
		s.add(CodeSessionStateMismatch, i, line, e, "/payload/status", "idle session cannot have an active run")
	}

	contradiction, excused := false, false
	if prev := st.active; prev != "" && !claimedSettled(p, prev) {

		r := s.runs[prev]
		if r != nil && !r.terminal && p.ActiveRunID != prev {
			if r.startedAt <= s.captureWindowStart(i, e) {
				contradiction = true
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/active_run_id", "snapshot contradicts a nonterminal active run", string(prev), string(p.ActiveRunID), string(prev))
			} else {
				excused = true
			}
		}
	}
	st.status = p.Status
	if !contradiction && !excused {
		st.active = p.ActiveRunID
	}
}

func (s *state) introduceRecoveredRun(i, line int, st *sessionTrack, run *runState, entry *protocol.ActiveRun) {
	run.order = len(st.order)
	run.admittedAt = i
	run.lastIndex, run.lastLine = i, line

	run.recovered = true
	if entry == nil {
		run.priorUnknown = true
	}
	for _, id := range entryPending(entry) {
		run.interactions[id] = &interactionState{opaque: true}
	}
	s.runs[run.id] = run
	st.order = append(st.order, run.id)
	s.refreshQueueWindows(run.session)
}

func entryPending(entry *protocol.ActiveRun) []protocol.InteractionID {
	if entry == nil {
		return nil
	}
	return entry.PendingInteractions
}

func resumeSequence(rec *recoveryExpectation, run protocol.RunID, asOf *uint64) uint64 {
	if asOf != nil {
		return *asOf + 1
	}
	if rec != nil && !rec.gap && rec.cursorSet && (rec.run == "" || rec.run == run) {
		return rec.cursor + 1
	}
	return 1
}

func (s *state) bootstrapRecoveredRuns(i, line int, p protocol.SessionState, st *sessionTrack) {
	rec := s.recoveries[p.SessionID]
	listed := map[protocol.RunID]bool{}
	for _, entry := range p.ActiveRuns {
		listed[entry.RunID] = true
		if s.runs[entry.RunID] != nil {
			continue
		}

		queued := entry.Status == protocol.RunQueued ||
			(entry.Status == protocol.RunCancelling && cancellingHoldsItsPlace(entry))
		s.introduceRecoveredRun(i, line, st, &runState{id: entry.RunID, session: p.SessionID, admitted: true, admittedQueued: queued, started: !queued, next: resumeSequence(rec, entry.RunID, entry.AsOfSequence), tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: entry.Status}, &entry)
	}

	if p.ActiveRunID != "" && !listed[p.ActiveRunID] && s.runs[p.ActiveRunID] == nil {
		s.introduceRecoveredRun(i, line, st, &runState{id: p.ActiveRunID, session: p.SessionID, admitted: true, started: true, next: resumeSequence(rec, p.ActiveRunID, nil), tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: protocol.RunRunning}, nil)
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

		valid = ok && !toolTerminal(current)
	default:

		valid = ok && (current == "started" || current == "progress")
	}
	if !valid && !(!ok && r.recovered) {
		code := CodeIllegalToolTransition
		if !ok && next != "requested" {
			code = CodeUnmatchedTool
		}
		s.add(code, i, line, e, "/tool_call_id", "illegal tool-call lifecycle transition")
	}
	if !ok && r.recovered {

		r.tools[p.ToolCallID] = toolTrack{status: next, owner: p.ExecutionOwner}
	}

	if ok && track.owner != "" && p.ExecutionOwner != track.owner {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/execution_owner", "tool execution owner changed mid-lifecycle", string(track.owner), string(p.ExecutionOwner))
	}

	if ok && p.Source != "" && p.Source != track.source {
		expected := track.source
		if expected == "" {
			expected = "no source"
		}
		s.addExpected(CodeUnmatchedToolSource, i, line, e, "/payload/source", "tool source changed mid-lifecycle", expected, p.Source, string(p.ToolCallID))
	}
	if !ok {
		track.owner = p.ExecutionOwner
		track.source = p.Source
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
	opened := uint64(0)
	if e.Sequence != nil {
		opened = *e.Sequence
	}
	r.interactions[id] = &interactionState{kind: kind, requestedBy: requested, respondedBy: responded, allowCancel: allowCancel, choices: choices, questions: questions, toolCallID: toolCallID, openedAt: opened}
}
func (s *state) interactionResolutionRequest(i, line int, e protocol.Envelope, kind string) {
	id, requested, responded := interactionFields(e, kind)
	x := s.lookupInteraction(e.RunID, id)
	if x == nil {
		if r := s.runs[e.RunID]; r != nil && r.priorUnknown {

			r.interactions[id] = &interactionState{opaque: true}
			return
		}
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution has no pending interaction")
		return
	}
	if x.opaque {

		return
	}
	if x.kind != kind {
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution has the wrong interaction kind")
	}

	if e.Type == protocol.TypeUserInputCancelRequest && !x.allowCancel {
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "interaction was not opened for cancellation")
	}

	if e.Type == protocol.TypeActionPermissionResolveRequest {
		var p protocol.PermissionResolveRequest
		_ = e.DecodePayload(&p)
		if p.ChoiceID != "" && !x.choices[p.ChoiceID] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/choice_id", "resolution selects a choice the permission did not offer", "offered choice", p.ChoiceID, string(id))
		}
	}

	if e.Type == protocol.TypeUserInputResolveRequest {
		var p protocol.UserInputResolveRequest
		_ = e.DecodePayload(&p)
		s.validateInputAnswers(i, line, e, id, x.questions, p.Answers)
	}
	if responded != x.respondedBy || (requested != "" && requested != x.requestedBy) {
		s.addExpected(CodeWrongInteractionResponder, i, line, e, "/payload/responded_by", "only the declared responder may resolve an interaction", string(x.respondedBy), string(responded), string(id))
	}
}

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
		r := s.runs[e.RunID]
		if r == nil || !r.priorUnknown {
			s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution event has no pending interaction")
			return
		}

		x = &interactionState{opaque: true}
		r.interactions[id] = x
	}
	if x.resolved {
		s.add(CodeDuplicateInteraction, i, line, e, "/payload", "interaction was resolved more than once")
	}
	if x.opaque {

		x.resolved = true
		if e.Sequence != nil && x.resolvedAt == 0 {
			x.resolvedAt = *e.Sequence
		}
		return
	}
	if responded != x.respondedBy || requested != x.requestedBy {
		s.addExpected(CodeWrongInteractionResponder, i, line, e, "/payload/responded_by", "resolution ownership differs from request", string(x.respondedBy), string(responded), string(id))
	}

	if x.kind != kind {
		s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution event has the wrong interaction kind", x.kind, kind, string(id))
	}

	if kind == "input" {
		var p protocol.UserInputResolvedPayload
		_ = e.DecodePayload(&p)
		if p.Status == protocol.InputSubmitted {
			s.validateInputAnswers(i, line, e, id, x.questions, p.Answers)
		}
	}

	if kind == "permission" {
		var p protocol.PermissionResolvedPayload
		_ = e.DecodePayload(&p)
		if p.Outcome == protocol.InteractionResolved && p.ChoiceID != "" && !x.choices[p.ChoiceID] {
			s.addExpected(CodeUnmatchedInteraction, i, line, e, "/payload/choice_id", "resolution event selects a choice the permission did not offer", "offered choice", p.ChoiceID, string(id))
		}

		if e.ToolCallID != "" && e.ToolCallID != x.toolCallID {
			s.addExpected(CodeScopeMismatch, i, line, e, "/tool_call_id", "resolution envelope reassigns the request's tool binding", string(x.toolCallID), string(e.ToolCallID))
		}
		if p.ToolCallID != "" && p.ToolCallID != x.toolCallID {
			s.addExpected(CodeScopeMismatch, i, line, e, "/payload/tool_call_id", "resolution payload reassigns the request's tool binding", string(x.toolCallID), string(p.ToolCallID))
		}
	}
	x.resolved = true
	if e.Sequence != nil && x.resolvedAt == 0 {
		x.resolvedAt = *e.Sequence
	}
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

	s.featureKeys(i, line, e, []string{name, "session.message." + name, "agent_control." + name, "action." + name})
}

func (s *state) featureKeys(i, line int, e protocol.Envelope, keys []string) {
	if s.currentCapability == "" || s.capabilitiesStale {
		s.add(CodeUnavailableCapability, i, line, e, "/type", "optional feature requires a current capability descriptor")
		return
	}
	if string(e.CapabilityRevision) != s.currentCapability {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "optional feature must cite the active capability descriptor", s.currentCapability, string(e.CapabilityRevision))
		return
	}
	for _, key := range keys {
		if level, ok := s.features[key]; ok {
			switch level {
			case protocol.SupportNative, protocol.SupportEmulated, protocol.SupportDegraded:
				return
			case protocol.SupportUnavailable:
				s.add(CodeUnavailableCapability, i, line, e, "/type", "event uses capability declared unavailable")
			default:

				s.addExpected(CodeUnavailableCapability, i, line, e, "/type", "event uses capability whose support level is not one this revision recognises; an unknown level is unavailable", "native, emulated, or degraded", string(level))
			}
			return
		}
	}
	s.add(CodeUnavailableCapability, i, line, e, "/type", "optional feature was not affirmatively advertised")
}
func (s *state) close(index int) {
	s.closeQueue()
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

		if r.admitted && !r.started && !r.terminal && !r.opaqueAdmission {
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
