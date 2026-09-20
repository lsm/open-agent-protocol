package validation

import (
	"bytes"
	"encoding/json"
	"math/big"
	"reflect"
	"sort"
	"strings"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	rungCapability = iota + 1
	rungDegradation
	rungUnsatisfiable
)

const (
	errorUnsupportedFeature = "unsupported_feature"
	errorCapabilityDegraded = "capability_degraded"
	errorModelNotFound      = "model_not_found"
)

const (
	reasonUnadvertised  = "unadvertised"
	reasonUnsatisfiable = "unsatisfiable"
)

type controlExpectation struct {
	rung int

	key string

	pointer string

	code, reason string
	detailName   string
	detailValue  string

	diagnostic string
	message    string
}

func (e *controlExpectation) less(other *controlExpectation) bool {
	if e.rung != other.rung {
		return e.rung < other.rung
	}
	if e.key != other.key {
		return e.key < other.key
	}
	return pointerLess(e.pointer, other.pointer)
}

func pointerLess(a, b string) bool {
	left, right := splitPointer(a), splitPointer(b)
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] == right[i] {
			continue
		}
		leftIndex, leftNumeric := pointerIndex(left[i])
		rightIndex, rightNumeric := pointerIndex(right[i])
		if leftNumeric && rightNumeric {
			return leftIndex < rightIndex
		}
		return left[i] < right[i]
	}
	return len(left) < len(right)
}

func splitPointer(p string) []string {
	if p == "" {
		return nil
	}
	segments := []string{}
	for _, segment := range bytes.Split([]byte(p), []byte("/")) {
		if len(segment) == 0 {
			continue
		}
		segments = append(segments, string(segment))
	}
	return segments
}

func pointerIndex(segment string) (int, bool) {
	index := 0
	for _, r := range segment {
		if r < '0' || r > '9' {
			return 0, false
		}
		index = index*10 + int(r-'0')
	}
	return index, len(segment) > 0
}

type admittedControls struct {
	present      bool
	modelPresent bool
	model        string
	instructions bool
	choice       *protocol.ToolChoice
	catalog      []string
	catalogKnown bool
	schema       *OutputSchema
	schemaRaw    json.RawMessage
	fixedResult  json.RawMessage
	mode         string
	calls        map[string]bool
}

type pendingSubmit struct {
	expectation *controlExpectation

	queue *queueWindow

	satisfiable map[string]bool
	controls    admittedControls
	index, line int

	session  protocol.SessionID
	revision string

	modelUnjudged bool
	modelListed   bool
}

func (s *state) submitControls(i, line int, e protocol.Envelope, p protocol.MessageSubmitRequest) {
	pending := &pendingSubmit{satisfiable: map[string]bool{}, index: i, line: line, session: p.SessionID, revision: s.currentCapability}
	var expectations []*controlExpectation
	defer func() {

		expectations = append(expectations, s.deliveryExpectations(i, line, e, p, pending)...)
		sort.SliceStable(expectations, func(a, b int) bool { return expectations[a].less(expectations[b]) })
		if len(expectations) > 0 {
			pending.expectation = expectations[0]
		}
		if s.pendingControls == nil {
			s.pendingControls = map[protocol.EnvelopeID]*pendingSubmit{}
		}
		s.pendingControls[e.ID] = pending
		s.openSubmits[p.SessionID] = append(s.openSubmits[p.SessionID], pending)
		s.refreshQueueWindows(p.SessionID)
	}()
	controls := []struct {
		key     string
		present bool
	}{
		{protocol.FeatureModelSelection, p.ModelID != nil},
		{protocol.FeatureInstructions, p.Instructions != nil},
		{protocol.FeatureToolSelection, len(p.ToolChoice) > 0},
		{protocol.FeatureStructuredOutput, len(p.OutputSchema) > 0},
	}
	any := false
	for _, control := range controls {
		if control.present {
			any = true
		}
	}
	if !any {
		return
	}
	pending.controls.present = true
	pending.controls.mode = s.featureDetail(protocol.FeatureModelSelection).Scope
	for _, control := range controls {
		if !control.present {
			continue
		}
		level, judged := s.controlDescriptor(i, line, e, control.key)
		if !judged {

			continue
		}
		if !affirmative(level) {
			expectations = append(expectations, &controlExpectation{
				rung: rungCapability, key: control.key, pointer: controlPointer(control.key),
				code: errorUnsupportedFeature, reason: reasonUnadvertised,
				detailName: "feature", detailValue: control.key,
				diagnostic: CodeUnavailableCapability,
				message:    "submission carries a control the endpoint has not affirmatively advertised",
			})
			continue
		}
		if level == protocol.SupportDegraded && !p.AllowsDegraded(control.key) {
			expectations = append(expectations, &controlExpectation{
				rung: rungDegradation, key: control.key, pointer: controlPointer(control.key),
				code: errorCapabilityDegraded, detailName: "feature", detailValue: control.key,
				diagnostic: CodeDegradedWithoutOptin,
				message:    "submission carries a degraded control without the caller's opt-in",
			})
			continue
		}
		if control.key == protocol.FeatureToolSelection {
			s.duplicateToolNames(i, line, e, p.SessionID)
		}
		defect, satisfiable := s.unsatisfiable(control.key, p, pending)
		if defect != nil {
			expectations = append(expectations, defect)
			continue
		}
		if satisfiable {
			pending.satisfiable[control.key] = true
		}
	}
}

func controlPointer(key string) string {
	switch key {
	case protocol.FeatureModelSelection:
		return "/payload/model_id"
	case protocol.FeatureInstructions:
		return "/payload/instructions"
	case protocol.FeatureToolSelection:
		return "/payload/tool_choice"
	case protocol.FeatureStructuredOutput:
		return "/payload/output_schema"
	}
	return "/payload"
}

func affirmative(level protocol.SupportLevel) bool {
	switch level {
	case protocol.SupportNative, protocol.SupportEmulated, protocol.SupportDegraded:
		return true
	}
	return false
}

func (s *state) controlDescriptor(i, line int, e protocol.Envelope, key string) (protocol.SupportLevel, bool) {
	if s.currentCapability == "" || s.capabilitiesStale {
		s.add(CodeUnavailableCapability, i, line, e, "/type", "optional feature requires a current capability descriptor")
		return "", false
	}
	if string(e.CapabilityRevision) != s.currentCapability {
		s.addExpected(CodeStaleCapabilityRevision, i, line, e, "/capability_revision", "optional feature must cite the active capability descriptor", s.currentCapability, string(e.CapabilityRevision))
		return "", false
	}
	if level, ok := s.features[key]; ok {
		return level, true
	}
	return protocol.SupportUnavailable, true
}

func (s *state) featureDetail(key string) protocol.FeatureSupport {
	return s.featureSupports[key]
}

func (s *state) unsatisfiable(key string, p protocol.MessageSubmitRequest, pending *pendingSubmit) (*controlExpectation, bool) {
	controls := &pending.controls
	unsatisfiableAs := func(pointer, detailName, detailValue, message string) *controlExpectation {
		if detailValue == "" {

			detailName = ""
		}
		return &controlExpectation{
			rung: rungUnsatisfiable, key: key, pointer: pointer,
			code: errorUnsupportedFeature, reason: reasonUnsatisfiable,
			detailName: detailName, detailValue: detailValue,
			diagnostic: CodeUnsatisfiableControl, message: message,
		}
	}
	switch key {
	case protocol.FeatureModelSelection:
		controls.modelPresent, controls.model = true, protocol.Control(p.ModelID)
		if controls.model == "" {

			return &controlExpectation{
				rung: rungUnsatisfiable, key: key, pointer: "/payload/model_id",
				code: errorModelNotFound, detailName: "model_id", detailValue: "",
				diagnostic: CodeUnsatisfiableControl,
				message:    "submission carries an empty model_id, which no catalog can list",
			}, false
		}

		catalog := s.track(p.SessionID).catalog
		switch {
		case catalog.binds(s.currentCapability) && !catalog.ids[controls.model]:
			return &controlExpectation{
				rung: rungUnsatisfiable, key: key, pointer: "/payload/model_id",
				code: errorModelNotFound, detailName: "model_id", detailValue: controls.model,
				diagnostic: CodeModelNotInCatalog,
				message:    "submission selects a model the catalog served under this revision does not list",
			}, false
		case catalog.binds(s.currentCapability):
			pending.modelListed = true
		default:
			pending.modelUnjudged = true
		}
		return nil, true
	case protocol.FeatureInstructions:

		controls.instructions = true
		return nil, true
	case protocol.FeatureToolSelection:
		policy, err := p.ToolChoicePolicy()
		if err != nil {
			return unsatisfiableAs("/payload/tool_choice", "", "", "tool_choice is not the typed policy: "+err.Error()), false
		}
		if policy == nil {

			return unsatisfiableAs("/payload/tool_choice", "", "", "tool_choice carries no typed policy"), false
		}
		catalog, known := s.toolCatalog(p.SessionID)

		known = known && duplicateToolName(catalog) == ""
		controls.catalog, controls.catalogKnown = catalog, known
		controls.choice = policy
		if defect := policy.Unsatisfiable(catalog, known); defect != nil {
			return unsatisfiableAs(defect.Pointer, "tool", defect.Tool, defect.Reason), false
		}

		return nil, true
	case protocol.FeatureStructuredOutput:
		compiled, err := CompileOutputSchema(p.OutputSchema)
		if err != nil {
			return unsatisfiableAs("/payload/output_schema", "field", "output_schema", err.Error()), false
		}
		controls.schema, controls.schemaRaw = compiled, append(json.RawMessage(nil), p.OutputSchema...)

		if fixed, ok := s.featureDetail(key).Constraints[protocol.ConstraintFixedResult]; ok && isJSONObject(fixed) {
			controls.fixedResult = fixed
			if err := compiled.Validate(json.RawMessage(fixed)); err != nil {
				return unsatisfiableAs("/payload/output_schema", "field", "output_schema", "the disclosed fixed result cannot satisfy the requested schema"), false
			}
		}
		return nil, true
	}
	return nil, false
}

func isJSONObject(document json.RawMessage) bool {
	trimmed := bytes.TrimSpace(document)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var value map[string]any
	return json.Unmarshal(trimmed, &value) == nil
}

func describeModes(modes []string) string {
	if len(modes) == 0 {
		return "none"
	}
	return strings.Join(modes, ", ")
}

func (s *state) toolCatalog(session protocol.SessionID) ([]string, bool) {
	if !s.catalogKnown {
		return nil, false
	}
	track := s.sessions[session]
	if track == nil || len(track.providedOrder) == 0 {
		return s.catalog, true
	}
	catalog := make([]string, 0, len(s.catalog)+len(track.providedOrder))
	catalog = append(catalog, s.catalog...)
	return append(catalog, track.providedOrder...), true
}

func duplicateToolName(catalog []string) string {
	seen := map[string]bool{}
	for _, name := range catalog {
		if seen[name] {
			return name
		}
		seen[name] = true
	}
	return ""
}

func (s *state) settleSubmitAdmission(i, line int, e protocol.Envelope, p protocol.MessageSubmitResponse) admittedControls {
	pending := s.pendingControls[e.InReplyTo]
	if pending == nil {
		return admittedControls{}
	}
	if pending.expectation != nil {
		s.addExpected(pending.expectation.diagnostic, i, line, e, "/payload/admission", pending.expectation.message, "a typed refusal naming "+pending.expectation.key, string(p.Admission), string(e.InReplyTo))

		return admittedControls{}
	}
	if pending.modelUnjudged {

		s.retainModelSelection(pending.session, unjudgedModel{
			model: pending.controls.model, revision: pending.revision, admitted: true,
			index: i, line: line, envelope: e, submission: e.InReplyTo,
		})
	}

	if pending.controls.modelPresent && p.ModelID != pending.controls.model {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "admission reports a model other than the one it admitted", pending.controls.model, p.ModelID, string(e.InReplyTo))
	}
	return pending.controls
}

func (s *state) settleControlRefusal(i, line int, e protocol.Envelope) {
	pending := s.pendingControls[e.InReplyTo]
	if pending == nil {
		return
	}
	var payload protocol.ErrorResponse
	_ = e.DecodePayload(&payload)
	detail := func(name string) (string, bool) {
		value, ok := payload.Error.Details[name]
		text, isText := value.(string)
		return text, ok && isText
	}
	attribution := s.attributeRefusal(e.InReplyTo, payload.Error)
	if attribution.discharged() {
		return
	}
	if expectation := pending.expectation; expectation != nil {
		if attribution.owns(expectation) {
			s.addExpected(expectation.diagnostic, i, line, e, "/payload/error", "refusal does not tell the caller what to change", expectation.describe(), describeRefusal(payload.Error), string(e.InReplyTo))
		}
		return
	}

	if pending.modelUnjudged {
		s.retainModelSelection(pending.session, unjudgedModel{
			model: pending.controls.model, revision: pending.revision,
			index: i, line: line, envelope: e, submission: e.InReplyTo,
		})
	} else if pending.modelListed {
		s.diagnoseFalseMiss(i, line, e, pending.controls.model)
	}

	if feature, ok := detail("feature"); payload.Error.Code == errorUnsupportedFeature && ok && pending.satisfiable[feature] {
		s.addExpected(CodeUnsatisfiableControl, i, line, e, "/payload/error", "refusal names a control the endpoint advertises and this request satisfies", "admission or a defect the refusal names", describeRefusal(payload.Error), string(e.InReplyTo))
		return
	}

	s.settleQueueRefusal(i, line, e, pending, payload)
}

func conformingRefusal(err protocol.ProtocolError, expectation *controlExpectation) bool {
	detail := func(name string) (string, bool) {
		value, ok := err.Details[name]
		text, isText := value.(string)
		return text, ok && isText
	}
	if err.Code != expectation.code {
		return false
	}
	if expectation.code == errorUnsupportedFeature {

		feature, ok := detail("feature")
		if !ok || feature != expectation.key {
			return false
		}
	}
	if expectation.reason != "" {
		reason, ok := detail("reason")
		if !ok || reason != expectation.reason {
			return false
		}
	}
	if expectation.detailName != "" {
		value, ok := detail(expectation.detailName)
		if !ok || value != expectation.detailValue {
			return false
		}
	}
	return true
}

func (e *controlExpectation) describe() string {
	description := e.code
	if e.reason != "" {
		description += "/" + e.reason
	}
	if e.code == errorUnsupportedFeature && e.detailName != "feature" {
		description += " feature=" + e.key
	}
	if e.detailName != "" {
		description += " " + e.detailName + "=" + e.detailValue
	}
	return description
}

func describeRefusal(err protocol.ProtocolError) string {
	description := err.Code
	if reason, ok := err.Details["reason"].(string); ok {
		description += "/" + reason
	}
	for _, name := range []string{"feature", "model_id", "tool", "field", "source"} {
		if value, ok := err.Details[name].(string); ok {
			description += " " + name + "=" + value
		}
	}
	return description
}

func (s *state) duplicateToolNames(i, line int, e protocol.Envelope, session protocol.SessionID) {
	if s.catalogAmbiguous {

		return
	}
	catalog, known := s.toolCatalog(session)
	if !known {
		return
	}
	if name := duplicateToolName(catalog); name != "" {
		s.addExpected(CodeDuplicateToolName, i, line, e, "/payload/tool_choice", "the catalog this tool_choice is judged against lists two tools with one name", "one tool per name", name)
	}
}

func (s *state) checkCompletedControls(i, line int, e protocol.Envelope, r *runState) {
	controls := r.controls
	if !controls.present {
		return
	}
	var p protocol.RunCompletedPayload
	_ = e.DecodePayload(&p)
	if p.ModelID != "" && controls.modelPresent && p.ModelID != controls.model {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/model_id", "completion attributes the run to a model other than the admitted one", controls.model, p.ModelID, string(r.id))
	}
	if controls.schema != nil {
		switch {
		case len(p.Result) == 0:
			s.addExpected(CodeUnappliedControl, i, line, e, "/payload/result", "completion under an admitted output_schema carries no result", "a result conforming to the admitted schema", "absent", string(r.id))
		default:
			if err := controls.schema.Validate(p.Result); err != nil {
				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/result", "completion result does not conform to the admitted output_schema", "a conforming result", string(p.Result), string(r.id))
			} else if len(controls.fixedResult) > 0 && !sameJSON(controls.fixedResult, p.Result) {

				s.addExpected(CodeUnappliedControl, i, line, e, "/payload/result", "completion does not carry the result the endpoint declared it would", string(controls.fixedResult), string(p.Result), string(r.id))
			}
		}
	}
}

func (s *state) checkCallAgainstChoice(i, line int, e protocol.Envelope, r *runState) {
	if !r.controls.present {
		return
	}
	var p protocol.ActionCallPayload
	_ = e.DecodePayload(&p)
	if p.Name == "" {
		return
	}
	if r.controls.calls == nil {
		r.controls.calls = map[string]bool{}
	}
	r.controls.calls[p.Name] = true
	if choice := r.controls.choice; choice != nil && !choice.Permits(p.Name, r.controls.catalog, r.controls.catalogKnown) {
		s.addExpected(CodeUnappliedControl, i, line, e, "/payload/name", "run requested a tool its admitted tool_choice excludes", "a tool the policy permits", p.Name, string(r.id))
	}
}

func sameJSON(a, b json.RawMessage) bool {
	left, leftErr := decodeExact(a)
	right, rightErr := decodeExact(b)
	if leftErr != nil || rightErr != nil {
		return false
	}

	return reflect.DeepEqual(canonical(left), canonical(right))
}

func decodeExact(document json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

type exactNumber string

func canonicalNumber(number json.Number) exactNumber {
	if rat, ok := new(big.Rat).SetString(number.String()); ok {
		return exactNumber(rat.RatString())
	}
	return exactNumber(number.String())
}

func canonical(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return canonicalNumber(typed)
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		pairs := make([][2]any, 0, len(keys))
		for _, key := range keys {
			pairs = append(pairs, [2]any{key, canonical(typed[key])})
		}
		return pairs
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = canonical(item)
		}
		return items
	}
	return value
}

func (s *state) checkSelectionModes(i, line int, e protocol.Envelope, p protocol.CapabilitiesResponse) {
	if support, ok := p.EffectiveSupport(protocol.FeatureToolSelection); ok && affirmative(support.Level) && support.Scope != "" && support.Scope != protocol.ScopeRun && support.Scope != protocol.ScopeSession {
		s.addExpected(CodeUndisclosedSelectionScope, i, line, e, "/payload/features/run.tool_selection/scope", "run.tool_selection discloses a scope that is not one the protocol defines", protocol.ScopeRun+" or "+protocol.ScopeSession, support.Scope)
	}

	support, ok := p.EffectiveSupport(protocol.FeatureModelSelection)
	if !ok || !affirmative(support.Level) {
		return
	}
	if support.Scope != protocol.ScopeRun && support.Scope != protocol.ScopeSession {
		s.addExpected(CodeUndisclosedSelectionScope, i, line, e, "/payload/features/run.model_selection/scope", "run.model_selection is advertised without disclosing how long a selection lives", protocol.ScopeRun+" or "+protocol.ScopeSession, support.Scope)
	}
}

func collectCatalog(p protocol.CapabilitiesResponse) []string {
	names := make([]string, 0, len(p.Tools))
	for _, tool := range p.Tools {
		names = append(names, tool.Name)
	}
	layers := make([]string, 0, len(p.Layers))
	for name := range p.Layers {
		layers = append(layers, name)
	}
	sort.Strings(layers)
	for _, name := range layers {
		for _, tool := range p.Layers[name].Tools {
			names = append(names, tool.Name)
		}
	}
	return names
}
