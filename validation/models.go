package validation

import (
	"encoding/json"

	"github.com/lsm/open-agent-protocol/protocol"
)

type pendingModelsQuery struct {
	expectation *controlExpectation

	satisfiable bool

	window int
}

type modelMark struct {
	model    string
	run      protocol.RunID
	sequence uint64
}

type unjudgedModel struct {
	model      string
	revision   string
	admitted   bool
	index      int
	line       int
	envelope   protocol.Envelope
	submission protocol.EnvelopeID
}

type heldCatalog struct {
	session  protocol.SessionID
	model    string
	position protocol.ModelEventPosition
	window   int
	index    int
	line     int
	envelope protocol.Envelope
}

type modelCatalog struct {
	revision string
	known    bool

	binding bool
	ids     map[string]bool
	models  map[string]protocol.ModelDescriptor
}

func (c *modelCatalog) binds(revision string) bool {
	return c != nil && c.known && c.binding && c.revision == revision && revision != ""
}

func (s *state) track(session protocol.SessionID) *sessionTrack {
	st := s.sessions[session]
	if st == nil {
		st = &sessionTrack{}
		s.sessions[session] = st
	}
	return st
}

func (s *state) observeModel(session protocol.SessionID, model string, run protocol.RunID, sequence uint64) {
	if model == "" {
		return
	}
	st := s.track(session)
	st.modelMarks = append(st.modelMarks, modelMark{model: model, run: run, sequence: sequence})
	s.reconcileHeldCatalogs(st)
}

type descriptorSnapshot struct {
	revision string
	stale    bool
	models   protocol.SupportLevel

	queue  protocol.SupportLevel
	limits *protocol.CapabilityLimits
}

func (d descriptorSnapshot) repeats(current string) bool {
	return d.revision != "" && !d.stale && d.revision == current
}

func (s *state) checkCatalogAdvertisement(i, line int, e protocol.Envelope, outgoing descriptorSnapshot) {
	if !outgoing.repeats(s.currentCapability) {
		return
	}
	if current := s.features[protocol.FeatureModelsList]; current != outgoing.models {
		s.addExpected(CodeUnannouncedCatalogChange, i, line, e, "/payload/features/"+protocol.FeatureModelsList, "models.list changed under one capability revision without a capabilities.updated", describeSupport(outgoing.models), describeSupport(current))
	}
}

func describeSupport(level protocol.SupportLevel) string {
	if level == "" {
		return "unadvertised"
	}
	return string(level)
}

func (s *state) modelsRequest(i, line int, e protocol.Envelope, p protocol.ModelsRequest) {
	st := s.track(p.SessionID)
	pending := &pendingModelsQuery{window: len(st.modelMarks) - 1}
	if pending.window < 0 {
		pending.window = 0
	}
	defer func() { s.pendingModels[e.ID] = pending }()
	level, judged := s.controlDescriptor(i, line, e, protocol.FeatureModelsList)
	if !judged {

		return
	}
	switch {
	case !affirmative(level):

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

func (s *state) modelsResponse(i, line int, e protocol.Envelope, p protocol.ModelsResponse) {

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
	if len(p.Providers) > 0 {
		s.checkProviders(i, line, e, p)
	}
	if p.CurrentModelID != "" && !ids[p.CurrentModelID] {

		s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/current_model_id", "catalog reports a current model it does not list", "one of the catalog's own ids", p.CurrentModelID)
	}
	st := s.track(p.SessionID)
	s.checkCatalogCurrentModel(i, line, e, p, st)

	level := s.features[protocol.FeatureModelsList]
	binding := level == protocol.SupportNative || level == protocol.SupportEmulated
	served := &modelCatalog{revision: string(e.CapabilityRevision), known: true, binding: binding, ids: ids, models: map[string]protocol.ModelDescriptor{}}
	for _, model := range p.Models {
		served.models[model.ID] = model
	}

	if served.revision == "" || served.revision != s.currentCapability || s.capabilitiesStale {
		return
	}
	if binding && st.catalog != nil && st.catalog.known && st.catalog.binding && st.catalog.revision == served.revision && !sameCatalog(st.catalog.models, served.models) {

		s.addExpected(CodeUnannouncedCatalogChange, i, line, e, "/payload/models", "catalog changed under one capability revision without a capabilities.updated", "the catalog served under "+served.revision, "a different catalog")
	}
	st.catalog = served
	s.reconcileUnjudgedModels(st, served)
}

func (s *state) checkProviders(i, line int, e protocol.Envelope, p protocol.ModelsResponse) {
	declared := make(map[string]bool, len(p.Providers))
	for _, provider := range p.Providers {
		if declared[provider.ID] {
			s.addExpected(CodeDuplicateProvider, i, line, e, "/payload/providers", "catalog lists two provider descriptors under one id, so a provider_id resolves to neither", "one descriptor per id", provider.ID)
			continue
		}
		declared[provider.ID] = true
	}
	reported := map[string]bool{}
	for _, model := range p.Models {
		if model.ProviderID == "" || declared[model.ProviderID] || reported[model.ProviderID] {
			continue
		}
		reported[model.ProviderID] = true
		s.addExpected(CodeUnmatchedProvider, i, line, e, "/payload/models", "a model attributes itself to a provider the catalog does not declare", "a provider declared in providers", model.ProviderID, model.ID)
	}
}

func sameCatalog(a, b map[string]protocol.ModelDescriptor) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	if leftErr != nil || rightErr != nil {

		return true
	}
	return sameJSON(left, right)
}

func (s *state) checkCatalogCurrentModel(i, line int, e protocol.Envelope, p protocol.ModelsResponse, st *sessionTrack) {

	if position := p.AsOfModelEvent; position != nil {
		if known, owned := s.positionOwner(p.SessionID, *position); known && !owned {

			s.foreignPosition(i, line, e, p.SessionID, *position)
			return
		}
		if !s.positionReached(p.SessionID, *position) {

			held := heldCatalog{session: p.SessionID, model: p.CurrentModelID, position: *position, index: i, line: line, envelope: e}
			if query := s.pendingModels[e.InReplyTo]; query != nil {
				held.window = query.window
			}
			st.heldCatalogs = append(st.heldCatalogs, held)
			return
		}
		if p.CurrentModelID == "" {

			return
		}

		if mark, ok := st.markAt(*position); ok {
			if mark != p.CurrentModelID {
				s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/current_model_id", "catalog reports a model the session did not hold at the event it names", mark, p.CurrentModelID, string(position.RunID))
			}
			return
		}

	}
	if p.CurrentModelID == "" {

		return
	}
	window := st.modelWindow(s.pendingModels[e.InReplyTo])
	if len(window) == 0 {

		return
	}
	for _, model := range window {
		if model == p.CurrentModelID {
			return
		}
	}
	s.addExpected(CodeSessionStateMismatch, i, line, e, "/payload/current_model_id", "catalog reports a model the session never held while the query was in flight", window[len(window)-1], p.CurrentModelID)
}

func (t *sessionTrack) markAt(position protocol.ModelEventPosition) (string, bool) {
	for _, mark := range t.modelMarks {
		if mark.run == position.RunID && mark.sequence == position.Sequence {
			return mark.model, true
		}
	}
	return "", false
}

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

func (s *state) positionOwner(session protocol.SessionID, position protocol.ModelEventPosition) (known, owned bool) {
	r := s.runs[position.RunID]
	if r == nil {
		return false, false
	}
	return true, r.session == session
}

func (s *state) positionReached(session protocol.SessionID, position protocol.ModelEventPosition) bool {
	r := s.runs[position.RunID]
	return r != nil && r.session == session && r.next > position.Sequence
}

func (s *state) foreignPosition(i, line int, e protocol.Envelope, session protocol.SessionID, position protocol.ModelEventPosition) {
	owner := session
	if r := s.runs[position.RunID]; r != nil {
		owner = r.session
	}
	s.addExpected(CodeScopeMismatch, i, line, e, "/payload/as_of_model_event/run_id", "catalog names a model event in a run owned by another session", string(session), string(owner), string(position.RunID))
}

func (s *state) reconcileEveryHeldCatalog() {
	for _, st := range s.sessions {
		if len(st.heldCatalogs) > 0 {
			s.reconcileHeldCatalogs(st)
		}
	}
}

func (s *state) reconcileHeldCatalogs(st *sessionTrack) {
	remaining := st.heldCatalogs[:0]
	for _, held := range st.heldCatalogs {
		known, owned := s.positionOwner(held.session, held.position)
		if known && !owned {

			s.foreignPosition(held.index, held.line, held.envelope, held.session, held.position)
			continue
		}
		if held.model == "" {

			if !known {
				remaining = append(remaining, held)
			}
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

func (s *state) retainModelSelection(session protocol.SessionID, entry unjudgedModel) {
	st := s.track(session)
	st.unjudgedModels = append(st.unjudgedModels, entry)
}

func (s *state) reconcileUnjudgedModels(st *sessionTrack, served *modelCatalog) {

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

			s.diagnoseFalseMiss(entry.index, entry.line, entry.envelope, entry.model)
		default:
			s.diagnoseCatalogMissRefusal(entry.index, entry.line, entry.envelope, entry.model)
		}
	}
	st.unjudgedModels = remaining
}

func (s *state) diagnoseFalseMiss(i, line int, e protocol.Envelope, model string) {
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	if payload.Error.Code != errorModelNotFound {
		return
	}
	s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/error", "refusal reports a model the endpoint's own catalog lists", "an admission", describeRefusal(payload.Error), model)
}

func (s *state) diagnoseCatalogMissRefusal(i, line int, e protocol.Envelope, model string) {
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	requested, ok := payload.Error.Details["model_id"].(string)
	if payload.Error.Code == errorModelNotFound && ok && requested == model {
		return
	}
	s.addExpected(CodeModelNotInCatalog, i, line, e, "/payload/error", "refusal of an unlisted model does not tell the caller which id to stop sending", errorModelNotFound+" model_id="+model, describeRefusal(payload.Error), model)
}

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

	s.addExpected(CodeUnhonouredCapability, i, line, e, "/payload/error", "catalog query refused on an endpoint that advertises models.list", "a served catalog", describeRefusal(payload.Error), string(e.InReplyTo))
}
