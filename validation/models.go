package validation

import (
	"reflect"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The session-scoped model catalog (unit `models`). A catalog is a promise in
// two directions: every id it lists is selectable, and every id it omits is
// not. Both directions are judged on the correlated response, as the run
// controls are, because the wire makes refusal the required behaviour for a
// query the endpoint cannot serve.

// pendingModelsQuery is what one models.request left for its response to
// settle: the refusal it owes, or the fact that it owes none.
type pendingModelsQuery struct {
	expectation *controlExpectation
	// satisfiable records that the key was advertised and, where degraded,
	// opted into. A refusal of such a query is one the endpoint's own
	// descriptor says it could have served.
	satisfiable bool
	// window is the index into the session's model marks of the value in
	// force when the query was made. A catalog captured while a mutation is
	// in flight may report any value the session held between the query and
	// its response, so the rule takes the set rather than one endpoint value.
	window int
}

// modelMark is one value the session's model took, with the run-scoped event
// that set it where there was one. A snapshot carries no position.
type modelMark struct {
	model    string
	run      protocol.RunID
	sequence uint64
}

// unjudgedModel is a selection made under a revision whose catalog the trace
// has not served yet. The first catalog under that revision reconciles it, so
// an endpoint cannot accept an unlisted model in the gap between a refresh and
// its catalog and have the gap hide it.
type unjudgedModel struct {
	model      string
	revision   string
	admitted   bool
	index      int
	line       int
	envelope   protocol.Envelope
	submission protocol.EnvelopeID
}

// heldCatalog is a models.response naming a model event the trace has not
// reached. It is reconciled when the event arrives, as a snapshot's
// as_of_sequence is.
type heldCatalog struct {
	model    string
	position protocol.ModelEventPosition
	index    int
	line     int
	envelope protocol.Envelope
}

// modelCatalog is the catalog one session was served, bound to the capability
// revision it was served under.
type modelCatalog struct {
	revision string
	known    bool
	// binding says the catalog governs admissions: only a native or emulated
	// catalog does. A degraded one discloses that it refreshes out of band,
	// so an earlier admission may have matched a list that was never served.
	binding bool
	ids     map[string]bool
	models  map[string]protocol.ModelDescriptor
}

// binds reports whether this catalog governs a selection made under revision.
func (c *modelCatalog) binds(revision string) bool {
	return c != nil && c.known && c.binding && c.revision == revision && revision != ""
}

// track returns the session's bookkeeping, creating it when a models envelope
// is the first thing the trace says about the session.
func (s *state) track(session protocol.SessionID) *sessionTrack {
	st := s.sessions[session]
	if st == nil {
		st = &sessionTrack{}
		s.sessions[session] = st
	}
	return st
}

// observeModel records a value the session's model took. A snapshot carries no
// position; a session_mutation's application is positioned at the run event
// that applied it, which is what a catalog's as_of_model_event can name.
func (s *state) observeModel(session protocol.SessionID, model string, run protocol.RunID, sequence uint64) {
	if model == "" {
		return
	}
	st := s.track(session)
	st.modelMarks = append(st.modelMarks, modelMark{model: model, run: run, sequence: sequence})
	s.reconcileHeldCatalogs(st)
}

// modelsRequest retains what one catalog query owes its correlated response.
// Nothing is diagnosed on the query itself: refusing a query the endpoint
// cannot serve is the required behaviour, and a conforming refusal must
// validate.
func (s *state) modelsRequest(i, line int, e protocol.Envelope, p protocol.ModelsRequest) {
	st := s.track(p.SessionID)
	pending := &pendingModelsQuery{window: len(st.modelMarks) - 1}
	if pending.window < 0 {
		pending.window = 0
	}
	defer func() { s.pendingModels[e.ID] = pending }()
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureModelsList)
	if !judged {
		// The descriptor is missing or stale, or the envelope cites the wrong
		// revision. Those branches are diagnosed on the request, as for every
		// optional envelope, and nothing is retained: which level the key has
		// cannot be read at all.
		return
	}
	switch {
	case !affirmative(level):
		// The capability rung's diagnostic on the served catalog is the gate's
		// own unavailable_capability, raised where the catalog is served; this
		// expectation exists to judge the refusal instead.
		pending.expectation = &controlExpectation{
			rung: rungCapability, key: protocol.FeatureModelsList, pointer: "/payload",
			code: errorUnsupportedFeature, reason: reasonUnadvertised,
			detailName: "feature", detailValue: protocol.FeatureModelsList,
			diagnostic: CodeUnavailableCapability,
			message:    "catalog query refused under a shape that does not name the unadvertised capability",
		}
	case level == protocol.SupportDegraded && !p.AllowsDegraded(protocol.FeatureModelsList):
		pending.expectation = &controlExpectation{
			rung: rungDegradation, key: protocol.FeatureModelsList, pointer: "/payload/allow_degraded_features",
			code: errorCapabilityDegraded, detailName: "feature", detailValue: protocol.FeatureModelsList,
			diagnostic: CodeDegradedWithoutOptin,
			message:    "catalog query omits the opt-in a degraded models.list requires",
		}
	default:
		pending.satisfiable = true
	}
}

// modelsResponse judges one served catalog: the gate it was served under, its
// internal consistency, the session model it reports, its stability within a
// capability revision, and the selections it retroactively settles.
func (s *state) modelsResponse(i, line int, e protocol.Envelope, p protocol.ModelsResponse) {
	// Nothing is applied without being advertised: a catalog served under a
	// key the descriptor omits is the endpoint acting on a capability it does
	// not have.
	s.featureKeys(i, line, e, []string{protocol.FeatureModelsList})
	if query := s.pendingModels[e.InReplyTo]; query != nil && query.expectation != nil && query.expectation.rung == rungDegradation {
		s.addExpected(CodeDegradedWithoutOptin, i, line, e, "/payload", "degraded catalog served without the caller's opt-in", "capability_degraded naming "+protocol.FeatureModelsList, "a served catalog", string(e.InReplyTo))
	}
	ids := make(map[string]bool, len(p.Models))
	defaults := 0
	duplicate := ""
	for _, model := range p.Models {
		if ids[model.ID] && duplicate == "" {
			duplicate = model.ID
		}
		ids[model.ID] = true
		if model.Default {
			defaults++
		}
	}
	if duplicate != "" {
		s.addExpected(CodeDuplicateModelID, i, line, e, "/payload/models", "catalog lists two descriptors under one id, so an accepted model_id denotes neither", "one descriptor per id", duplicate)
	}
	if defaults > 1 {
		s.addExpected(CodeAmbiguousDefaultModel, i, line, e, "/payload/models", "catalog marks more than one model as the default", "at most one default", uintString(uint64(defaults)))
	}
	if p.CurrentModelID != "" && !ids[p.CurrentModelID] {
		// A picker shown a current model the catalog does not describe could
		// not resolve it, and re-selecting the same id would be refused by the
		// catalog rule, so the adapter that served it is diagnosed.
		s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/current_model_id", "catalog reports a current model it does not list", "one of the catalog's own ids", p.CurrentModelID)
	}
	st := s.track(p.SessionID)
	s.checkCatalogCurrentModel(i, line, e, p, st)
	level := s.features[protocol.FeatureModelsList]
	binding := level == protocol.SupportNative || level == protocol.SupportEmulated
	served := &modelCatalog{revision: s.currentCapability, known: true, binding: binding, ids: ids, models: map[string]protocol.ModelDescriptor{}}
	for _, model := range p.Models {
		served.models[model.ID] = model
	}
	if binding && st.catalog != nil && st.catalog.known && st.catalog.binding && st.catalog.revision == served.revision && !reflect.DeepEqual(st.catalog.models, served.models) {
		// The wire makes a catalog change a capability invalidation, so
		// availability may not change without capabilities.updated. At
		// degraded the descriptor's reason discloses out-of-band refresh and
		// the check stands down, which is what degraded means here.
		s.addExpected(CodeUnannouncedCatalogChange, i, line, e, "/payload/models", "catalog changed under one capability revision without a capabilities.updated", "the catalog served under "+served.revision, "a different catalog")
	}
	st.catalog = served
	s.reconcileUnjudgedModels(st, served)
}

// checkCatalogCurrentModel judges the current model a catalog reports against
// the values the session actually held. A catalog overlapping a mutation is
// captured at an instant the trace cannot name, so the rule takes the set of
// values the model held across the query's window and diagnoses only a value
// that was never the session's model within it. A catalog that carries its own
// position is judged at that point instead, and one naming a position the
// trace has not reached is held until it arrives.
func (s *state) checkCatalogCurrentModel(i, line int, e protocol.Envelope, p protocol.ModelsResponse, st *sessionTrack) {
	if p.CurrentModelID == "" {
		return
	}
	if position := p.AsOfModelEvent; position != nil {
		if mark, ok := st.markAt(*position); ok {
			if mark != p.CurrentModelID {
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/current_model_id", "catalog reports a model the session did not hold at the event it names", mark, p.CurrentModelID, string(position.RunID))
			}
			return
		}
		if !s.positionReached(*position) {
			// An endpoint reporting a position the trace has not reached is
			// ahead of it, not wrong: the claim is reconciled when the event
			// arrives.
			st.heldCatalogs = append(st.heldCatalogs, heldCatalog{model: p.CurrentModelID, position: *position, index: i, line: line, envelope: e})
			return
		}
		// The named position is one the trace passed without it moving the
		// session model, so the position says nothing and the window rule
		// stands.
	}
	window := st.modelWindow(s.pendingModels[e.InReplyTo])
	if len(window) == 0 {
		// The trace has never reported this session's model, so there is
		// nothing to contradict.
		return
	}
	for _, model := range window {
		if model == p.CurrentModelID {
			return
		}
	}
	s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/current_model_id", "catalog reports a model the session never held while the query was in flight", window[len(window)-1], p.CurrentModelID)
}

// markAt reports the model a positioned mark recorded, if the session has one
// at exactly that run and sequence.
func (t *sessionTrack) markAt(position protocol.ModelEventPosition) (string, bool) {
	for _, mark := range t.modelMarks {
		if mark.run == position.RunID && mark.sequence == position.Sequence {
			return mark.model, true
		}
	}
	return "", false
}

// modelWindow is the set of values the session's model held between one query
// and its response, ending with the value in force now.
func (t *sessionTrack) modelWindow(query *pendingModelsQuery) []string {
	start := 0
	if query != nil && query.window < len(t.modelMarks) {
		start = query.window
	}
	window := make([]string, 0, len(t.modelMarks)-start)
	for _, mark := range t.modelMarks[start:] {
		window = append(window, mark.model)
	}
	return window
}

// positionReached reports whether the trace has already carried the run-scoped
// event a catalog names.
func (s *state) positionReached(position protocol.ModelEventPosition) bool {
	r := s.runs[position.RunID]
	return r != nil && r.next > position.Sequence
}

// reconcileHeldCatalogs settles the catalogs that named a model event the
// trace had not reached when they arrived.
func (s *state) reconcileHeldCatalogs(st *sessionTrack) {
	remaining := st.heldCatalogs[:0]
	for _, held := range st.heldCatalogs {
		mark, ok := st.markAt(held.position)
		if !ok {
			remaining = append(remaining, held)
			continue
		}
		if mark != held.model {
			s.addExpected(CodeSessionStateMismatch, held.index, held.line, held.envelope, "/payload/current_model_id", "catalog reports a model the session did not hold at the event it names", mark, held.model, string(held.position.RunID))
		}
	}
	st.heldCatalogs = remaining
}

// retainModelSelection holds a selection made under a revision whose catalog
// the trace has not served, so the first catalog under that revision settles
// it rather than the gap hiding it.
func (s *state) retainModelSelection(session protocol.SessionID, entry unjudgedModel) {
	st := s.track(session)
	st.unjudgedModels = append(st.unjudgedModels, entry)
}

// reconcileUnjudgedModels settles every selection retained under this
// catalog's revision. A retained admission the catalog omits is the endpoint
// accepting a model it does not serve; a retained refusal is held to the same
// code-and-detail test the immediately judged path applies, because deferring
// the judgement must not weaken it.
func (s *state) reconcileUnjudgedModels(st *sessionTrack, served *modelCatalog) {
	if !served.binds(served.revision) {
		return
	}
	remaining := st.unjudgedModels[:0]
	for _, entry := range st.unjudgedModels {
		if entry.revision != served.revision {
			remaining = append(remaining, entry)
			continue
		}
		listed := served.ids[entry.model]
		switch {
		case entry.admitted && !listed:
			s.addExpected(CodeModelNotInCatalog, entry.index, entry.line, entry.envelope, "/payload/model_id", "admission accepted a model the catalog served under this revision does not list", "a listed model id", entry.model, string(entry.submission))
		case entry.admitted:
		case listed:
			// The catalog lists the id, so the refusal claimed a miss that the
			// endpoint's own catalog denies.
			s.diagnoseFalseMiss(entry.index, entry.line, entry.envelope, entry.model)
		default:
			s.diagnoseCatalogMissRefusal(entry.index, entry.line, entry.envelope, entry.model)
		}
	}
	st.unjudgedModels = remaining
}

// diagnoseFalseMiss judges a refusal against a catalog that does list the
// requested id. A catalog is a promise that its ids are selectable, and a
// false miss is the more damaging failure of the two: the client discards a
// selection that was valid, and re-listing only confirms the id it was just
// told does not exist.
func (s *state) diagnoseFalseMiss(i, line int, e protocol.Envelope, model string) {
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	if payload.Error.Code != errorModelNotFound {
		return
	}
	s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/error", "refusal reports a model the endpoint's own catalog lists", "an admission", describeRefusal(payload.Error), model)
}

// diagnoseCatalogMissRefusal holds a refusal of an unlisted model to the code
// and detail a caller can act on: model_not_found naming the id it asked for.
// Any other code — internal_error most of all — or a refusal without that
// detail leaves the caller unable to tell which id to stop sending, so the
// adapter that swallows a catalog miss behind an undiagnosable failure is
// diagnosed exactly as the one that accepts it.
func (s *state) diagnoseCatalogMissRefusal(i, line int, e protocol.Envelope, model string) {
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	requested, ok := payload.Error.Details["model_id"].(string)
	if payload.Error.Code == errorModelNotFound && ok && requested == model {
		return
	}
	s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/error", "refusal of an unlisted model does not tell the caller which id to stop sending", errorModelNotFound+" model_id="+model, describeRefusal(payload.Error), model)
}

// settleModelsRefusal judges a refused catalog query. A refusal under a code,
// feature, or reason that does not tell the caller what to change is the same
// defect as no refusal at all, and a refusal of a query the descriptor says
// the endpoint could serve is what makes an advertised key mean something.
func (s *state) settleModelsRefusal(i, line int, e protocol.Envelope) {
	query := s.pendingModels[e.InReplyTo]
	if query == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	if expectation := query.expectation; expectation != nil {
		if !conformingRefusal(payload.Error, expectation) {
			s.addExpected(expectation.diagnostic, i, line, e, "/payload/error", expectation.message, expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
		}
		return
	}
	if !query.satisfiable {
		return
	}
	// No unit rule names a defect in a catalog query the endpoint advertises
	// and the caller consented to, so the generic form applies: an operation
	// within every disclosed constraint, refused under the governing key.
	s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error", "catalog query refused on an endpoint that advertises models.list", "a served catalog", describeRefusal(payload.Error), string(e.InReplyTo))
}
