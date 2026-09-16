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
//
// window is the query's window start, kept because the named position may
// arrive carrying no model at all — a completion, say. A position that moved
// nothing says nothing, so the ordinary window rule stands there, and without
// this the held claim would never be settled under any rule.
type heldCatalog struct {
	// session is the catalog's own session, kept because the position's run
	// may not be known yet: whether it belongs here is decided when it appears.
	session  protocol.SessionID
	model    string
	position protocol.ModelEventPosition
	window   int
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
//
// The revision is what retires a catalog, not the act of re-reading the
// descriptor. A capabilities.response repeating the active revision repeats
// one descriptor and licenses no different catalog, so the stored one goes on
// binding across it; discarding there would let any endpoint answer
// capabilities.request between two listings and never owe
// unannounced_catalog_change for the change in between.
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

// checkCatalogAdvertisement holds a capabilities.response that repeats the
// active revision to repeating what that revision said about the catalog.
//
// A revision identifies exactly one descriptor, which is why re-reading
// capabilities does not discard the catalog served under it. The same
// identity makes a same-revision response that *changes* models.list the
// defect: without this rule an endpoint could publish catalog A at `native`,
// re-answer capabilities.request at the same revision with models.list
// `degraded`, and serve a different catalog B — and the stability rule would
// skip it, because the rule only binds a native or emulated catalog. Two
// catalogs would stand under one revision with nothing announcing the change,
// which is exactly what the diagnostic exists to catch. It is raised where the
// descriptor is published rather than on the catalog that follows, because the
// descriptor is the thing that changed: one fault, one diagnosis, and the fix
// is to introduce a new revision.
//
// Only the advertised level is compared, because that is what every rule in
// this unit keys on. A reason reworded under one revision is prose, and
// diagnosing prose would make the rule noisy without making it stronger.
func (s *state) checkCatalogAdvertisement(i, line int, e protocol.Envelope, previousRevision string, previous protocol.SupportLevel) {
	if previousRevision == "" || previousRevision != s.currentCapability {
		return
	}
	if current := s.features[protocol.FeatureModelsList]; current != previous {
		s.addExpected(CodeUnannouncedCatalogChange, i, line, e, "/payload/features/"+protocol.FeatureModelsList, "models.list changed under one capability revision without a capabilities.updated", describeSupport(previous), describeSupport(current))
	}
}

// describeSupport renders one key's advertisement, including its absence.
func describeSupport(level protocol.SupportLevel) string {
	if level == "" {
		return "unadvertised"
	}
	return string(level)
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
		if known, owned := s.positionOwner(p.SessionID, *position); known && !owned {
			s.foreignPosition(i, line, e, p.SessionID, *position)
			return
		}
		if mark, ok := st.markAt(*position); ok {
			if mark != p.CurrentModelID {
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/current_model_id", "catalog reports a model the session did not hold at the event it names", mark, p.CurrentModelID, string(position.RunID))
			}
			return
		}
		if !s.positionReached(p.SessionID, *position) {
			// An endpoint reporting a position the trace has not reached is
			// ahead of it, not wrong: the claim is reconciled when the event
			// arrives, whether or not that event turns out to carry a model.
			// A run the trace has not seen at all is held on the same terms,
			// and its ownership is judged when it appears.
			held := heldCatalog{session: p.SessionID, model: p.CurrentModelID, position: *position, index: i, line: line, envelope: e}
			if query := s.pendingModels[e.InReplyTo]; query != nil {
				held.window = query.window
			}
			st.heldCatalogs = append(st.heldCatalogs, held)
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

// positionOwner reports what the trace knows about the run a catalog's
// position names: whether the run has been seen at all, and whether it belongs
// to the session whose catalog named it.
//
// A position is a claim about one session's model, so it can only be made
// about that session's own runs. The run map is the endpoint's, not the
// session's, so the owner is compared here rather than assumed: without it a
// catalog for one session could name a run in another and be judged — or, if
// that run were still ahead, never judged at all, because the held claim is
// revisited through the run's own session.
func (s *state) positionOwner(session protocol.SessionID, position protocol.ModelEventPosition) (known, owned bool) {
	r := s.runs[position.RunID]
	if r == nil {
		return false, false
	}
	return true, r.session == session
}

// positionReached reports whether this session's trace has already carried the
// run-scoped event a catalog names. A run belonging to another session is
// never reached here, whatever its own cursor says.
func (s *state) positionReached(session protocol.SessionID, position protocol.ModelEventPosition) bool {
	r := s.runs[position.RunID]
	return r != nil && r.session == session && r.next > position.Sequence
}

// foreignPosition diagnoses a catalog naming a model event in another
// session's run. It is a scope defect, and the claim it carries is not this
// session's to judge, so nothing further is read from it.
func (s *state) foreignPosition(i, line int, e protocol.Envelope, session protocol.SessionID, position protocol.ModelEventPosition) {
	owner := session
	if r := s.runs[position.RunID]; r != nil {
		owner = r.session
	}
	s.addExpected(CodeScopeMismatch, i, line, e, "/payload/as_of_model_event/run_id", "catalog names a model event in a run owned by another session", string(session), string(owner), string(position.RunID))
}

// reconcileEveryHeldCatalog settles held claims across every session, not only
// the one whose run moved. A catalog names a run before the trace knows whose
// it is, so the session that holds the claim and the session the run lands in
// need not be the same: settling only the run's own session would leave a
// claim on another session's catalog waiting for an event that will never
// reach it.
func (s *state) reconcileEveryHeldCatalog() {
	for _, st := range s.sessions {
		if len(st.heldCatalogs) > 0 {
			s.reconcileHeldCatalogs(st)
		}
	}
}

// reconcileHeldCatalogs settles the catalogs that named a model event the
// trace had not reached when they arrived. It runs wherever the trace can
// newly satisfy one: when a model mark is recorded, and when a run reaches the
// named sequence at all.
//
// The second is what keeps a held claim from escaping judgement entirely. A
// position that arrives carrying no model — a completion, an ordinary delta —
// moved nothing and so says nothing, and a claim resting on it is judged by
// the ordinary window rule rather than left standing forever on the strength
// of naming a position that never mattered. Only a position the trace never
// reaches stays held: diagnosing that would convict an endpoint for a place
// the trace simply never got to.
func (s *state) reconcileHeldCatalogs(st *sessionTrack) {
	remaining := st.heldCatalogs[:0]
	for _, held := range st.heldCatalogs {
		if known, owned := s.positionOwner(held.session, held.position); known && !owned {
			// The run the claim named has appeared and belongs to another
			// session, which is decided here because it could not be decided
			// when the catalog arrived.
			s.foreignPosition(held.index, held.line, held.envelope, held.session, held.position)
			continue
		}
		if mark, ok := st.markAt(held.position); ok {
			if mark != held.model {
				s.addExpected(CodeSessionStateMismatch, held.index, held.line, held.envelope, "/payload/current_model_id", "catalog reports a model the session did not hold at the event it names", mark, held.model, string(held.position.RunID))
			}
			continue
		}
		if !s.positionReached(held.session, held.position) {
			remaining = append(remaining, held)
			continue
		}
		// The window is taken as it stands now rather than as it stood at the
		// response: a catalog reporting a value the session took after the
		// response but at or before this position was ahead of the trace, which
		// is the whole reason the position was honoured. A value the session
		// never held is still a value the session never held.
		window := st.modelMarks[min(held.window, len(st.modelMarks)):]
		if len(window) == 0 {
			continue
		}
		matched := false
		for _, mark := range window {
			if mark.model == held.model {
				matched = true
				break
			}
		}
		if !matched {
			s.addExpected(CodeSessionStateMismatch, held.index, held.line, held.envelope, "/payload/current_model_id", "catalog names an event that moved no model, and reports a model the session never held", window[len(window)-1].model, held.model, string(held.position.RunID))
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
	// Only a native or emulated catalog settles a retained selection: at
	// degraded the list refreshes out of band, so the one this selection was
	// judged against may never have been served at all.
	if !served.binding || served.revision == "" {
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
