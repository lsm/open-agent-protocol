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
	// controls is the control set the run was admitted with, so the checks
	// that follow are keyed off the request rather than the response.
	controls     admittedControls
	tools        map[protocol.ToolCallID]toolTrack
	interactions map[protocol.InteractionID]*interactionState
	// The queue unit's per-run bookkeeping. order is the run's position in
	// its session's admission order, which is what the ordering rule and
	// active_runs are judged against; admittedQueued records that the run was
	// admitted as a reservation rather than started, so the queued subset a
	// bound counts is the set of reservations still awaiting promotion rather
	// than every run whose start has not yet reached the trace.
	order          int
	admittedQueued bool
	// startedAt is the trace index the run's run.started reached, which is
	// what a capture window is measured against: a run that began after a
	// snapshot was requested is one the snapshot was right not to name.
	startedAt int
	// startSequence is the sequence run.started carried, which is the
	// position a state capture names when it reports the model a promotion
	// installed. terminalAt is the trace index the run settled at, which is
	// what decides whether a snapshot listing it was stale or merely
	// captured before the terminal it could not have seen.
	startSequence uint64
	terminalAt    int
	// admittedAt and submitRequest are the trace positions of the run's
	// admission: the index of its submit response, and the envelope id of the
	// request that admitted it. A state snapshot's membership anchor names the
	// request, and whether an admission fell inside a snapshot's window is
	// decided by the response's index.
	admittedAt    int
	submitRequest protocol.EnvelopeID
	// deferredControls is a queued run's model control, held from admission
	// until promotion: a reservation's session_mutation must not move the
	// session default while an earlier run is still started, and a
	// reservation's per_run snapshot of that default must be taken where the
	// run actually begins.
	deferredControls bool
	// recovered marks a run this trace never saw admitted: a reattach named
	// it and everything it did before the cursor is outside the trace. What a
	// recovered document can state about it is taken from the document; what
	// no document can state — the tool calls it had open, the cancellation it
	// had already accepted — is unknown rather than absent, and a rule that
	// derives its expectation from this trace's history would derive an empty
	// one and convict the endpoint for the disconnect.
	recovered bool
	// priorUnknown marks a run a recovery introduced without saying what it
	// was blocked on. Everything that run did before the cursor is outside
	// this trace, so an interaction id the trace has never carried for it is
	// neither evidence that the interaction exists nor evidence that it does
	// not, and a set derived from history here is empty because the validator
	// saw nothing rather than because nothing happened.
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
	// opaque marks an interaction the trace never saw opened: a recovered
	// entry named it as pending and nothing else about it. Its ownership,
	// kind, questions, choices and tool binding are unknown rather than
	// absent, so nothing is held to them.
	opaque bool
	// openedAt and resolvedAt are the run sequences the interaction joined
	// and left the pending set at, so an active_runs entry that states the
	// position it was captured at is judged there rather than at the position
	// the trace happens to have reached.
	openedAt, resolvedAt uint64
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
	// carriesMessage marks a session.open.request that carried a first
	// message, which makes it the admitting request for the run the open
	// produced — the one request that is an admission without being a submit.
	carriesMessage bool
	// gates are the packed capability gates the request carried, with their
	// advertisement under the descriptor current when it was made.
	gates []packGate
}
type sessionTrack struct {
	status protocol.SessionStatus
	active protocol.RunID
	// currentModel is the model a control-free submission would use, as the
	// last snapshot reported it. expectedDefault is what it must still be
	// after a per_run selection: that application binds its own run and
	// leaves the session default alone, for good rather than for the run's
	// lifetime, so the comparison survives the terminal and any later
	// capability refresh. Only an application the validator credits moves it.
	currentModel    string
	currentKnown    bool
	expectedDefault string
	guardDefault    bool
	// order is every run admitted on the session, in admission order. The
	// queue unit reads the nonterminal prefix of it as the active set.
	order []protocol.RunID
	// openingModel is the session default before any model-affecting event,
	// which is what a state capture marked at the genesis position reports.
	openingModel string
	openingKnown bool
	// mutated records that a session_mutation selection has been applied on
	// this session, after which the opening model is history rather than the
	// current default.
	mutated bool
	// attached is the sanitized projection of the sources the open attached,
	// kept for the session's lifetime: attachment is not revocable in this
	// unit, so every later catalog and snapshot is held to them. attachedOrder
	// fixes the order they are judged in.
	attached      map[string]protocol.ToolSourceDescriptor
	attachedOrder []string
	// toolCatalog is the last tool catalog this session was served, with the revision
	// it was served under, so no sourced call is judged against stale names.
	toolCatalog *sessionCatalog
	// The models unit's per-session bookkeeping: the catalog this session was
	// served and the revision it was served under, the values the session's
	// model took (so a catalog captured mid-flight is judged against the set
	// rather than one instant), the selections no catalog could judge yet, and
	// the catalogs whose declared position the trace has not reached.
	catalog        *modelCatalog
	modelMarks     []modelMark
	unjudgedModels []unjudgedModel
	heldCatalogs   []heldCatalog
}

// toolTrack retains a tool call's lifecycle status and the execution owner that
// opened it; the owner must not change mid-lifecycle. source is the attribution
// the call was requested under, kept for the same reason and needed separately:
// `name` is optional on the progress and terminal payloads, so without the
// requested source retained here a later event could omit the name and name
// another source, and the catalog lookup would miss and accept it merely
// because that source is declared somewhere.
type toolTrack struct {
	status string
	owner  protocol.ParticipantID
	source string
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
	// featureSupports keeps each key's full disclosure — its mode, the modes
	// it enforces, the constraints it declares — beside the level the gate
	// reads, so a refusal can be checked against what the endpoint promised.
	featureSupports map[string]protocol.FeatureSupport
	catalog         []string
	catalogKnown    bool
	// catalogAmbiguous records that the active descriptor's own effective
	// catalog was already diagnosed as ambiguous, so the policy-time check
	// does not blame every submission for the descriptor's one defect.
	catalogAmbiguous bool
	// pendingControls retains what each control-carrying submit request owes
	// its correlated response, keyed by the request's envelope id.
	pendingControls map[protocol.EnvelopeID]*pendingSubmit
	// openSubmits are the unanswered submit requests per session, in arrival
	// order. The queue unit reads them twice: as the window each retained
	// expectation is judged across, and as the in-flight reservations a
	// concurrent admission may already have taken.
	openSubmits map[protocol.SessionID][]*pendingSubmit
	// limits is the admission bounds the active descriptor discloses, or nil
	// when it disclosed none. A refresh replaces them; absence enforces
	// nothing, since absence advertises no bound.
	limits *protocol.CapabilityLimits
	// deferred holds every state claim the trace has not yet reached — a
	// capture position ahead of its run, a settled run whose terminal has not
	// arrived — so an accurate snapshot is reconciled rather than diagnosed.
	deferred []*deferredStateClaim
	// ledGroups is every listing whose leading entries are still being
	// settled, kept so the trace's end can judge what never resolved.
	ledGroups  []*ledGroup
	recoveries map[protocol.SessionID]*recoveryExpectation
	// pendingLists and pendingOpens retain what a catalog request and an
	// attaching open owe their correlated responses, for the same reason
	// pendingControls does: the wire makes a typed refusal the required
	// behaviour, so the gate is settled on the response.
	pendingLists map[protocol.EnvelopeID]*pendingList
	pendingOpens map[protocol.EnvelopeID]*pendingOpen
	// pendingSubscribes are the opens that elected subscribe against an
	// endpoint advertising it, held until the response says whether the
	// endpoint honoured what it advertised.
	pendingSubscribes map[protocol.EnvelopeID]bool
	// descriptorAttribution is the active descriptor's own tool-to-source
	// mapping. A descriptor that publishes a catalog publishes an attribution
	// with it, and until a session-scoped list supersedes it that mapping is
	// the one a call is judged against — the same reason the descriptor's own
	// `sources` are held to every rule a served catalog's are.
	descriptorAttribution map[string]string
	// declaredSources is the active descriptor's declared tool sources,
	// normalized across its layers.
	declaredSources map[string]protocol.ToolSourceDescriptor
	// pendingModels retains what each catalog query owes its correlated
	// response, keyed by the query's envelope id.
	pendingModels map[protocol.EnvelopeID]*pendingModelsQuery
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
	return &state{fixture: f, ids: map[protocol.EnvelopeID]int{}, requests: map[protocol.EnvelopeID]*requestState{}, participants: map[protocol.ParticipantID]bool{}, sessions: map[protocol.SessionID]*sessionTrack{}, runs: map[protocol.RunID]*runState{}, recoveries: map[protocol.SessionID]*recoveryExpectation{}, features: map[string]protocol.SupportLevel{}, featureSupports: map[string]protocol.FeatureSupport{}, pendingControls: map[protocol.EnvelopeID]*pendingSubmit{}, pendingLists: map[protocol.EnvelopeID]*pendingList{}, pendingOpens: map[protocol.EnvelopeID]*pendingOpen{}, pendingSubscribes: map[protocol.EnvelopeID]bool{}, declaredSources: map[string]protocol.ToolSourceDescriptor{}, pendingModels: map[protocol.EnvelopeID]*pendingModelsQuery{}, openSubmits: map[protocol.SessionID][]*pendingSubmit{}}
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
			// An unknown request has no typed payload decoder, but its
			// generic payload scope and its envelope scope are still the
			// wire's own: they must agree, and the result is retained so the
			// generic correlation checks bind its response — a request on
			// run A answered on run B is a scope_mismatch whatever the
			// operation is called.
			session, run = unknownScope(e)
		}
		// A request that declares scope in both its envelope and payload must
		// agree; otherwise its stored correlation scope is self-contradictory.
		// A sequenced unknown request is also a run event below, and runEvent
		// is then its sole scope checker.
		if !s.tolerantRunEvent(e) {
			s.checkScope(i, line, e, session, run)
		}
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
		// What this descriptor said about the catalog before it was replaced,
		// so a response repeating the active revision can be held to repeating
		// the descriptor too. The staleness travels with it: after an
		// announced update the revision has already moved while the features
		// still describe the descriptor being replaced, and comparing those
		// two would hold an announcement against the announcement.
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
		// The descriptor and the catalog served under the old revision are
		// invalidated; a session's expected default model is not, or an
		// unrelated refresh would excuse a per_run selection that moved it.
		// The open-time attachments are not invalidated either: they are
		// session-lifetime facts the next catalog must still carry.
		s.catalog, s.catalogKnown = nil, false
		// The bounds belong to the descriptor that disclosed them; until the
		// refreshed one arrives no bound is advertised, and absence enforces
		// nothing rather than carrying the old numbers forward.
		s.limits = nil
		s.declaredSources = map[string]protocol.ToolSourceDescriptor{}
		s.descriptorAttribution = nil
	case protocol.TypeModelsRequest:
		var p protocol.ModelsRequest
		_ = e.DecodePayload(&p)
		// The envelope/payload agreement was judged with every request's, and
		// the response's scope against this one is judged where responses
		// correlate; what is left is what the query owes.
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
		// Reopening a known session must not clear a tracked nonterminal run:
		// that would let a later admission overlap it unseen.
		st := s.track(p.SessionID)
		if p.Recovery != nil && p.Recovery.Recovered {
			s.bootstrapRecoveredRuns(i, line, p, st)
		}
		// Before the state document is applied and judged, because this
		// response is the admission: the run it names exists from here, and a
		// capture check that ran first would diagnose the open for reporting a
		// run the trace did not carry — a run this very envelope admits.
		s.compoundOpenResponse(i, line, e, p)
		s.applyStateDocument(i, line, e, p, st)
		s.checkSessionCapture(i, line, e, p, st)
		// The open response is a session-state document, so the model it
		// reports is the first value the session is known to hold: what a
		// catalog served before any snapshot is judged against, and the
		// default a per_run selection must leave alone. Without the second a
		// run control admitted before the first snapshot would be guarded
		// against a value nobody had reported, which guards nothing.
		//
		// A response that names no model is left alone rather than recorded as
		// "known to be none": the member is optional, so its absence is
		// silence, and treating silence as a value would guard every later
		// snapshot against the empty string.
		if p.CurrentModelID != "" {
			st.currentModel, st.currentKnown = p.CurrentModelID, true
			if !st.mutated && !st.openingKnown {
				// The open response is a snapshot taken before anything can
				// have moved the default, so a model it states is the value
				// the session opened on — which is what a capture marked at
				// genesis reports, and the only thing such a capture can be
				// judged against. A promotion inside the first read's window
				// is one that capture may ignore, so without this the first
				// snapshot answers to nothing and the mutation behind it
				// stops the opening value from ever being learned.
				//
				// Only a model the response states. What an absent member
				// means here is the same question the line above leaves
				// alone, and this leaves it alone too.
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
		// A per_run application binds its own run and leaves the session
		// default untouched; a snapshot reporting anything else — the run's
		// model or a third one — says the endpoint moved it.
		if st.guardDefault && p.CurrentModelID != st.expectedDefault {
			s.addExpected(CodeUnappliedControl, i, line, e, "/payload/current_model_id", "a per_run model selection moved the session default", st.expectedDefault, p.CurrentModelID)
		}
		s.checkSessionCapture(i, line, e, p, st)
		st.currentModel, st.currentKnown = p.CurrentModelID, true
		if !st.mutated && !st.openingKnown {
			// The session default before any model-affecting event is what a
			// capture marked at the genesis position reports; it is learned
			// from the first snapshot taken before the first mutation.
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
			// A requested delivery outside this revision's vocabulary is
			// opaque in tolerant mode: which capability key it needs is a
			// later revision's rule, not one this validator can apply.
			//
			// queue is the exception the queue unit makes: the wire requires
			// an unadvertised queue to be refused, so diagnosing the request
			// would fail the conduct the protocol mandates. Its gate is
			// retained by submitControls and settled on the correlated
			// response, as T1's control gate is. T4 moves steer the same way.
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
		// The catalog is gated on action.tools.list, never on the action.tools
		// family key: that key means lifecycle observation, and several
		// adapters advertise it while stating outright that they expose no
		// portable catalog, so aliasing it would let a served catalog pass a
		// gate the endpoint never claimed. The gate is settled on the
		// correlated response, because refusing a catalog an endpoint does not
		// serve is the conduct the wire requires.
		s.toolsListRequest(i, line, e)
	case protocol.TypeActionToolsListResponse:
		s.toolsListResponse(i, line, e)
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
		// A refusal is the required behaviour for a control the endpoint
		// cannot honour, so the refusal itself is what the gate judges. A
		// catalog query the endpoint cannot serve is refused on the same
		// terms.
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
		} else if s.envelopeScoped(e.Type) {
			// A type this revision does not define is classified by its wire
			// scope, whether it is tolerated or claimed by a loaded pack.
			// Whatever the type is called, the generic session_id and run_id
			// its payload declares must agree with its envelope (a request or
			// response was already held to that above). An envelope carrying
			// both run_id and sequence is a run-scoped event and enters the
			// type-independent bookkeeping (an accepted admission, sequence
			// contiguity, terminality); one carrying session_id alone is
			// session-scoped and advances no cursor; one carrying neither
			// touches no bookkeeping at all.
			switch {
			case s.tolerantRunEvent(e):
				// runEvent is the sole scope checker for a run event; a
				// second generic check here would report one defect twice.
				s.runEvent(i, line, e)
			case !s.isRequestType(e.Type):
				// An unknown request was held to this above. An unknown
				// response is held to it here, whether or not it correlated
				// with a request, as a known response is in its own case.
				session, run := unknownScope(e)
				s.checkScope(i, line, e, session, run)
			}
		}
	}
	// A submit window closes once its correlated response has been judged:
	// every retained queue expectation is settled across the window, so the
	// window must still be open while the response is read.
	if e.Type == protocol.TypeSessionMessageSubmitResponse || e.Type == protocol.TypeErrorResponse {
		s.closeSubmitWindow(e.InReplyTo)
	}
	// The pack rules run after the core case so a packed member on a core
	// response is judged against the descriptor that response installs: the
	// initial capabilities.response advertises the very key its own packed
	// member is gated on, and no revision is current before it.
	s.packEnvelope(i, line, e)
}

// isKnownType reports whether the type is one this revision defines. The
// payload table is the authority: every known type has a decode target.
func isKnownType(t protocol.EnvelopeType) bool {
	return payloadTarget(t) != nil
}

// tolerantRunEvent reports whether an envelope whose type this revision does
// not define is classified as a run-scoped event: it carries both run_id and
// sequence. runEvent then does its scope check and run bookkeeping. A type a
// loaded pack claims is classified the same way as a tolerated one — a pack
// adds vocabulary, not a second lifecycle — so a packed run event advances the
// run cursor rather than leaving a gap for the next core event to be blamed
// for.
func (s *state) tolerantRunEvent(e protocol.Envelope) bool {
	return s.envelopeScoped(e.Type) && e.RunID != "" && e.Sequence != nil
}

// unknownScope is the scope of an envelope whose type this revision does not
// define (tolerant mode). The generic payload members session_id and run_id
// are read when present, so a payload declaring a scope other than the
// envelope's is still a scope_mismatch; the envelope's own scope stands in
// for an absent member, since an unknown type's payload owes none.
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

// collectFeatures indexes every key a descriptor discloses anywhere — its
// top-level features and each layer's — resolving each one through
// CapabilityDescriptor.EffectiveSupport so the state machine's gate and the
// descriptor-time checks read a layered descriptor identically. Iterating the
// layers directly and letting the last write win would make the answer depend
// on Go's map iteration order whenever two sections named one key.
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
		// generic payload members and its envelope; the correlation check
		// binds it to the request it answers. Its own envelope/payload
		// agreement is judged in apply's generic unknown path, whether or
		// not it correlated, as a known response's is in its own case.
		session, run = unknownScope(e)
	}
	// A response that names no scope at all cannot be shown to answer within
	// the request's scope, so an empty value is a mismatch too. A known
	// response always names its scope (schema-required), so this only ever
	// bites an unknown response whose envelope and payload are both silent.
	if req.session != "" && session != req.session {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/session_id", "response session does not match the request scope", string(req.session), string(session), string(e.InReplyTo))
	}
	if req.run != "" && run != req.run {
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
	case protocol.TypeUserInputResolveRequest:
		var p protocol.UserInputResolveRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeUserInputCancelRequest:
		var p protocol.UserInputCancelRequest
		_ = e.DecodePayload(&p)
		return p.SessionID, p.RunID
	case protocol.TypeActionToolsListRequest:
		// A list request that names a session asks for that session's
		// effective catalog, so the correlation checks bind its response to
		// the same scope: an unscoped response to a scoped request is a
		// scope_mismatch, not an endpoint-level catalog, and an adapter
		// cannot evade the lifetime-catalog check by dropping the scope.
		//
		// The payload's session is optional here, unlike every request above,
		// because an unscoped list asks for the endpoint's own catalog. So a
		// request that names its session on the envelope alone still names it:
		// without this fallback the correlation scope would be empty and a
		// response repeating nothing would answer a scoped request unjudged,
		// which is the one shape this check exists to reject.
		var p protocol.ToolsListRequest
		_ = e.DecodePayload(&p)
		if p.SessionID == "" {
			return e.SessionID, ""
		}
		return p.SessionID, ""
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
	st := s.sessions[p.SessionID]
	if st == nil {
		st = &sessionTrack{}
		s.sessions[p.SessionID] = st
	}
	// A second admission on a session that already has a nonterminal run is
	// the queue unit's admission: legal only as a reservation, and only where
	// the descriptor advertises the queue. Anything else keeps decision
	// 0001's one-nonterminal-run rule.
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
		// The run's status is what the response declared, not the queued
		// shape's: a known status is judged from there, a foreign one is
		// opaque until a known status is reached (see run.status.updated).
		status = p.Status
	}
	run := &runState{id: p.RunID, session: p.SessionID, admitted: true, next: 1, lastIndex: i, lastLine: line, admittedModel: p.ModelID, opaqueAdmission: opaque, controls: controls, tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: status}
	run.order = len(st.order)
	run.admittedQueued = queued
	run.admittedAt = i
	run.submitRequest = e.InReplyTo
	// A reservation's model control is held until promotion: its
	// session_mutation must not move the session default while an earlier run
	// is still started, and its per_run snapshot of that default has to be
	// taken where the run begins rather than where it was reserved.
	run.deferredControls = queued && controls.present && controls.modelPresent
	s.runs[p.RunID] = run
	st.order = append(st.order, p.RunID)
	// st.active is the run a snapshot has to keep naming, so it tracks the
	// started run and nothing else. A reservation is not one: active_run_id
	// stays absent until it starts, and recording it here made a snapshot that
	// correctly reports no started run read as erasing a live one. The pointer
	// moves at the promotion instead, which is where the run actually begins.
	if !queued {
		if st.active == "" {
			st.active = p.RunID
		} else if prev := s.runs[st.active]; prev == nil || prev.terminal {
			st.active = p.RunID
		}
	}
	s.refreshQueueWindows(p.SessionID)
	// A snapshot may name a run whose admission is still in flight. This is
	// that admission: the entries that led it are judged here, where what
	// they were waiting on is finally known.
	s.admitLedEntries(run)
}

// applyModelControl moves the session's expected default for one admitted
// model selection, in the way the disclosed mode says it moves.
func (s *state) applyModelControl(st *sessionTrack, controls admittedControls) {
	switch controls.mode {
	case protocol.ModePerRun:
		// The default the run was admitted against is what every later
		// snapshot is judged against, and only an application the
		// validator credits moves it.
		st.expectedDefault, st.guardDefault = st.currentModel, st.currentKnown
	case protocol.ModeSessionMutation:
		// A session mutation changes the session default where it is
		// applied — at admission for a started run, at promotion for a
		// reservation — and the run that applied it is the authority every
		// snapshot taken while it is started is judged against
		// (premature_session_mutation). The per_run guard stays off: this
		// selection is meant to move the default.
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
		// A placeholder so the rest of this run's events are read rather than
		// dropped, and the only created run deliberately left out of the
		// session's admission order: admission order is precisely what this
		// run does not have, the trace has already been convicted for that,
		// and counting it against the queue would diagnose the same fault a
		// second time under another name.
		r = &runState{id: e.RunID, session: e.SessionID, next: 1, tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}}
		s.runs[e.RunID] = r
	}
	r.lastIndex = i
	r.lastLine = line
	// Every state claim the trace had not reached is reconciled against this
	// envelope once the run's own bookkeeping is done, whichever branch
	// below returns: a capture position the run has now reached, and a
	// settled run's claimed terminal, which must be the next envelope its
	// domain publishes.
	defer s.reconcileDeferred(i, line, e, r)
	// A catalog held against a position in this run may become settleable
	// here: this event moves the run's cursor, and may itself record the model
	// the catalog named, or simply reveal whose run it is. It is settled after
	// the event is fully interpreted, so a model this event records is in
	// evidence; for every event rather than only the ones that record a model,
	// because an event that moves no model still settles the claim that rested
	// on it; and across every session, because the session holding the claim
	// need not be the one this run belongs to.
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
			// A pre-start-settled run is still settled: its terminal is its
			// one absorbing event whether or not run.started ever appeared.
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
			// A session mutation runs immediately before the run it was
			// requested for starts, so the start is the model-affecting event
			// a catalog's as_of_model_event can name.
			s.observeModel(r.session, r.controls.model, r.id, *e.Sequence)
		}
		// The admitted model is authoritative for the run: a started event naming
		// a different model would misattribute the same execution.
		if r.admittedModel != "" {
			var p protocol.RunStartedPayload
			_ = e.DecodePayload(&p)
			switch {
			case p.ModelID == "" && r.controls.modelPresent:
				// An admitted model_id is authoritative for the run and
				// run.started repeats it. Omitting it is not silence about a
				// model nobody chose: the caller chose one, and a consumer
				// reading the start boundary cannot see that the control was
				// applied — which is the whole of what the repeat is for.
				// A run whose submission carried no model_id keeps the
				// present-only comparison: there the id is attribution the
				// endpoint volunteers, not a control it owes.
				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "run.started omits the model the run was admitted under", string(r.admittedModel), "absent", string(r.id))
			case p.ModelID != "" && p.ModelID != r.admittedModel:
				// The run was admitted under one model and started under
				// another: the control was admitted and not applied.
				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "run.started model disagrees with the admitted model", string(r.admittedModel), string(p.ModelID), string(r.id))
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
		s.checkCallAgainstChoice(i, line, e, r)
		s.checkCallSource(i, line, e)
	// The catalog resolution runs on the requested event alone, which is where
	// the attribution is established; every later event of the same call is
	// bound to it by the mid-lifecycle check in tool(), which needs no name
	// and so cannot be evaded by omitting one.
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
		// A cancel exchange is evidence, and a run this trace never saw
		// admitted may have been cancelled before the cursor: the exchange
		// that accepted it is behind the disconnect and no envelope for it
		// will arrive. Requiring it here would convict the one endpoint that
		// answered the reattach honestly.
		if e.Type == protocol.TypeRunCancelled && !r.cancelAccepted && !r.recovered {
			s.add(CodeIllegalRunTransition, i, line, e, "/type", "run.cancelled requires accepted cancellation")
		}
		if e.Type == protocol.TypeRunCompleted {
			// A tool_choice's positive requirements bind a completed
			// response; a run that fails or is cancelled first is not judged.
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
		// A terminal frees a reservation, and every open submit window on the
		// session is re-reckoned against the set as it now stands.
		s.refreshQueueWindows(r.session)
	}
}

// applyStateDocument is what every session-state document says about its
// session, wherever it arrives. session.open.response carries the same
// document — SessionOpenResponse is SessionState, and the schema defines the
// open response as that document — and a client reattaching reads it as its
// initial state, so a rule keyed to the response type rather than to the
// document is a rule with a hole in it. The model bookkeeping stays with each
// branch, because an open response that names no model is silent where a state
// response is authoritative.
func (s *state) applyStateDocument(i, line int, e protocol.Envelope, p protocol.SessionState, st *sessionTrack) {
	if p.ActiveRunID != "" && p.Status == protocol.SessionIdle {
		s.add(CodeSessionStateMismatch, i, line, e, "/payload/status", "idle session cannot have an active run")
	}
	// A snapshot may not erase a run the trace has admitted and not terminated:
	// that would allow an overlapping second admission on one session. Keep the
	// tracked run when the snapshot contradicts it.
	contradiction, excused := false, false
	if prev := st.active; prev != "" && !claimedSettled(p, prev) {
		// A snapshot that claims it already removed the run states so in
		// as_of.settled, and that claim is judged on its own terms — the
		// terminal it names must be the next thing the run publishes. It
		// is not a contradiction, so it is not diagnosed twice, and the
		// pointer follows the snapshot because the snapshot said the run
		// is over and answers for saying so.
		//
		// A run that began after the read was requested is different. A
		// promotion happens inside the endpoint and its run.started can
		// drain after the response, so a snapshot naming no started run
		// where none had started is describing the moment it was taken —
		// but excusing that omission is not agreeing with it. The run did
		// start, nothing reconciles the omission later, and letting the
		// snapshot clear the pointer would leave every snapshot after it
		// free to omit the run as well.
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

// bootstrapRecoveredRuns registers the runs a recovered open response
// introduces. A reattach joins a session already under way: the response is
// the first thing the trace hears about its runs and is authoritative about
// them by construction, since there is no earlier admission for it to
// contradict. So the runs it lists are taken from it, and the document is then
// held to everything that does not depend on where they came from — that it
// lists each of them once, that none of them has already settled, that it
// names the one it says is executing and only one, and that its own status
// agrees with the rest of it. An open response that names runs without
// declaring a recovery declares nothing that could have created them, and its
// entries are runs from nowhere like any others.
// introduceRecoveredRun registers a run a recovery names that the trace has
// never carried. It is what an admission does minus the admission itself: the
// run table, the session's admission order, and the position the run entered
// it at, followed by the windows every submission still in flight is judged
// in. A run entered in the table alone is a run the queue rules cannot see —
// every accounting and ordering check reads the session's order, so a run
// missing from it leaves the session looking empty and a second started
// admission beside it escapes both the overlap rule and execution order.
func (s *state) introduceRecoveredRun(i, line int, st *sessionTrack, run *runState, entry *protocol.ActiveRun) {
	run.order = len(st.order)
	run.admittedAt = i
	run.lastIndex, run.lastLine = i, line
	// The document is also the trace's first authoritative word on what the
	// run is blocked on. An entry that names its pending interactions names
	// ones the trace will never see opened — their requests are behind the
	// cursor — so they are recorded as pending from before every position this
	// trace can state, and recorded opaquely, because naming an interaction is
	// not describing it. Where no entry introduced the run the document said
	// nothing at all, and nothing is what the validator knows.
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

// resumeSequence is where a run a recovery introduces picks its trace up: the
// position the entry states, then the cursor the recovery resumed from, then
// the beginning. Both recovery paths take it from here rather than each
// deciding, because they ask one question of one recovery block, and the
// answer was right in only one of them.
//
// The cursor belongs to the run the recovery names, so a second entry beside it
// — a reservation the reattach also carries — starts from the beginning rather
// than from a position that was never about it. A declared gap states no
// position at all: its cursor is the retained boundary the endpoint could not
// serve from, not where the events that follow come from, so counting from it
// would diagnose the endpoint for the gap it declared.
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
		// The entry's own shape is what the reattach knows. A queued one is a
		// reservation that has not begun; a cancelling one says nothing about
		// whether its run began, so it answers with the queue place it reports
		// holding, exactly as it does where the trace has not reached its
		// start; anything else is a run that has begun, before everything this
		// trace can see.
		queued := entry.Status == protocol.RunQueued ||
			(entry.Status == protocol.RunCancelling && cancellingHoldsItsPlace(entry))
		s.introduceRecoveredRun(i, line, st, &runState{id: entry.RunID, session: p.SessionID, admitted: true, admittedQueued: queued, started: !queued, next: resumeSequence(rec, entry.RunID, entry.AsOfSequence), tools: map[protocol.ToolCallID]toolTrack{}, interactions: map[protocol.InteractionID]*interactionState{}, status: entry.Status}, &entry)
	}
	// active_runs is required only where active_run_id cannot carry the
	// answer, so a reattach whose session holds one started run says so with
	// the pointer alone, and that run is introduced by the document just as an
	// entry would be. Without it the retained replay that may follow the open
	// response directly has no admission behind it and no position to resume
	// from — the shape the state-response path has always bootstrapped, and
	// the one the listing never sees because there is no listing.
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
		// conformance.md:143-146 permits a call cancelled or denied before
		// execution starts, so cancellation from requested stays valid; a
		// terminal call cannot terminate twice.
		valid = ok && !toolTerminal(current)
	default:
		// completed / failed require execution to have begun: conformance.md:145
		// emits exactly one terminal event for each *started* tool call.
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
		// No document names the tool calls a recovered run had open, so a
		// call this trace never saw requested is one that opened before the
		// cursor rather than one that never opened. Its lifecycle is picked up
		// from here: what this trace does see of it is judged as any other
		// call's, and only the part it cannot see stands down.
		r.tools[p.ToolCallID] = toolTrack{status: next, owner: p.ExecutionOwner}
	}
	// execution_owner is required on every action-call payload and must not be
	// reassigned mid-lifecycle.
	if ok && track.owner != "" && p.ExecutionOwner != track.owner {
		s.addExpected(CodeScopeMismatch, i, line, e, "/payload/execution_owner", "tool execution owner changed mid-lifecycle", string(track.owner), string(p.ExecutionOwner))
	}
	// The attribution is held the same way, and more strictly: `source` is
	// optional, so an absent one on a later event carries no attribution and
	// says nothing, while a present one that differs from the requested
	// source — including one introduced where the request named none — has
	// moved the call to another endpoint mid-lifecycle.
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
			// Same as the resolution event: a run introduced by a recovery
			// that said nothing about what it was blocked on may be asked to
			// resolve an interaction opened before the cursor, and unmatched
			// here is what the validator does not know.
			r.interactions[id] = &interactionState{opaque: true}
			return
		}
		s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution has no pending interaction")
		return
	}
	if x.opaque {
		// Answering a recovered interaction is what reattaching to a blocked
		// run is for: the client reads pending_interactions and resolves what
		// it finds there. The request that opened it is behind the cursor, so
		// its kind, ownership, cancellation policy, offered choices and
		// questions were never stated to this trace — they are unknown, not
		// empty, and a request cannot be held to fields nobody stated. The
		// event side stands the same checks down for the same reason.
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
		r := s.runs[e.RunID]
		if r == nil || !r.priorUnknown {
			s.add(CodeUnmatchedInteraction, i, line, e, "/payload", "resolution event has no pending interaction")
			return
		}
		// The run entered this trace through a recovery that said nothing
		// about what it was blocked on, so an interaction it resolves may have
		// been opened before the cursor and no envelope for it will ever
		// arrive. Unmatched here is what the validator does not know, not what
		// the endpoint got wrong. The resolution is still recorded, so a later
		// snapshot that keeps the interaction pending is judged against it.
		x = &interactionState{opaque: true}
		r.interactions[id] = x
	}
	if x.resolved {
		s.add(CodeDuplicateInteraction, i, line, e, "/payload", "interaction was resolved more than once")
	}
	if x.opaque {
		// The request is behind the recovery cursor: a recovered entry named
		// this interaction as pending and said nothing else about it. Its
		// ownership, kind, questions, choices and tool binding are unknown,
		// not absent, and a resolution cannot answer to fields nobody stated.
		// That it is resolved once, and that it leaves the pending set where
		// it does, are facts of this trace and stay judged.
		x.resolved = true
		if e.Sequence != nil && x.resolvedAt == 0 {
			x.resolvedAt = *e.Sequence
		}
		return
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
	// A core feature name is shorthand for the descriptor keys it may live
	// under; a packed key (see packFeature) is never expanded.
	s.featureKeys(i, line, e, []string{name, "session.message." + name, "agent_control." + name, "action." + name})
}

// featureKeys judges an envelope's use of an optional feature against the
// first of the given descriptor keys the descriptor names.
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
		// Whether run.started was owed at all is unknown under a foreign
		// admission (see runState.opaqueAdmission); a terminal always is.
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
